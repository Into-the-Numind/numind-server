// Tests for MembershipService.DeductCredits (§3.5 three-pool priority deduction).
// Placed in biz/credit to co-locate with the credit billing boundary tests.
package credit_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	biz "numind-server/internal/numind/biz/membership"
	"numind-server/internal/pkg/errno"
	model "numind-server/internal/pkg/model/membership"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test DB + Helpers
// ─────────────────────────────────────────────────────────────────────────────

// newDeductDB creates an isolated SQLite DB (temp file) with only the membership
// tables required by MembershipService.DeductCredits.
//
// We use a temporary FILE (not ":memory:") because:
//   - Plain ":memory:" gives each GORM pool connection its own in-memory DB,
//     making tables from one connection invisible to another.
//   - "file::memory:?cache=shared" shares one DB across connections but still
//     suffers from locking issues under concurrent reads inside a transaction.
//   - A temp file works reliably for nested/concurrent SQLite access in tests.
func newDeductDB(t *testing.T) *gorm.DB {
	t.Helper()
	f, err := os.CreateTemp("", "mship_deduct_*.sqlite")
	require.NoError(t, err)
	_ = f.Close()
	path := f.Name()
	t.Cleanup(func() { _ = os.Remove(path) })

	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

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

	return db
}

// insertActiveSub inserts an active subscription for userID, returning the row.
func insertActiveSub(t *testing.T, db *gorm.DB, userID uint64, months int, now time.Time) *model.Subscription {
	t.Helper()
	expiresAt := addMonths(now, months)
	sub := &model.Subscription{
		UserID:               userID,
		FirstStartedAt:       now,
		CurrentStartedAt:     now,
		ExpiresAt:            expiresAt,
		TotalMonthsPurchased: months,
		Source:               model.SourceB2BGrant,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	require.NoError(t, db.Create(sub).Error)
	return sub
}

// insertExpiredSub inserts an already-expired subscription for userID.
func insertExpiredSub(t *testing.T, db *gorm.DB, userID uint64, now time.Time) {
	t.Helper()
	pastStart := now.AddDate(0, -2, 0)
	expiresAt := now.AddDate(0, -1, 0) // expired 1 month ago
	sub := &model.Subscription{
		UserID:               userID,
		FirstStartedAt:       pastStart,
		CurrentStartedAt:     pastStart,
		ExpiresAt:            expiresAt,
		TotalMonthsPurchased: 1,
		Source:               model.SourceB2BGrant,
		CreatedAt:            pastStart,
		UpdatedAt:            pastStart,
	}
	require.NoError(t, db.Create(sub).Error)
}

// insertActiveTrial inserts an active trial grant for userID with given remaining credits.
func insertActiveTrial(t *testing.T, db *gorm.DB, userID uint64, remaining int, now time.Time) {
	t.Helper()
	tg := &model.TrialGrant{
		UserID:           userID,
		GrantedAt:        now,
		ExpiresAt:        now.AddDate(0, 0, 3),
		CreditsRemaining: remaining,
		Source:           model.SourceB2BGrant,
		CreatedAt:        now,
	}
	require.NoError(t, db.Create(tg).Error)
}

// insertBooster inserts a user_booster_balance row for userID.
func insertBooster(t *testing.T, db *gorm.DB, userID uint64, remaining int64) {
	t.Helper()
	bal := &model.UserBoosterBalance{
		UserID:           userID,
		CreditsRemaining: remaining,
		UpdatedAt:        time.Now().UTC(),
	}
	require.NoError(t, db.Create(bal).Error)
}

// addMonths is a simple month-adder for test helpers (not anchor-aware, good enough for setup).
func addMonths(t time.Time, n int) time.Time {
	return t.AddDate(0, n, 0)
}

// ─────────────────────────────────────────────────────────────────────────────
// setupActiveUserWithAllThreePools sets up a user with:
//   - active subscription (3 months from now, giving a fresh cycle with 2000 credits)
//   - active trial (200 credits)
//   - booster balance (1200 credits)
//
// Returns (db, svc, userID=20)
// ─────────────────────────────────────────────────────────────────────────────
func setupActiveUserWithAllThreePools(t *testing.T) (*gorm.DB, *biz.MembershipService, uint64) {
	t.Helper()
	db := newDeductDB(t)
	svc := biz.NewMembershipService(db)
	now := time.Now().UTC().Truncate(time.Second)
	userID := uint64(20)

	insertActiveSub(t, db, userID, 3, now)
	insertActiveTrial(t, db, userID, 200, now)
	insertBooster(t, db, userID, 1200)

	return db, svc, userID
}

// ─────────────────────────────────────────────────────────────────────────────
// setupExpiredSubWithBooster sets up a user with:
//   - expired subscription
//   - booster balance (600 credits) — should be FROZEN (INV-15)
//
// Returns (db, svc, userID=30)
// ─────────────────────────────────────────────────────────────────────────────
func setupExpiredSubWithBooster(t *testing.T) (*gorm.DB, *biz.MembershipService, uint64) {
	t.Helper()
	db := newDeductDB(t)
	svc := biz.NewMembershipService(db)
	now := time.Now().UTC().Truncate(time.Second)
	userID := uint64(30)

	insertExpiredSub(t, db, userID, now)
	insertBooster(t, db, userID, 600)

	return db, svc, userID
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestDeductCredits_InvalidAmount verifies amount <= 0 is rejected.
func TestDeductCredits_InvalidAmount(t *testing.T) {
	db := newDeductDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	_, err := svc.DeductCredits(ctx, 99, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrInvalidParameter)

	_, err = svc.DeductCredits(ctx, 99, -1)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrInvalidParameter)
}

// TestDeductCredits_TrialOnlyDeduction verifies trial credits are drained first.
func TestDeductCredits_TrialOnlyDeduction(t *testing.T) {
	db := newDeductDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	userID := uint64(21)

	// Only trial, no sub, no booster.
	insertActiveTrial(t, db, userID, 200, now)

	result, err := svc.DeductCredits(ctx, userID, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(100), result.FromTrial)
	assert.Equal(t, int64(0), result.FromCycle)
	assert.Equal(t, int64(0), result.FromBooster)

	// Verify trial remaining = 100.
	var tg model.TrialGrant
	require.NoError(t, db.Where("user_id = ?", userID).Take(&tg).Error)
	assert.Equal(t, 100, tg.CreditsRemaining)
}

// TestDeductCredits_TrialFirstThenCycle verifies trial → cycle priority when
// both pools are active.
func TestDeductCredits_TrialFirstThenCycle(t *testing.T) {
	_, svc, userID := setupActiveUserWithAllThreePools(t)
	ctx := context.Background()

	// Deduct 250 — should take 200 from trial, 50 from cycle.
	result, err := svc.DeductCredits(ctx, userID, 250)
	require.NoError(t, err)
	assert.Equal(t, int64(200), result.FromTrial)
	assert.Equal(t, int64(50), result.FromCycle)
	assert.Equal(t, int64(0), result.FromBooster)
}

// TestDeductCredits_BoosterFrozenWhenNoActiveMembership verifies INV-15:
// booster is frozen when subscription is expired AND trial is not active.
func TestDeductCredits_BoosterFrozenWhenNoActiveMembership(t *testing.T) {
	db, svc, userID := setupExpiredSubWithBooster(t)
	ctx := context.Background()

	// Attempt to deduct — no active trial or sub, so booster is frozen.
	_, err := svc.DeductCredits(ctx, userID, 100)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrInsufficientCredits)

	// Booster balance must be unchanged (INV-15).
	var bal model.UserBoosterBalance
	require.NoError(t, db.Where("user_id = ?", userID).Take(&bal).Error)
	assert.Equal(t, int64(600), bal.CreditsRemaining, "booster must not be touched when frozen")
}

// TestDeductCredits_BoosterUsedWhenTrialActive verifies that booster is
// accessible when only a trial is active (no subscription).
func TestDeductCredits_BoosterUsedWhenTrialActive(t *testing.T) {
	db := newDeductDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	userID := uint64(22)

	// Trial (50 credits) + booster (600 credits). No subscription.
	insertActiveTrial(t, db, userID, 50, now)
	insertBooster(t, db, userID, 600)

	// Deduct 200 — trial exhausted (50), then booster covers the rest (150).
	result, err := svc.DeductCredits(ctx, userID, 200)
	require.NoError(t, err)
	assert.Equal(t, int64(50), result.FromTrial)
	assert.Equal(t, int64(0), result.FromCycle)
	assert.Equal(t, int64(150), result.FromBooster)

	// Booster remaining = 600 - 150 = 450.
	var bal model.UserBoosterBalance
	require.NoError(t, db.Where("user_id = ?", userID).Take(&bal).Error)
	assert.Equal(t, int64(450), bal.CreditsRemaining)
}

// TestDeductCredits_InsufficientCredits verifies ErrInsufficientCredits when
// total pool capacity < requested amount.
func TestDeductCredits_InsufficientCredits(t *testing.T) {
	db := newDeductDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	userID := uint64(23)

	// Only 50 credits in trial.
	insertActiveTrial(t, db, userID, 50, now)

	_, err := svc.DeductCredits(ctx, userID, 200)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrInsufficientCredits)
}

// TestDeductCredits_AllThreePools verifies all three pools are drained in order
// when a large deduction is requested.
func TestDeductCredits_AllThreePools(t *testing.T) {
	db, svc, userID := setupActiveUserWithAllThreePools(t)
	ctx := context.Background()

	// Deduct 2900 — trial(200) + cycle(2000) + booster(700 out of 1200).
	result, err := svc.DeductCredits(ctx, userID, 2900)
	require.NoError(t, err)
	assert.Equal(t, int64(200), result.FromTrial)
	assert.Equal(t, int64(2000), result.FromCycle)
	assert.Equal(t, int64(700), result.FromBooster)
	assert.Equal(t, int64(2900), result.FromTrial+result.FromCycle+result.FromBooster)

	// Verify booster remaining = 1200 - 700 = 500.
	var bal model.UserBoosterBalance
	require.NoError(t, db.Where("user_id = ?", userID).Take(&bal).Error)
	assert.Equal(t, int64(500), bal.CreditsRemaining)
}
