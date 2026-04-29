package membership_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestDB creates an isolated in-memory SQLite DB for membership biz tests.
// Uses raw DDL (not AutoMigrate) to avoid SQLite ENUM incompatibility with
// GORM tag `type:enum(...)` on MySQL models. This mirrors the approach used
// in store/membership/test_helpers_test.go.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	migrateMembershipSchema(t, db)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// migrateMembershipSchema creates membership tables for SQLite unit tests.
// Raw DDL mirrors the MySQL schema defined in the GORM models, with ENUM
// columns mapped to TEXT (SQLite has no native ENUM type).
func migrateMembershipSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
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
		`CREATE INDEX IF NOT EXISTS idx_sub_expires_at ON subscription (expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_sub_granter_expires ON subscription (granter_user_id, expires_at)`,
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
		`CREATE INDEX IF NOT EXISTS idx_trial_expires_at ON trial_grant (expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_trial_granter_expires ON trial_grant (granter_user_id, expires_at)`,
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
		`CREATE INDEX IF NOT EXISTS idx_cycle_user_end ON credit_cycle (user_id, cycle_end)`,
		`CREATE TABLE IF NOT EXISTS user_booster_balance (
			user_id            INTEGER PRIMARY KEY,
			credits_remaining  INTEGER NOT NULL DEFAULT 0,
			updated_at         DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_booster_updated_at ON user_booster_balance (updated_at)`,
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
		`CREATE INDEX IF NOT EXISTS idx_event_user_occurred ON membership_event (user_id, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_event_type_occurred ON membership_event (event_type, occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_event_granter_occurred ON membership_event (granter_user_id, occurred_at)`,
	}
	for _, stmt := range ddl {
		require.NoError(t, db.Exec(stmt).Error, "DDL: %s", stmt[:50])
	}
}

// seedUserPair is a placeholder for future tasks that require a User table.
// GrantTrial does not query the users table, so this is a no-op stub that
// allows subsequent biz tasks to extend it without changing the helper signature.
func seedUserPair(t *testing.T, db *gorm.DB, parentID, childID uint64) {
	t.Helper()
	// GrantTrial does not join the users table; no seeding required here.
	// Phase 3+ tasks (GrantSubscription) that check parent-child relationships
	// should extend this helper to insert rows into the users table.
	_ = parentID
	_ = childID
}
