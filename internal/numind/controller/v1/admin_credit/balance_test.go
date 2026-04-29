package admin_credit_test

// balance_test.go — HTTP handler tests for GET /v1/admin/users/:user_id/balance.
//
// GetUserBalance only uses membershipSvc; creditBiz is not exercised here.
// We pass nil for creditBiz in New() — only GetUserBalance is called in these tests.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/membership"
	admincredit "numind-server/internal/numind/controller/v1/admin_credit"
	"numind-server/internal/numind/store"
)

// ---------------------------------------------------------------------------
// DB + helpers
// ---------------------------------------------------------------------------

func newAdminBalanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	ddl := []string{
		`CREATE TABLE IF NOT EXISTS subscription (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL UNIQUE,
			first_started_at DATETIME NOT NULL,
			current_started_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL,
			total_months_purchased INTEGER NOT NULL,
			source TEXT NOT NULL DEFAULT 'b2b_grant',
			granter_user_id INTEGER,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS trial_grant (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL UNIQUE,
			granted_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL,
			credits_remaining INTEGER NOT NULL DEFAULT 200,
			source TEXT NOT NULL DEFAULT 'b2b_grant',
			granter_user_id INTEGER,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS credit_cycle (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			subscription_id INTEGER NOT NULL,
			cycle_start DATETIME NOT NULL,
			cycle_end DATETIME NOT NULL,
			credits_granted INTEGER NOT NULL DEFAULT 0,
			credits_remaining INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(user_id, cycle_start)
		)`,
		`CREATE TABLE IF NOT EXISTS user_booster_balance (
			user_id INTEGER PRIMARY KEY,
			credits_remaining INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS membership_event (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			product_type TEXT NOT NULL,
			months INTEGER,
			quantity INTEGER,
			amount_cents INTEGER NOT NULL DEFAULT 0,
			source TEXT NOT NULL,
			granter_user_id INTEGER,
			idempotency_key TEXT UNIQUE,
			subscription_id INTEGER,
			occurred_at DATETIME NOT NULL
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

// newAdminBalanceRouter mounts GetUserBalance under the admin route.
func newAdminBalanceRouter(t *testing.T, ctrl *admincredit.AdminCreditWithMembership) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/admin/users/:user_id/balance", ctrl.GetUserBalance)
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestAdminGetUserBalance_FreeUser verifies a user with no membership returns
// membership_state=free and zero balance fields.
func TestAdminGetUserBalance_FreeUser(t *testing.T) {
	db := newAdminBalanceTestDB(t)
	ds := store.NewTestStore(db)
	svc := membership.NewMembershipService(db)

	// creditBiz=nil is safe: GetUserBalance does not call creditBiz.
	ctrl := admincredit.NewWithMembership(admincredit.New(nil, ds), svc)
	r := newAdminBalanceRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/users/999/balance", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                          `json:"code"`
		Data admincredit.FullBalanceView  `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, "free", env.Data.MembershipState)
	assert.Equal(t, int64(0), env.Data.BoosterTotal)
	assert.Equal(t, int64(0), env.Data.BoosterUsable)
}

// TestAdminGetUserBalance_IncludesBooster verifies booster fields are populated.
func TestAdminGetUserBalance_IncludesBooster(t *testing.T) {
	db := newAdminBalanceTestDB(t)
	ds := store.NewTestStore(db)
	svc := membership.NewMembershipService(db)

	// Insert active trial + booster balance for user 42.
	require.NoError(t, db.Exec(`INSERT INTO trial_grant
		(user_id, granted_at, expires_at, credits_remaining, source, created_at)
		VALUES (42, datetime('now'), datetime('now','+3 days'), 200, 'b2b_grant', datetime('now'))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO user_booster_balance (user_id, credits_remaining, updated_at)
		VALUES (42, 600, datetime('now'))`).Error)

	ctrl := admincredit.NewWithMembership(admincredit.New(nil, ds), svc)
	r := newAdminBalanceRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/users/42/balance", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                          `json:"code"`
		Data admincredit.FullBalanceView  `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, "trial", env.Data.MembershipState)
	assert.Equal(t, int64(600), env.Data.BoosterTotal, "admin should see booster_total")
	// Trial is active → booster is NOT frozen → BoosterUsable == BoosterTotal.
	assert.Equal(t, int64(600), env.Data.BoosterUsable)
}

// TestAdminGetUserBalance_InvalidID returns 400 for non-numeric user_id.
func TestAdminGetUserBalance_InvalidID(t *testing.T) {
	db := newAdminBalanceTestDB(t)
	ds := store.NewTestStore(db)
	svc := membership.NewMembershipService(db)
	ctrl := admincredit.NewWithMembership(admincredit.New(nil, ds), svc)
	r := newAdminBalanceRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/users/notanumber/balance", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
