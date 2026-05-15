// Tests for RechargeWithOrderTx (T5 cleanup: payment callback writes new tables only).
//
// Placed in the credit_test (external) package alongside existing credit biz tests.
package credit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	membershipmodel "numind-server/internal/pkg/model/membership"
)

// readBoosterRemaining reads credits_remaining from user_booster_balance
// using raw SQL to avoid SQLite datetime scan issues with time.Time fields.
func readBoosterRemaining(t *testing.T, db *gorm.DB, userID uint) int64 {
	t.Helper()
	var remaining int64
	err := db.Raw("SELECT COALESCE(credits_remaining, 0) FROM user_booster_balance WHERE user_id = ?", userID).
		Scan(&remaining).Error
	require.NoError(t, err, "read booster balance for user %d", userID)
	return remaining
}

// readBoosterEventCount counts booster_granted events for userID.
func readBoosterEventCount(t *testing.T, db *gorm.DB, userID uint) int64 {
	t.Helper()
	var count int64
	db.Raw("SELECT COUNT(*) FROM membership_event WHERE user_id = ? AND event_type = 'booster_granted'", userID).
		Scan(&count)
	return count
}

// newRechargeTestDB creates an isolated SQLite DB with both the legacy credit
// tables (for store.NewTestStore wiring) and the new membership tables (for
// BoosterBalances.Increment + Events.Create written by RechargeWithOrderTx).
func newRechargeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "_recharge?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Legacy credit tables (required by store.NewTestStore / GetOrCreateAccount).
	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditPackage{},
		&model.CreditTransaction{},
	), "auto-migrate legacy tables")

	// New membership tables required by BoosterBalances.Increment and Events.Create.
	require.NoError(t, db.AutoMigrate(
		&membershipmodel.UserBoosterBalance{},
		&membershipmodel.MembershipEvent{},
	), "auto-migrate membership tables")

	return db
}

// ─────────────────────────────────────────────────────────────────────────────
// T5 defensive error tests: non-booster product types must return
// ErrUnsupportedProductType (production-unreachable; guards future callers).
// ─────────────────────────────────────────────────────────────────────────────

// TestRechargeWithOrderTx_Trial_ReturnsUnsupportedProductType verifies that
// passing productType="trial" returns ErrUnsupportedProductType.
// Trial memberships must go through the B2B grant path (spec §5.10).
func TestRechargeWithOrderTx_Trial_ReturnsUnsupportedProductType(t *testing.T) {
	db := newRechargeTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)
	ctx := context.Background()

	err := db.Transaction(func(tx *gorm.DB) error {
		return biz.RechargeWithOrderTx(ctx, tx, 1, 42, model.ProductTypeTrial, 0)
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, credit.ErrUnsupportedProductType,
		"productType=trial must return ErrUnsupportedProductType — use B2B grant path instead")
}

// TestRechargeWithOrderTx_Monthly_ReturnsUnsupportedProductType verifies that
// passing productType="monthly" returns ErrUnsupportedProductType.
func TestRechargeWithOrderTx_Monthly_ReturnsUnsupportedProductType(t *testing.T) {
	db := newRechargeTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)
	ctx := context.Background()

	err := db.Transaction(func(tx *gorm.DB) error {
		return biz.RechargeWithOrderTx(ctx, tx, 1, 43, model.ProductTypeMonthly, 1)
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, credit.ErrUnsupportedProductType,
		"productType=monthly must return ErrUnsupportedProductType — use B2B grant path instead")
}

// TestRechargeWithOrderTx_Yearly_ReturnsUnsupportedProductType verifies that
// passing productType="yearly" returns ErrUnsupportedProductType.
func TestRechargeWithOrderTx_Yearly_ReturnsUnsupportedProductType(t *testing.T) {
	db := newRechargeTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)
	ctx := context.Background()

	err := db.Transaction(func(tx *gorm.DB) error {
		return biz.RechargeWithOrderTx(ctx, tx, 1, 44, model.ProductTypeYearly, 12)
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, credit.ErrUnsupportedProductType,
		"productType=yearly must return ErrUnsupportedProductType — use B2B grant path instead")
}

// ─────────────────────────────────────────────────────────────────────────────
// T5 booster path: must write user_booster_balance + membership_event.
// Must NOT write credit_package (the old table).
// ─────────────────────────────────────────────────────────────────────────────

// TestRechargeWithOrderTx_Booster_WritesNewTablesOnly verifies the T5 post-condition:
//   - user_booster_balance.credits_remaining = quantity × 600 (upsert)
//   - membership_event row written with event_type='booster_granted'
//   - credit_package table: 0 new rows (the old INSERT path is gone)
func TestRechargeWithOrderTx_Booster_WritesNewTablesOnly(t *testing.T) {
	db := newRechargeTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)
	ctx := context.Background()

	const (
		userID   = uint(201)
		orderID  = uint64(9001)
		quantity = 3 // 3 booster packs = 1800 credits
	)

	err := db.Transaction(func(tx *gorm.DB) error {
		return biz.RechargeWithOrderTx(ctx, tx, userID, orderID, model.ProductTypeBooster, quantity)
	})
	require.NoError(t, err, "booster recharge must succeed")

	// Verify user_booster_balance.credits_remaining = 3 × 600 = 1800.
	// Use raw SQL to avoid SQLite datetime scan issues with time.Time fields.
	remaining := readBoosterRemaining(t, db, userID)
	assert.Equal(t, int64(1800), remaining, "credits_remaining must equal quantity × 600")

	// Verify row actually exists (readBoosterRemaining returns 0 for missing too).
	var ubbCount int64
	db.Raw("SELECT COUNT(*) FROM user_booster_balance WHERE user_id = ?", userID).Scan(&ubbCount)
	assert.Equal(t, int64(1), ubbCount, "user_booster_balance row must be created")

	// Verify membership_event written with correct fields using raw queries.
	assert.Equal(t, int64(1), readBoosterEventCount(t, db, userID),
		"exactly one membership_event row must be written")

	type evtFields struct {
		Quantity    int
		AmountCents int64
		ProductType string
		Source      string
	}
	var evt evtFields
	require.NoError(t, db.Raw(
		`SELECT quantity, amount_cents, product_type, source
		   FROM membership_event
		  WHERE user_id = ? AND event_type = 'booster_granted'`, userID).
		Scan(&evt).Error, "read membership_event fields")
	assert.Equal(t, quantity, evt.Quantity, "event.Quantity must match purchased quantity")
	assert.Equal(t, int64(quantity)*2990, evt.AmountCents, "event.AmountCents must equal quantity × 2990")
	assert.Equal(t, model.ProductTypeBooster, evt.ProductType)
	assert.Equal(t, "self_purchase", evt.Source)

	// Verify credit_package table has 0 rows — the old INSERT path is gone (T5 invariant).
	var cpCount int64
	db.Model(&model.CreditPackage{}).Where("user_id = ?", userID).Count(&cpCount)
	assert.Equal(t, int64(0), cpCount,
		"credit_package must have 0 rows after T5 — booster no longer writes old table")
}

// TestRechargeWithOrderTx_Booster_QuantityFloor verifies that quantity<1 is
// treated as quantity=1 (safety floor from CreateOrder spec §5.2).
func TestRechargeWithOrderTx_Booster_QuantityFloor(t *testing.T) {
	db := newRechargeTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)
	ctx := context.Background()

	const (
		userID  = uint(202)
		orderID = uint64(9002)
	)

	err := db.Transaction(func(tx *gorm.DB) error {
		return biz.RechargeWithOrderTx(ctx, tx, userID, orderID, model.ProductTypeBooster, 0)
	})
	require.NoError(t, err)

	remaining := readBoosterRemaining(t, db, userID)
	assert.Equal(t, int64(600), remaining,
		"quantity=0 must be floored to 1, giving 600 credits")
}

// TestRechargeWithOrderTx_Booster_IdempotencyKeyUnique verifies that calling
// RechargeWithOrderTx twice with the same orderID (same idempotency_key)
// returns an error on the second call (DB UNIQUE on idempotency_key).
// In production the order's pay_status=pending guard in fulfillOrder prevents
// double fulfillment before RechargeWithOrderTx is even reached, but the
// membership_event UNIQUE index provides a second safety layer.
func TestRechargeWithOrderTx_Booster_IdempotencyKeyUnique(t *testing.T) {
	db := newRechargeTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)
	ctx := context.Background()

	const (
		userID  = uint(203)
		orderID = uint64(9003)
	)

	call := func() error {
		return db.Transaction(func(tx *gorm.DB) error {
			return biz.RechargeWithOrderTx(ctx, tx, userID, orderID, model.ProductTypeBooster, 1)
		})
	}

	require.NoError(t, call(), "first call must succeed")
	err := call()
	require.Error(t, err, "second call with same orderID must fail (idempotency_key UNIQUE)")

	// Balance must still be 600 (only first call counted, tx rolled back on second).
	assert.Equal(t, int64(600), readBoosterRemaining(t, db, userID),
		"balance must not be double-incremented")

	// Only one membership_event row.
	assert.Equal(t, int64(1), readBoosterEventCount(t, db, userID),
		"only one membership_event should exist")
}

// TestRechargeWithOrderTx_Booster_Accumulates verifies that multiple distinct
// booster orders accumulate credits in user_booster_balance (Increment is
// additive, not a replace).
func TestRechargeWithOrderTx_Booster_Accumulates(t *testing.T) {
	db := newRechargeTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)
	ctx := context.Background()

	const userID = uint(204)

	call := func(orderID uint64, qty int) {
		t.Helper()
		err := db.Transaction(func(tx *gorm.DB) error {
			return biz.RechargeWithOrderTx(ctx, tx, userID, orderID, model.ProductTypeBooster, qty)
		})
		require.NoError(t, err, "order %d must succeed", orderID)
	}

	call(9004, 1) // +600
	call(9005, 2) // +1200

	assert.Equal(t, int64(1800), readBoosterRemaining(t, db, userID),
		"two distinct orders should accumulate: 600 + 1200 = 1800")
	assert.Equal(t, int64(2), readBoosterEventCount(t, db, userID),
		"two membership_event rows, one per order")
}
