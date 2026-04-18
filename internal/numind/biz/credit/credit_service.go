package credit

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// creditService is the ICreditService implementation that dispatches by
// user.BillingMode into one of the two internal strategies:
//   - legacyTierImpl  (BillingMode == legacy_tier)
//   - creditsImpl     (BillingMode == credits, default)
//
// See spec §1.2 / §1.3 / §1.4 for the dispatch rules and §1.6 for the caller
// template.
type creditService struct {
	store   store.IStore
	biz     ICreditBiz
	pricing pricing.ICalculator
	// Pre-instantiated legs so each dispatch is a plain method call.
	legacy  *legacyTierImpl
	credits *creditsImpl
}

// NewCreditService constructs the singleton ICreditService used throughout the
// app. Pass the existing store.IStore, an ICreditBiz (for DeductCreditsTx +
// quota queries), and a pricing.ICalculator (used by credits leg for R2
// estimation). pricing may be nil — the legacy leg never touches it, so
// legacy-only callers in tests can pass nil.
func NewCreditService(ds store.IStore, biz ICreditBiz, pc pricing.ICalculator) ICreditService {
	return &creditService{
		store:   ds,
		biz:     biz,
		pricing: pc,
		legacy:  &legacyTierImpl{biz: biz},
		credits: &creditsImpl{store: ds, biz: biz, pricing: pc},
	}
}

// CheckAndEstimate dispatches to legacy or credits leg based on user.BillingMode.
func (s *creditService) CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error) {
	if user.BillingMode == model.BillingModeLegacyTier {
		return s.legacy.CheckAndEstimate(ctx, user, op, in)
	}
	return s.credits.CheckAndEstimate(ctx, user, op, in)
}

// Reserve dispatches by billing_mode. legacy_tier MUST be guarded by the
// caller via SkipDeduction — reaching legacy.Reserve panics by design.
func (s *creditService) Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64, idempotencyKey *string) (*Reservation, error) {
	if user.BillingMode == model.BillingModeLegacyTier {
		return s.legacy.Reserve(ctx, user, op, estimated, coefID, idempotencyKey)
	}
	return s.credits.Reserve(ctx, user, op, estimated, coefID, idempotencyKey)
}

// Reconcile looks up the reservation by ID; there is no user to dispatch on.
// Since legacy_tier never creates reservations, any reservation that reaches
// Reconcile is a credits-mode reservation.
func (s *creditService) Reconcile(ctx context.Context, reservationID uint64, actualCostCents int64) error {
	return s.credits.Reconcile(ctx, reservationID, actualCostCents)
}

// Refund has the same dispatch rationale as Reconcile — reservations only
// exist in credits mode.
func (s *creditService) Refund(ctx context.Context, reservationID uint64, reason string) error {
	return s.credits.Refund(ctx, reservationID, reason)
}

// FinalizeReservation is the single defer-exit point. A nil reservation is
// a safe no-op: legacy_tier callers pass nil because Reserve was skipped.
// Otherwise dispatches to the credits leg (only path that creates reservations).
func (s *creditService) FinalizeReservation(ctx context.Context, rsv *Reservation, actualCostCents *int64, opErr *error) error {
	if rsv == nil {
		return nil
	}
	return s.credits.FinalizeReservation(ctx, rsv, actualCostCents, opErr)
}

// GetBalance dispatches by billing_mode. legacy_tier returns
// RemainingRuns/MonthlyLimit snapshot; credits returns the credit_package
// FIFO breakdown.
func (s *creditService) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
	if user.BillingMode == model.BillingModeLegacyTier {
		return s.legacy.GetBalance(ctx, user)
	}
	return s.credits.GetBalance(ctx, user)
}

// ---------------------------------------------------------------------------
// legacyTierImpl — Grandfathering Option E (spec §1.3 / §3.6)
//
// Behaviour:
//   - CheckAndEstimate → SkipDeduction=true always; Sufficient derived from
//     user.CanRunSOP(); on !canRun wraps ErrInsufficientCredits with Reason
//   - Reserve / Reconcile / Refund → panic("unreachable: legacy_tier must be
//     guarded by SkipDeduction")
//   - GetBalance → user.GetRemainingSOPRuns() + MonthlyLimit, no credit_package
// ---------------------------------------------------------------------------

type legacyTierImpl struct {
	biz ICreditBiz // unused for now but kept for symmetry with creditsImpl
}

// CheckAndEstimate for legacy tier: delegate to user.CanRunSOP() and translate
// its Chinese denial message into PreCheckResult.Reason so the caller can
// surface the same text the user sees today.
func (l *legacyTierImpl) CheckAndEstimate(_ context.Context, user *model.User, _ Operation, _ EstimationInput) (*PreCheckResult, error) {
	canRun, reason := user.CanRunSOP()
	pre := &PreCheckResult{
		SkipDeduction: true,
		Balance:       l.buildLegacyBalance(user),
	}
	if !canRun {
		pre.Sufficient = false
		pre.Reason = reason
		// Wrap ErrInsufficientCredits so callers can use errors.Is to
		// classify, and log/trace retain the zh reason.
		return pre, fmt.Errorf("%w: %s", ErrInsufficientCredits, reason)
	}
	pre.Sufficient = true
	return pre, nil
}

// Reserve must never be reached on legacy_tier. Callers gate via
// pre.SkipDeduction; any code that misses this is a bug and should blow up
// loudly at dev time rather than silently debit nothing.
func (l *legacyTierImpl) Reserve(_ context.Context, _ *model.User, _ Operation, _ int64, _ uint64, _ *string) (*Reservation, error) {
	panic("unreachable: legacy_tier must be guarded by SkipDeduction")
}

func (l *legacyTierImpl) GetBalance(_ context.Context, user *model.User) (*BalanceBreakdown, error) {
	return l.buildLegacyBalance(user).Ptr(), nil
}

// buildLegacyBalance computes the RemainingRuns/MonthlyLimit snapshot WITHOUT
// touching credit_package. Premium users receive nil/nil (unlimited, no cap).
// Trial users receive their 10-cap; standard users receive the 20/month cap.
func (l *legacyTierImpl) buildLegacyBalance(user *model.User) BalanceBreakdown {
	remaining := user.GetRemainingSOPRuns() // -1 = unlimited (premium)
	actualTier := user.GetActualUserTier()

	bal := BalanceBreakdown{BillingMode: model.BillingModeLegacyTier}
	switch actualTier {
	case model.UserTierPremium:
		// Unlimited: both RemainingRuns and MonthlyLimit are nil.
		// (Spec §1.8: "nil = premium unlimited")
		return bal
	case model.UserTierStandard:
		limit := model.StandardUserMonthlySOPLimit
		bal.MonthlyLimit = &limit
		if remaining >= 0 {
			bal.RemainingRuns = &remaining
		}
		return bal
	case model.UserTierTrial:
		limit := model.TrialUserSOPLimit
		bal.MonthlyLimit = &limit
		if remaining >= 0 {
			bal.RemainingRuns = &remaining
		}
		return bal
	default: // free
		zero := 0
		bal.RemainingRuns = &zero
		zeroLimit := 0
		bal.MonthlyLimit = &zeroLimit
		return bal
	}
}

// Ptr returns a pointer copy of the BalanceBreakdown so callers that require
// *BalanceBreakdown don't need to take the address of a stack value.
func (b BalanceBreakdown) Ptr() *BalanceBreakdown { return &b }

// ---------------------------------------------------------------------------
// creditsImpl — new-system credits mode (spec §1.4)
//
// Stubs here; filled in by Tasks C.3 / C.4 / C.5 / C.8. Each method returns a
// placeholder error for now so the service compiles and legacy-only tests
// can exercise the dispatcher without requiring credits-mode infrastructure.
// ---------------------------------------------------------------------------

type creditsImpl struct {
	store   store.IStore
	biz     ICreditBiz
	pricing pricing.ICalculator
}

func (c *creditsImpl) CheckAndEstimate(_ context.Context, _ *model.User, _ Operation, _ EstimationInput) (*PreCheckResult, error) {
	return nil, fmt.Errorf("creditsImpl.CheckAndEstimate: not yet implemented (Task C.5)")
}

func (c *creditsImpl) Reserve(_ context.Context, _ *model.User, _ Operation, _ int64, _ uint64, _ *string) (*Reservation, error) {
	return nil, fmt.Errorf("creditsImpl.Reserve: not yet implemented (Task C.3)")
}

func (c *creditsImpl) Reconcile(_ context.Context, _ uint64, _ int64) error {
	return fmt.Errorf("creditsImpl.Reconcile: not yet implemented (Task C.4)")
}

func (c *creditsImpl) Refund(_ context.Context, _ uint64, _ string) error {
	return fmt.Errorf("creditsImpl.Refund: not yet implemented (Task C.4)")
}

func (c *creditsImpl) FinalizeReservation(_ context.Context, _ *Reservation, _ *int64, _ *error) error {
	return fmt.Errorf("creditsImpl.FinalizeReservation: not yet implemented (Task C.4)")
}

func (c *creditsImpl) GetBalance(_ context.Context, _ *model.User) (*BalanceBreakdown, error) {
	return nil, fmt.Errorf("creditsImpl.GetBalance: not yet implemented (Task C.3)")
}
