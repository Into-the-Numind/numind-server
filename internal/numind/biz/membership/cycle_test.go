// White-box tests for ensureCurrentCycle (package membership, same package).
// These tests call the unexported ensureCurrentCycle directly to verify the
// lazy-create semantics and boundary conditions (§3.4).
package membership

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/errno"
	model "numind-server/internal/pkg/model/membership"
)

// ─────────────────────────────────────────────────────────────────────────────
// White-box test DB setup
// ─────────────────────────────────────────────────────────────────────────────

func newCycleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

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

// insertSub directly inserts a subscription row into the test DB and returns it.
func insertSub(t *testing.T, db *gorm.DB, userID uint64, currentStartedAt, expiresAt time.Time, months int) *model.Subscription {
	t.Helper()
	now := time.Now().UTC()
	sub := &model.Subscription{
		UserID:               userID,
		FirstStartedAt:       currentStartedAt,
		CurrentStartedAt:     currentStartedAt,
		ExpiresAt:            expiresAt,
		TotalMonthsPurchased: months,
		Source:               model.SourceB2BGrant,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	require.NoError(t, db.Create(sub).Error)
	return sub
}

// ─────────────────────────────────────────────────────────────────────────────
// TestEnsureCurrentCycle_FirstCall — creates cycle row on first call
// ─────────────────────────────────────────────────────────────────────────────

func TestEnsureCurrentCycle_FirstCall(t *testing.T) {
	db := newCycleTestDB(t)
	svc := NewMembershipService(db)
	ctx := context.Background()

	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC) // 3 months
	sub := insertSub(t, db, 1, start, expires, 3)

	txNow := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC) // still in month 0

	var cycle *model.CreditCycle
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		cycle, e = svc.ensureCurrentCycle(ctx, tx, sub, txNow)
		return e
	})
	require.NoError(t, err)
	require.NotNil(t, cycle)

	assert.Equal(t, sub.UserID, cycle.UserID)
	assert.Equal(t, sub.ID, cycle.SubscriptionID)
	assert.Equal(t, start, cycle.CycleStart)
	assert.Equal(t, cycleCredits, cycle.CreditsGranted)
	assert.Equal(t, cycleCredits, cycle.CreditsRemaining)

	// CycleEnd should be AnchorAddMonths(start, 1) = 2026-02-15
	expectedEnd := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, expectedEnd, cycle.CycleEnd)

	// Verify row in DB.
	var dbCycle model.CreditCycle
	require.NoError(t, db.Where("user_id = ? AND cycle_start = ?", sub.UserID, start).Take(&dbCycle).Error)
	assert.Equal(t, cycleCredits, dbCycle.CreditsGranted)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestEnsureCurrentCycle_Idempotent — second call returns same row, not a new one
// ─────────────────────────────────────────────────────────────────────────────

func TestEnsureCurrentCycle_Idempotent(t *testing.T) {
	db := newCycleTestDB(t)
	svc := NewMembershipService(db)
	ctx := context.Background()

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	sub := insertSub(t, db, 2, start, expires, 3)

	txNow := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	var cycle1, cycle2 *model.CreditCycle
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		cycle1, e = svc.ensureCurrentCycle(ctx, tx, sub, txNow)
		return e
	})
	require.NoError(t, err)

	// Mutate the row to simulate partial spend.
	require.NoError(t, db.Model(&model.CreditCycle{}).
		Where("id = ?", cycle1.ID).
		Update("credits_remaining", 1500).Error)

	// Second call inside a new transaction should return the existing row.
	err = db.Transaction(func(tx *gorm.DB) error {
		var e error
		cycle2, e = svc.ensureCurrentCycle(ctx, tx, sub, txNow)
		return e
	})
	require.NoError(t, err)
	require.NotNil(t, cycle2)

	// Same row ID → not re-created.
	assert.Equal(t, cycle1.ID, cycle2.ID)
	// Mutated credits_remaining should survive (row was not overwritten).
	assert.Equal(t, 1500, cycle2.CreditsRemaining)

	// Verify only one row in DB.
	var count int64
	require.NoError(t, db.Model(&model.CreditCycle{}).Where("user_id = ?", sub.UserID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestEnsureCurrentCycle_ExpiredSub — returns ErrSubscriptionExpired when
// txNow >= sub.ExpiresAt (cycle boundary = sub.ExpiresAt, so txNow >= cycleEnd)
// ─────────────────────────────────────────────────────────────────────────────

func TestEnsureCurrentCycle_ExpiredSub(t *testing.T) {
	db := newCycleTestDB(t)
	svc := NewMembershipService(db)
	ctx := context.Background()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) // 1 month
	sub := insertSub(t, db, 3, start, expires, 1)

	// txNow equals ExpiresAt — subscription boundary reached.
	txNow := expires

	var callErr error
	err := db.Transaction(func(tx *gorm.DB) error {
		_, callErr = svc.ensureCurrentCycle(ctx, tx, sub, txNow)
		return nil // don't roll back tx on business error
	})
	require.NoError(t, err)
	assert.ErrorIs(t, callErr, errno.ErrSubscriptionExpired)
}

