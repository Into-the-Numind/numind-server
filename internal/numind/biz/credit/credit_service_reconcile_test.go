package credit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// setupReservation is a test helper that runs Reserve inside a fresh test DB
// and returns the seeded user, the reservation, and the store for subsequent
// Reconcile/Refund calls.
func setupReservation(
	t *testing.T, userID uint, reserveCredits int64, packages []model.CreditPackage,
) (credit.ICreditService, store.IStore, *credit.Reservation) {
	t.Helper()
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil)

	seedPackagesAndAccount(t, db, userID, packages)
	user := newCreditsUser(userID)
	rsv, err := svc.Reserve(context.Background(), user, credit.OpSopRun, reserveCredits, 1, nil)
	require.NoError(t, err)
	require.NotNil(t, rsv)
	return svc, ds, rsv
}

// --- Task C.4: Reconcile normal path (actual < reserved) ---

func TestReconcile_ActualLessThanReserved_RefundsDelta(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 300, 180, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	// actualCost = 150 cents → delta = 150-180 = -30 → refund 30 to package 1
	err := svc.Reconcile(context.Background(), rsv.ID, 150)
	require.NoError(t, err)

	// Reservation state transitioned to reconciled
	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "reconciled", row.Status)
	require.NotNil(t, row.ActualCostCents)
	assert.EqualValues(t, 150, *row.ActualCostCents)
	require.NotNil(t, row.Delta)
	assert.EqualValues(t, -30, *row.Delta)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "normal", *row.FinalizeReason)
	require.NotNil(t, row.ReconciledAt)

	// Package balance bumped back up by 30
	var pkg model.CreditPackage
	require.NoError(t, ds.DB().Where("user_id = ?", uint(300)).First(&pkg).Error)
	assert.EqualValues(t, 850, pkg.RemainCredits) // 1000-180+30

	// Account balance also bumped back up by 30
	var acc model.CreditAccount
	require.NoError(t, ds.DB().Where("user_id = ?", uint(300)).First(&acc).Error)
	assert.EqualValues(t, 850, acc.Balance)
}

// --- Task C.4: Reconcile top-up path (actual > reserved) ---

func TestReconcile_ActualGreaterThanReserved_TopsUp(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 301, 100, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	// actualCost = 130 → delta = 130-100 = +30 → top-up 30 via FIFO
	err := svc.Reconcile(context.Background(), rsv.ID, 130)
	require.NoError(t, err)

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "reconciled", row.Status)
	require.NotNil(t, row.Delta)
	assert.EqualValues(t, 30, *row.Delta)

	// Package remaining after Reserve(100) + top-up(30) = 1000-130 = 870
	var pkg model.CreditPackage
	require.NoError(t, ds.DB().Where("user_id = ?", uint(301)).First(&pkg).Error)
	assert.EqualValues(t, 870, pkg.RemainCredits)

	var acc model.CreditAccount
	require.NoError(t, ds.DB().Where("user_id = ?", uint(301)).First(&acc).Error)
	assert.EqualValues(t, 870, acc.Balance)
}

// --- Task C.4: Reconcile exact-match path (delta=0, no-op on balances) ---

func TestReconcile_ActualEqualsReserved_Noop(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 302, 100, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	err := svc.Reconcile(context.Background(), rsv.ID, 100)
	require.NoError(t, err)

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "reconciled", row.Status)
	require.NotNil(t, row.Delta)
	assert.EqualValues(t, 0, *row.Delta)

	// Package remains at Reserve-debit level: 1000-100 = 900
	var pkg model.CreditPackage
	require.NoError(t, ds.DB().Where("user_id = ?", uint(302)).First(&pkg).Error)
	assert.EqualValues(t, 900, pkg.RemainCredits)
}

// --- Task C.4: Reconcile idempotency (terminal → ErrAlreadyFinalized) ---

func TestReconcile_AlreadyFinalized_ReturnsSentinel(t *testing.T) {
	now := time.Now()
	svc, _, rsv := setupReservation(t, 303, 100, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	require.NoError(t, svc.Reconcile(context.Background(), rsv.ID, 80))
	// Second call on terminal reservation → ErrAlreadyFinalized
	err := svc.Reconcile(context.Background(), rsv.ID, 80)
	require.Error(t, err)
	assert.True(t, errors.Is(err, credit.ErrAlreadyFinalized),
		"expected ErrAlreadyFinalized, got %v", err)
}

// --- Task C.4: Reconcile — reservation not found ---

func TestReconcile_NotFound_ReturnsSentinel(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil)
	err := svc.Reconcile(context.Background(), 999999, 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, credit.ErrReservationNotFound))
}

// --- Task C.4: Refund full amount, seq ASC ---

func TestRefund_WalksSeqAscAndRestoresPackages(t *testing.T) {
	now := time.Now()
	// Reserve 150 across two packages: sub(50) → booster(100)
	svc, ds, rsv := setupReservation(t, 400, 150, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 50, RemainCredits: 50,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{Type: model.CreditTypeBooster, TotalCredits: 600, RemainCredits: 600,
			ActivatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)},
	})
	require.Len(t, rsv.Items, 2)

	require.NoError(t, svc.Refund(context.Background(), rsv.ID, "op_failed"))

	// Reservation state
	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "refunded", row.Status)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "op_failed", *row.FinalizeReason)

	// Each package restored: sub 50→0(after Reserve)→50(after Refund); booster 600→500(after Reserve)→600(after Refund)
	var pkgs []model.CreditPackage
	require.NoError(t, ds.DB().Where("user_id = ?", uint(400)).Order("expires_at ASC").Find(&pkgs).Error)
	require.Len(t, pkgs, 2)
	assert.EqualValues(t, 50, pkgs[0].RemainCredits)
	// Status should be revived from exhausted → active after refund
	assert.Equal(t, model.CreditPackageActive, pkgs[0].Status)
	assert.EqualValues(t, 600, pkgs[1].RemainCredits)

	// Balance fully restored
	var acc model.CreditAccount
	require.NoError(t, ds.DB().Where("user_id = ?", uint(400)).First(&acc).Error)
	assert.EqualValues(t, 650, acc.Balance)
}

// --- Task C.4: Refund idempotency ---

func TestRefund_AlreadyFinalized_ReturnsSentinel(t *testing.T) {
	now := time.Now()
	svc, _, rsv := setupReservation(t, 401, 80, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})
	require.NoError(t, svc.Refund(context.Background(), rsv.ID, "user_cancelled"))
	err := svc.Refund(context.Background(), rsv.ID, "anything")
	require.Error(t, err)
	assert.True(t, errors.Is(err, credit.ErrAlreadyFinalized))
}

// --- Task C.4: FinalizeReservation dispatch table ---

func TestFinalizeReservation_OpErrTriggersRefund(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 500, 120, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	var actual int64 = 80 // set but should be ignored because opErr wins
	opErr := errors.New("llm timeout")
	require.NoError(t, svc.FinalizeReservation(context.Background(), rsv, &actual, &opErr))

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "refunded", row.Status, "opErr must take precedence over actualCost")
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "op_failed", *row.FinalizeReason)
}

func TestFinalizeReservation_ContextCancelled_UserCancelled(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 501, 120, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	opErr := context.Canceled
	require.NoError(t, svc.FinalizeReservation(context.Background(), rsv, nil, &opErr))

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "refunded", row.Status)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "user_cancelled", *row.FinalizeReason)
}

func TestFinalizeReservation_DeadlineExceeded_ProviderTimeout(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 502, 120, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	opErr := context.DeadlineExceeded
	require.NoError(t, svc.FinalizeReservation(context.Background(), rsv, nil, &opErr))

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "refunded", row.Status)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "provider_timeout", *row.FinalizeReason)
}

func TestFinalizeReservation_NilActualCost_RefundsNoActualCost(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 503, 120, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	var opErr error // nil
	// actualCost pointer is nil
	require.NoError(t, svc.FinalizeReservation(context.Background(), rsv, nil, &opErr))

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "refunded", row.Status)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "no_actual_cost", *row.FinalizeReason)
}

func TestFinalizeReservation_ZeroActualCost_RefundsNoActualCost(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 504, 120, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	var opErr error
	var actual int64 // 0
	require.NoError(t, svc.FinalizeReservation(context.Background(), rsv, &actual, &opErr))

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "refunded", row.Status)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "no_actual_cost", *row.FinalizeReason)
}

func TestFinalizeReservation_HappyPath_Reconciles(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 505, 120, []model.CreditPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	var opErr error
	var actual int64 = 95
	require.NoError(t, svc.FinalizeReservation(context.Background(), rsv, &actual, &opErr))

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "reconciled", row.Status, "happy path must reconcile, not refund")
	require.NotNil(t, row.Delta)
	assert.EqualValues(t, -25, *row.Delta) // 95 - 120
}
