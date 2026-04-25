package credit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
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
//
// The estimation biz is built internally from ds + pc so callers don't need
// to know about the sub-dependency. If pc is nil, the estimation leg is also
// nil and creditsImpl.CheckAndEstimate returns a config error rather than
// panicking — legacy-only tests exercise this path safely.
func NewCreditService(ds store.IStore, biz ICreditBiz, pc pricing.ICalculator) ICreditService {
	var est IEstimationBiz
	if pc != nil {
		est = NewEstimationBiz(ds, pc)
	}
	return &creditService{
		store:   ds,
		biz:     biz,
		pricing: pc,
		legacy:  &legacyTierImpl{biz: biz},
		credits: &creditsImpl{store: ds, biz: biz, pricing: pc, estimation: est},
	}
}

// isEffectiveLegacy returns true if the user should be treated as a legacy
// tier member regardless of their billing_mode field. This enables a smooth
// transition: users with an active legacy membership (tier not free, not
// expired) are served by the legacy path even if billing_mode is still
// "credits" (e.g. migration not yet run). Once their membership expires,
// they naturally fall through to the credits path.
func isEffectiveLegacy(user *model.User) bool {
	if user.BillingMode == model.BillingModeLegacyTier {
		return true
	}
	return user.HasActiveMembership()
}

// CheckAndEstimate dispatches to legacy or credits leg. Users with an active
// legacy membership are always routed to the legacy leg for a smooth transition,
// regardless of the billing_mode field value.
func (s *creditService) CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error) {
	if isEffectiveLegacy(user) {
		return s.legacy.CheckAndEstimate(ctx, user, op, in)
	}
	return s.credits.CheckAndEstimate(ctx, user, op, in)
}

// Reserve dispatches by billing mode. legacy_tier MUST be guarded by the
// caller via SkipDeduction — reaching legacy.Reserve panics by design.
func (s *creditService) Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64, idempotencyKey *string) (*Reservation, error) {
	if isEffectiveLegacy(user) {
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

// GetBalance dispatches by effective billing mode. legacy_tier returns
// RemainingRuns/MonthlyLimit snapshot; credits returns the credit_package
// FIFO breakdown.
func (s *creditService) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
	if isEffectiveLegacy(user) {
		return s.legacy.GetBalance(ctx, user)
	}
	return s.credits.GetBalance(ctx, user)
}

// budgetOperationMap normalises raw billing operation strings (as they appear
// in the context-budget event pipeline) to canonical credit.Operation values.
// Spec §6.1.1 — one-to-one map, 12 entries.
//
// "context_compression" is intentionally absent: it is an internal pipeline
// operation with no per-user credit charge. Callers that encounter an uncharged
// operation should branch before reaching ICreditService entirely.
var budgetOperationMap = map[string]Operation{
	"sop_node_execute":              OpSopRun,
	"sop_run":                       OpSopRun,
	"sop_chat":                      OpSopChat,
	"sop_chat_stream":               OpSopChat,
	"chatbot_chat":                  OpChatbotChat,
	"chatbot.stream":                OpChatbotChat,
	"salesrag_chat":                 OpSalesragChat,
	"salesrag_chat_generate":        OpSalesragChat,
	"salesrag_strategy_select":      OpSalesragChat,
	"salesrag_analyze_profile":      OpSalesragChat,
	"salesrag_analyze_profile_text": OpSalesragChat,
	"salesrag_chat_style_text":      OpSalesragChat,
}

// CheckAndEstimateBudget is the budget-aware precheck entry point. It
// normalises the raw operation via budgetOperationMap, then dispatches:
//   - legacy-tier users → SkipDeduction=true, no estimation performed.
//   - credits users → computes EstimatedCredits from token counts (flat
//     estimate: tokens × a fixed rate, falling back to GetEstimatedCredits
//     if pricing.ICalculator is nil in tests).
//   - unknown operation + user billing context → ErrUnknownBudgetOperation
//     (fail-closed: never silently charge a default operation).
//
// This is a parallel API to CheckAndEstimate; the R2 char-based path is
// preserved unchanged.
func (s *creditService) CheckAndEstimateBudget(ctx context.Context, user *model.User, input BudgetPrecheckInput) (*PreCheckResult, error) {
	// Step 1: normalize operation.
	op, found := budgetOperationMap[input.Operation]
	if !found {
		// Unknown operation with a billing-context user: fail closed.
		return nil, fmt.Errorf("%w: operation=%q", ErrUnknownBudgetOperation, input.Operation)
	}

	// Step 2: legacy-tier dispatch — preserve existing contract, no estimation.
	// legacyTierImpl.CheckAndEstimate ignores EstimationInput entirely; gating
	// is based on user.UserTier and user.CanRunSOP(). The PromptChars value
	// here is unused and intentionally left as the token estimate to avoid an
	// extra unit conversion.
	if isEffectiveLegacy(user) {
		return s.legacy.CheckAndEstimate(ctx, user, op, EstimationInput{
			PromptChars: input.EstimatedPromptTokens, // EstimationInput is ignored by legacyTierImpl — it gates on user.CanRunSOP() only
			Model:       input.Model,
			Provider:    input.Provider,
		})
	}

	// Step 3: credits mode — estimate from token counts.
	// Compute estimated credits: if pricing calculator available, use it;
	// otherwise fall back to the flat table (test / local path).
	var estimatedCredits int64
	if s.pricing != nil {
		costCents, err := s.pricing.CalculateCost(ctx, "llm_chat", input.Provider, input.Model,
			input.EstimatedPromptTokens, input.EstimatedCompletionTokens)
		if err == nil {
			estimatedCredits = costCents // cost_cents are used directly as credits (1:1 in test env)
		}
	}
	if estimatedCredits <= 0 {
		// Fallback: use the flat table (useful when pricing DB is unavailable or in tests).
		estimatedCredits = GetEstimatedCredits(string(op))
	}

	// Step 4: check balance.
	bal, err := s.credits.GetBalance(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("CheckAndEstimateBudget: balance: %w", err)
	}
	total := bal.SubRemain + bal.BoosterRemain
	pre := &PreCheckResult{
		SkipDeduction:    false,
		Sufficient:       total >= estimatedCredits,
		EstimatedCredits: estimatedCredits,
		Balance:          *bal,
	}
	if !pre.Sufficient {
		return pre, fmt.Errorf("%w: need %d credits, have %d", ErrInsufficientCredits, estimatedCredits, total)
	}
	return pre, nil
}

// ReserveBudget creates a credit_reservation with estimation_source='context_budget',
// coefficient_id=NULL, and the token-profile/event metadata from the input.
// Legacy-tier users (SkipDeduction path) are a safe no-op: returns (nil, nil).
// Spec §6.1.2.
func (s *creditService) ReserveBudget(ctx context.Context, user *model.User, input BudgetReservationInput) (*Reservation, error) {
	// Precheck first to validate operation + check balance.
	pre, err := s.CheckAndEstimateBudget(ctx, user, input.BudgetPrecheckInput)
	if err != nil {
		return nil, fmt.Errorf("ReserveBudget: precheck: %w", err)
	}

	// Legacy-tier: skip deduction entirely.
	if pre.SkipDeduction {
		return nil, nil
	}

	// Determine credits to reserve: caller may override the estimate.
	estimated := input.EstimatedCredits
	if estimated <= 0 {
		estimated = pre.EstimatedCredits
	}
	if estimated <= 0 {
		// Absolute fallback.
		estimated = GetEstimatedCredits(string(budgetOperationMap[input.Operation]))
	}

	// Normalize operation for reference type / billing labelling.
	op := budgetOperationMap[input.Operation]

	// Delegate to the credits impl to do FIFO deduction + reservation insert,
	// but we need to write context_budget-specific fields into the row.
	// We call into credits.reserveBudgetRow which shares the FIFO tx logic but
	// sets the context_budget metadata.
	var idempKey *string
	if input.IdempotencyKey != "" {
		k := input.IdempotencyKey
		idempKey = &k
	}
	return s.credits.reserveBudgetRow(ctx, user, op, estimated, input, idempKey)
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
// creditsImpl — new-system credits mode (spec §1.4).
//
// Implements ICreditService for users with billing_mode='credits':
//   - Reserve → FIFO pre-deduct (DeductCreditsTx) + writes credit_reservation +
//     items snapshot; emits credit-reserve span.
//   - Reconcile → refund excess (reserved > actual) or top-up debt (reserved <
//     actual). Debt overflow logs hasDebt + Langfuse span (§5.3 ledger follow-up).
//   - Refund → full restore on cancellation via FinalizeReservation.
//   - GetBalance → SubRemain + BoosterRemain + expires-at breakdown.
// ---------------------------------------------------------------------------

type creditsImpl struct {
	store      store.IStore
	biz        ICreditBiz
	pricing    pricing.ICalculator
	estimation IEstimationBiz // wired in NewCreditService
}

// CheckAndEstimate — credits mode. Computes R2 estimate via the estimation
// biz layer, fetches the current balance snapshot, and returns PreCheckResult
// with Sufficient derived from a simple balance >= estimated check.
// ErrInsufficientCredits is wrapped + returned on shortfall so the caller's
// wrapCreditError helper can surface a zh message.
//
// Emits:
//   - trace-level metadata: billing_mode / deducted_from / credit_balance_at_start
//     (spec §5.1.5)
//   - span: credit-estimate (spec §5.1.1) with input {operation, prompt_chars,
//     model, provider, billing_mode} and output {estimated_credits, sufficient,
//     skip_deduction, coefficient_id, char_to_token_ratio,
//     completion_prompt_ratio, safety_buffer_pct, sub_remain_before,
//     booster_remain_before}.
func (c *creditsImpl) CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error) {
	if c.estimation == nil {
		return nil, fmt.Errorf("creditsImpl.CheckAndEstimate: estimation biz not configured (wire error)")
	}
	bal, err := c.GetBalance(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("checkAndEstimate: balance: %w", err)
	}
	// Trace metadata added BEFORE the span so both are visible on the trace
	// root even if estimation errors mid-flight.
	updateTraceMetadataForCredits(ctx, user, *bal)

	estimated, coefID, err := c.estimation.EstimateCredits(ctx, op, in.PromptChars, in.Model, in.Provider)
	if err != nil {
		return nil, fmt.Errorf("checkAndEstimate: estimate: %w", err)
	}
	total := bal.SubRemain + bal.BoosterRemain
	pre := &PreCheckResult{
		SkipDeduction:    false,
		Sufficient:       total >= estimated,
		EstimatedCredits: estimated,
		CoefficientID:    coefID,
		Balance:          *bal,
	}

	// Load the coefficient row for span emission (char/prompt/buffer values).
	// Cheap: coef lookup is already cached by the estimation biz in prod.
	var coef *model.CreditEstimationCoefficient
	if impl := c.estimationImpl(); impl != nil {
		if row, coefErr := impl.getActiveCoefficient(ctx, in.Provider, in.Model, string(op)); coefErr == nil {
			coef = row
		}
	}
	emitCreditEstimateSpan(ctx, user, op, in, pre, coef)

	if !pre.Sufficient {
		return pre, fmt.Errorf("%w: need %d credits, have %d", ErrInsufficientCredits, estimated, total)
	}
	return pre, nil
}

// estimationImpl is a narrow escape hatch exposing the concrete estimationBiz
// so CheckAndEstimate can re-use its getActiveCoefficient helper for span
// emission. If a different IEstimationBiz implementation is wired at test
// time, this returns nil and span emission falls back to nil coef (no
// detail fields) rather than crashing.
func (c *creditsImpl) estimationImpl() *estimationBiz {
	if impl, ok := c.estimation.(*estimationBiz); ok {
		return impl
	}
	return nil
}

// Reserve: same-transaction (DeductCreditsTx + INSERT credit_reservation +
// INSERT credit_reservation_item × N). Idempotency is enforced by the unique
// index uk_idempotency_key — a duplicate key hit dispatches to a lookup of
// the existing reservation and that row is returned unchanged.
//
// Spec refs: §1.4 / §3.10 (tx nesting contract) / §3.2 (runNode sequence).
func (c *creditsImpl) Reserve(
	ctx context.Context, user *model.User, op Operation,
	estimated int64, coefID uint64, idempotencyKey *string,
) (*Reservation, error) {
	if estimated <= 0 {
		return nil, fmt.Errorf("creditsImpl.Reserve: estimated credits must be > 0, got %d", estimated)
	}

	// Idempotency fast path: if the key already exists, return existing row.
	// This short-circuits BEFORE opening a tx — no wasted deduction if
	// caller retries after a successful first attempt.
	if idempotencyKey != nil && *idempotencyKey != "" {
		if existing, err := c.findReservationByIdempKey(ctx, *idempotencyKey); err == nil && existing != nil {
			return existing, nil
		}
	}

	// Ensure credit_account exists BEFORE the tx so that the ensure-call
	// does not race for a connection with the outer tx (spec §3.10 +
	// DeductCreditsTx doc comment).
	if _, err := c.store.Credits().GetOrCreateAccount(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("creditsImpl.Reserve: ensure credit_account: %w", err)
	}

	reference := referenceFromOp(op)
	rsvRow := &model.CreditReservation{
		UserID:          user.ID,
		ReferenceType:   reference.refType,
		ReferenceID:     reference.refID, // filled in from idempotencyKey / "pending"
		Operation:       string(op),
		ReservedCredits: estimated,
		CoefficientID:   &coefID,
		Status:          string(StatusReserved),
		IdempotencyKey:  idempotencyKey,
	}
	if idempotencyKey != nil {
		rsvRow.ReferenceID = *idempotencyKey
	} else {
		rsvRow.ReferenceID = ""
	}

	var items []PackageDeduction
	txErr := c.store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. FIFO deduction inside the outer tx — returns items for seq emission.
		debited, err := c.biz.DeductCreditsTx(ctx, tx, user.ID, estimated, "reserve:"+string(op))
		if err != nil {
			return err // ErrInsufficientCredits bubbles up; tx rolls back.
		}
		items = debited

		// 2. Write the reservation row.
		if err := tx.Create(rsvRow).Error; err != nil {
			// Unique-constraint on idempotency_key means a concurrent writer
			// inserted the same key — look it up and use that row.
			if isUniqueKeyViolation(err) && idempotencyKey != nil {
				existing, lookupErr := c.findReservationByIdempKeyTx(ctx, tx, *idempotencyKey)
				if lookupErr != nil {
					return fmt.Errorf("creditsImpl.Reserve: idempotency lookup: %w", lookupErr)
				}
				if existing != nil {
					// Mutate the outer rsvRow to point at the winner so caller
					// sees a consistent view. Return a sentinel so outer tx
					// aborts and we return the existing row from outside.
					*rsvRow = toDBReservation(existing)
					return errReservationAlreadyExists
				}
			}
			return fmt.Errorf("creditsImpl.Reserve: insert credit_reservation: %w", err)
		}

		// 3. Write reservation items in FIFO order (seq = idx+1).
		itemRows := make([]model.CreditReservationItem, 0, len(items))
		for i, d := range items {
			itemRows = append(itemRows, model.CreditReservationItem{
				ReservationID:    rsvRow.ID,
				PackageID:        d.PackageID,
				Credits:          d.Credits,
				PackageType:      d.PackageType,
				PackageExpiresAt: d.ExpiresAt,
				Seq:              i + 1,
			})
		}
		if len(itemRows) > 0 {
			if err := tx.Create(&itemRows).Error; err != nil {
				return fmt.Errorf("creditsImpl.Reserve: insert reservation items: %w", err)
			}
		}
		return nil
	})

	// If the concurrent-idempotency path triggered, re-fetch and return the
	// winner row. Caller never sees an error.
	if errors.Is(txErr, errReservationAlreadyExists) && idempotencyKey != nil {
		existing, err := c.findReservationByIdempKey(ctx, *idempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("creditsImpl.Reserve: post-conflict lookup: %w", err)
		}
		return existing, nil
	}
	if txErr != nil {
		return nil, txErr
	}

	var resultCoefID uint64
	if rsvRow.CoefficientID != nil {
		resultCoefID = *rsvRow.CoefficientID
	}
	result := &Reservation{
		ID:              rsvRow.ID,
		UserID:          rsvRow.UserID,
		ReferenceType:   rsvRow.ReferenceType,
		ReferenceID:     rsvRow.ReferenceID,
		Operation:       Operation(rsvRow.Operation),
		ReservedCredits: rsvRow.ReservedCredits,
		CoefficientID:   resultCoefID,
		Status:          StatusReserved,
		IdempotencyKey:  rsvRow.IdempotencyKey,
		Items:           toReservationItems(items),
		CreatedAt:       rsvRow.CreatedAt,
	}

	// Post-commit balance snapshot for span output (spec §5.1.2).
	subAfter, boosterAfter := int64(0), int64(0)
	if bal, berr := c.GetBalance(ctx, user); berr == nil {
		subAfter = bal.SubRemain
		boosterAfter = bal.BoosterRemain
	}
	emitCreditReserveSpan(ctx, user, result, subAfter, boosterAfter)

	return result, nil
}

// Reconcile adjusts the reservation against the actual LLM cost:
//
//   - delta < 0 (actual < reserved): refund |delta| credits by walking items
//     seq ASC and crediting each item.package_id up to its debit amount. If
//     the refund exhausts the delta before walking all items, stop early.
//   - delta > 0 (actual > reserved): top-up |delta| credits via
//     DeductCreditsTx (FIFO) so any new packages that activated since Reserve
//     are eligible. A short-balance here is recorded as a
//     credit_transaction.operation=reconcile_debt entry per spec §5.3.
//   - delta == 0: no balance movement.
//
// In all paths the reservation transitions reserved → reconciled with
// actual_cost_cents / delta / reconciled_at / finalize_reason='normal'
// atomically inside a single transaction. Spec §1.4 / §3.3.
func (c *creditsImpl) Reconcile(ctx context.Context, reservationID uint64, actualCostCents int64) error {
	// ICreditService.Reconcile public entry — token counts unknown at this call
	// site; delegates to reconcileWithTokens with 0/0. Callers routing through
	// FinalizeReservation should set rsv.ActualPromptTokens / ActualCompletionTokens
	// so tokens reach the credit-reconcile span metadata.
	return c.reconcileWithTokens(ctx, reservationID, actualCostCents, 0, 0)
}

// reconcileWithTokens is the internal Reconcile implementation. Separated from
// the public Reconcile so FinalizeReservation can thread token metadata to the
// credit-reconcile span without widening the ICreditService interface.
func (c *creditsImpl) reconcileWithTokens(
	ctx context.Context, reservationID uint64, actualCostCents int64,
	actualPromptTokens, actualCompletionTokens int,
) error {
	var (
		reservedCredits    int64
		delta              int64
		reconcileDirection string
		hasDebt            bool // P1-2: set true when top-up ran into ErrInsufficientCredits
		refundedPackages   []map[string]interface{}
	)
	txErr := c.store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, row, err := c.loadReservationForUpdate(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		if ReservationStatus(row.Status) != StatusReserved {
			return ErrAlreadyFinalized
		}
		var items []model.CreditReservationItem
		if err := tx.WithContext(ctx).
			Where("reservation_id = ?", reservationID).
			Order("seq ASC").
			Find(&items).Error; err != nil {
			return fmt.Errorf("load items: %w", err)
		}

		reservedCredits = row.ReservedCredits
		delta = actualCostCents - row.ReservedCredits
		switch {
		case delta < 0:
			reconcileDirection = "refund"
			if err := c.refundToItems(ctx, tx, row.UserID, items, -delta); err != nil {
				return err
			}
			// Build the refunded_to_packages span output snapshot.
			refundedPackages = snapshotItemsAsMap(items, -delta)
		case delta > 0:
			reconcileDirection = "topup"
			// Top-up: FIFO debit from whatever packages are active NOW.
			if _, err := c.biz.DeductCreditsTx(ctx, tx, row.UserID, delta,
				model.CreditTxOpPrefixReconcile+row.Operation); err != nil {
				// If the user no longer has enough credits, we still reconcile
				// (record the debt via delta) rather than blocking completion —
				// the operation already succeeded and the user owes credits.
				if !errors.Is(err, ErrInsufficientCredits) {
					return err
				}
				// ErrInsufficientCredits → log + continue + 写 debt 台账（spec §5.3）。
				// CreditTransaction 以 amount=delta（正，表示欠多少）+
				// operation="reconcile_debt:<op>" 记录，供 ops 事后按
				// `WHERE operation LIKE 'reconcile_debt:%'` 审计与追收。
				// hasDebt=true 同时上报 Langfuse span。
				hasDebt = true
				debtRow := &model.CreditTransaction{
					UserID:     row.UserID,
					PackageID:  0, // 无具体 package（扣不到任何包才进这条分支）
					Amount:     delta,
					Operation:  model.CreditTxOpPrefixReconcileDebt + row.Operation,
					BizRefType: "reservation",
					BizRefID:   strconv.FormatUint(reservationID, 10),
					CreatedAt:  time.Now(),
				}
				if derr := c.store.Credits().CreateTransaction(ctx, tx, debtRow); derr != nil {
					// 台账失败不阻塞对账（span has_debt=true 已是兜底），但记 error log。
					log.Errorw("Reconcile debt ledger write failed",
						"reservation_id", reservationID, "delta", delta, "err", derr)
				}
				log.Warnw("Reconcile top-up insufficient — recorded as debt",
					"reservation_id", reservationID, "delta", delta, "err", err)
			}
		default:
			reconcileDirection = "noop"
		}

		finalizeReason := "normal"
		now := time.Now()
		updates := map[string]interface{}{
			"status":            string(StatusReconciled),
			"actual_cost_cents": actualCostCents,
			"delta":             delta,
			"finalize_reason":   finalizeReason,
			"reconciled_at":     now,
		}
		if err := tx.WithContext(ctx).Model(&model.CreditReservation{}).
			Where("id = ?", reservationID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("update reservation: %w", err)
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}
	// Emit span only on successful reconcile (spec §5.1.3). Token counts are
	// threaded from FinalizeReservation via rsv.ActualPromptTokens /
	// ActualCompletionTokens; absent callers pass 0.
	emitCreditReconcileSpan(ctx, reservationID,
		reservedCredits, actualCostCents, delta,
		actualPromptTokens, actualCompletionTokens,
		reconcileDirection, refundedPackages, hasDebt)
	return nil
}

// snapshotItemsAsMap builds the span-output representation of the items
// affected by a refund / reconcile. When amount is smaller than the item's
// credits the returned entry reflects the actually-refunded portion.
func snapshotItemsAsMap(items []model.CreditReservationItem, amount int64) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	remaining := amount
	for _, item := range items {
		if remaining <= 0 {
			break
		}
		this := remaining
		if this > item.Credits {
			this = item.Credits
		}
		out = append(out, map[string]interface{}{
			"package_id":   item.PackageID,
			"credits":      this,
			"package_type": item.PackageType,
			"seq":          item.Seq,
		})
		remaining -= this
	}
	return out
}

// Refund transitions reserved → refunded and restores each item.credits back
// to its original package_id (seq ASC). If a package is already expired, the
// refund to that package is a no-op per spec §2.4 (expired_by_cron etc.).
func (c *creditsImpl) Refund(ctx context.Context, reservationID uint64, reason string) error {
	var (
		totalRefunded   int64
		refundedItemMap []map[string]interface{}
	)
	txErr := c.store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, row, err := c.loadReservationForUpdate(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		if ReservationStatus(row.Status) != StatusReserved {
			return ErrAlreadyFinalized
		}
		var items []model.CreditReservationItem
		if err := tx.WithContext(ctx).
			Where("reservation_id = ?", reservationID).
			Order("seq ASC").
			Find(&items).Error; err != nil {
			return fmt.Errorf("load items: %w", err)
		}

		refundedItemMap = make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			refunded, err := c.refundOneItem(ctx, tx, row.UserID, item, item.Credits)
			if err != nil {
				return err
			}
			totalRefunded += refunded
			refundedItemMap = append(refundedItemMap, map[string]interface{}{
				"package_id":   item.PackageID,
				"credits":      refunded, // actually-refunded amount (may be 0 if package expired)
				"package_type": item.PackageType,
				"seq":          item.Seq,
			})
		}

		now := time.Now()
		updates := map[string]interface{}{
			"status":          string(StatusRefunded),
			"finalize_reason": reason,
			"reconciled_at":   now,
		}
		if err := tx.WithContext(ctx).Model(&model.CreditReservation{}).
			Where("id = ?", reservationID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("update reservation: %w", err)
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}
	emitCreditRefundSpan(ctx, reservationID, reason, totalRefunded, refundedItemMap)
	return nil
}

// refundToItems walks items seq ASC and refunds up to `amount` credits total.
// Used by Reconcile's delta<0 path. Stops once amount is exhausted.
func (c *creditsImpl) refundToItems(
	ctx context.Context, tx *gorm.DB, userID uint,
	items []model.CreditReservationItem, amount int64,
) error {
	remaining := amount
	for _, item := range items {
		if remaining <= 0 {
			break
		}
		refundThis := remaining
		if refundThis > item.Credits {
			refundThis = item.Credits
		}
		actually, err := c.refundOneItem(ctx, tx, userID, item, refundThis)
		if err != nil {
			return err
		}
		remaining -= actually
	}
	return nil
}

// refundOneItem refunds `amount` credits back to item.PackageID and bumps the
// account balance accordingly. Expired packages are skipped (returns 0, nil)
// per spec §2.4 — the caller already captured the refund in reservation state
// so the user does not see a second refund from cron.
func (c *creditsImpl) refundOneItem(
	ctx context.Context, tx *gorm.DB, userID uint,
	item model.CreditReservationItem, amount int64,
) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}
	var pkg model.CreditPackage
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&pkg, item.PackageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Package deleted (GDPR purge etc.) — best-effort no-op.
			return 0, nil
		}
		return 0, fmt.Errorf("lock package: %w", err)
	}
	if pkg.Status == model.CreditPackageExpired {
		// Spec §2.4: expired package → refund is a no-op.
		return 0, nil
	}
	pkg.RemainCredits += amount
	// Revive exhausted packages when refund brings them above zero.
	if pkg.Status == model.CreditPackageExhausted && pkg.RemainCredits > 0 {
		pkg.Status = model.CreditPackageActive
	}
	if err := tx.WithContext(ctx).Save(&pkg).Error; err != nil {
		return 0, fmt.Errorf("update package remain_credits: %w", err)
	}
	// Write a CreditTransaction audit row (positive amount = refund).
	txn := &model.CreditTransaction{
		UserID:    userID,
		PackageID: pkg.ID,
		Amount:    amount,
		Operation: "refund",
	}
	if err := tx.WithContext(ctx).Create(txn).Error; err != nil {
		return 0, fmt.Errorf("write refund transaction: %w", err)
	}
	// Bump the cached balance.
	if err := c.store.Credits().UpdateBalance(ctx, tx, userID, amount); err != nil {
		return 0, fmt.Errorf("update balance: %w", err)
	}
	return amount, nil
}

// FinalizeReservation is the single defer-exit point. Dispatch:
//
//	opErr != nil         → Refund(reason = classifyReason(opErr))
//	actualCost == nil/0  → Refund(reason = "no_actual_cost")  // pricing failure or stream abort
//	otherwise            → Reconcile(actualCost)
//
// The rsv parameter is trusted — callers pass the Reservation returned from
// Reserve, so we use rsv.ID as the key. The returned error is generally
// ignored by defer (it's an observability signal, not a control-flow one).
func (c *creditsImpl) FinalizeReservation(
	ctx context.Context, rsv *Reservation, actualCostCents *int64, opErr *error,
) error {
	if rsv == nil {
		return nil
	}
	if opErr != nil && *opErr != nil {
		return c.Refund(ctx, rsv.ID, classifyReason(*opErr))
	}
	if actualCostCents == nil || *actualCostCents == 0 {
		return c.Refund(ctx, rsv.ID, "no_actual_cost")
	}
	return c.reconcileWithTokens(ctx, rsv.ID, *actualCostCents,
		rsv.ActualPromptTokens, rsv.ActualCompletionTokens)
}

// classifyReason maps a Go error into one of the spec §5.1.4 refund reason
// ENUM values. Unknown errors default to "op_failed".
func classifyReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "user_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "provider_timeout"
	default:
		return "op_failed"
	}
}

// GetBalance returns the credit_package breakdown (sub + booster). The same
// fields are consumed by the frontend credits.ts store (spec §1.8 + §2.11.1).
// SubExpiresAt / BoosterEarliestExpiresAt fill expiry timestamps so the front-
// end can show "本月 MM-DD 过期" / "最早 YYYY-MM-DD 过期" badges (review P1-A fix).
func (c *creditsImpl) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
	subTotal, subRemain, boosterTotal, boosterRemain, err := c.store.Credits().GetQuotaBreakdown(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("creditsImpl.GetBalance: quota breakdown: %w", err)
	}
	bal := &BalanceBreakdown{
		BillingMode:   model.BillingModeCredits,
		SubTotal:      subTotal,
		SubRemain:     subRemain,
		BoosterTotal:  boosterTotal,
		BoosterRemain: boosterRemain,
	}

	// P1-A fix: query earliest-expiring active package per type to fill the
	// expiry fields (best-effort; if query fails we log and return bal with
	// nil expiry fields — the spec marks them optional).
	db := c.store.DB().WithContext(ctx)
	type expiryRow struct {
		ExpiresAt time.Time
	}
	var subRow expiryRow
	if err := db.Raw(
		`SELECT expires_at FROM credit_package
		 WHERE user_id = ? AND type = ? AND status = 'active' AND expires_at > NOW()
		 ORDER BY expires_at ASC LIMIT 1`,
		user.ID, model.CreditTypeSubscription,
	).Scan(&subRow).Error; err != nil {
		log.Warnw("creditsImpl.GetBalance: sub expires_at query failed", "user_id", user.ID, "err", err)
	} else if !subRow.ExpiresAt.IsZero() {
		t := subRow.ExpiresAt
		bal.SubExpiresAt = &t
	}
	var boosterRow expiryRow
	if err := db.Raw(
		`SELECT expires_at FROM credit_package
		 WHERE user_id = ? AND type = ? AND status = 'active' AND expires_at > NOW()
		 ORDER BY expires_at ASC LIMIT 1`,
		user.ID, model.CreditTypeBooster,
	).Scan(&boosterRow).Error; err != nil {
		log.Warnw("creditsImpl.GetBalance: booster expires_at query failed", "user_id", user.ID, "err", err)
	} else if !boosterRow.ExpiresAt.IsZero() {
		t := boosterRow.ExpiresAt
		bal.BoosterEarliestExpiresAt = &t
	}
	return bal, nil
}

// ---------------------------------------------------------------------------
// creditsImpl helpers
// ---------------------------------------------------------------------------

// opReference maps an Operation onto the credit_reservation.reference_type
// column (kept separate from the column name for future-proofing).
type opReference struct {
	refType string
	refID   string // caller-provided at ReferenceID assembly time
}

func referenceFromOp(op Operation) opReference {
	switch op {
	case OpSopRun:
		return opReference{refType: "sop_run"}
	case OpSopChat:
		return opReference{refType: "sop_chat"}
	case OpSalesragChat:
		return opReference{refType: "salesrag_chat"}
	case OpProfileAnalysis:
		return opReference{refType: "profile_analysis"}
	case OpFileParse:
		return opReference{refType: "file_parse"}
	case OpStyleAnalysis:
		return opReference{refType: "style_analysis"}
	case OpOCR:
		return opReference{refType: "ocr"}
	default:
		return opReference{refType: string(op)}
	}
}

// errReservationAlreadyExists is the internal sentinel used to unwind the
// Reserve transaction when a concurrent writer hit the idempotency_key
// unique constraint ahead of us.
var errReservationAlreadyExists = errors.New("reserve: idempotency key winner elsewhere")

// isUniqueKeyViolation detects MySQL/SQLite unique index violations without
// importing driver-specific error types. Both drivers surface the message
// containing the words "UNIQUE constraint failed" (SQLite) or
// "Duplicate entry" (MySQL).
func isUniqueKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, needle := range []string{
		"UNIQUE constraint failed",
		"Duplicate entry",
		"duplicate key value",
		"1062",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// findReservationByIdempKey looks up an existing credit_reservation + its
// items via a plain (non-locking) query. Returns (nil, nil) when no match.
func (c *creditsImpl) findReservationByIdempKey(ctx context.Context, key string) (*Reservation, error) {
	var row model.CreditReservation
	if err := c.store.DB().WithContext(ctx).
		Where("idempotency_key = ?", key).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return c.loadReservationWithItems(ctx, c.store.DB().WithContext(ctx), row.ID)
}

// findReservationByIdempKeyTx is the same lookup but inside a caller-managed
// transaction. Used to recover from a unique-key conflict inside Reserve.
func (c *creditsImpl) findReservationByIdempKeyTx(ctx context.Context, tx *gorm.DB, key string) (*Reservation, error) {
	var row model.CreditReservation
	if err := tx.WithContext(ctx).Where("idempotency_key = ?", key).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return c.loadReservationWithItems(ctx, tx, row.ID)
}

// loadReservationWithItems is the canonical reservation loader used by
// Reconcile/Refund/FinalizeReservation as well as the idempotency path. Use a
// FOR UPDATE variant via SelectForUpdate=true when inside a mutating tx.
func (c *creditsImpl) loadReservationWithItems(ctx context.Context, tx *gorm.DB, reservationID uint64) (*Reservation, error) {
	var row model.CreditReservation
	if err := tx.WithContext(ctx).First(&row, reservationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReservationNotFound
		}
		return nil, err
	}
	var items []model.CreditReservationItem
	if err := tx.WithContext(ctx).
		Where("reservation_id = ?", reservationID).
		Order("seq ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return fromDBReservation(&row, items), nil
}

// loadReservationForUpdate is the locking variant (SELECT ... FOR UPDATE).
// Used by Reconcile and Refund to atomically transition state.
func (c *creditsImpl) loadReservationForUpdate(ctx context.Context, tx *gorm.DB, reservationID uint64) (*Reservation, *model.CreditReservation, error) {
	var row model.CreditReservation
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&row, reservationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrReservationNotFound
		}
		return nil, nil, err
	}
	var items []model.CreditReservationItem
	if err := tx.WithContext(ctx).
		Where("reservation_id = ?", reservationID).
		Order("seq ASC").
		Find(&items).Error; err != nil {
		return nil, nil, err
	}
	return fromDBReservation(&row, items), &row, nil
}

// fromDBReservation maps the GORM model → domain struct.
func fromDBReservation(row *model.CreditReservation, items []model.CreditReservationItem) *Reservation {
	var coefID uint64
	if row.CoefficientID != nil {
		coefID = *row.CoefficientID
	}
	rsv := &Reservation{
		ID:              row.ID,
		UserID:          row.UserID,
		ReferenceType:   row.ReferenceType,
		ReferenceID:     row.ReferenceID,
		Operation:       Operation(row.Operation),
		ReservedCredits: row.ReservedCredits,
		CoefficientID:   coefID,
		Status:          ReservationStatus(row.Status),
		ActualCostCents: row.ActualCostCents,
		Delta:           row.Delta,
		FinalizeReason:  row.FinalizeReason,
		IdempotencyKey:  row.IdempotencyKey,
		CreatedAt:       row.CreatedAt,
		ReconciledAt:    row.ReconciledAt,
	}
	if len(items) > 0 {
		rsv.Items = make([]ReservationItem, 0, len(items))
		for _, i := range items {
			rsv.Items = append(rsv.Items, ReservationItem{
				PackageID:        i.PackageID,
				Credits:          i.Credits,
				PackageType:      i.PackageType,
				PackageExpiresAt: i.PackageExpiresAt,
				Seq:              i.Seq,
			})
		}
	}
	return rsv
}

// toDBReservation is the inverse — used only by the idempotency-conflict
// recovery path to rehydrate the outer rsvRow from a domain object.
func toDBReservation(rsv *Reservation) model.CreditReservation {
	var coefID *uint64
	if rsv.CoefficientID != 0 {
		coefID = &rsv.CoefficientID
	}
	return model.CreditReservation{
		ID:              rsv.ID,
		UserID:          rsv.UserID,
		ReferenceType:   rsv.ReferenceType,
		ReferenceID:     rsv.ReferenceID,
		Operation:       string(rsv.Operation),
		ReservedCredits: rsv.ReservedCredits,
		CoefficientID:   coefID,
		Status:          string(rsv.Status),
		ActualCostCents: rsv.ActualCostCents,
		Delta:           rsv.Delta,
		FinalizeReason:  rsv.FinalizeReason,
		IdempotencyKey:  rsv.IdempotencyKey,
		ReconciledAt:    rsv.ReconciledAt,
		CreatedAt:       rsv.CreatedAt,
	}
}

// reserveBudgetRow is the creditsImpl entry point for context-budget reservations.
// It mirrors the Reserve tx logic but writes estimation_source='context_budget'
// and populates the token profile / event id / provider / model columns.
// coefficient_id is left NULL (no R2 coefficient on the context-budget path).
// Spec §6.1.2.
func (c *creditsImpl) reserveBudgetRow(
	ctx context.Context, user *model.User, op Operation,
	estimated int64, input BudgetReservationInput, idempotencyKey *string,
) (*Reservation, error) {
	if estimated <= 0 {
		return nil, fmt.Errorf("creditsImpl.reserveBudgetRow: estimated credits must be > 0, got %d", estimated)
	}

	// Idempotency fast path — same contract as Reserve.
	if idempotencyKey != nil && *idempotencyKey != "" {
		if existing, err := c.findReservationByIdempKey(ctx, *idempotencyKey); err == nil && existing != nil {
			return existing, nil
		}
	}

	// Ensure credit_account exists before opening the tx.
	if _, err := c.store.Credits().GetOrCreateAccount(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("creditsImpl.reserveBudgetRow: ensure credit_account: %w", err)
	}

	reference := referenceFromOp(op)
	rsvRow := &model.CreditReservation{
		UserID:          user.ID,
		ReferenceType:   reference.refType,
		Operation:       string(op),
		ReservedCredits: estimated,
		CoefficientID:   nil, // context_budget path never uses R2 coefficient
		Status:          string(StatusReserved),
		IdempotencyKey:  idempotencyKey,
		// Context-budget extension fields (spec §3.6).
		EstimationSource:          "context_budget",
		EstimatedPromptTokens:     input.EstimatedPromptTokens,
		EstimatedCompletionTokens: input.EstimatedCompletionTokens,
		Provider:                  input.Provider,
		Model:                     input.Model,
	}
	if idempotencyKey != nil {
		rsvRow.ReferenceID = *idempotencyKey
	}
	// Populate optional nullable FK fields only when non-zero.
	if input.TokenProfileID != 0 {
		v := input.TokenProfileID
		rsvRow.TokenProfileID = &v
	}
	if input.ContextBudgetEventID != 0 {
		v := input.ContextBudgetEventID
		rsvRow.ContextBudgetEventID = &v
	}

	var items []PackageDeduction
	txErr := c.store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		debited, err := c.biz.DeductCreditsTx(ctx, tx, user.ID, estimated, "budget_reserve:"+string(op))
		if err != nil {
			return err
		}
		items = debited

		if err := tx.Create(rsvRow).Error; err != nil {
			if isUniqueKeyViolation(err) && idempotencyKey != nil {
				existing, lookupErr := c.findReservationByIdempKeyTx(ctx, tx, *idempotencyKey)
				if lookupErr != nil {
					return fmt.Errorf("creditsImpl.reserveBudgetRow: idempotency lookup: %w", lookupErr)
				}
				if existing != nil {
					*rsvRow = toDBReservation(existing)
					return errReservationAlreadyExists
				}
			}
			return fmt.Errorf("creditsImpl.reserveBudgetRow: insert credit_reservation: %w", err)
		}

		itemRows := make([]model.CreditReservationItem, 0, len(items))
		for i, d := range items {
			itemRows = append(itemRows, model.CreditReservationItem{
				ReservationID:    rsvRow.ID,
				PackageID:        d.PackageID,
				Credits:          d.Credits,
				PackageType:      d.PackageType,
				PackageExpiresAt: d.ExpiresAt,
				Seq:              i + 1,
			})
		}
		if len(itemRows) > 0 {
			if err := tx.Create(&itemRows).Error; err != nil {
				return fmt.Errorf("creditsImpl.reserveBudgetRow: insert reservation items: %w", err)
			}
		}
		return nil
	})

	if errors.Is(txErr, errReservationAlreadyExists) && idempotencyKey != nil {
		existing, err := c.findReservationByIdempKey(ctx, *idempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("creditsImpl.reserveBudgetRow: post-conflict lookup: %w", err)
		}
		return existing, nil
	}
	if txErr != nil {
		return nil, txErr
	}

	result := &Reservation{
		ID:              rsvRow.ID,
		UserID:          rsvRow.UserID,
		ReferenceType:   rsvRow.ReferenceType,
		ReferenceID:     rsvRow.ReferenceID,
		Operation:       Operation(rsvRow.Operation),
		ReservedCredits: rsvRow.ReservedCredits,
		CoefficientID:   0, // context_budget: no coefficient
		Status:          StatusReserved,
		IdempotencyKey:  rsvRow.IdempotencyKey,
		Items:           toReservationItems(items),
		CreatedAt:       rsvRow.CreatedAt,
	}
	return result, nil
}

// toReservationItems maps the FIFO debit []PackageDeduction into the
// domain []ReservationItem (seq is re-derived here from index).
func toReservationItems(items []PackageDeduction) []ReservationItem {
	out := make([]ReservationItem, 0, len(items))
	for i, d := range items {
		out = append(out, ReservationItem{
			PackageID:        d.PackageID,
			Credits:          d.Credits,
			PackageType:      d.PackageType,
			PackageExpiresAt: d.ExpiresAt,
			Seq:              i + 1,
		})
	}
	return out
}
