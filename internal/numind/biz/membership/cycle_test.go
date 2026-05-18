// White-box tests for ensureCurrentCycle (package membership, same package).
// These tests call the unexported ensureCurrentCycle directly to verify the
// lazy-create semantics and boundary conditions (§3.4).
package membership

import (
	"context"
	"strings"
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
	// Use file-based in-memory URI with a unique name per test so that all
	// connections within this test share the same SQLite in-memory database.
	// With plain ":memory:", each pooled connection gets its own empty schema;
	// with a named shared URI, all connections see the same tables and data.
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
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
		// credit_transaction is required by DeductCreditsTx (T1 ledger writes).
		`CREATE TABLE IF NOT EXISTS credit_transaction (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id          INTEGER NOT NULL,
			package_id       INTEGER NOT NULL DEFAULT 0,
			source_type      TEXT,
			source_id        INTEGER,
			amount           INTEGER NOT NULL,
			operation        TEXT NOT NULL DEFAULT '',
			usage_record_id  INTEGER,
			biz_ref_type     TEXT NOT NULL DEFAULT '',
			biz_ref_id       TEXT NOT NULL DEFAULT '',
			created_at       DATETIME NOT NULL
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

// ─────────────────────────────────────────────────────────────────────────────
// T1: DeductCreditsTx source_type population tests
//
// These tests verify that DeductionResult.Items carry the correct SourceType
// and SourceID for each pool, which callers use to populate
// credit_transaction.source_type / credit_transaction.source_id (T1 migration).
// ─────────────────────────────────────────────────────────────────────────────

// insertTrialGrant directly inserts a trial_grant row and returns it.
func insertTrialGrant(t *testing.T, db *gorm.DB, userID uint64, expiresAt time.Time, creditsRemaining int) *model.TrialGrant {
	t.Helper()
	now := time.Now().UTC()
	tg := &model.TrialGrant{
		UserID:           userID,
		GrantedAt:        now,
		ExpiresAt:        expiresAt,
		CreditsRemaining: creditsRemaining,
		Source:           model.SourceB2BGrant,
		CreatedAt:        now,
	}
	require.NoError(t, db.Create(tg).Error)
	return tg
}

// insertBoosterBalance inserts a user_booster_balance row and returns it.
func insertBoosterBalance(t *testing.T, db *gorm.DB, userID uint64, credits int64) *model.UserBoosterBalance {
	t.Helper()
	ubb := &model.UserBoosterBalance{
		UserID:           userID,
		CreditsRemaining: credits,
		UpdatedAt:        time.Now().UTC(),
	}
	require.NoError(t, db.Create(ubb).Error)
	return ubb
}

// TestDeductCreditsTx_SourceType_Trial verifies that a deduction from the trial
// pool produces a DeductItem with SourceType="trial" and SourceID=trial_grant.id.
// This SourceType value is used by callers to populate credit_transaction.source_type.
func TestDeductCreditsTx_SourceType_Trial(t *testing.T) {
	db := newCycleTestDB(t)
	svc := NewMembershipService(db)
	ctx := context.Background()

	userID := uint64(10)
	now := time.Now().UTC()
	futureExpiry := now.Add(72 * time.Hour)

	tg := insertTrialGrant(t, db, userID, futureExpiry, 200)

	var result *DeductionResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		result, e = svc.DeductCreditsTx(ctx, tx, userID, 50, "test:trial_only", now)
		return e
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, int64(50), result.FromTrial)
	assert.Equal(t, int64(0), result.FromCycle)
	assert.Equal(t, int64(0), result.FromBooster)
	require.Len(t, result.Items, 1, "one DeductItem for trial pool")

	item := result.Items[0]
	assert.Equal(t, DeductSourceTrial, item.SourceType, "SourceType must be 'trial'")
	assert.Equal(t, tg.ID, item.SourceID, "SourceID must be trial_grant.id")
	assert.Equal(t, int64(50), item.Amount)
}

// TestDeductCreditsTx_SourceType_Cycle verifies that a deduction from the cycle
// pool produces a DeductItem with SourceType="cycle" and SourceID=credit_cycle.id.
func TestDeductCreditsTx_SourceType_Cycle(t *testing.T) {
	db := newCycleTestDB(t)
	svc := NewMembershipService(db)
	ctx := context.Background()

	userID := uint64(11)
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	insertSub(t, db, userID, start, expires, 3)

	txNow := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)

	var result *DeductionResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		result, e = svc.DeductCreditsTx(ctx, tx, userID, 100, "test:cycle_only", txNow)
		return e
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, int64(0), result.FromTrial)
	assert.Equal(t, int64(100), result.FromCycle)
	assert.Equal(t, int64(0), result.FromBooster)
	require.Len(t, result.Items, 1, "one DeductItem for cycle pool")

	item := result.Items[0]
	assert.Equal(t, DeductSourceCycle, item.SourceType, "SourceType must be 'cycle'")
	assert.NotZero(t, item.SourceID, "SourceID must be credit_cycle.id (non-zero)")
}

// TestDeductCreditsTx_SourceType_Booster verifies that a deduction from the
// booster pool (after trial is exhausted) produces a DeductItem with
// SourceType="booster" and SourceID=userID (user_booster_balance.user_id).
func TestDeductCreditsTx_SourceType_Booster(t *testing.T) {
	db := newCycleTestDB(t)
	svc := NewMembershipService(db)
	ctx := context.Background()

	userID := uint64(12)
	now := time.Now().UTC()
	// Trial with only 10 credits → deduct 50 → overflows into booster.
	tg := insertTrialGrant(t, db, userID, now.Add(72*time.Hour), 10)
	insertBoosterBalance(t, db, userID, 600)

	var result *DeductionResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		result, e = svc.DeductCreditsTx(ctx, tx, userID, 50, "test:trial_then_booster", now)
		return e
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, int64(10), result.FromTrial, "all 10 trial credits consumed first")
	assert.Equal(t, int64(40), result.FromBooster, "remaining 40 from booster")
	require.Len(t, result.Items, 2, "two DeductItems: trial + booster")

	trialItem := result.Items[0]
	assert.Equal(t, DeductSourceTrial, trialItem.SourceType, "first item SourceType='trial'")
	assert.Equal(t, tg.ID, trialItem.SourceID, "first item SourceID=trial_grant.id")

	boosterItem := result.Items[1]
	assert.Equal(t, DeductSourceBooster, boosterItem.SourceType, "second item SourceType='booster'")
	assert.Equal(t, userID, boosterItem.SourceID, "booster SourceID=userID (user_booster_balance PK)")
}

// TestDeductCreditsTx_SourceType_TrialThenCycleThenBooster verifies the full
// three-pool priority ordering (trial → cycle → booster) with SourceType
// correctly set on each DeductItem. This is the critical path for ensuring
// credit_transaction.source_type correctly identifies each pool.
func TestDeductCreditsTx_SourceType_TrialThenCycleThenBooster(t *testing.T) {
	db := newCycleTestDB(t)
	svc := NewMembershipService(db)
	ctx := context.Background()

	userID := uint64(13)
	// Subscription: 3 months starting today
	start := time.Now().UTC().Truncate(time.Second)
	expires := start.AddDate(0, 3, 0)
	insertSub(t, db, userID, start, expires, 3)
	// Trial: 50 credits remaining
	tg := insertTrialGrant(t, db, userID, start.Add(72*time.Hour), 50)
	// Booster: 600 credits
	insertBoosterBalance(t, db, userID, 600)

	// Deduct 2100: 50 trial + 2000 cycle + 50 booster
	var result *DeductionResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		result, e = svc.DeductCreditsTx(ctx, tx, userID, 2100, "test:all_three_pools", start.Add(time.Hour))
		return e
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, int64(50), result.FromTrial)
	assert.Equal(t, int64(2000), result.FromCycle)
	assert.Equal(t, int64(50), result.FromBooster)
	require.Len(t, result.Items, 3, "three DeductItems: trial, cycle, booster")

	assert.Equal(t, DeductSourceTrial, result.Items[0].SourceType)
	assert.Equal(t, tg.ID, result.Items[0].SourceID)
	assert.Equal(t, int64(50), result.Items[0].Amount)

	assert.Equal(t, DeductSourceCycle, result.Items[1].SourceType)
	assert.NotZero(t, result.Items[1].SourceID)
	assert.Equal(t, int64(2000), result.Items[1].Amount)

	assert.Equal(t, DeductSourceBooster, result.Items[2].SourceType)
	assert.Equal(t, userID, result.Items[2].SourceID)
	assert.Equal(t, int64(50), result.Items[2].Amount)
}

// TestRefundCreditsTx_SourceExpired_TriggersRefundLost verifies the
// "refund lost" fallback in RefundCreditsTx (cycle.go:483-502): when the
// original deduction source is expired AND there is no active booster /
// active cycle to fall back to, the refund cannot be returned anywhere.
//
// Expected post-condition:
//   - refundedAmount returned is 0 (no pool credited)
//   - refundedTo is empty (no DeductSource matched)
//   - the expired source row is NOT mutated (credits_remaining stays put)
//   - a membership_event row with event_type='refund_lost' is created,
//     ProductType='trial' (the original deduction source), Source='system'
//   - no credit_transaction row is written (writeLedgerRefund only fires
//     on a successful refund; the lost path emits a membership_event only)
func TestRefundCreditsTx_SourceExpired_TriggersRefundLost(t *testing.T) {
	db := newCycleTestDB(t)
	svc := NewMembershipService(db)
	ctx := context.Background()

	userID := uint64(20)
	now := time.Now().UTC()
	// Trial that EXPIRED 24h ago — original source unavailable for refund.
	expiredTrial := insertTrialGrant(t, db, userID, now.Add(-24*time.Hour), 0)
	originalRemaining := expiredTrial.CreditsRemaining

	// No active sub, no active trial, no booster row → all fallback paths
	// (Step 2 / Step 3 in cycle.go:448-481) skip → refund_lost.

	var refundedTo DeductSource
	var refundedID uint64
	var refundedAmount int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var e error
		refundedTo, refundedID, refundedAmount, e = svc.RefundCreditsTx(
			ctx, tx, userID, DeductSourceTrial, expiredTrial.ID, 30, now,
		)
		return e
	})
	require.NoError(t, err, "refund_lost path returns nil error (caller distinguishes via amount==0)")

	// Return values: nothing credited.
	assert.Equal(t, DeductSource(""), refundedTo, "refundedTo empty when refund is lost")
	assert.Equal(t, uint64(0), refundedID, "refundedID zero when refund is lost")
	assert.Equal(t, int64(0), refundedAmount, "refundedAmount=0 signals refund lost")

	// Expired trial row must NOT be mutated.
	var trialAfter model.TrialGrant
	require.NoError(t, db.First(&trialAfter, expiredTrial.ID).Error)
	assert.Equal(t, originalRemaining, trialAfter.CreditsRemaining,
		"expired trial credits_remaining must not change on refund_lost")

	// membership_event row created with event_type='refund_lost'.
	var events []model.MembershipEvent
	require.NoError(t, db.Where("user_id = ? AND event_type = ?", userID, model.EventTypeRefundLost).
		Find(&events).Error)
	require.Len(t, events, 1, "exactly one refund_lost event must be written")
	ev := events[0]
	assert.Equal(t, string(DeductSourceTrial), ev.ProductType,
		"ProductType records the original DeductSource for audit")
	assert.Equal(t, int64(30), ev.AmountCents,
		"AmountCents repurposed to hold the lost credit amount (see RefundCreditsTx godoc)")
	assert.Equal(t, model.SourceSystem, ev.Source,
		"Source='system' to keep B2B / self_purchase reports clean")

	// P2#11: refund_lost path also writes a zero-amount credit_transaction row
	// so the audit invariant SUM(credit_transaction)==net flow per user holds.
	// The lost amount is recorded on membership_event above; the credit_transaction
	// exists for ledger completeness with operation='refund_lost' and amount=0.
	type lostRow struct {
		Amount     int64
		Operation  string
		SourceType *string
		SourceID   *uint64
	}
	var rows []lostRow
	require.NoError(t, db.Table("credit_transaction").
		Select("amount, operation, source_type, source_id").
		Where("user_id = ?", userID).Scan(&rows).Error)
	require.Len(t, rows, 1, "exactly one refund_lost credit_transaction must be written (P2#11)")
	got := rows[0]
	assert.Equal(t, int64(0), got.Amount,
		"refund_lost ledger row has amount=0 so balance sums are unaffected")
	assert.Equal(t, "refund_lost", got.Operation,
		"refund_lost ledger row uses operation='refund_lost' for reconciliation joins")
	require.NotNil(t, got.SourceType, "SourceType must reference the original source")
	assert.Equal(t, string(DeductSourceTrial), *got.SourceType)
	require.NotNil(t, got.SourceID, "SourceID must reference the original reservation source")
	assert.Equal(t, expiredTrial.ID, *got.SourceID)
}
