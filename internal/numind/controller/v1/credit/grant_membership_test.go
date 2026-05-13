package credit_test

// grant_membership_test.go — HTTP handler tests for POST /v1/users/children/:child_id/grant-membership.
//
// Strategy: real MembershipService backed by SQLite in-memory DB (same approach as
// biz/membership/*_test.go). This exercises the full biz → store → HTTP response
// path without mocking, which is faithful to the actual runtime behavior.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/membership"
	creditctl "numind-server/internal/numind/controller/v1/credit"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// test infrastructure
// ---------------------------------------------------------------------------

// newMembershipTestDB creates an isolated SQLite in-memory DB with the
// membership schema (mirrors biz/membership/test_helpers_test.go DDL).
func newMembershipTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	// Raw DDL mirrors MySQL schema; ENUM → TEXT for SQLite compatibility.
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS subscription (
			id                     INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                INTEGER NOT NULL UNIQUE,
			first_started_at       DATETIME NOT NULL,
			current_started_at     DATETIME NOT NULL,
			expires_at             DATETIME NOT NULL,
			total_months_purchased INTEGER NOT NULL,
			source                 TEXT NOT NULL DEFAULT 'b2b_grant',
			granter_user_id        INTEGER,
			created_at             DATETIME NOT NULL,
			updated_at             DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS trial_grant (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER NOT NULL UNIQUE,
			granted_at        DATETIME NOT NULL,
			expires_at        DATETIME NOT NULL,
			credits_remaining INTEGER NOT NULL DEFAULT 200,
			source            TEXT NOT NULL DEFAULT 'b2b_grant',
			granter_user_id   INTEGER,
			created_at        DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS credit_cycle (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER NOT NULL,
			subscription_id   INTEGER NOT NULL,
			cycle_start       DATETIME NOT NULL,
			cycle_end         DATETIME NOT NULL,
			credits_granted   INTEGER NOT NULL DEFAULT 0,
			credits_remaining INTEGER NOT NULL DEFAULT 0,
			created_at        DATETIME NOT NULL,
			updated_at        DATETIME NOT NULL,
			UNIQUE(user_id, cycle_start)
		)`,
		`CREATE TABLE IF NOT EXISTS user_booster_balance (
			user_id            INTEGER PRIMARY KEY,
			credits_remaining  INTEGER NOT NULL DEFAULT 0,
			updated_at         DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS membership_event (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER NOT NULL,
			event_type        TEXT NOT NULL,
			product_type      TEXT NOT NULL,
			months            INTEGER,
			quantity          INTEGER,
			amount_cents      INTEGER NOT NULL DEFAULT 0,
			source            TEXT NOT NULL,
			granter_user_id   INTEGER,
			idempotency_key   TEXT UNIQUE,
			subscription_id   INTEGER,
			occurred_at       DATETIME NOT NULL
		)`,
	}
	for _, stmt := range ddl {
		require.NoError(t, db.Exec(stmt).Error)
	}

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// newGrantRouter mounts the GrantMembership handler at the test route.
// idemKey is injected via the "idempotency_key" gin context key (simulating
// the RequireIdempotencyKey middleware).
func newGrantRouter(t *testing.T, ctrl *creditctl.CreditController, user *model.User, idemKey string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			c.Set("current_user", user)
		}
		if idemKey != "" {
			c.Set("idempotency_key", idemKey)
		}
		c.Next()
	})
	r.POST("/v1/users/children/:child_id/grant-membership", ctrl.GrantMembership)
	return r
}

// makeGrantCtrl builds a CreditController wired to the given membership service.
// The credit/creditSvc/promptEstimator fields are nil because GrantMembership
// only touches membershipSvc.
func makeGrantCtrl(svc *membership.MembershipService) *creditctl.CreditController {
	return creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, nil).
		WithMembershipSvc(svc)
}

// makeUser returns a parent User with ID parentID.
func makeUser(parentID uint) *model.User {
	u := &model.User{}
	u.ID = parentID
	return u
}

// postGrant sends a POST request to the grant endpoint for childID.
func postGrant(t *testing.T, r *gin.Engine, childID uint64, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost,
		"/v1/users/children/"+uint64ToStr(childID)+"/grant-membership",
		bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func uint64ToStr(n uint64) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 20)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Happy-path tests
// ---------------------------------------------------------------------------

// TestGrantMembership_Trial_HappyPath verifies a fresh trial grant returns
// HTTP 200 with event_type="trial_granted" and an expires_at 3 days out.
func TestGrantMembership_Trial_HappyPath(t *testing.T) {
	db := newMembershipTestDB(t)
	svc := membership.NewMembershipService(db)
	ctrl := makeGrantCtrl(svc)
	r := newGrantRouter(t, ctrl, makeUser(1), "idem-trial-001")

	w := postGrant(t, r, 101, map[string]interface{}{
		"product_type": "trial",
	})

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int `json:"code"`
		Data struct {
			ChildUserID uint64 `json:"child_user_id"`
			ProductType string `json:"product_type"`
			EventType   string `json:"event_type"`
			ExpiresAt   string `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, uint64(101), env.Data.ChildUserID)
	assert.Equal(t, "trial", env.Data.ProductType)
	assert.Equal(t, "trial_granted", env.Data.EventType)
	assert.NotEmpty(t, env.Data.ExpiresAt)

	// Verify the trial is 3 days from now (roughly).
	expiresAt, err := time.Parse(time.RFC3339, env.Data.ExpiresAt)
	require.NoError(t, err)
	diff := expiresAt.Sub(time.Now().UTC())
	assert.True(t, diff > 2*24*time.Hour && diff < 4*24*time.Hour,
		"trial must expire in ~3 days, got %v", diff)
}

// TestGrantMembership_Monthly_NewSubscription verifies a fresh monthly grant
// returns HTTP 200 with event_type="sub_granted" (scenario=new).
func TestGrantMembership_Monthly_NewSubscription(t *testing.T) {
	db := newMembershipTestDB(t)
	svc := membership.NewMembershipService(db)
	ctrl := makeGrantCtrl(svc)
	r := newGrantRouter(t, ctrl, makeUser(1), "idem-monthly-001")

	w := postGrant(t, r, 202, map[string]interface{}{
		"product_type": "monthly",
		"months":       3,
	})

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int `json:"code"`
		Data struct {
			ChildUserID uint64 `json:"child_user_id"`
			ProductType string `json:"product_type"`
			EventID     uint64 `json:"event_id"`
			EventType   string `json:"event_type"`
			ExpiresAt   string `json:"expires_at"`
			Scenario    string `json:"scenario"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, uint64(202), env.Data.ChildUserID)
	assert.Equal(t, "monthly", env.Data.ProductType)
	assert.Equal(t, "sub_granted", env.Data.EventType)
	assert.Equal(t, "new", env.Data.Scenario)
	assert.True(t, env.Data.EventID > 0, "event_id must be set")
	assert.NotEmpty(t, env.Data.ExpiresAt)
}

// TestGrantMembership_Monthly_Renewal verifies a renewal (second grant while
// subscription is still active) returns event_type="sub_renewed".
func TestGrantMembership_Monthly_Renewal(t *testing.T) {
	db := newMembershipTestDB(t)
	svc := membership.NewMembershipService(db)
	ctx := context.Background()

	// First grant (scenario=new).
	_, err := svc.GrantOrRenewSubscription(ctx, membership.GrantSubscriptionRequest{
		ParentUserID: 1,
		UserID:       303,
		ProductType:  "monthly",
		Months:       1,
	})
	require.NoError(t, err)

	ctrl := makeGrantCtrl(svc)
	r := newGrantRouter(t, ctrl, makeUser(1), "idem-renew-001")

	// Second grant (scenario=renew, same child still has active sub).
	w := postGrant(t, r, 303, map[string]interface{}{
		"product_type": "monthly",
		"months":       2,
	})

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int `json:"code"`
		Data struct {
			EventType string `json:"event_type"`
			Scenario  string `json:"scenario"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, "sub_renewed", env.Data.EventType)
	assert.Equal(t, "renew", env.Data.Scenario)
}

// ---------------------------------------------------------------------------
// Validation error tests
// ---------------------------------------------------------------------------

// TestGrantMembership_Trial_MonthsNonZero_Returns400 verifies that sending
// months > 0 with product_type=trial is rejected (trial has fixed 3-day duration).
func TestGrantMembership_Trial_MonthsNonZero_Returns400(t *testing.T) {
	db := newMembershipTestDB(t)
	ctrl := makeGrantCtrl(membership.NewMembershipService(db))
	r := newGrantRouter(t, ctrl, makeUser(1), "idem-val-001")

	w := postGrant(t, r, 101, map[string]interface{}{
		"product_type": "trial",
		"months":       3,
	})

	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
}

// TestGrantMembership_Monthly_MonthsZero_Returns400 verifies that months=0
// is rejected for product_type=monthly.
func TestGrantMembership_Monthly_MonthsZero_Returns400(t *testing.T) {
	db := newMembershipTestDB(t)
	ctrl := makeGrantCtrl(membership.NewMembershipService(db))
	r := newGrantRouter(t, ctrl, makeUser(1), "idem-val-002")

	w := postGrant(t, r, 202, map[string]interface{}{
		"product_type": "monthly",
		"months":       0,
	})

	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
}

// TestGrantMembership_Monthly_Months13_Returns400 verifies that months=13
// is rejected (binding:"max=12" + controller check).
func TestGrantMembership_Monthly_Months13_Returns400(t *testing.T) {
	db := newMembershipTestDB(t)
	ctrl := makeGrantCtrl(membership.NewMembershipService(db))
	r := newGrantRouter(t, ctrl, makeUser(1), "idem-val-003")

	w := postGrant(t, r, 202, map[string]interface{}{
		"product_type": "monthly",
		"months":       13,
	})

	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
}

// ---------------------------------------------------------------------------
// Conflict / duplicate tests
// ---------------------------------------------------------------------------

// TestGrantMembership_Trial_Duplicate_Returns409 verifies that a second trial
// grant for the same child returns HTTP 409 ErrTrialAlreadyGranted.
func TestGrantMembership_Trial_Duplicate_Returns409(t *testing.T) {
	db := newMembershipTestDB(t)
	svc := membership.NewMembershipService(db)

	// First grant succeeds.
	ctrl1 := makeGrantCtrl(svc)
	r1 := newGrantRouter(t, ctrl1, makeUser(1), "idem-dup-001")
	w1 := postGrant(t, r1, 404, map[string]interface{}{"product_type": "trial"})
	require.Equal(t, http.StatusOK, w1.Code, "first grant must succeed: %s", w1.Body.String())

	// Second grant — different idempotency key, same child → AlreadyGranted.
	ctrl2 := makeGrantCtrl(svc)
	r2 := newGrantRouter(t, ctrl2, makeUser(1), "idem-dup-002")
	w2 := postGrant(t, r2, 404, map[string]interface{}{"product_type": "trial"})
	assert.Equal(t, http.StatusConflict, w2.Code, "body: %s", w2.Body.String())
}

// ---------------------------------------------------------------------------
// Idempotency tests
// ---------------------------------------------------------------------------

// TestGrantMembership_Idempotency_Replay returns HTTP 200 with the same result
// when the same Idempotency-Key is repeated with the same child and body.
func TestGrantMembership_Idempotency_Replay(t *testing.T) {
	db := newMembershipTestDB(t)
	svc := membership.NewMembershipService(db)

	idemKey := "idem-replay-001"
	childID := uint64(505)

	// First call.
	ctrl1 := makeGrantCtrl(svc)
	r1 := newGrantRouter(t, ctrl1, makeUser(1), idemKey)
	w1 := postGrant(t, r1, childID, map[string]interface{}{"product_type": "trial"})
	require.Equal(t, http.StatusOK, w1.Code, "first call: %s", w1.Body.String())

	// Replay — same key, same child — must return 200 with same data.
	ctrl2 := makeGrantCtrl(svc)
	r2 := newGrantRouter(t, ctrl2, makeUser(1), idemKey)
	w2 := postGrant(t, r2, childID, map[string]interface{}{"product_type": "trial"})
	assert.Equal(t, http.StatusOK, w2.Code, "replay: %s", w2.Body.String())

	// Both responses should have the same expires_at.
	var env1, env2 struct {
		Data struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w1.Body).Decode(&env1))
	require.NoError(t, json.NewDecoder(w2.Body).Decode(&env2))
	assert.Equal(t, env1.Data.ExpiresAt, env2.Data.ExpiresAt,
		"replayed response must return same expires_at as original")
}

// TestGrantMembership_Idempotency_Conflict_Returns409 verifies that using the
// same Idempotency-Key for a different child returns HTTP 409 ErrIdempotencyKeyConflict.
func TestGrantMembership_Idempotency_Conflict_Returns409(t *testing.T) {
	db := newMembershipTestDB(t)
	svc := membership.NewMembershipService(db)

	idemKey := "idem-conflict-001"

	// First call for child 601.
	ctrl1 := makeGrantCtrl(svc)
	r1 := newGrantRouter(t, ctrl1, makeUser(1), idemKey)
	w1 := postGrant(t, r1, 601, map[string]interface{}{"product_type": "trial"})
	require.Equal(t, http.StatusOK, w1.Code, "first call: %s", w1.Body.String())

	// Same key, different child 602 → conflict.
	ctrl2 := makeGrantCtrl(svc)
	r2 := newGrantRouter(t, ctrl2, makeUser(1), idemKey)
	w2 := postGrant(t, r2, 602, map[string]interface{}{"product_type": "trial"})
	assert.Equal(t, http.StatusConflict, w2.Code, "body: %s", w2.Body.String())
}

// ---------------------------------------------------------------------------
// Self-purchase guard
// ---------------------------------------------------------------------------

// TestGrantMembership_SelfPurchase_Returns403 verifies that a parent trying to
// grant a monthly subscription to themselves is rejected (§B2B2C constraint).
func TestGrantMembership_SelfPurchase_Returns403(t *testing.T) {
	db := newMembershipTestDB(t)
	ctrl := makeGrantCtrl(membership.NewMembershipService(db))
	// parent ID = 700; child ID = 700 (same) → ErrMembershipSelfPurchaseDisabled.
	r := newGrantRouter(t, ctrl, makeUser(700), "idem-self-001")

	w := postGrant(t, r, 700, map[string]interface{}{
		"product_type": "monthly",
		"months":       1,
	})

	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
}

// ---------------------------------------------------------------------------
// Auth guard
// ---------------------------------------------------------------------------

// TestGrantMembership_Unauthenticated_Returns401 verifies that a missing user
// context results in HTTP 401.
func TestGrantMembership_Unauthenticated_Returns401(t *testing.T) {
	db := newMembershipTestDB(t)
	ctrl := makeGrantCtrl(membership.NewMembershipService(db))
	r := newGrantRouter(t, ctrl, nil, "idem-unauth-001")

	w := postGrant(t, r, 101, map[string]interface{}{"product_type": "trial"})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ensure middleware package is imported (used via middleware.GetCurrentUser indirectly).
var _ = middleware.GetCurrentUser
