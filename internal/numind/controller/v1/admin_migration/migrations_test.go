// Package admin_migration_test covers the billing-mode-init migration
// endpoints. Because the migration is irreversible in prod, tests focus on:
//   - status endpoint correctly classifies PENDING vs EXECUTED
//   - pre-migration stats partition users by tier
//   - execute endpoint flips only in-period users (billing_mode guard)
//   - execute is idempotent (repeat call migrates 0)
//   - action_log row is written for audit trail
package admin_migration_test

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

	admin_migration "numind-server/internal/numind/controller/v1/admin_migration"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// test harness
// ---------------------------------------------------------------------------

// userTableDDL mirrors the MySQL user columns we touch, using SQLite-compatible
// types. The production ENUM('legacy_tier','credits') is flattened to TEXT; the
// CHECK constraint reproduces the allowed values so bad writes would be caught.
const userTableDDL = `
CREATE TABLE IF NOT EXISTS ` + "`user`" + ` (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	created_at DATETIME,
	updated_at DATETIME,
	deleted_at DATETIME,
	phone TEXT,
	nickname TEXT,
	avatar_url TEXT,
	parent_user_id INTEGER,
	total_sop_runs INTEGER DEFAULT 0,
	monthly_sop_runs INTEGER DEFAULT 0,
	monthly_reset_at DATETIME,
	user_tier TEXT DEFAULT 'free',
	tier_expires DATETIME,
	billing_mode TEXT NOT NULL DEFAULT 'credits' CHECK(billing_mode IN ('legacy_tier','credits')),
	username TEXT,
	password TEXT,
	is_admin INTEGER DEFAULT 0,
	status INTEGER DEFAULT 0,
	last_login DATETIME
);
`

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use a per-test in-memory DB (no cache=shared) so parallel test runs under
	// -race don't see one another's writes. The sqlite "file::memory:?cache=shared"
	// DSN is global-ish; using just ":memory:" with MaxOpenConns=1 isolates us.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Hand-roll user table (see userTableDDL).
	require.NoError(t, db.Exec(userTableDDL).Error)
	// action_log is a plain ActionLogM struct — AutoMigrate works.
	require.NoError(t, db.AutoMigrate(&model.ActionLogM{}))
	return db
}

// seedUser creates a test user matching the hand-rolled user table schema.
// We use Exec because GORM's Create on model.User would try to materialize
// AutoMigrate and hit the ENUM column parse issue.
func seedUser(t *testing.T, db *gorm.DB, id uint, tier, billingMode string, expires *time.Time) {
	t.Helper()
	var exp interface{}
	if expires != nil {
		exp = *expires
	}
	err := db.Exec(`INSERT INTO user (id, user_tier, billing_mode, tier_expires, username) VALUES (?, ?, ?, ?, ?)`,
		id, tier, billingMode, exp, "user"+uintToStr(uint64(id))).Error
	require.NoError(t, err)
}

func adminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		u := &model.User{Username: "alice", IsAdmin: true}
		u.ID = 42
		c.Set("current_user", u)
		c.Next()
	}
}

func newRouter(ctrl *admin_migration.MigrationController) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(adminMiddleware())
	r.GET("/v1/admin/migrations/billing-mode-init/status", ctrl.GetInitStatus)
	r.POST("/v1/admin/migrations/billing-mode-init", ctrl.InitBillingMode)
	return r
}

// ---------------------------------------------------------------------------
// Status tests
// ---------------------------------------------------------------------------

// TestGetInitStatus_Pending returns PENDING + stats when no legacy_tier users.
func TestGetInitStatus_Pending(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	future := time.Now().Add(30 * 24 * time.Hour)
	// In-period users eligible for migration
	seedUser(t, db, 1, model.UserTierStandard, model.BillingModeCredits, &future)
	seedUser(t, db, 2, model.UserTierPremium, model.BillingModeCredits, &future)
	seedUser(t, db, 3, model.UserTierTrial, model.BillingModeCredits, &future)
	// Free — not eligible
	seedUser(t, db, 4, model.UserTierFree, model.BillingModeCredits, nil)

	ctrl := admin_migration.NewMigrationController(ds)
	r := newRouter(ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/migrations/billing-mode-init/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                                 `json:"code"`
		Data admin_migration.MigrationStatusResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.False(t, env.Data.AlreadyExecuted)
	require.NotNil(t, env.Data.PreMigrationStats)
	assert.Equal(t, int64(1), env.Data.PreMigrationStats.Trial)
	assert.Equal(t, int64(1), env.Data.PreMigrationStats.Standard)
	assert.Equal(t, int64(1), env.Data.PreMigrationStats.Premium)
	assert.Equal(t, int64(3), env.Data.PreMigrationStats.TotalInPeriod, "trial+standard+premium")
}

// TestGetInitStatus_AlreadyExecuted flips into EXECUTED when at least one
// legacy_tier user exists, and returns executed_at from the action_log audit.
func TestGetInitStatus_AlreadyExecuted(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	future := time.Now().Add(30 * 24 * time.Hour)
	seedUser(t, db, 1, model.UserTierStandard, model.BillingModeLegacyTier, &future)
	seedUser(t, db, 2, model.UserTierPremium, model.BillingModeCredits, &future)

	// Seed action_log entry (audit proof of prior execution).
	targetID := uint(42)
	require.NoError(t, db.Create(&model.ActionLogM{
		UserID: 42, Action: "billing_mode.init",
		Target: "billing_mode_migration", TargetID: &targetID, Detail: "1 user",
	}).Error)

	ctrl := admin_migration.NewMigrationController(ds)
	r := newRouter(ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/migrations/billing-mode-init/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                                 `json:"code"`
		Data admin_migration.MigrationStatusResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.True(t, env.Data.AlreadyExecuted)
	assert.Equal(t, int64(1), env.Data.MigratedCount, "count of legacy_tier users")
	require.NotNil(t, env.Data.ExecutedAt, "populated from latest action_log row")
}

// ---------------------------------------------------------------------------
// Execute tests
// ---------------------------------------------------------------------------

// TestInitBillingMode_Success flips in-period users to legacy_tier and writes
// one audit row. Free and expired users stay on credits.
func TestInitBillingMode_Success(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	future := time.Now().Add(30 * 24 * time.Hour)
	past := time.Now().Add(-30 * 24 * time.Hour)

	seedUser(t, db, 1, model.UserTierStandard, model.BillingModeCredits, &future) // migrate
	seedUser(t, db, 2, model.UserTierPremium, model.BillingModeCredits, &future)  // migrate
	seedUser(t, db, 3, model.UserTierTrial, model.BillingModeCredits, &future)    // migrate
	seedUser(t, db, 4, model.UserTierStandard, model.BillingModeCredits, &past)   // expired — stay
	seedUser(t, db, 5, model.UserTierFree, model.BillingModeCredits, nil)         // free — stay

	ctrl := admin_migration.NewMigrationController(ds)
	r := newRouter(ctrl)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/migrations/billing-mode-init", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                              `json:"code"`
		Data admin_migration.MigrationRunResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	assert.Equal(t, int64(3), env.Data.MigratedCount, "only in-period trial/standard/premium")
	assert.Equal(t, "alice", env.Data.ExecutedBy)

	// Assert the DB state
	assertBillingMode(t, db, 1, model.BillingModeLegacyTier)
	assertBillingMode(t, db, 2, model.BillingModeLegacyTier)
	assertBillingMode(t, db, 3, model.BillingModeLegacyTier)
	assertBillingMode(t, db, 4, model.BillingModeCredits, "expired must stay credits")
	assertBillingMode(t, db, 5, model.BillingModeCredits, "free must stay credits")

	// Audit row
	var logs []model.ActionLogM
	require.NoError(t, db.Where("action = ?", "billing_mode.init").Find(&logs).Error)
	require.Len(t, logs, 1, "one audit row written")
	assert.Equal(t, uint(42), logs[0].UserID)
}

// TestInitBillingMode_Idempotent — re-running migrates 0 more users.
// Uses a fresh gin.Engine per request to avoid a benign gin/sql goroutine
// race where sql.Rows.awaitDone from the first request outlives its context
// and reads c.Request while the engine is processing the next request.
func TestInitBillingMode_Idempotent(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	future := time.Now().Add(30 * 24 * time.Hour)
	seedUser(t, db, 1, model.UserTierStandard, model.BillingModeCredits, &future)

	ctrl := admin_migration.NewMigrationController(ds)

	// First call on its own router
	{
		r := newRouter(ctrl)
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/migrations/billing-mode-init", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var env1 struct {
			Data admin_migration.MigrationRunResp `json:"data"`
		}
		require.NoError(t, json.NewDecoder(w.Body).Decode(&env1))
		assert.Equal(t, int64(1), env1.Data.MigratedCount)
	}

	// Second call on a fresh router — WHERE billing_mode='credits' guard zeros the count
	{
		r := newRouter(ctrl)
		req2 := httptest.NewRequest(http.MethodPost, "/v1/admin/migrations/billing-mode-init", nil)
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req2)
		require.Equal(t, http.StatusOK, w2.Code, "body: %s", w2.Body.String())

		var env2 struct {
			Data admin_migration.MigrationRunResp `json:"data"`
		}
		require.NoError(t, json.NewDecoder(w2.Body).Decode(&env2))
		assert.Equal(t, int64(0), env2.Data.MigratedCount, "re-run is no-op")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertBillingMode(t *testing.T, db *gorm.DB, userID uint, expected string, msg ...string) {
	t.Helper()
	var got string
	require.NoError(t, db.Raw("SELECT billing_mode FROM user WHERE id = ?", userID).Scan(&got).Error)
	context := ""
	if len(msg) > 0 {
		context = " (" + msg[0] + ")"
	}
	assert.Equalf(t, expected, got, "user %d billing_mode mismatch%s", userID, context)
}

func uintToStr(n uint64) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
