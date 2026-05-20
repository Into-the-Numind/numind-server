package credit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// translateMembershipInsufficient maps membership.MembershipService.DeductCreditsTx's
// errno.ErrInsufficientCredits return into this package's credit.ErrInsufficientCredits
// sentinel so callers (and tests) can errors.Is-check a single package-local error.
// Other error types pass through unchanged.
//
// We intentionally drop the wrapped membership errno here: keeping it produced
// log lines like "insufficient credits: insufficient credits" (the two layers
// carry the same semantic message). Callers that need richer context wrap with
// their own format string (see e.g. credit_service.go:240/315/442 which append
// the shortfall amounts).
func translateMembershipInsufficient(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errno.ErrInsufficientCredits) {
		return fmt.Errorf("%w", ErrInsufficientCredits)
	}
	return err
}

// creditService is the ICreditService implementation. Post legacy-deprecation
// (T1), the dispatch is gone — all flows route directly to creditsImpl.
// The struct remains so the interface boundary (and span/metadata wiring) stays
// stable across future strategy additions.
type creditService struct {
	store         store.IStore
	biz           ICreditBiz
	pricing       pricing.ICalculator
	membershipSvc *membership.MembershipService
	// Pre-instantiated leg so each dispatch is a plain method call.
	credits *creditsImpl

	// adminConsumer is the credit-local interface. biz.go wire layer constructs
	// a budget.AdminTestConsumer + adapter struct that satisfies credit.AdminTestConsumer.
	// nil until SetAdminTestConsumer is called — ReserveAgentTest then fails fast.
	adminConsumer AdminTestConsumer // #12 agent-mode-billing-integration
}

// NewCreditService constructs the singleton ICreditService used throughout the
// app. Pass the existing store.IStore, an ICreditBiz (for quota queries), a
// pricing.ICalculator (used by credits leg for R2 estimation), and a
// membershipSvc (used for cycle/booster/trial balance reads).
//
// The estimation biz is built internally from ds + pc so callers don't need to
// know about the sub-dependency. If pc is nil, the estimation leg is also nil
// and creditsImpl.CheckAndEstimate returns a config error rather than panicking.
func NewCreditService(ds store.IStore, biz ICreditBiz, pc pricing.ICalculator, membershipSvc *membership.MembershipService) ICreditService {
	var est IEstimationBiz
	if pc != nil {
		est = NewEstimationBiz(ds, pc)
	}
	return &creditService{
		store:         ds,
		biz:           biz,
		pricing:       pc,
		membershipSvc: membershipSvc,
		credits:       &creditsImpl{store: ds, biz: biz, pricing: pc, estimation: est, membershipSvc: membershipSvc},
	}
}

// CheckAndEstimate routes the R2 char-based precheck to the credits leg.
// Post legacy-deprecation (T1) there is no longer a billing-mode dispatch.
func (s *creditService) CheckAndEstimate(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*PreCheckResult, error) {
	return s.credits.CheckAndEstimate(ctx, user, op, in)
}

// Reserve creates a credit_reservation via the credits leg. Post
// legacy-deprecation (T1) all users route through this path.
func (s *creditService) Reserve(ctx context.Context, user *model.User, op Operation, estimated int64, coefID uint64, idempotencyKey *string) (*Reservation, error) {
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

// GetBalance returns the three-pool (sub + booster + trial) credits breakdown.
// Post legacy-deprecation (T1) the billing-mode dispatch is removed.
//
// #12 agent-mode-billing-integration: appends AdminTestPool field for parent
// accounts (User.ParentUserID == nil). Sub-accounts get nil (JSON omitempty
// elides the field). Fetch failure logs warn and proceeds with nil.
func (s *creditService) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
	bal, err := s.credits.GetBalance(ctx, user)
	if err != nil {
		return nil, err
	}
	if isParentAccount(user) && s.adminConsumer != nil {
		status, err := s.adminConsumer.Status(ctx, user.ID, time.Now().UTC())
		if err != nil {
			log.Warnw("GetBalance: admin_test status fetch failed", "user_id", user.ID, "error", err)
		} else if status != nil {
			bal.AdminTestPool = &AdminTestPoolView{
				Granted:      status.Granted,
				Used:         status.Used,
				Remaining:    status.Remaining,
				PeriodEnd:    status.PeriodEnd.Format("2006-01-02"),
				DaysToExpire: status.DaysToExpire,
			}
		}
	}
	return bal, nil
}

// SetAdminTestConsumer injects the credit-local AdminTestConsumer dependency (#12).
// Setter pattern chosen instead of constructor param to avoid breaking 4 existing
// NewCreditService callers; biz.go wire layer constructs a budget-side impl + adapter.
func (s *creditService) SetAdminTestConsumer(c AdminTestConsumer) {
	s.adminConsumer = c
}

// isParentAccount returns true when user is a "parent" (B2B 父账户) in the B2B2C model.
// v1 rule: User.ParentUserID == nil means top-level account (independent or parent).
// #14 may refine with explicit role / B2B grant flag.
func isParentAccount(u *model.User) bool {
	return u != nil && u.ParentUserID == nil
}

// ReserveAgentTest implements ICreditService — see contracts.go for contract.
func (s *creditService) ReserveAgentTest(ctx context.Context, parentUser *model.User, estimated int64, idempotencyKey *string) (*Reservation, error) {
	if s.adminConsumer == nil {
		return nil, fmt.Errorf("ReserveAgentTest: admin consumer not wired")
	}
	if parentUser == nil {
		return nil, fmt.Errorf("ReserveAgentTest: parent user is nil")
	}
	if estimated <= 0 {
		return nil, fmt.Errorf("ReserveAgentTest: estimated must be > 0, got %d", estimated)
	}
	txID, err := s.adminConsumer.Consume(ctx, parentUser.ID, estimated)
	if err != nil {
		// 桥接：credit.ErrAdminTestExhausted (local sentinel) 或 errno.ErrAdminTestExhausted
		// 或 budget.ErrAdminTestExhausted（通过 adapter 透传）都映射为 errno errno.
		if errors.Is(err, ErrAdminTestExhausted) || errors.Is(err, errno.ErrAdminTestExhausted) {
			return nil, errno.ErrAdminTestExhausted
		}
		return nil, fmt.Errorf("ReserveAgentTest: consume: %w", err)
	}
	rsv := &model.CreditReservation{
		UserID:           parentUser.ID,
		Operation:        "agent_test",
		ReferenceType:    "agent_test",
		ReferenceID:      fmt.Sprintf("admin_test_tx:%d", txID),
		ReservedCredits:  estimated,
		EstimationSource: "agent_test",
		Status:           "reserved",
	}
	if idempotencyKey != nil {
		rsv.IdempotencyKey = idempotencyKey
	}
	if err := s.store.DB().WithContext(ctx).Create(rsv).Error; err != nil {
		return nil, fmt.Errorf("ReserveAgentTest: create reservation: %w", err)
	}
	return &Reservation{
		ID:              rsv.ID,
		UserID:          parentUser.ID,
		ReferenceType:   "agent_test",
		ReferenceID:     rsv.ReferenceID,
		Operation:       "agent_test",
		ReservedCredits: estimated,
		Status:          StatusReserved,
		CreatedAt:       rsv.CreatedAt,
	}, nil
}

// ReconcileAgentTest implements ICreditService — see contracts.go for contract.
func (s *creditService) ReconcileAgentTest(ctx context.Context, reservationID uint64, actualCostCents int64) error {
	if s.adminConsumer == nil {
		return fmt.Errorf("ReconcileAgentTest: admin consumer not wired")
	}
	var rsv model.CreditReservation
	if err := s.store.DB().WithContext(ctx).First(&rsv, reservationID).Error; err != nil {
		return fmt.Errorf("ReconcileAgentTest: fetch reservation %d: %w", reservationID, err)
	}
	if rsv.Operation != "agent_test" {
		return fmt.Errorf("ReconcileAgentTest: reservation %d is not agent_test (got %s)", reservationID, rsv.Operation)
	}
	if rsv.Status != "reserved" {
		return fmt.Errorf("ReconcileAgentTest: reservation %d already %s", reservationID, rsv.Status)
	}
	var origTxID uint64
	if _, err := fmt.Sscanf(rsv.ReferenceID, "admin_test_tx:%d", &origTxID); err != nil {
		return fmt.Errorf("ReconcileAgentTest: parse ref ID %q: %w", rsv.ReferenceID, err)
	}
	refund := rsv.ReservedCredits - actualCostCents
	if refund > 0 {
		if err := s.adminConsumer.Refund(ctx, rsv.UserID, origTxID, refund); err != nil {
			return fmt.Errorf("ReconcileAgentTest: refund: %w", err)
		}
	} else if refund < 0 {
		topup := -refund
		if _, err := s.adminConsumer.Consume(ctx, rsv.UserID, topup); err != nil {
			return fmt.Errorf("ReconcileAgentTest: topup: %w", err)
		}
	}
	now := time.Now().UTC()
	delta := actualCostCents - rsv.ReservedCredits
	return s.store.DB().WithContext(ctx).Model(&rsv).Updates(map[string]any{
		"status":            "reconciled",
		"actual_cost_cents": actualCostCents,
		"delta":             delta,
		"reconciled_at":     now,
	}).Error
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
// normalises the raw operation via budgetOperationMap, then estimates
// EstimatedCredits from token counts (flat estimate: tokens × a fixed rate,
// falling back to GetEstimatedCredits if pricing.ICalculator is nil in tests).
// An unknown operation surfaces ErrUnknownBudgetOperation — fail-closed so we
// never silently charge a default operation.
//
// Post legacy-deprecation (T1) the billing-mode dispatch is removed.
// This is a parallel API to CheckAndEstimate; the R2 char-based path is
// preserved unchanged.
func (s *creditService) CheckAndEstimateBudget(ctx context.Context, user *model.User, input BudgetPrecheckInput) (*PreCheckResult, error) {
	// Step 1: normalize operation.
	op, found := budgetOperationMap[input.Operation]
	if !found {
		// Unknown operation with a billing-context user: fail closed.
		return nil, fmt.Errorf("%w: operation=%q", ErrUnknownBudgetOperation, input.Operation)
	}

	// Step 2: estimate from token counts. If pricing calculator available, use
	// it; otherwise fall back to the flat table (test / local path).
	var estimatedCredits int64
	if s.pricing != nil {
		costCents, err := s.pricing.CalculateCost(ctx, "llm_chat", input.Provider, input.Model,
			input.EstimatedPromptTokens, input.EstimatedCompletionTokens)
		if err == nil {
			// credits == cost_cents in this system (1:1 system-wide, no environment-specific conversion).
			// Note: this skips the R2 char-path safety buffer (1 + safetyBufferPct) because
			// context budget provides token estimates that are already conservative — the
			// buffer is built into TokenEstimationProfile.SafetyMultiplier upstream.
			estimatedCredits = costCents
		}
	}
	if estimatedCredits <= 0 {
		// Fallback: use the flat table (useful when pricing DB is unavailable or in tests).
		estimatedCredits = GetEstimatedCredits(string(op))
	}

	// Step 3: check balance.
	bal, err := s.credits.GetBalance(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("CheckAndEstimateBudget: balance: %w", err)
	}
	total := bal.SubRemain + bal.BoosterRemain + bal.TrialRemain
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
// Spec §6.1.2.
func (s *creditService) ReserveBudget(ctx context.Context, user *model.User, input BudgetReservationInput) (*Reservation, error) {
	// Precheck first to validate operation + check balance.
	pre, err := s.CheckAndEstimateBudget(ctx, user, input.BudgetPrecheckInput)
	if err != nil {
		return nil, fmt.Errorf("ReserveBudget: precheck: %w", err)
	}

	// Defensive: if a future precheck path ever sets SkipDeduction, honor it
	// by returning (nil, nil). Post legacy-deprecation (T1) this branch is
	// unreachable on the current callgraph.
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
	store         store.IStore
	biz           ICreditBiz
	pricing       pricing.ICalculator
	estimation    IEstimationBiz                // wired in NewCreditService
	membershipSvc *membership.MembershipService // wired in NewCreditService; used by T6+ for cycle/booster/trial reads
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
	total := bal.SubRemain + bal.BoosterRemain + bal.TrialRemain
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

	// Apply per-user-type multiplier (e.g. trial users burn at 0.5× rate).
	// The multiplier is snapshotted onto the reservation row so Reconcile can
	// apply the identical factor to actualCostCents regardless of later package
	// state changes.
	rawMultiplier, utmErr := c.store.Credits().GetUserTypeCreditMultiplier(ctx, user.ID)
	if utmErr != nil {
		log.Warnw("creditsImpl.Reserve: GetUserTypeCreditMultiplier failed, falling back to 1.0",
			"user_id", user.ID, "err", utmErr)
		rawMultiplier = 1.0
	}
	adjustedEstimated, snapshotMultiplier := applyUserTypeMultiplier(estimated, rawMultiplier)

	reference := referenceFromOp(op)
	rsvRow := &model.CreditReservation{
		UserID:             user.ID,
		ReferenceType:      reference.refType,
		ReferenceID:        reference.refID, // filled in from idempotencyKey / "pending"
		Operation:          string(op),
		ReservedCredits:    adjustedEstimated,
		CoefficientID:      &coefID,
		Status:             string(StatusReserved),
		IdempotencyKey:     idempotencyKey,
		UserTypeMultiplier: snapshotMultiplier,
	}
	if idempotencyKey != nil {
		rsvRow.ReferenceID = *idempotencyKey
	} else {
		rsvRow.ReferenceID = ""
	}

	// T6: legacy creditBiz.DeductCreditsTx fallback removed. All credits-mode
	// deduction goes through MembershipService.DeductCreditsTx (writes
	// credit_cycle / user_booster_balance / trial_grant). membershipSvc is
	// always wired in production (NewBiz); a nil here is a config bug.
	if c.membershipSvc == nil {
		return nil, fmt.Errorf("creditsImpl.Reserve: membershipSvc is nil (T6 wiring bug)")
	}
	var newPathItems []membership.DeductItem
	log.C(ctx).Infow("reserve: credits-mode new path (DeductCreditsTx)",
		"user_id", user.ID, "amount", adjustedEstimated, "operation", string(op))
	txErr := c.store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. FIFO deduction inside the outer tx — MembershipService writes
		// credit_cycle / user_booster_balance / trial_grant. Also writes
		// credit_transaction rows (T1 ledger contract) with operation="reserve:<op>".
		result, err := c.membershipSvc.DeductCreditsTx(ctx, tx, uint64(user.ID), adjustedEstimated, "reserve:"+string(op), time.Now().UTC())
		if err != nil {
			// Translate membership's errno.ErrInsufficientCredits to credit.ErrInsufficientCredits
			// so callers can errors.Is-check the package-local sentinel.
			return translateMembershipInsufficient(err)
		}
		newPathItems = result.Items

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
		// items carry source_type + source_id (package_id NULL — credit_package is gone).
		itemRows := make([]model.CreditReservationItem, 0, len(newPathItems))
		for i, di := range newPathItems {
			sourceType := string(di.SourceType)
			sourceID := di.SourceID
			// Map DeductSource → legacy PackageType column value for back-compat.
			pkgType := mapDeductSourceToPkgType(di.SourceType)
			itemRows = append(itemRows, model.CreditReservationItem{
				ReservationID:    rsvRow.ID,
				SourceType:       &sourceType,
				SourceID:         &sourceID,
				Credits:          di.Amount,
				PackageType:      pkgType,
				PackageExpiresAt: di.ExpiresAt,
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
		Items:           toReservationItems(newPathItems),
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
//     are eligible. If the user's balance is now insufficient for the top-up,
//     the platform absorbs the cost (audit P1-2, no-debt policy): an audit-
//     only credit_transaction with amount=0 and
//     operation="reserve_underestimate_absorbed" is written for ops monitoring,
//     and the reservation finalizes as status=reconciled normally. The
//     previous "reconcile_debt:<op>" positive-amount IOU has been retired
//     because it was never collected and confused ledger sums.
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
		hasDebt            bool // P1-2 (post-2026-05 audit): set true when top-up insufficient → platform absorbs cost. Surfaces on Langfuse span for ops monitoring.
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

		// Apply the user-type multiplier that was snapshotted at Reserve time.
		// actualCostCents from the billing middleware is the raw model cost; we
		// must scale it by the same factor so delta is computed on like-for-like
		// terms (both reservedCredits and adjustedActual reflect the multiplier).
		multiplier := row.UserTypeMultiplier
		if multiplier <= 0 {
			// Zero snapshot means the row predates this feature (written before
			// user_type_multiplier column existed); treat as no-discount.
			multiplier = 1.0
		}
		adjustedActual := int64(math.Round(float64(actualCostCents) * multiplier))
		if adjustedActual < 0 {
			adjustedActual = 0
		}

		reservedCredits = row.ReservedCredits
		delta = adjustedActual - row.ReservedCredits
		actualCostCents = adjustedActual // write back so span + DB record carry the adjusted value
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
			// Top-up: FIFO debit from whatever pools are active NOW.
			// T6: legacy creditBiz.DeductCreditsTx fallback removed. All credits
			// flow through MembershipService.DeductCreditsTx (writes new
			// credit_cycle / user_booster_balance / trial_grant tables, plus
			// credit_transaction rows with operation="reconcile:<op>").
			if c.membershipSvc == nil {
				return fmt.Errorf("creditsImpl.Reconcile: membershipSvc is nil (T6 wiring bug)")
			}
			_, topupErr := c.membershipSvc.DeductCreditsTx(ctx, tx, uint64(row.UserID), delta,
				model.CreditTxOpPrefixReconcile+row.Operation, time.Now().UTC())
			topupErr = translateMembershipInsufficient(topupErr)
			if err := topupErr; err != nil {
				// Non-insufficient errors (DB, lock, etc.) propagate and abort
				// the reconcile transaction so caller can retry.
				if !errors.Is(err, ErrInsufficientCredits) {
					return err
				}
				// P1-2 (post-2026-05 audit, policy A — no-debt absorb):
				// When top-up exceeds the user's remaining balance, the platform
				// absorbs the cost rather than writing a positive-amount
				// "reconcile_debt:<op>" credit_transaction that future Reserves
				// never subtract (an invisible, never-collected IOU).
				//
				// Old behavior (removed): Amount=+delta credit_transaction with
				//   operation="reconcile_debt:<op>" — misleading because it
				//   inflated ledger sums while no real debt was ever recovered.
				//
				// New behavior:
				//   1. Audit-only credit_transaction (Amount=0,
				//      operation="reserve_underestimate_absorbed") so ops can
				//      monitor underestimation via
				//      `WHERE operation = 'reserve_underestimate_absorbed'`
				//      without polluting ledger sums.
				//   2. hasDebt=true is still surfaced on the Langfuse span
				//      (semantically: "absorbed by platform") so pricing /
				//      coefficient tuning has signal.
				//   3. Reservation finalizes normally (finalize_reason='normal',
				//      retained for ENUM compatibility — see migration
				//      20260507_120000_finalize_reason_enum_extension.sql).
				//      The absorption is identified by the audit
				//      credit_transaction row, not the reservation column.
				hasDebt = true
				absorbRow := &model.CreditTransaction{
					UserID:     row.UserID,
					PackageID:  0, // no specific package — absorbed by platform.
					Amount:     0, // audit-only; preserves ledger sum invariant.
					Operation:  "reserve_underestimate_absorbed",
					BizRefType: "reservation",
					BizRefID:   strconv.FormatUint(reservationID, 10),
					CreatedAt:  time.Now(),
				}
				if derr := c.store.Credits().CreateTransaction(ctx, tx, absorbRow); derr != nil {
					// Audit-row failure does not block reconcile (span hasDebt=true
					// is the secondary signal), but log loudly.
					log.Errorw("Reconcile absorb audit row write failed",
						"reservation_id", reservationID,
						"actual_cost_cents", actualCostCents,
						"reserved_credits", row.ReservedCredits,
						"shortfall", delta,
						"user_id", row.UserID,
						"operation", row.Operation,
						"err", derr)
				}
				log.Warnw("Reconcile top-up insufficient — platform absorbed cost",
					"reservation_id", reservationID,
					"user_id", row.UserID,
					"operation", row.Operation,
					"reserved_credits", row.ReservedCredits,
					"actual_cost_cents", actualCostCents,
					"shortfall", delta,
					"err", err)
				// Continue: do NOT propagate err; reservation will finalize as
				// status=reconciled with delta=delta (the underestimate amount).
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

// refundOneItem refunds `amount` credits back to the original pool. Dispatch
// by item.SourceType:
//   - SourceType IS NOT NULL (new path post-T1) → call MembershipService
//     .RefundCreditsTx which routes to credit_cycle / user_booster_balance /
//     trial_grant with D2 fallback chain.
//   - SourceType IS NULL + PackageID IS NULL → inconsistent row, skip safely.
//   - SourceType IS NULL + PackageID IS NOT NULL → legacy reservation from before T1.
//     After T11 (credit_package dropped), these rows cannot be refunded to the old table.
//     They are skipped with a no-op (package rows are in the archive table, read-only).
//
// Spec §2.4 + credits-deduct-cycle-wiring §3.3 + INV-2.
// T11: legacy dispatch path removed (credit_package table dropped).
func (c *creditsImpl) refundOneItem(
	ctx context.Context, tx *gorm.DB, userID uint,
	item model.CreditReservationItem, amount int64,
) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}

	// New-path dispatch: item carries source_type/source_id → route to
	// MembershipService.RefundCreditsTx for cycle/booster/trial refund.
	if item.SourceType != nil && item.SourceID != nil {
		// Fail-loud guard: if MembershipService is not wired, a silent return
		// here would lose the user's credits permanently. Surfacing this as an
		// error allows the outer transaction to roll back, and the reservation
		// stays alive for a later retry once the service is wired (T6 wiring bug).
		if c.membershipSvc == nil {
			return 0, fmt.Errorf("refundOneItem: membershipSvc is nil but SourceType=%v (T6 wiring bug)", *item.SourceType)
		}
		_, _, refundedAmt, err := c.membershipSvc.RefundCreditsTx(
			ctx, tx,
			uint64(userID),
			membership.DeductSource(*item.SourceType),
			*item.SourceID,
			amount,
			time.Now().UTC(),
		)
		if err != nil {
			return 0, fmt.Errorf("refundOneItem new path: %w", err)
		}
		return refundedAmt, nil
	}

	// T11: legacy dispatch (credit_package) has been removed — the table was archived
	// and dropped. Reservations with SourceType=NULL were created before T1 (source_type
	// back-fill, 2026-05-15). All post-T1 reservations have SourceType set. Any NULL-source
	// reservation that reaches here is a pre-T1 legacy row; refund is a safe no-op because
	// the underlying credit_package row no longer exists.
	return 0, nil
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

// GetBalance returns the breakdown (sub + booster + trial) from the three-pool SOT.
//
// T11 (credits-cleanup): legacy fallback path reading credit_package has been removed
// (table was archived and dropped). All credits-mode users go through MembershipService.
// MembershipService is required — if nil, GetBalance returns a wiring error
// rather than silently returning a TrialRemain-less legacy-shaped breakdown
// (audit P2#3, 2026-05).
//
// Spec §1.8 + §2.11.1 + credits-deduct-cycle-wiring §3.1.
func (c *creditsImpl) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
	// Audit P2#3: previously this method silently fell back to
	// store.Credits().GetQuotaBreakdown when membershipSvc was nil, returning a
	// BalanceBreakdown without TrialRemain populated. That fallback existed for
	// test convenience but masked misuse in production (precheck sums would
	// understate spendable balance and incorrectly reject ops). The fallback
	// is now removed; misuse fails fast.
	if c.membershipSvc == nil {
		return nil, fmt.Errorf("creditsImpl.GetBalance: membershipSvc is nil — must be wired via NewCreditService")
	}
	view, err := c.membershipSvc.GetBalance(ctx, uint64(user.ID), time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("creditsImpl.GetBalance via membership: %w", err)
	}
	return &BalanceBreakdown{
		BillingMode:    "credits",
		SubTotal:       view.CycleRemaining, // cycle is the recurring sub pool
		SubRemain:      view.CycleRemaining,
		BoosterTotal:   view.BoosterUsable,
		BoosterRemain:  view.BoosterUsable,
		TrialRemain:    view.TrialRemaining,
		SubExpiresAt:   view.SubExpiresAt,
		TrialExpiresAt: view.TrialExpiresAt,
	}, nil
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

// mapDeductSourceToPkgType maps the new DeductSource enum onto the legacy
// credit_reservation_item.package_type column values, kept for back-compat with
// existing analytic queries that group by package_type.
func mapDeductSourceToPkgType(s membership.DeductSource) string {
	switch s {
	case membership.DeductSourceTrial:
		return model.CreditTypeTrial
	case membership.DeductSourceCycle:
		return model.CreditTypeSubscription
	case membership.DeductSourceBooster:
		return model.CreditTypeBooster
	}
	return ""
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

	// Apply per-user-type multiplier (same logic as Reserve).
	rawMultiplier, utmErr := c.store.Credits().GetUserTypeCreditMultiplier(ctx, user.ID)
	if utmErr != nil {
		log.Warnw("creditsImpl.reserveBudgetRow: GetUserTypeCreditMultiplier failed, falling back to 1.0",
			"user_id", user.ID, "err", utmErr)
		rawMultiplier = 1.0
	}
	adjustedEstimated, snapshotMultiplier := applyUserTypeMultiplier(estimated, rawMultiplier)

	reference := referenceFromOp(op)
	rsvRow := &model.CreditReservation{
		UserID:             user.ID,
		ReferenceType:      reference.refType,
		Operation:          string(op),
		ReservedCredits:    adjustedEstimated,
		CoefficientID:      nil, // context_budget path never uses R2 coefficient
		Status:             string(StatusReserved),
		IdempotencyKey:     idempotencyKey,
		UserTypeMultiplier: snapshotMultiplier,
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

	// T6: legacy creditBiz.DeductCreditsTx fallback removed. All credits-mode
	// deduction goes through MembershipService.DeductCreditsTx.
	if c.membershipSvc == nil {
		return nil, fmt.Errorf("creditsImpl.reserveBudgetRow: membershipSvc is nil (T6 wiring bug)")
	}
	var newPathItems []membership.DeductItem
	log.C(ctx).Infow("reserveBudgetRow: credits-mode new path (DeductCreditsTx)",
		"user_id", user.ID, "amount", adjustedEstimated, "operation", string(op))
	txErr := c.store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Also writes credit_transaction rows (T1 ledger) with operation="budget_reserve:<op>".
		result, err := c.membershipSvc.DeductCreditsTx(ctx, tx, uint64(user.ID), adjustedEstimated, "budget_reserve:"+string(op), time.Now().UTC())
		if err != nil {
			return translateMembershipInsufficient(err)
		}
		newPathItems = result.Items

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

		itemRows := make([]model.CreditReservationItem, 0, len(newPathItems))
		for i, di := range newPathItems {
			sourceType := string(di.SourceType)
			sourceID := di.SourceID
			pkgType := mapDeductSourceToPkgType(di.SourceType)
			itemRows = append(itemRows, model.CreditReservationItem{
				ReservationID:    rsvRow.ID,
				SourceType:       &sourceType,
				SourceID:         &sourceID,
				Credits:          di.Amount,
				PackageType:      pkgType,
				PackageExpiresAt: di.ExpiresAt,
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
		Items:           toReservationItems(newPathItems),
		CreatedAt:       rsvRow.CreatedAt,
	}
	return result, nil
}

// applyUserTypeMultiplier scales estimated credits by the given multiplier and
// floors the result to a minimum of 1. Returns (adjusted, multiplier). The
// multiplier is returned unchanged so callers can snapshot it on the reservation row.
// Guarding multiplier <= 0 here means callers never need to repeat the check.
func applyUserTypeMultiplier(estimated int64, multiplier float64) (adjusted int64, snapshotMultiplier float64) {
	if multiplier <= 0 {
		multiplier = 1.0
	}
	adj := int64(math.Round(float64(estimated) * multiplier))
	if adj <= 0 {
		adj = 1
	}
	return adj, multiplier
}

// toReservationItems maps the FIFO debit []membership.DeductItem into the
// domain []ReservationItem (seq is re-derived here from index).
//
// T6: legacy PackageDeduction path deleted. SourceType/SourceID always carry
// the trial/cycle/booster pool; PackageID is always nil (credit_package gone).
func toReservationItems(items []membership.DeductItem) []ReservationItem {
	out := make([]ReservationItem, 0, len(items))
	for i, d := range items {
		sourceType := string(d.SourceType)
		sourceID := d.SourceID
		out = append(out, ReservationItem{
			SourceType:       &sourceType,
			SourceID:         &sourceID,
			Credits:          d.Amount,
			PackageType:      mapDeductSourceToPkgType(d.SourceType),
			PackageExpiresAt: d.ExpiresAt,
			Seq:              i + 1,
		})
	}
	return out
}
