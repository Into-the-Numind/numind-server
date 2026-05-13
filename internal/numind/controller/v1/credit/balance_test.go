package credit_test

// balance_test.go — HTTP handler tests for GET /v1/users/children/:child_id/balance.
//
// Strategy: real MembershipService backed by SQLite in-memory DB, real store.
// Parent-child relationship enforced via Customers().GetSubUser.

import (
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
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// DB setup for balance tests (users + membership schema)
// ---------------------------------------------------------------------------

// newBalanceTestDB creates an SQLite in-memory DB with user + membership tables.
func newBalanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// User table (minimal columns needed). model.User.TableName() returns "user".
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS "user" (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		username        TEXT    NOT NULL DEFAULT '',
		billing_mode    TEXT    NOT NULL DEFAULT 'credits',
		parent_user_id  INTEGER,
		created_at      DATETIME,
		updated_at      DATETIME,
		deleted_at      DATETIME
	)`).Error)

	// Membership tables
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
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// insertUser inserts a user row for test setup.
// Note: model.User.TableName() == "user" (single word, not "users").
func insertUser(t *testing.T, db *gorm.DB, id uint, parentID *uint) {
	t.Helper()
	if parentID == nil {
		require.NoError(t, db.Exec(`INSERT INTO "user" (id, billing_mode, created_at, updated_at)
			VALUES (?, 'credits', datetime('now'), datetime('now'))`, id).Error)
	} else {
		require.NoError(t, db.Exec(`INSERT INTO "user" (id, billing_mode, parent_user_id, created_at, updated_at)
			VALUES (?, 'credits', ?, datetime('now'), datetime('now'))`, id, *parentID).Error)
	}
}

// newBalanceController creates a CreditController for balance endpoint testing.
func newBalanceController(ds store.IStore, svc *membership.MembershipService) *creditctl.CreditController {
	return creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, ds).
		WithMembershipSvc(svc)
}

// newChildBalanceRouter mounts the GetChildBalance handler.
func newChildBalanceRouter(t *testing.T, ctrl *creditctl.CreditController, user *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(setCurrentUserMiddleware(user))
	r.GET("/v1/users/children/:child_id/balance", ctrl.GetChildBalance)
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestGetChildBalance_HappyPath verifies a parent can read a child's balance.
func TestGetChildBalance_HappyPath(t *testing.T) {
	db := newBalanceTestDB(t)
	ds := store.NewTestStore(db)
	svc := membership.NewMembershipService(db)

	parentID := uint(1)
	childID := uint(101)
	insertUser(t, db, parentID, nil)
	insertUser(t, db, childID, &parentID)

	ctrl := newBalanceController(ds, svc)
	r := newChildBalanceRouter(t, ctrl, mustUser(parentID, model.BillingModeCredits))

	req := httptest.NewRequest(http.MethodGet, "/v1/users/children/101/balance", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                        `json:"code"`
		Data creditctl.ChildBalanceView `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, "free", env.Data.MembershipState, "child with no membership should be 'free'")
}

// TestGetChildBalance_WithActiveTrial verifies trial_remaining is populated.
func TestGetChildBalance_WithActiveTrial(t *testing.T) {
	db := newBalanceTestDB(t)
	ds := store.NewTestStore(db)
	svc := membership.NewMembershipService(db)

	parentID := uint(2)
	childID := uint(202)
	insertUser(t, db, parentID, nil)
	insertUser(t, db, childID, &parentID)

	// Insert an active trial for the child.
	expiresAt := time.Now().Add(48 * time.Hour)
	require.NoError(t, db.Exec(`INSERT INTO trial_grant
		(user_id, granted_at, expires_at, credits_remaining, source, created_at)
		VALUES (?, datetime('now'), ?, 150, 'b2b_grant', datetime('now'))`,
		childID, expiresAt.UTC().Format("2006-01-02 15:04:05")).Error)

	ctrl := newBalanceController(ds, svc)
	r := newChildBalanceRouter(t, ctrl, mustUser(parentID, model.BillingModeCredits))

	req := httptest.NewRequest(http.MethodGet, "/v1/users/children/202/balance", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                        `json:"code"`
		Data creditctl.ChildBalanceView `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, "trial", env.Data.MembershipState)
	assert.Equal(t, int64(150), env.Data.TrialRemaining)
	// Booster fields must NOT be present in ChildBalanceView (privacy constraint).
}

// TestGetChildBalance_ForbiddenWhenNotChild verifies cross-parent access is denied.
func TestGetChildBalance_ForbiddenWhenNotChild(t *testing.T) {
	db := newBalanceTestDB(t)
	ds := store.NewTestStore(db)
	svc := membership.NewMembershipService(db)

	parentID := uint(3)
	otherParentID := uint(4)
	childOfOther := uint(303)
	insertUser(t, db, parentID, nil)
	insertUser(t, db, otherParentID, nil)
	insertUser(t, db, childOfOther, &otherParentID)

	// Parent 3 tries to query child 303 (child of parent 4) — must be forbidden.
	ctrl := newBalanceController(ds, svc)
	r := newChildBalanceRouter(t, ctrl, mustUser(parentID, model.BillingModeCredits))

	req := httptest.NewRequest(http.MethodGet, "/v1/users/children/303/balance", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
}

// TestGetChildBalance_Unauthenticated returns 401.
func TestGetChildBalance_Unauthenticated(t *testing.T) {
	db := newBalanceTestDB(t)
	ds := store.NewTestStore(db)
	svc := membership.NewMembershipService(db)

	ctrl := newBalanceController(ds, svc)
	r := newChildBalanceRouter(t, ctrl, nil) // no user

	req := httptest.NewRequest(http.MethodGet, "/v1/users/children/101/balance", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestGetChildBalance_InvalidChildID returns 400 for non-numeric child_id.
func TestGetChildBalance_InvalidChildID(t *testing.T) {
	db := newBalanceTestDB(t)
	ds := store.NewTestStore(db)
	svc := membership.NewMembershipService(db)

	ctrl := newBalanceController(ds, svc)
	r := newChildBalanceRouter(t, ctrl, mustUser(1, model.BillingModeCredits))

	req := httptest.NewRequest(http.MethodGet, "/v1/users/children/notanumber/balance", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
