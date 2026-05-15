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
//
// T6: switched to newCreditServiceWithMembership so MembershipService is wired
// (legacy creditBiz.DeductCreditsTx fallback no longer exists).
func setupReservation(
	t *testing.T, userID uint, reserveCredits int64, packages []seedPackage,
) (credit.ICreditService, store.IStore, *credit.Reservation) {
	t.Helper()
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := newCreditServiceWithMembership(ds, db, nil)

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
	svc, ds, rsv := setupReservation(t, 300, 180, []seedPackage{
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

	// T6: balance now lives in credit_cycle, not credit_account. Refund to
	// the cycle pool (via MembershipService.RefundCreditsTx) bumps remaining
	// back up by 30 (= reserved 180 − refund 30 → net debit 150).
	var cycleRemaining int64
	require.NoError(t, ds.DB().Raw(
		`SELECT credits_remaining FROM credit_cycle WHERE user_id = ?`, uint(300),
	).Scan(&cycleRemaining).Error)
	assert.EqualValues(t, 850, cycleRemaining, "1000 − 180 + 30 refund")
}

// --- Task C.4: Reconcile top-up path (actual > reserved) ---

func TestReconcile_ActualGreaterThanReserved_TopsUp(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 301, 100, []seedPackage{
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

	// T6: credit_cycle.credits_remaining after Reserve(100) + top-up(30) = 1000-130 = 870.
	var cycleRemaining int64
	require.NoError(t, ds.DB().Raw(
		`SELECT credits_remaining FROM credit_cycle WHERE user_id = ?`, uint(301),
	).Scan(&cycleRemaining).Error)
	assert.EqualValues(t, 870, cycleRemaining)
}

// --- AI-6: Reconcile top-up with insufficient balance writes debt ledger ---

// TestReconcile_Topup_Insufficient_WritesDebtLedger verifies that when
// Reconcile top-up runs into ErrInsufficientCredits, the service:
//  1. Does NOT fail the reservation (business already succeeded).
//  2. Writes a CreditTransaction row with operation='reconcile_debt:<op>'
//     and amount=delta (spec §5.3).
//  3. Keeps the reservation state=reconciled so ops can audit the debt
//     via `WHERE operation LIKE 'reconcile_debt:%'`.
func TestReconcile_Topup_Insufficient_WritesDebtLedger(t *testing.T) {
	now := time.Now()
	// Seed a package with only 105 credits (barely enough to reserve 100,
	// but not enough for the subsequent top-up of +30 → 130 actual).
	svc, ds, rsv := setupReservation(t, 310, 100, []seedPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 105, RemainCredits: 105,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	// After Reserve(100): package 105→5 remain, account 105→5. Now Reconcile
	// with actual=130 → delta=+30, but only 5 left → ErrInsufficientCredits
	// inside DeductCreditsTx → debt path.
	err := svc.Reconcile(context.Background(), rsv.ID, 130)
	require.NoError(t, err, "debt path should NOT fail reconcile; business already succeeded")

	// Reservation transitioned to reconciled (not blocked by debt).
	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "reconciled", row.Status, "reservation must still finalize")
	require.NotNil(t, row.Delta)
	assert.EqualValues(t, 30, *row.Delta)

	// Debt row written with operation prefix + amount=delta + biz ref to rsv.
	var debts []model.CreditTransaction
	require.NoError(t, ds.DB().
		Where("user_id = ? AND operation LIKE ?", uint(310), model.CreditTxOpPrefixReconcileDebt+"%").
		Find(&debts).Error)
	require.Len(t, debts, 1, "exactly one reconcile_debt row expected")
	assert.EqualValues(t, 30, debts[0].Amount, "amount should equal unpaid delta")
	assert.Equal(t, model.CreditTxOpPrefixReconcileDebt+string(credit.OpSopRun), debts[0].Operation)
	assert.Equal(t, "reservation", debts[0].BizRefType)
}

// --- Task C.4: Reconcile exact-match path (delta=0, no-op on balances) ---

func TestReconcile_ActualEqualsReserved_Noop(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 302, 100, []seedPackage{
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

	// T6: credit_cycle.credits_remaining stays at Reserve-debit level: 1000-100 = 900.
	var cycleRemaining int64
	require.NoError(t, ds.DB().Raw(
		`SELECT credits_remaining FROM credit_cycle WHERE user_id = ?`, uint(302),
	).Scan(&cycleRemaining).Error)
	assert.EqualValues(t, 900, cycleRemaining)
}

// --- Task C.4: Reconcile idempotency (terminal → ErrAlreadyFinalized) ---

func TestReconcile_AlreadyFinalized_ReturnsSentinel(t *testing.T) {
	now := time.Now()
	svc, _, rsv := setupReservation(t, 303, 100, []seedPackage{
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
	svc := newCreditServiceWithMembership(ds, db, nil)
	err := svc.Reconcile(context.Background(), 999999, 10)
	require.Error(t, err)
	assert.True(t, errors.Is(err, credit.ErrReservationNotFound))
}

// --- Task C.4: Refund full amount, seq ASC ---

func TestRefund_WalksSeqAscAndRestoresPackages(t *testing.T) {
	now := time.Now()
	// Reserve 150 across two packages: sub(50) → booster(100)
	svc, ds, rsv := setupReservation(t, 400, 150, []seedPackage{
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

	// T6: refunds went to the new pools (cycle + booster) via
	// MembershipService.RefundCreditsTx. Restored sub 50→0→50 (cycle) and
	// booster 600→500→600.
	var cycleRemaining int64
	require.NoError(t, ds.DB().Raw(
		`SELECT credits_remaining FROM credit_cycle WHERE user_id = ?`, uint(400),
	).Scan(&cycleRemaining).Error)
	assert.EqualValues(t, 50, cycleRemaining)

	var boosterRemaining int64
	require.NoError(t, ds.DB().Raw(
		`SELECT credits_remaining FROM user_booster_balance WHERE user_id = ?`, uint(400),
	).Scan(&boosterRemaining).Error)
	assert.EqualValues(t, 600, boosterRemaining)
}

// --- F-9 regression: Refund with reason='provider_err' must succeed ---
//
// Verifies that Refund() accepts 'provider_err' as finalize_reason without
// returning an error. In production this goes through MySQL ENUM; here the
// test runs against SQLite (plain TEXT), which doesn't enforce ENUM constraints.
// The test therefore exercises code-level correctness (no error returned, row
// status transitions to 'refunded', finalize_reason='provider_err' stored) but
// CANNOT reproduce the MySQL Error 1265 that triggered this bug — that can only
// be caught by the migration test or integration tests against a live MySQL
// instance. The comment stands as documentation of the limitation.
func TestRefund_ProviderErr_SucceedsAndPersistsReason(t *testing.T) {
	now := time.Now()
	svc, ds, rsv := setupReservation(t, 450, 120, []seedPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})

	// This is the exact call path that triggered F-9: context_budget.go sets
	// fi.ErrorCode = "provider_err" → finalizeReservationIfNeeded passes it
	// as reason to Refund() → MySQL rejects with Error 1265.
	err := svc.Refund(context.Background(), rsv.ID, "provider_err")
	require.NoError(t, err, "Refund with reason='provider_err' must not error (F-9 regression)")

	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "refunded", row.Status)
	require.NotNil(t, row.FinalizeReason)
	assert.Equal(t, "provider_err", *row.FinalizeReason,
		"finalize_reason must be persisted as 'provider_err'")
	require.NotNil(t, row.ReconciledAt,
		"reconciled_at must be set on refund transition")

	// T11: credit_account.balance dropped — verify via credit_cycle.credits_remaining.
	// Credits fully restored after refund.
	var cycleRemain int64
	require.NoError(t, ds.DB().Raw(
		`SELECT credits_remaining FROM credit_cycle WHERE user_id = ?`, uint(450),
	).Scan(&cycleRemain).Error)
	assert.EqualValues(t, 500, cycleRemain, "balance must be fully restored after refund")
}

// --- F-9 regression: Refund with context_budget_refund and nil_stream ---
//
// These are the other new ENUM values added by F-9 fix that also go through
// finalizeReservationIfNeeded in context_budget.go.
func TestRefund_ContextBudgetReasons_Succeed(t *testing.T) {
	now := time.Now()

	for _, reason := range []string{"context_budget_refund", "nil_stream"} {
		reason := reason // capture for subtest
		t.Run(reason, func(t *testing.T) {
			svc, ds, rsv := setupReservation(t, 451, 80, []seedPackage{
				{Type: model.CreditTypeSubscription, TotalCredits: 500, RemainCredits: 500,
					ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
			})

			err := svc.Refund(context.Background(), rsv.ID, reason)
			require.NoError(t, err, "Refund with reason=%q must not error (F-9 regression)", reason)

			var row model.CreditReservation
			require.NoError(t, ds.DB().First(&row, rsv.ID).Error)
			assert.Equal(t, "refunded", row.Status)
			require.NotNil(t, row.FinalizeReason)
			assert.Equal(t, reason, *row.FinalizeReason)
		})
	}
}

// --- Task C.4: Refund idempotency ---

func TestRefund_AlreadyFinalized_ReturnsSentinel(t *testing.T) {
	now := time.Now()
	svc, _, rsv := setupReservation(t, 401, 80, []seedPackage{
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
	svc, ds, rsv := setupReservation(t, 500, 120, []seedPackage{
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
	svc, ds, rsv := setupReservation(t, 501, 120, []seedPackage{
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
	svc, ds, rsv := setupReservation(t, 502, 120, []seedPackage{
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
	svc, ds, rsv := setupReservation(t, 503, 120, []seedPackage{
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
	svc, ds, rsv := setupReservation(t, 504, 120, []seedPackage{
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
	svc, ds, rsv := setupReservation(t, 505, 120, []seedPackage{
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
