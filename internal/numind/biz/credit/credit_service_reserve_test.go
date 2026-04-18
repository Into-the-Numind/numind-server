package credit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newCreditReserveTestDB extends newCreditTestDB with the reservation tables.
// Because CreditReservation.Status / FinalizeReason are MySQL ENUMs, we
// bypass AutoMigrate for those fields and hand-roll the CREATE TABLE in
// SQLite-compatible SQL (plain TEXT columns — SQLite has no native ENUM).
func newCreditReserveTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// AutoMigrate the tables that have no MySQL-specific types.
	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditPackage{},
		&model.CreditTransaction{},
		&model.UsageRecord{},
	))

	// Hand-roll the reservation tables so SQLite accepts the ENUM columns as TEXT.
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    reference_type TEXT NOT NULL,
    reference_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    reserved_credits INTEGER NOT NULL,
    coefficient_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'reserved',
    actual_cost_cents INTEGER,
    delta INTEGER,
    finalize_reason TEXT,
    idempotency_key TEXT,
    reconciled_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uk_idempotency_key ON credit_reservation(idempotency_key);`).Error)

	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation_item (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    reservation_id INTEGER NOT NULL,
    package_id INTEGER NOT NULL,
    credits INTEGER NOT NULL,
    package_type TEXT NOT NULL,
    package_expires_at DATETIME NOT NULL,
    seq INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uk_reservation_seq ON credit_reservation_item(reservation_id, seq);`).Error)

	return db
}

// newCreditsUser returns a credits-mode user with no membership constraints
// (the default for Reserve path tests).
func newCreditsUser(id uint) *model.User {
	u := &model.User{
		BillingMode: model.BillingModeCredits,
		UserTier:    model.UserTierStandard,
	}
	u.ID = id
	return u
}

// --- Task C.3: creditsImpl.Reserve ---

// TestReserve_HappyPath_SinglePackage verifies Reserve deducts credits FIFO
// and persists one credit_reservation + one credit_reservation_item.
func TestReserve_HappyPath_SinglePackage(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil)

	userID := uint(200)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	idemp := "sop_run:1:1"
	rsv, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 180, 1, &idemp)
	require.NoError(t, err)
	require.NotNil(t, rsv)
	assert.Equal(t, userID, rsv.UserID)
	assert.Equal(t, credit.OpSopRun, rsv.Operation)
	assert.EqualValues(t, 180, rsv.ReservedCredits)
	assert.EqualValues(t, 1, rsv.CoefficientID)
	assert.Equal(t, credit.StatusReserved, rsv.Status)
	require.NotNil(t, rsv.IdempotencyKey)
	assert.Equal(t, idemp, *rsv.IdempotencyKey)
	require.Len(t, rsv.Items, 1)
	assert.Equal(t, 1, rsv.Items[0].Seq)
	assert.EqualValues(t, 180, rsv.Items[0].Credits)
	assert.Equal(t, model.CreditTypeSubscription, rsv.Items[0].PackageType)

	// DB row written
	var dbRsv model.CreditReservation
	require.NoError(t, db.First(&dbRsv, rsv.ID).Error)
	assert.Equal(t, "reserved", dbRsv.Status)
	assert.EqualValues(t, 180, dbRsv.ReservedCredits)

	// Item written with seq=1
	var items []model.CreditReservationItem
	require.NoError(t, db.Where("reservation_id = ?", rsv.ID).Order("seq ASC").Find(&items).Error)
	require.Len(t, items, 1)
	assert.Equal(t, 1, items[0].Seq)

	// Balance decremented
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 820, acc.Balance) // 1000-180
}

// TestReserve_FIFOCrossPackage verifies Reserve spans FIFO across two
// packages (sub → booster) and persists seq=1, seq=2 items in order.
func TestReserve_FIFOCrossPackage(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil)

	userID := uint(201)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 50, RemainCredits: 50,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{Type: model.CreditTypeBooster, TotalCredits: 600, RemainCredits: 600,
			ActivatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)},
	})

	rsv, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 150, 2, nil)
	require.NoError(t, err)
	require.Len(t, rsv.Items, 2)

	assert.Equal(t, 1, rsv.Items[0].Seq)
	assert.EqualValues(t, 50, rsv.Items[0].Credits)
	assert.Equal(t, model.CreditTypeSubscription, rsv.Items[0].PackageType)

	assert.Equal(t, 2, rsv.Items[1].Seq)
	assert.EqualValues(t, 100, rsv.Items[1].Credits)
	assert.Equal(t, model.CreditTypeBooster, rsv.Items[1].PackageType)

	// No idempotency_key on this call
	assert.Nil(t, rsv.IdempotencyKey)
}

// TestReserve_InsufficientRollsBack verifies that insufficient balance
// returns ErrInsufficientCredits and leaves zero reservation rows.
func TestReserve_InsufficientRollsBack(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil)

	userID := uint(202)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 50, RemainCredits: 50,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	_, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 200, 1, nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, credit.ErrInsufficientCredits))

	// Transaction rolled back: zero reservation rows, balance unchanged.
	var rsvCount int64
	require.NoError(t, db.Model(&model.CreditReservation{}).Count(&rsvCount).Error)
	assert.EqualValues(t, 0, rsvCount)

	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 50, acc.Balance)
}

// TestReserve_IdempotencyReturnsExisting verifies that a duplicate Reserve
// with the same idempotency_key returns the pre-existing reservation WITHOUT
// double-deducting credits.
func TestReserve_IdempotencyReturnsExisting(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil)

	userID := uint(203)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	idemp := "sop_run:999:1"
	rsv1, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 120, 1, &idemp)
	require.NoError(t, err)

	// Second call with same idempotency_key — must return same reservation.
	rsv2, err := svc.Reserve(context.Background(), user, credit.OpSopRun, 120, 1, &idemp)
	require.NoError(t, err)
	assert.Equal(t, rsv1.ID, rsv2.ID, "idempotency key must dedupe")

	// Balance decremented once only.
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 880, acc.Balance, "idempotent retry must not double-deduct")

	// Only one reservation row persisted.
	var rsvCount int64
	require.NoError(t, db.Model(&model.CreditReservation{}).Where("user_id = ?", userID).Count(&rsvCount).Error)
	assert.EqualValues(t, 1, rsvCount)
}

// TestReserve_GetBalanceCredits verifies that credits-mode GetBalance returns
// the package-based breakdown (sub + booster, no RemainingRuns).
func TestReserve_GetBalanceCredits(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil)

	userID := uint(204)
	user := newCreditsUser(userID)
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 2000, RemainCredits: 1800,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{Type: model.CreditTypeBooster, TotalCredits: 600, RemainCredits: 300,
			ActivatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)},
	})

	bal, err := svc.GetBalance(context.Background(), user)
	require.NoError(t, err)
	assert.Equal(t, model.BillingModeCredits, bal.BillingMode)
	assert.EqualValues(t, 2000, bal.SubTotal)
	assert.EqualValues(t, 1800, bal.SubRemain)
	assert.EqualValues(t, 600, bal.BoosterTotal)
	assert.EqualValues(t, 300, bal.BoosterRemain)
	assert.Nil(t, bal.RemainingRuns, "credits mode must not set RemainingRuns")
}
