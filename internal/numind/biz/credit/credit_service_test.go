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

// --- Task C.2: legacyTierImpl ---

// TestLegacyCheckAndEstimate_CanRun verifies that a legacy_tier user whose
// CanRunSOP()=true receives SkipDeduction=true and no error.
func TestLegacyCheckAndEstimate_CanRun(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil /* pricing */, nil)

	future := time.Now().Add(24 * time.Hour)
	// Within the 30-day cycle: MonthlyResetAt = just now so no auto-reset.
	resetAt := time.Now().Add(-time.Hour)
	user := &model.User{
		BillingMode:    model.BillingModeLegacyTier,
		UserTier:       model.UserTierStandard,
		TierExpires:    &future,
		MonthlySopRuns: 5,
		MonthlyResetAt: &resetAt,
	}
	user.ID = 1

	pre, err := svc.CheckAndEstimate(context.Background(), user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 100, Model: "qwen-turbo", Provider: "ali",
	})
	require.NoError(t, err)
	require.NotNil(t, pre)
	assert.True(t, pre.SkipDeduction, "legacy_tier user must skip deduction")
	assert.True(t, pre.Sufficient)
	assert.EqualValues(t, 0, pre.EstimatedCredits, "no estimation for legacy_tier")
	assert.Empty(t, pre.Reason, "no reason when can-run=true")
	assert.Equal(t, model.BillingModeLegacyTier, pre.Balance.BillingMode)
	require.NotNil(t, pre.Balance.RemainingRuns)
	assert.Equal(t, 15, *pre.Balance.RemainingRuns, "standard limit 20 - monthly_runs 5 = 15")
}

// TestLegacyCheckAndEstimate_CannotRun verifies that a trial user at the run
// cap gets ErrInsufficientCredits wrapped with the Chinese reason from
// user.CanRunSOP().
func TestLegacyCheckAndEstimate_CannotRun(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	future := time.Now().Add(24 * time.Hour)
	user := &model.User{
		BillingMode:    model.BillingModeLegacyTier,
		UserTier:       model.UserTierTrial,
		TierExpires:    &future,
		MonthlySopRuns: 10, // trial limit reached
	}
	user.ID = 2

	pre, err := svc.CheckAndEstimate(context.Background(), user, credit.OpSopRun, credit.EstimationInput{})
	require.Error(t, err)
	require.True(t, errors.Is(err, credit.ErrInsufficientCredits),
		"must wrap ErrInsufficientCredits, got %v", err)
	require.NotNil(t, pre, "pre should still carry the Reason for wrapCreditError")
	assert.True(t, pre.SkipDeduction)
	assert.False(t, pre.Sufficient)
	assert.Contains(t, pre.Reason, "体验会员运行次数已达上限",
		"Reason must carry CanRunSOP's zh message, got %q", pre.Reason)
	// Error string must also include the reason for log/trace visibility.
	assert.Contains(t, err.Error(), "体验会员运行次数已达上限")
}

// TestLegacyCheckAndEstimate_FreeUser verifies that a free user (explicitly
// legacy_tier) gets CanRunSOP's denial message.
func TestLegacyCheckAndEstimate_FreeUser(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	user := &model.User{BillingMode: model.BillingModeLegacyTier, UserTier: model.UserTierFree}
	user.ID = 3
	pre, err := svc.CheckAndEstimate(context.Background(), user, credit.OpSopRun, credit.EstimationInput{})
	require.Error(t, err)
	require.True(t, errors.Is(err, credit.ErrInsufficientCredits))
	assert.Contains(t, pre.Reason, "免费用户")
}

// TestLegacyReserve_Panics verifies that calling Reserve with a legacy_tier
// user panics ("unreachable: legacy_tier must be guarded by SkipDeduction").
// Caller MUST check pre.SkipDeduction and skip Reserve entirely.
func TestLegacyReserve_Panics(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	user := &model.User{BillingMode: model.BillingModeLegacyTier}
	user.ID = 4
	assert.PanicsWithValue(t, "unreachable: legacy_tier must be guarded by SkipDeduction", func() {
		_, _ = svc.Reserve(context.Background(), user, credit.OpSopRun, 100, 1, nil)
	})
}

// TestLegacyGetBalance_ReturnsRemainingRuns verifies GetBalance for legacy
// tier returns the numeric RemainingRuns + MonthlyLimit snapshot without
// touching credit_package.
func TestLegacyGetBalance_ReturnsRemainingRuns(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	future := time.Now().Add(24 * time.Hour)

	t.Run("standard", func(t *testing.T) {
		resetAt := time.Now().Add(-time.Hour)
		user := &model.User{BillingMode: model.BillingModeLegacyTier,
			UserTier: model.UserTierStandard, TierExpires: &future, MonthlySopRuns: 8,
			MonthlyResetAt: &resetAt}
		user.ID = 10
		bal, err := svc.GetBalance(context.Background(), user)
		require.NoError(t, err)
		assert.Equal(t, model.BillingModeLegacyTier, bal.BillingMode)
		require.NotNil(t, bal.RemainingRuns)
		assert.Equal(t, 12, *bal.RemainingRuns) // 20-8
		require.NotNil(t, bal.MonthlyLimit)
		assert.Equal(t, 20, *bal.MonthlyLimit)
	})

	t.Run("premium unlimited → nil remaining/limit", func(t *testing.T) {
		user := &model.User{BillingMode: model.BillingModeLegacyTier,
			UserTier: model.UserTierPremium, TierExpires: &future}
		user.ID = 11
		bal, err := svc.GetBalance(context.Background(), user)
		require.NoError(t, err)
		assert.Equal(t, model.BillingModeLegacyTier, bal.BillingMode)
		assert.Nil(t, bal.RemainingRuns, "premium=unlimited → nil RemainingRuns")
		assert.Nil(t, bal.MonthlyLimit, "premium has no monthly cap")
	})

	t.Run("trial 10-cap", func(t *testing.T) {
		user := &model.User{BillingMode: model.BillingModeLegacyTier,
			UserTier: model.UserTierTrial, TierExpires: &future, MonthlySopRuns: 3}
		user.ID = 12
		bal, err := svc.GetBalance(context.Background(), user)
		require.NoError(t, err)
		require.NotNil(t, bal.RemainingRuns)
		assert.Equal(t, 7, *bal.RemainingRuns) // 10-3
		require.NotNil(t, bal.MonthlyLimit)
		assert.Equal(t, 10, *bal.MonthlyLimit)
	})
}

// TestLegacyFinalizeReservation_NilIsNoOp verifies FinalizeReservation(rsv=nil)
// is a safe no-op — legacy_tier callers pass nil to the defer.
func TestLegacyFinalizeReservation_NilIsNoOp(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	var opErr error
	var actual int64
	err := svc.FinalizeReservation(context.Background(), nil, &actual, &opErr)
	require.NoError(t, err)
}
