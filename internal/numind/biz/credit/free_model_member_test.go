package credit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// Integration tests for the free-model member gate (feature free-model-member-only,
// C3/C4/C5/C7). Uses a REAL pricing calculator (a seeded zero-priced rule makes
// IsFreeModel return true) and REAL membership (a seeded active sub makes
// IsActiveMember return true), so the wiring of the whole gate is exercised.

const (
	fmProvider  = "youshu"
	fmFreeModel = "agnes"     // seeded with input=output=0 → IsFreeModel true
	fmPaidModel = "qwen-plus" // seeded with non-zero price → not free
)

// buildFMService builds a fully-wired ICreditService over an in-memory DB with
// pricing_rule support, seeding a free rule for agnes + a paid rule for qwen-plus.
func buildFMService(t *testing.T) (credit.ICreditService, *gorm.DB) {
	t.Helper()
	db := newCreditTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.PricingRule{}, &model.CreditEstimationCoefficient{}))
	ds := store.NewTestStore(db)
	seedPricingRule(t, db, "llm_chat", fmProvider, fmFreeModel, 0, 0)     // free
	seedPricingRule(t, db, "llm_chat", fmProvider, fmPaidModel, 200, 800) // paid
	pc := pricing.NewCalculator(ds.Billing())
	return newCreditServiceWithMembership(ds, db, pc), db
}

// seedActiveMember seeds an active subscription (0 cycle credits) → the user is a
// member by validity but has zero balance.
func seedActiveMember(t *testing.T, db *gorm.DB, userID uint) {
	t.Helper()
	now := time.Now()
	seedPackagesAndAccount(t, db, userID, []seedPackage{{
		Type:          model.CreditTypeSubscription,
		RemainCredits: 0,
		ActivatedAt:   now.AddDate(0, -1, 0),
		ExpiresAt:     now.AddDate(0, 1, 0),
	}})
}

// AC1: member (0 balance) + free model via gateway path → skip deduction, zero estimate.
func TestCheckAndEstimateBudget_FreeModel_Member_SkipsDeduction(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8001)
	seedActiveMember(t, db, uid)
	user := &model.User{}
	user.ID = uid

	pre, err := svc.CheckAndEstimateBudget(context.Background(), user, credit.BudgetPrecheckInput{
		Operation: "chatbot_chat", Provider: fmProvider, Model: fmFreeModel,
		EstimatedPromptTokens: 1000, EstimatedCompletionTokens: 500,
	})
	require.NoError(t, err)
	require.NotNil(t, pre)
	assert.True(t, pre.SkipDeduction, "member + free model must skip deduction")
	assert.True(t, pre.Sufficient)
	assert.EqualValues(t, 0, pre.EstimatedCredits)
}

// AC3: non-member + free model via gateway path → ErrModelMembershipOnly.
func TestCheckAndEstimateBudget_FreeModel_NonMember_Blocks(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8002)
	seedPackagesAndAccount(t, db, uid, nil) // account only → non-member
	user := &model.User{}
	user.ID = uid

	_, err := svc.CheckAndEstimateBudget(context.Background(), user, credit.BudgetPrecheckInput{
		Operation: "chatbot_chat", Provider: fmProvider, Model: fmFreeModel,
		EstimatedPromptTokens: 1000, EstimatedCompletionTokens: 500,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errno.ErrModelMembershipOnly),
		"free user + free model must get ErrModelMembershipOnly, got %v", err)
}

// AC4 regression: a paid model is NOT touched by the free-model gate — a
// 0-balance user still hits normal balance gating (ErrInsufficientCredits).
func TestCheckAndEstimateBudget_PaidModel_ZeroBalance_Insufficient(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8003)
	seedPackagesAndAccount(t, db, uid, nil) // non-member, genuine 0 balance
	user := &model.User{}
	user.ID = uid

	pre, err := svc.CheckAndEstimateBudget(context.Background(), user, credit.BudgetPrecheckInput{
		Operation: "chatbot_chat", Provider: fmProvider, Model: fmPaidModel,
		EstimatedPromptTokens: 1000, EstimatedCompletionTokens: 500,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, credit.ErrInsufficientCredits),
		"paid model + 0 balance must be insufficient, got %v", err)
	require.NotNil(t, pre)
	assert.False(t, pre.SkipDeduction, "paid model must not skip deduction")
}

// AC4 regression: a member with balance can still use a paid model normally
// (the gate must not block paid models for members).
func TestCheckAndEstimateBudget_PaidModel_MemberWithBalance_OK(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8007)
	now := time.Now()
	seedPackagesAndAccount(t, db, uid, []seedPackage{{
		Type:          model.CreditTypeSubscription,
		RemainCredits: 2000,
		ActivatedAt:   now.AddDate(0, -1, 0),
		ExpiresAt:     now.AddDate(0, 1, 0),
	}})
	user := &model.User{}
	user.ID = uid

	pre, err := svc.CheckAndEstimateBudget(context.Background(), user, credit.BudgetPrecheckInput{
		Operation: "chatbot_chat", Provider: fmProvider, Model: fmPaidModel,
		EstimatedPromptTokens: 1000, EstimatedCompletionTokens: 500,
	})
	require.NoError(t, err)
	require.NotNil(t, pre)
	assert.True(t, pre.Sufficient, "member with quota must pass for a paid model")
	assert.False(t, pre.SkipDeduction, "paid model must not skip deduction")
	assert.Greater(t, pre.EstimatedCredits, int64(0), "paid model must have a non-zero estimate")
}

// AC1 end-to-end: ReserveBudget for member + free model creates NO reservation
// (zero deduction).
func TestReserveBudget_FreeModel_Member_NoReservation(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8004)
	seedActiveMember(t, db, uid)
	user := &model.User{}
	user.ID = uid

	rsv, err := svc.ReserveBudget(context.Background(), user, credit.BudgetReservationInput{
		BudgetPrecheckInput: credit.BudgetPrecheckInput{
			Operation: "chatbot_chat", Provider: fmProvider, Model: fmFreeModel,
			EstimatedPromptTokens: 1000, EstimatedCompletionTokens: 500,
		},
	})
	require.NoError(t, err)
	require.Nil(t, rsv, "member + free model must create NO reservation (zero deduction)")
}

// Default R2 path (modelKey==""): member + free model → skip deduction.
func TestCheckAndEstimate_FreeModel_Member_SkipsDeduction(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8005)
	seedActiveMember(t, db, uid)
	user := &model.User{}
	user.ID = uid

	pre, err := svc.CheckAndEstimate(context.Background(), user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 1000, Model: fmFreeModel, Provider: fmProvider,
	})
	require.NoError(t, err)
	require.NotNil(t, pre)
	assert.True(t, pre.SkipDeduction)
	assert.EqualValues(t, 0, pre.EstimatedCredits)
}

// Default R2 path: non-member + free model → ErrModelMembershipOnly.
func TestCheckAndEstimate_FreeModel_NonMember_Blocks(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8006)
	seedPackagesAndAccount(t, db, uid, nil)
	user := &model.User{}
	user.ID = uid

	_, err := svc.CheckAndEstimate(context.Background(), user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 1000, Model: fmFreeModel, Provider: fmProvider,
	})
	require.True(t, errors.Is(err, errno.ErrModelMembershipOnly), "got %v", err)
}

// C7: EnforceModelMembership — free+non-member blocks; free+member, paid, unknown all allow.
func TestEnforceModelMembership(t *testing.T) {
	svc, db := buildFMService(t)
	ctx := context.Background()
	memberID := uint(8101)
	seedActiveMember(t, db, memberID)
	const nonMember = uint64(8102)

	require.NoError(t, svc.EnforceModelMembership(ctx, uint64(memberID), fmProvider, fmFreeModel),
		"free model + member → allowed")

	err := svc.EnforceModelMembership(ctx, nonMember, fmProvider, fmFreeModel)
	require.Error(t, err)
	require.True(t, errors.Is(err, errno.ErrModelMembershipOnly), "free model + non-member → blocked, got %v", err)

	require.NoError(t, svc.EnforceModelMembership(ctx, nonMember, fmProvider, fmPaidModel),
		"paid model → not gated")
	require.NoError(t, svc.EnforceModelMembership(ctx, nonMember, fmProvider, "no-such-model"),
		"unknown model (no pricing rule) → not free → not gated")
}

// C5: ICreditService.IsActiveMember delegates correctly.
func TestServiceIsActiveMember(t *testing.T) {
	svc, db := buildFMService(t)
	ctx := context.Background()
	memberID := uint(8201)
	seedActiveMember(t, db, memberID)

	ok, err := svc.IsActiveMember(ctx, uint64(memberID))
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = svc.IsActiveMember(ctx, 8202) // non-member
	require.NoError(t, err)
	require.False(t, ok)
}

// AC2 end-to-end: a trial member whose trial credits are exhausted (0) still
// gets SkipDeduction on a free model (member judged by validity, not balance).
func TestCheckAndEstimateBudget_FreeModel_TrialMemberZeroCredits_SkipsDeduction(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8008)
	now := time.Now()
	seedPackagesAndAccount(t, db, uid, []seedPackage{{
		Type: model.CreditTypeTrial, RemainCredits: 0, ExpiresAt: now.AddDate(0, 0, 3),
	}})
	user := &model.User{}
	user.ID = uid

	pre, err := svc.CheckAndEstimateBudget(context.Background(), user, credit.BudgetPrecheckInput{
		Operation: "chatbot_chat", Provider: fmProvider, Model: fmFreeModel,
		EstimatedPromptTokens: 1000, EstimatedCompletionTokens: 500,
	})
	require.NoError(t, err)
	require.NotNil(t, pre)
	assert.True(t, pre.SkipDeduction, "trial member with 0 credits + free model must skip (AC2)")
}

// R2/default-path regression (AC4): a paid model passes through the gate to
// normal billing; a 0-balance non-member is blocked with ErrInsufficientCredits.
func TestCheckAndEstimate_PaidModel_ZeroBalance_Insufficient(t *testing.T) {
	svc, db := buildFMService(t)
	uid := uint(8009)
	seedPackagesAndAccount(t, db, uid, nil) // non-member, 0 balance
	seedCoefficient(t, db, fmProvider, fmPaidModel, "sop_run", 1.5, 0.5, 0.2, 1, true)
	user := &model.User{}
	user.ID = uid

	_, err := svc.CheckAndEstimate(context.Background(), user, credit.OpSopRun, credit.EstimationInput{
		PromptChars: 1000, Model: fmPaidModel, Provider: fmProvider,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, credit.ErrInsufficientCredits), "got %v", err)
}
