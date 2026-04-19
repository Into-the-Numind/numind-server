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

// newCreditTestDB creates an isolated in-memory SQLite DB preloaded with the
// minimum set of tables required by the deduction path (no credit_reservation
// tables — those use MySQL ENUM types which SQLite doesn't parse, and are
// unused by DeductCredits / DeductCreditsTx).
func newCreditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// file::memory:?cache=shared keeps all connections inside the GORM pool
	// sharing the same SQLite schema. With plain ":memory:" each pooled
	// connection gets its own empty DB — AutoMigrate on conn #1 is invisible
	// to conn #2, which is why credits_store.GetOrCreateAccount sometimes
	// sees "no such table: credit_account" even though AutoMigrate succeeded.
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	// Force the pool to a single connection for test determinism — also sidesteps
	// SQLite's "database is locked" errors under parallel read/write.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditPackage{},
		&model.CreditTransaction{},
		&model.UsageRecord{},
	), "auto-migrate")
	return db
}

// seedPackagesAndAccount inserts a user, credit_account and a set of active
// credit_packages. Packages are returned in FIFO order (earliest ExpiresAt
// first). Total balance is the sum of remain_credits.
func seedPackagesAndAccount(t *testing.T, db *gorm.DB, userID uint, pkgs []model.CreditPackage) {
	t.Helper()

	// Ensure account row exists
	var total int64
	for _, p := range pkgs {
		total += p.RemainCredits
	}
	acc := model.CreditAccount{UserID: userID, Balance: total, Status: "active"}
	require.NoError(t, db.Create(&acc).Error)

	for i := range pkgs {
		pkgs[i].UserID = userID
		if pkgs[i].Status == "" {
			pkgs[i].Status = model.CreditPackageActive
		}
		require.NoError(t, db.Create(&pkgs[i]).Error)
	}
}

// --- Task C.1: DeductCreditsTx external tx variant ---

// TestDeductCreditsTx_ReturnsFIFOItems verifies that DeductCreditsTx deducts
// credits FIFO by expires_at ASC and returns one PackageDeduction per package
// actually debited.
func TestDeductCreditsTx_ReturnsFIFOItems(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)

	now := time.Now()
	userID := uint(100)
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 50, RemainCredits: 50,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{Type: model.CreditTypeBooster, TotalCredits: 600, RemainCredits: 600,
			ActivatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)},
	})

	var items []credit.PackageDeduction
	err := db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		items, txErr = biz.DeductCreditsTx(context.Background(), tx, userID, 150, "reserve:sop_run")
		return txErr
	})
	require.NoError(t, err)

	require.Len(t, items, 2, "150 credits across sub(50)+booster(600) => 2 packages drained")
	// FIFO: first subscription (50), then booster (100)
	assert.Equal(t, int64(50), items[0].Credits)
	assert.Equal(t, model.CreditTypeSubscription, items[0].PackageType)
	assert.Equal(t, int64(100), items[1].Credits)
	assert.Equal(t, model.CreditTypeBooster, items[1].PackageType)

	// Verify package state: first drained (exhausted), second has 500 remaining
	var pkgs []model.CreditPackage
	require.NoError(t, db.Where("user_id = ?", userID).Order("expires_at ASC").Find(&pkgs).Error)
	assert.EqualValues(t, 0, pkgs[0].RemainCredits)
	assert.Equal(t, model.CreditPackageExhausted, pkgs[0].Status)
	assert.EqualValues(t, 500, pkgs[1].RemainCredits)
	assert.Equal(t, model.CreditPackageActive, pkgs[1].Status)

	// Balance updated
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 500, acc.Balance)
}

// TestDeductCreditsTx_InsufficientReturnsErrInsufficient verifies insufficient
// balance returns ErrInsufficientCredits and rolls back via the caller tx.
func TestDeductCreditsTx_InsufficientReturnsErrInsufficient(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)

	now := time.Now()
	userID := uint(101)
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 10, RemainCredits: 10,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	err := db.Transaction(func(tx *gorm.DB) error {
		_, dErr := biz.DeductCreditsTx(context.Background(), tx, userID, 100, "reserve:sop_run")
		return dErr
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, credit.ErrInsufficientCredits),
		"expected ErrInsufficientCredits, got %v", err)

	// Transaction rolled back: balance should be untouched (10)
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 10, acc.Balance)

	// No transactions created (rolled back)
	var txnCount int64
	require.NoError(t, db.Model(&model.CreditTransaction{}).Where("user_id = ?", userID).Count(&txnCount).Error)
	assert.EqualValues(t, 0, txnCount)
}

// TestDeductCreditsTx_RollsBackOnOuterError verifies that when the outer
// transaction fails AFTER DeductCreditsTx succeeds, the deduction is rolled back.
// This is the critical contract for Reserve: the outer tx composes DeductCreditsTx
// with credit_reservation INSERT, and either both succeed or both roll back.
func TestDeductCreditsTx_RollsBackOnOuterError(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)

	now := time.Now()
	userID := uint(102)
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 100, RemainCredits: 100,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	sentinel := errors.New("outer-tx-failure")
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := biz.DeductCreditsTx(context.Background(), tx, userID, 30, "reserve:sop_run"); err != nil {
			return err
		}
		// Simulate an outer-tx error after deduction succeeded.
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	// Balance should be unchanged (100) because the outer tx rolled back.
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 100, acc.Balance)

	// Package should still have 100 remaining.
	var pkg model.CreditPackage
	require.NoError(t, db.Where("user_id = ?", userID).First(&pkg).Error)
	assert.EqualValues(t, 100, pkg.RemainCredits)
	assert.Equal(t, model.CreditPackageActive, pkg.Status)

	// No CreditTransaction rows persisted.
	var txnCount int64
	require.NoError(t, db.Model(&model.CreditTransaction{}).Where("user_id = ?", userID).Count(&txnCount).Error)
	assert.EqualValues(t, 0, txnCount)
}

// TestDeductCredits_LegacyWrapperStillWorks verifies that the existing
// DeductCredits signature continues to behave the same after refactor:
// it opens its own transaction and writes CreditTransaction rows with
// operation/bizRefType/bizRefID/usageRecordID set.
func TestDeductCredits_LegacyWrapperStillWorks(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)

	now := time.Now()
	userID := uint(103)
	seedPackagesAndAccount(t, db, userID, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 200, RemainCredits: 200,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	err := biz.DeductCredits(context.Background(), userID, 50, "sop_run", "sop_run", "42", nil)
	require.NoError(t, err)

	// Balance deducted
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", userID).First(&acc).Error)
	assert.EqualValues(t, 150, acc.Balance)

	// CreditTransaction row carries the biz ref metadata
	var txns []model.CreditTransaction
	require.NoError(t, db.Where("user_id = ?", userID).Find(&txns).Error)
	require.Len(t, txns, 1)
	assert.EqualValues(t, -50, txns[0].Amount)
	assert.Equal(t, "sop_run", txns[0].Operation)
	assert.Equal(t, "sop_run", txns[0].BizRefType)
	assert.Equal(t, "42", txns[0].BizRefID)
}

// TestDeductCredits_Zero verifies the no-op path: zero credits returns nil
// without touching the DB.
func TestDeductCredits_Zero(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewCreditBiz(ds)

	err := biz.DeductCredits(context.Background(), uint(999), 0, "noop", "", "", nil)
	require.NoError(t, err)

	var accounts []model.CreditAccount
	require.NoError(t, db.Find(&accounts).Error)
	assert.Empty(t, accounts, "zero-credit deduction must not create an account")
}
