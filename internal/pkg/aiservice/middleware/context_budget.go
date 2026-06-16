package middleware

import (
	"context"
	"fmt"
	"sync"
	"time"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// ContextBudgetService interface
// ----------------------------------------------------------------------------

// ContextBudgetService is the biz-layer service that orchestrates context budget
// operations. Task 7 (biz/contextbudget) provides the real implementation;
// this middleware only holds the interface definition and calls through it.
type ContextBudgetService interface {
	// Prepare normalises the operation, loads policy/profile, estimates token
	// counts, runs the planner if needed, and persists a context_budget_event
	// row. It returns a PrepareResult the middleware uses to inject messages
	// and to decide whether to reserve credits.
	//
	// When PrepareResult.SkipBudget is true the middleware skips everything
	// except injecting the already-persisted skipped-event id into context.
	//
	// Returns contextbudget.ErrContextTooLarge / ErrCurrentInputTooLarge when
	// the context cannot fit even after all legal compression phases; the
	// middleware must propagate these to the caller without invoking the provider.
	Prepare(ctx context.Context, input PrepareInput) (*PrepareResult, error)

	// Finalize patches the context_budget_event row with actual usage, marks
	// the event status (ok / failed / skipped), and optionally triggers
	// credit reconcile/refund metadata updates.
	Finalize(ctx context.Context, input FinalizeInput) error
}

// ContextBudgetCreditService is the credit-budget facade required by the
// ContextBudgetCredits middleware.
//
// Note on method scope: CheckAndEstimateBudget and ReserveBudget are called
// directly by the middleware before invoking the provider. FinalizeReservation
// and Refund are NOT called by the middleware itself — they are required for
// the biz/contextbudget package's Finalize implementation (Task 7), which
// reconciles or refunds the reservation based on actual usage. They are
// declared here so a single composable facade satisfies both consumers.
type ContextBudgetCreditService interface {
	// LoadUser resolves a *model.User by its primary key. Required because
	// the middleware only carries userID through ctx; downstream credit
	// methods need the full struct.
	LoadUser(ctx context.Context, userID uint) (*model.User, error)

	// CheckAndEstimateBudget runs the pre-call balance check using token-based
	// estimates.
	CheckAndEstimateBudget(ctx context.Context, user *model.User, input credit.BudgetPrecheckInput) (*credit.PreCheckResult, error)

	// ReserveBudget creates a credit_reservation with estimation_source='context_budget'.
	// Legacy-tier users (SkipDeduction=true per precheck) are a no-op: returns (nil, nil).
	ReserveBudget(ctx context.Context, user *model.User, input credit.BudgetReservationInput) (*credit.Reservation, error)

	// FinalizeReservation reconciles an existing reservation with the actual cost
	// (in credits). Idempotent — calling on a terminal reservation is a no-op.
	FinalizeReservation(ctx context.Context, reservationID uint64, actualCredits int64, reason string) error

	// Refund fully refunds an existing reservation. Idempotent.
	Refund(ctx context.Context, reservationID uint64, reason string) error

	// EnforceModelMembership returns errno.ErrModelMembershipOnly when
	// (provider, model) is a 0-priced model AND userID is NOT a member; nil
	// otherwise. The middleware calls this for every chat request — independent
	// of ContextFragments and of Policy.ChargeUser — so a non-member can never
	// reach a member-only free model (free-model-member-only C7/AC3).
	EnforceModelMembership(ctx context.Context, userID uint, provider, model string) error
}

// ----------------------------------------------------------------------------
// PrepareInput / PrepareResult / FinalizeInput types
// ----------------------------------------------------------------------------

// PrepareInput is the argument passed to ContextBudgetService.Prepare.
type PrepareInput struct {
	// Operation is the raw operation string extracted from billing context.
	// The service layer is responsible for normalising it.
	Operation string
	// UserID is 0 for internal/admin calls with no user context.
	UserID uint
	// User is the resolved user object. May be nil; the service reloads it
	// from DB before Reserve when ChargeUser=true.
	User *model.User
	// Route is the resolved route for the current call.
	Route *registry.ResolvedRoute
	// Fragments are the ordered context fragments supplied by the business producer.
	Fragments []contextbudget.ContextFragment
	// ContextWindow is read from route.Capability.ContextWindow.
	ContextWindow int
	// MaxOutputTokens is read from route.Capability.MaxOutputTokens.
	MaxOutputTokens int
	// TaskID is an optional trace / span identifier passed to Langfuse.
	TaskID string
	// Metadata carries opaque business key-value pairs for tracing.
	Metadata map[string]string
}

// PrepareResult is returned by ContextBudgetService.Prepare.
type PrepareResult struct {
	// Fragments are the (possibly compressed) fragments after the planner ran.
	Fragments []contextbudget.ContextFragment
	// Messages are the fully rendered ChatMessage entries for this request.
	// ContextBudgetCredits replaces ChatRequest.Messages with this slice.
	Messages []aiservice.ChatMessage
	// Plan is the planner output (Actions, EstimatedAfter, Feasible).
	Plan contextbudget.Plan
	// EstimatedBefore is the token count before compression planning.
	EstimatedBefore int
	// EstimatedAfter is the token count after the accepted plan.
	EstimatedAfter int
	// SafeInputBudget is the safe token budget threshold used.
	SafeInputBudget int
	// Policy is the BudgetPolicy selected for this operation.
	Policy contextbudget.BudgetPolicy
	// TokenProfileID is the database ID of the token profile that was used.
	// 0 means a default/fallback profile was applied.
	TokenProfileID uint64
	// EventID is the ID of the context_budget_event row persisted by Prepare.
	// The middleware passes this to Finalize so the event can be updated.
	EventID uint64
	// NormalizedOp is the canonical operation string after normalisation.
	NormalizedOp string
	// SkipBudget is true for non-migrated callers that send empty Fragments
	// for operations that do not yet participate in context budget.
	// When true the middleware is a passthrough for the provider call.
	SkipBudget bool
	// PolicyID is the database ID of the context_budget_policy row used.
	// 0 means a default/synthetic policy was applied (no DB row).
	// Resolves Task 6 P2-D: budget_policy_id in usage_record metadata.
	PolicyID uint64
}

// FinalizeInput is the argument passed to ContextBudgetService.Finalize.
type FinalizeInput struct {
	// EventID is the context_budget_event to update.
	EventID uint64
	// ReservationID is the credit_reservation created by Reserve (0 = none).
	ReservationID uint64
	// ActualPromptTokens from provider usage (0 when CalibrationSkipped=true).
	ActualPromptTokens int
	// ActualCompletionTokens from provider usage (0 when CalibrationSkipped=true).
	ActualCompletionTokens int
	// EstimatedCredits is the pre-call credit estimate (used when actual is unavailable).
	EstimatedCredits int64
	// PricingCostCents is the cost in cents derived from actual token pricing.
	PricingCostCents int64
	// UsageRecordID is the id of the usage_record written by the Billing middleware.
	UsageRecordID uint64
	// CompressionActions lists the action types applied by the planner.
	CompressionActions []string
	// Status is one of "ok", "compressed", "failed", "skipped".
	Status string
	// ErrorCode is a machine-readable code for error events.
	ErrorCode string
	// CalibrationSkipped is true when the final usage was unavailable and the
	// event was finalised with the estimated cost instead.
	CalibrationSkipped bool
	// Refund, when true, instructs the service to refund the reservation rather
	// than reconcile it. Used for provider errors and context cancellation.
	Refund bool
}

// ----------------------------------------------------------------------------
// Context keys for budget metadata propagation to Billing middleware
// ----------------------------------------------------------------------------

// ctxKeyBudgetMetadata is the context key used to pass budget metadata from
// ContextBudgetCredits to the inner Billing middleware so it can be merged into
// usage_record.metadata.
type ctxKeyBudgetMetadata struct{}

// ----------------------------------------------------------------------------
// finalCostHolder — shared mutable cost handoff (Option B)
// ----------------------------------------------------------------------------

// finalCostHolder is a pointer-sized mutable struct injected into ctx by
// ContextBudgetCredits (after Reserve). The inner Billing middleware writes
// CostCents after computing the actual cost from the pricing rule so that
// ContextBudgetCredits's finalizeReservationIfNeeded can pass the real value
// to FinalizeReservation instead of the EstimatedCredits placeholder.
//
// Context is immutable-by-copy; sharing a *finalCostHolder pointer through
// context.WithValue is the standard Go pattern for mutable cross-middleware
// state without breaking context semantics.
//
// Thread-safety: Set is called from the inner Billing middleware goroutine;
// Get is called from the ContextBudgetCredits finalize path. The mutex makes
// concurrent access safe (mirrors budgetMetadataHolder from F-5 fix b498a99).
type finalCostHolder struct {
	mu        sync.Mutex
	costCents int64
	set       bool
}

// Set stores costCents and marks the holder as set. Subsequent calls overwrite.
func (h *finalCostHolder) Set(c int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.costCents = c
	h.set = true
}

// Get returns the stored cost and true if Set was called at least once.
// Returns 0 and false when the holder is empty (Set was never called).
func (h *finalCostHolder) Get() (int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.costCents, h.set
}

// ctxKeyFinalCostHolder is the context key for *finalCostHolder.
type ctxKeyFinalCostHolder struct{}

// withFinalCostHolder injects a *finalCostHolder pointer into ctx.
func withFinalCostHolder(ctx context.Context, h *finalCostHolder) context.Context {
	return context.WithValue(ctx, ctxKeyFinalCostHolder{}, h)
}

// finalCostHolderFromCtx extracts the *finalCostHolder from ctx.
// Returns nil if the holder was not injected (e.g. legacy paths without budget).
func finalCostHolderFromCtx(ctx context.Context) *finalCostHolder {
	h, _ := ctx.Value(ctxKeyFinalCostHolder{}).(*finalCostHolder)
	return h
}

// ----------------------------------------------------------------------------
// budgetMetadataHolder — shared mutable budget handoff (mirrors finalCostHolder)
// ----------------------------------------------------------------------------

// budgetMetadataHolder is a pointer-sized mutable struct injected into ctx by
// the outer Tracing middleware BEFORE calling next. ContextBudgetCredits writes
// the fully-populated budgetMetadata into the holder immediately after calling
// withBudgetMetadata(ctx, ...) so that Tracing's close path (which holds the
// *original* ctx) can read the budget fields without needing the child ctx.
//
// This solves F-5: ContextBudgetCredits returns a new ctx via context.WithValue
// which is only visible to inner middlewares (Billing, Retry, Adapter). The outer
// Tracing middleware closes the Langfuse generation with the original ctx where
// budgetMetadataFromCtx returns ok=false. The holder bridges this gap.
//
// Thread-safety: Set is called from the ContextBudgetCredits goroutine; Get is
// called from the Tracing close goroutine. The mutex makes the operation safe
// even though in practice Set always happens-before Get (the channel close in
// the streaming path is the synchronisation point — see memory ordering note
// in wrapStreamForBilling).
type budgetMetadataHolder struct {
	mu   sync.Mutex
	set  bool
	meta budgetMetadata
}

// Set stores meta and marks the holder as set. Subsequent calls overwrite.
func (h *budgetMetadataHolder) Set(m budgetMetadata) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.meta = m
	h.set = true
}

// Get returns the stored metadata and true if Set was called at least once.
// Returns zero value and false when the holder is empty.
func (h *budgetMetadataHolder) Get() (budgetMetadata, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.meta, h.set
}

// ctxKeyBudgetMetadataHolder is the context key for *budgetMetadataHolder.
// Using a private struct type avoids any collision with other packages.
type ctxKeyBudgetMetadataHolder struct{}

// withBudgetMetadataHolder injects a fresh *budgetMetadataHolder into ctx and
// returns the new ctx together with a pointer to the holder so the caller can
// read it later.
func withBudgetMetadataHolder(ctx context.Context) (context.Context, *budgetMetadataHolder) {
	h := &budgetMetadataHolder{}
	return context.WithValue(ctx, ctxKeyBudgetMetadataHolder{}, h), h
}

// budgetMetadataHolderFromCtx extracts the *budgetMetadataHolder from ctx.
// Returns (nil, false) when the holder was not injected.
func budgetMetadataHolderFromCtx(ctx context.Context) (*budgetMetadataHolder, bool) {
	h, ok := ctx.Value(ctxKeyBudgetMetadataHolder{}).(*budgetMetadataHolder)
	return h, ok && h != nil
}

// budgetMetadata is the structured metadata attached to the context.
type budgetMetadata struct {
	EventID               uint64 `json:"event_id,omitempty"`
	TokenProfileID        uint64 `json:"token_profile_id,omitempty"`
	SafeInputBudget       int    `json:"safe_input_budget,omitempty"`
	EstimatedPromptBefore int    `json:"estimated_prompt_tokens_before,omitempty"`
	EstimatedPromptAfter  int    `json:"estimated_prompt_tokens_after,omitempty"`
	// EstimatedCompletionTokens is the per-(provider, model) historical-average
	// completion-token estimate produced by Deps.CompletionEstimator (see
	// effectiveCompletionTokens). Falls back to policy.ReservedOutputTokens
	// when no historical data exists. Spec §11.2 contracts on this field name;
	// `reserved_output_tokens` (below) still carries the policy worst-case
	// bound for trace/observability comparison.
	EstimatedCompletionTokens int    `json:"estimated_completion_tokens,omitempty"`
	CompressionStatus         string `json:"compression_status,omitempty"`
	// CompressionActions holds the list of non-keep action types applied by the planner.
	// e.g. ["summarize", "reference", "drop"]
	CompressionActions []string `json:"compression_actions,omitempty"`
	// ReservedOutputTokens is the output-token budget from the policy (spec §11.2).
	ReservedOutputTokens int `json:"reserved_output_tokens,omitempty"`
	// ReservationID is the credit_reservation.id created by ReserveBudget (0 = none).
	// Non-zero only when ChargeUser=true and a reservation was successfully created.
	ReservationID uint64 `json:"reservation_id,omitempty"`
	// PolicyID is the context_budget_policy.id used for this request.
	// Resolves Task 6 P2-D: budget_policy_id in usage_record metadata.
	PolicyID uint64 `json:"budget_policy_id,omitempty"`

	// --- Langfuse tracing metadata fields (spec §11.1) ---
	// These are populated by ContextBudgetCredits for the Tracing middleware.

	// ContextWindow is the model context window size (from route.Capability.ContextWindow).
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is the model maximum output tokens (from route.Capability.MaxOutputTokens).
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// SafeRatio is the safe ratio used in budget calculation (policy.SafeRatio).
	SafeRatio float64 `json:"safe_ratio,omitempty"`
	// FixedOverheadTokens is the fixed overhead added to every request.
	FixedOverheadTokens int `json:"fixed_overhead_tokens,omitempty"`
	// DroppedFragmentCount is the number of fragments with ActionDrop in the plan.
	DroppedFragmentCount int `json:"dropped_fragment_count,omitempty"`
	// SummarizedFragmentCount is the number of fragments with ActionSummarize in the plan.
	SummarizedFragmentCount int `json:"summarized_fragment_count,omitempty"`
	// CriticalFragmentCount is the number of fragments where isCritical==true.
	// Populated by ContextBudgetCredits from the PrepareResult.
	CriticalFragmentCount int `json:"critical_fragment_count,omitempty"`
	// TokenProfileFallback is true when a fallback/default token profile was used.
	TokenProfileFallback bool `json:"token_profile_fallback,omitempty"`
	// CalibrationSkipped is true when the final actual token usage was unavailable.
	CalibrationSkipped bool `json:"calibration_skipped,omitempty"`
}

// withBudgetMetadata injects budget metadata into ctx for the Billing middleware.
func withBudgetMetadata(ctx context.Context, meta budgetMetadata) context.Context {
	return context.WithValue(ctx, ctxKeyBudgetMetadata{}, meta)
}

// budgetMetadataFromCtx extracts budget metadata from ctx; returns zero value if absent.
func budgetMetadataFromCtx(ctx context.Context) (budgetMetadata, bool) {
	meta, ok := ctx.Value(ctxKeyBudgetMetadata{}).(budgetMetadata)
	return meta, ok
}

// ----------------------------------------------------------------------------
// ContextBudgetCredits middleware
// ----------------------------------------------------------------------------

// ContextBudgetCredits returns a Middleware that implements spec §5.1 context
// budget gateway flow:
//
//  1. If ContextFragments is empty: passthrough for non-migrated callers.
//  2. Call service.Prepare — normalise op, load policy/profile, estimate,
//     plan compression, persist context_budget_event.
//  3. If Prepare returns ErrContextTooLarge/ErrCurrentInputTooLarge: return
//     typed error without invoking the provider.
//  4. Replace ChatRequest.Messages with PrepareResult.Messages.
//  5. If Policy.ChargeUser=true: run credit precheck + ReserveBudget.
//  6. Inject budget metadata into ctx for the inner Billing middleware.
//  7. Call next (provider via Billing→Retry→Adapter).
//  8. Non-streaming: Finalize immediately (steps 8-9 below).
//  9. Streaming: wrap the channel; finalize on final usage / error / close /
//     context cancellation (sync.Once guarantees idempotency).
//
// Steps 10-16 of spec §5.1 (record/persist context_budget_event, attach event
// id to usage record metadata, observe streaming termination, refund/reconcile
// reservation) are delegated to ContextBudgetService.Finalize (Task 7's
// biz/contextbudget implementation). The middleware only triggers Finalize at
// the right moment with FinalizeInput; the biz layer owns the data persistence.
//
// When Deps.ContextBudget is nil the middleware is a transparent passthrough
// (useful for unit tests that wire only a subset of Deps).
func ContextBudgetCredits(deps Deps) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error) {
			// Passthrough when ContextBudget service is not wired.
			if deps.ContextBudget == nil {
				return next(ctx, route, req)
			}

			// Only operate on ChatRequest; passthrough for OCR/ASR/Embed/Rerank.
			chatReq, ok := asChatReq(req)
			if !ok {
				return next(ctx, route, req)
			}

			// Free-model member gate (free-model-member-only C7/AC3): a 0-priced model
			// is member-only. Enforce for EVERY chat request, independent of
			// ContextFragments (covers no-KB chats) and of Policy.ChargeUser, so a
			// non-member can never reach a free model on any chat path.
			if deps.CreditService != nil && route != nil {
				gateUserID := uint(0)
				if bc := billing.FromContext(ctx); bc != nil {
					gateUserID = bc.UserID
				}
				if gateUserID == 0 {
					gateUserID, _ = ctx.Value(ctxKeyUserID{}).(uint)
				}
				if gateUserID != 0 {
					if err := deps.CreditService.EnforceModelMembership(ctx, gateUserID, route.Provider.Name, route.ServiceKey); err != nil {
						return nil, fmt.Errorf("ContextBudgetCredits: %w", err)
					}
				}
			}

			// bill-only mode (agent-mode-billing): the agent manages its own
			// context; its ReAct messages carry tool_calls/tool_call_id/
			// reasoning_content that the fragment renderer would drop. Bill from a
			// direct estimate WITHOUT compressing or replacing Messages.
			billOnly := GatewayBillingOnlyFromCtx(ctx)

			// ----------------------------------------------------------------
			// Step 1: if no fragments AND not bill-only, passthrough.
			// ----------------------------------------------------------------
			if !billOnly && len(chatReq.ContextFragments) == 0 {
				return next(ctx, route, req)
			}

			// ----------------------------------------------------------------
			// Step 2: extract billing context.
			// ----------------------------------------------------------------
			bc := billing.FromContext(ctx)
			operation := ""
			userID := uint(0)
			if bc != nil {
				operation = bc.Operation
				userID = bc.UserID
			}
			if userID == 0 {
				userID, _ = ctx.Value(ctxKeyUserID{}).(uint)
			}

			// ----------------------------------------------------------------
			// Step 3: obtain a PrepareResult.
			//   - bill-only: synthesize (no compression; Messages=nil so Step 4
			//     keeps the agent's original tool-structured messages).
			//   - default:   deps.ContextBudget.Prepare (compress + render).
			// ----------------------------------------------------------------
			var result *PrepareResult
			var err error
			if billOnly {
				// Anomaly guard: agent bill-only callers never set fragments. If
				// some caller sets both, fragments are silently ignored below —
				// surface it rather than dropping context unobserved.
				if len(chatReq.ContextFragments) > 0 {
					deps.warnw("ContextBudgetCredits: bill-only mode with non-empty ContextFragments; fragments ignored",
						"operation", operation, "user_id", userID)
				}
				result = synthBillOnlyResult(operation, route, chatReq.Messages)
			} else {
				prepIn := PrepareInput{
					Operation:       operation,
					UserID:          userID,
					Route:           route,
					Fragments:       chatReq.ContextFragments,
					ContextWindow:   route.Capability.ContextWindow,
					MaxOutputTokens: route.Capability.MaxOutputTokens,
					TaskID:          route.TaskID,
				}
				if bc != nil {
					prepIn.Metadata = bc.Meta
				}
				result, err = deps.ContextBudget.Prepare(ctx, prepIn)
				if err != nil {
					// Typed budget errors propagate before provider call.
					return nil, fmt.Errorf("ContextBudgetCredits: %w", err)
				}
				// SkipBudget means this is a migrated operation with no fragments
				// (already handled in Step 1, but defence-in-depth).
				if result.SkipBudget {
					return next(ctx, route, req)
				}
			}

			// ----------------------------------------------------------------
			// Step 4: inject rendered Messages into ChatRequest.
			// ----------------------------------------------------------------
			if len(result.Messages) > 0 {
				chatReq.Messages = result.Messages
			}
			// Clear fragments from the request — provider does not understand them.
			chatReq.ContextFragments = nil
			req = chatReq

			// ----------------------------------------------------------------
			// Step 5 (optional): credit precheck + Reserve.
			// ----------------------------------------------------------------
			// Compute the per-(provider, model) historical completion-token
			// estimate ONCE so both the credit precheck (doReserveBudget) and
			// the budgetMetadata trace field below see the same value. Falls
			// back to ReservedOutputTokens when the estimator returns no data.
			completionTokens := effectiveCompletionTokens(ctx, deps, route, result.Policy.ReservedOutputTokens)
			var reservationID uint64
			var reservedCredits int64
			if result.Policy.ChargeUser && deps.CreditService != nil && userID != 0 {
				reservationID, reservedCredits, err = doReserveBudget(ctx, deps, result, userID, route, completionTokens)
				if err != nil {
					// Insufficient credits or reserve error — don't call provider.
					return nil, fmt.Errorf("ContextBudgetCredits: %w", err)
				}
			}

			// ----------------------------------------------------------------
			// Step 5b: inject finalCostHolder into ctx so the inner Billing
			// middleware can publish the real cost_cents back to us.
			// Only inject when a reservation exists — no reservation means no
			// reconcile, and the holder would be unused overhead.
			// ----------------------------------------------------------------
			var holder *finalCostHolder
			if reservationID > 0 {
				holder = &finalCostHolder{}
				ctx = withFinalCostHolder(ctx, holder)
			}

			// ----------------------------------------------------------------
			// Step 6: inject budget metadata into ctx for Billing middleware.
			// ----------------------------------------------------------------
			// compression_status is "compressed" when the planner produced at
			// least one non-keep action (summarize/reference/drop/reuse_summary).
			// Plan.Feasible is always true at this point — Prepare returns
			// ErrContextTooLarge when infeasible, so checking !Feasible here
			// would be dead code that never fires.
			compressionStatus := "ok"
			// Build the deduped set of non-keep action type strings and count
			// dropped / summarized / critical fragments for Langfuse metadata.
			actionTypeSet := make(map[string]struct{})
			droppedCount := 0
			summarizedCount := 0
			for _, action := range result.Plan.Actions {
				if action.Type != contextbudget.ActionKeep {
					compressionStatus = "compressed"
					actionTypeSet[string(action.Type)] = struct{}{}
				}
				if action.Type == contextbudget.ActionDrop {
					droppedCount++
				}
				if action.Type == contextbudget.ActionSummarize {
					summarizedCount++
				}
			}
			compressionActions := make([]string, 0, len(actionTypeSet))
			for at := range actionTypeSet {
				compressionActions = append(compressionActions, at)
			}
			// Count critical fragments (isCritical is package-level, not exported; we
			// approximate via Role==RoleImmutable or Critical==true as a lightweight
			// proxy that mirrors the isCritical logic visible from this package).
			criticalCount := 0
			for _, f := range result.Fragments {
				if f.Critical || f.Role == contextbudget.RoleImmutable {
					criticalCount++
				}
			}
			theBudgetMetadata := budgetMetadata{
				EventID:                   result.EventID,
				TokenProfileID:            result.TokenProfileID,
				SafeInputBudget:           result.SafeInputBudget,
				EstimatedPromptBefore:     result.EstimatedBefore,
				EstimatedPromptAfter:      result.EstimatedAfter,
				EstimatedCompletionTokens: completionTokens,
				CompressionStatus:         compressionStatus,
				CompressionActions:        compressionActions,
				ReservedOutputTokens:      result.Policy.ReservedOutputTokens,
				ReservationID:             reservationID,
				PolicyID:                  result.PolicyID,
				ContextWindow:             route.Capability.ContextWindow,
				MaxOutputTokens:           route.Capability.MaxOutputTokens,
				SafeRatio:                 result.Policy.SafeRatio,
				FixedOverheadTokens:       result.Policy.FixedOverheadTokens,
				DroppedFragmentCount:      droppedCount,
				SummarizedFragmentCount:   summarizedCount,
				CriticalFragmentCount:     criticalCount,
				// TokenProfileFallback: true when TokenProfileID == 0 (default profile used)
				TokenProfileFallback: result.TokenProfileID == 0,
			}
			ctx = withBudgetMetadata(ctx, theBudgetMetadata)

			// F-5 fix: also write into the *budgetMetadataHolder injected by the
			// outer Tracing middleware (into the original ctx). This bridges the
			// ctx-immutability gap: context.WithValue returns a new ctx that is
			// only visible to inner middlewares; the outer Tracing close path holds
			// the pre-mutation ctx and therefore budgetMetadataFromCtx(ctx)==false.
			// The holder pointer in the original ctx is shared by reference, so
			// writing here makes the metadata visible to Tracing without ctx mutation.
			// Set is called BEFORE next(ctx, ...) so both streaming and non-streaming
			// Tracing close paths are guaranteed to see the populated holder.
			if h, ok := budgetMetadataHolderFromCtx(ctx); ok {
				h.Set(theBudgetMetadata)
			}

			// Build the base FinalizeInput that will be used by the finalizer.
			baseFI := buildBaseFinalizeInput(result, reservationID, reservedCredits)

			// ----------------------------------------------------------------
			// Step 7: call next (Billing → Retry → Adapter).
			// ----------------------------------------------------------------
			resp, callErr := next(ctx, route, req)

			// ----------------------------------------------------------------
			// Step 8/9: finalize — non-streaming vs streaming.
			// ----------------------------------------------------------------
			if ch, isStream := resp.(<-chan aiservice.ChatChunk); isStream {
				// Streaming: wrap channel; finalize after terminal event.
				wrapped := wrapStreamForContextBudget(ctx, ch, deps, result, baseFI, callErr)
				return wrapped, callErr
			}

			// Non-streaming: finalize immediately.
			fi := baseFI
			if callErr != nil {
				fi.Status = "failed"
				fi.ErrorCode = "provider_err"
				fi.Refund = true
			} else {
				fi.Status = "ok"
				if chatResp, ok := asChatResponse(resp); ok && chatResp != nil {
					fi.ActualPromptTokens = chatResp.Usage.PromptTokens
					fi.ActualCompletionTokens = chatResp.Usage.CompletionTokens
				} else {
					fi.CalibrationSkipped = true
				}
			}
			if finalErr := deps.ContextBudget.Finalize(ctx, fi); finalErr != nil {
				deps.warnw("ContextBudgetCredits: Finalize error (non-streaming)",
					"event_id", result.EventID,
					"error", finalErr,
				)
			}
			finalizeReservationIfNeeded(ctx, deps, fi)

			return resp, callErr
		}
	}
}

// finalizeReservationIfNeeded calls FinalizeReservation (or Refund on error)
// against the credit service if a reservation was created during Reserve.
// No-op when ReservationID == 0 (legacy-tier user / ChargeUser=false / no
// user context).
//
// Actual cost resolution (spec §6.4):
//   - Reads the *finalCostHolder injected by ContextBudgetCredits step 5b.
//   - If Billing middleware called holder.Set(c) (from pricing rule + actual
//     token counts), that value — including 0 — is used as actualCredits for
//     reconcile. A 0/0 pricing rule legitimately produces cost=0 and must NOT
//     fall back to EstimatedCredits (F-7 fix).
//   - Falls back to fi.EstimatedCredits only when the holder is absent or Set
//     was never called (e.g. legacy non-streaming paths, pricing-rule miss, or
//     error paths where Billing never computed a cost).
//
// Failures are logged warn — finalize must never propagate errors to the caller.
func finalizeReservationIfNeeded(ctx context.Context, deps Deps, fi FinalizeInput) {
	if fi.ReservationID == 0 || deps.CreditService == nil {
		return
	}
	// Settle (Refund/Reconcile) on a non-cancelled ctx: this path runs precisely
	// when the request ctx may already be cancelled (user/admin cancel, client
	// disconnect). Using the cancelled ctx would make the settle DB write fail
	// with context.Canceled, leaving the reservation stuck in 'reserved' until
	// the 1h sweeper. context.WithoutCancel keeps values (trace, etc.) but drops
	// cancellation/deadline so the refund/reconcile persists PROMPTLY. A short
	// timeout guards against a hung DB. (agent-mode-billing T11.)
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	ctx = settleCtx
	if fi.Refund {
		reason := fi.ErrorCode
		if reason == "" {
			reason = "context_budget_refund"
		}
		if err := deps.CreditService.Refund(ctx, fi.ReservationID, reason); err != nil {
			deps.warnw("ContextBudgetCredits: Refund error",
				"reservation_id", fi.ReservationID,
				"reason", reason,
				"error", err,
			)
		}
		return
	}

	// Resolve the actual cost from the finalCostHolder set by the Billing
	// middleware (real pricing-rule cost, including a legitimate 0 from a 0/0
	// rule). F-7: Get()'s ok flag — not CostCents > 0 — distinguishes "Billing
	// computed 0" (reconcile as 0) from "holder never Set" (pricing missed).
	var actualCredits int64
	costResolved := false
	if holder := finalCostHolderFromCtx(ctx); holder != nil {
		if c, ok := holder.Get(); ok {
			actualCredits = c
			costResolved = true
		}
	}

	// Fix ③: holder unset → DO NOT charge a fabricated fallback (the old
	// EstimatedCredits=ReservedOutputTokens path is what billed 64000 credits).
	// We cannot price the call, so refund the reservation. Distinguish the cause
	// for ops (P1-2): no token usage (stream truncated / calibration skipped) is
	// benign; usage present but unpriced is a real pricing_rule config gap that
	// must be loud so the missing rule gets added (fix ① makes this rare).
	if !costResolved {
		hasUsage := fi.ActualPromptTokens > 0 || fi.ActualCompletionTokens > 0
		if fi.CalibrationSkipped || !hasUsage {
			deps.warnw("ContextBudgetCredits: reconcile without usage data — refunding (benign)",
				"reservation_id", fi.ReservationID,
				"event_id", fi.EventID,
			)
		} else {
			deps.errorw("ContextBudgetCredits: pricing unavailable despite token usage — REFUNDING, not charging fallback (pricing_rule config gap)",
				"reservation_id", fi.ReservationID,
				"event_id", fi.EventID,
				"prompt_tokens", fi.ActualPromptTokens,
				"completion_tokens", fi.ActualCompletionTokens,
			)
		}
		if err := deps.CreditService.Refund(ctx, fi.ReservationID, "pricing_unavailable"); err != nil {
			deps.warnw("ContextBudgetCredits: Refund error (pricing unavailable)",
				"reservation_id", fi.ReservationID,
				"error", err,
			)
		}
		return
	}

	if err := deps.CreditService.FinalizeReservation(ctx, fi.ReservationID, actualCredits, "context_budget_reconcile"); err != nil {
		deps.warnw("ContextBudgetCredits: FinalizeReservation error",
			"reservation_id", fi.ReservationID,
			"actual_credits", actualCredits,
			"error", err,
		)
	}
}

// effectiveCompletionTokens returns the per-(provider, model) historical
// completion-token estimate from deps.CompletionEstimator, falling back to
// policy.ReservedOutputTokens when:
//   - no estimator is wired (nil)
//   - route metadata is missing
//   - the estimator has insufficient historical samples
//   - the historical estimate exceeds the policy worst-case bound
//
// Centralising the fallback contract here keeps the budgetMetadata trace
// field (spec §11.2) and the credit precheck input (doReserveBudget) using
// the same value, so observability matches what was actually reserved.
func effectiveCompletionTokens(ctx context.Context, deps Deps, route *registry.ResolvedRoute, reservedOutputTokens int) int {
	if deps.CompletionEstimator == nil || route == nil || route.Provider.Name == "" || route.ServiceKey == "" {
		return reservedOutputTokens
	}
	tokens, hasData := deps.CompletionEstimator.Estimate(ctx, route.Provider.Name, route.ServiceKey)
	if !hasData || tokens <= 0 {
		return reservedOutputTokens
	}
	// Never exceed the policy worst-case: if historical mean × safety > max,
	// the model is hitting its output cap and ReservedOutputTokens is the
	// correct ceiling anyway.
	if tokens > reservedOutputTokens {
		return reservedOutputTokens
	}
	return tokens
}

// defaultBillOnlyReservedOutputTokens caps the bill-only cold-start output-token
// reservation (fix ②). The pre-fix default of MaxOutputTokens/2 means a 128K
// model reserves 64000 tokens — which, priced at a model like claude, is ~796
// credits held for a request whose real output is a few hundred tokens. That
// gross over-reservation is also what the reconcile fallback bug amplified into
// the 64000-credit overcharge. This ceiling covers the vast majority of chat /
// agent completions; the per-(provider,model) historical estimator
// (effectiveCompletionTokens) refines the reserve downward toward the real
// average once data exists, and Reconcile settles against actual usage — i.e.
// the reserve is an estimate (a temporary hold), never the final charge.
//
// Deliberately a flat constant, not config: it gates billing, so a hot-editable
// value carries business risk. If a future operation needs a different cold-start
// ceiling (e.g. long-output agent_run vs short chatbot_chat), extend this into a
// map[operation]int here rather than wiring it through config.
const defaultBillOnlyReservedOutputTokens = 8192

// synthBillOnlyResult builds a PrepareResult for bill-only mode: no compression,
// no message replacement (Messages=nil → Step 4 keeps the caller's original
// messages), ChargeUser=true, and a direct token estimate. The estimate is
// approximate; Reconcile corrects it against the provider's actual token usage.
func synthBillOnlyResult(operation string, route *registry.ResolvedRoute, messages []aiservice.ChatMessage) *PrepareResult {
	// reserved = min(MaxOutputTokens/2, 8192): keep the historical "half the
	// window" safety for small-window models, but cap large-window models so a
	// cold-start image/agent turn no longer freezes ~796 credits. MaxOutputTokens
	// unknown (0) or negative → fall back to the 8192 ceiling.
	reserved := min(route.Capability.MaxOutputTokens/2, defaultBillOnlyReservedOutputTokens)
	if reserved <= 0 {
		reserved = defaultBillOnlyReservedOutputTokens
	}
	return &PrepareResult{
		NormalizedOp:   operation,
		EstimatedAfter: billOnlyPromptEstimate(messages),
		Policy: contextbudget.BudgetPolicy{
			Operation:            operation,
			ReservedOutputTokens: reserved,
			ChargeUser:           true,
		},
		// Messages nil → Step 4 keeps original tool-structured messages.
		// Plan zero-value → Step 6 metadata renders "ok".
	}
}

// billOnlyPromptEstimate is a rough prompt-token estimate from message text,
// counting plain text, multipart text, tool-call names+arguments (JSON, often
// large in a ReAct loop), and reasoning content. ~2 chars/token (conservative
// for Chinese-heavy content). This is only the pre-charge size; the
// authoritative charge comes from Reconcile using the provider's reported usage.
func billOnlyPromptEstimate(messages []aiservice.ChatMessage) int {
	runes := 0
	for _, m := range messages {
		runes += len([]rune(m.Content.Text))
		for _, p := range m.Content.Parts {
			runes += len([]rune(p.Text))
		}
		for _, tc := range m.ToolCalls {
			runes += len([]rune(tc.Function.Name)) + len([]rune(tc.Function.Arguments))
		}
		runes += len([]rune(m.ReasoningContent))
	}
	est := runes / 2
	if est < 1 {
		est = 1
	}
	return est
}

// doReserveBudget performs the credit precheck and reserves the budget.
// Returns the reservationID (0 if no reservation was created).
//
// completionTokens is the value computed by effectiveCompletionTokens (caller
// passes it in so the same value can be mirrored into budgetMetadata).
// doReserveBudget reserves credits and returns (reservationID, reservedCredits,
// err). reservedCredits is the real pre-call credit estimate that was deducted
// (precheck.EstimatedCredits) — a CREDIT amount, threaded into FinalizeInput so
// the reconcile/event layer records the true reserve (not a token count). It is
// 0 when no reservation was created (SkipDeduction / nil reservation).
func doReserveBudget(
	ctx context.Context,
	deps Deps,
	result *PrepareResult,
	userID uint,
	route *registry.ResolvedRoute,
	completionTokens int,
) (uint64, int64, error) {
	precheckIn := credit.BudgetPrecheckInput{
		UserID:                    userID,
		Operation:                 result.NormalizedOp,
		EstimatedPromptTokens:     result.EstimatedAfter,
		EstimatedCompletionTokens: completionTokens,
		Provider:                  route.Provider.Name,
		Model:                     route.ServiceKey,
		TokenProfileID:            result.TokenProfileID,
		ContextBudgetEventID:      result.EventID,
		// Pool selector (agent-mode-billing): "" = default three-pool;
		// "admin_test" routes to the parent Builder 试聊 pool. Threaded from
		// ctx (set by the agent runner for IsTest runs). Inherited by reserveIn.
		Pool: BillingPoolFromCtx(ctx),
	}

	// Spec §6.1.2 step 1: load a fresh user before Reserve. The middleware
	// only has userID in ctx; the credit facade resolves it via the user store.
	user, err := deps.CreditService.LoadUser(ctx, userID)
	if err != nil {
		return 0, 0, fmt.Errorf("LoadUser: %w", err)
	}

	precheck, err := deps.CreditService.CheckAndEstimateBudget(ctx, user, precheckIn)
	if err != nil {
		return 0, 0, err
	}

	// SkipDeduction: legacy-tier user — no reservation.
	if precheck.SkipDeduction {
		return 0, 0, nil
	}

	reserveIn := credit.BudgetReservationInput{
		BudgetPrecheckInput: precheckIn,
		EstimatedCredits:    precheck.EstimatedCredits,
		ReferenceID:         ReservationRefFromCtx(ctx), // additive: business ref (empty if absent)
	}
	rsv, err := deps.CreditService.ReserveBudget(ctx, user, reserveIn)
	if err != nil {
		return 0, 0, err
	}
	if rsv == nil {
		return 0, 0, nil
	}
	return rsv.ID, precheck.EstimatedCredits, nil
}

// buildBaseFinalizeInput builds the common FinalizeInput skeleton from a
// PrepareResult, reservation ID, and the real reserved credit amount. The caller
// fills in Status/Refund/tokens.
//
// reservedCredits is the actual credits held by Reserve (precheck.EstimatedCredits)
// — a CREDIT value. Fix ③ replaced the previous `int64(ReservedOutputTokens)`,
// which put a TOKEN COUNT into a credits field: on a pricing miss the reconcile
// fallback then charged that count as credits (the 64000 overcharge), and the
// context_budget_event ReconcileDelta (= cost − EstimatedCredits) mixed cents with
// tokens. With the real reserve here, EstimatedCredits is observability-only —
// finalizeReservationIfNeeded refunds (never charges it) when the holder is unset.
func buildBaseFinalizeInput(result *PrepareResult, reservationID uint64, reservedCredits int64) FinalizeInput {
	actions := make([]string, 0, len(result.Plan.Actions))
	for _, a := range result.Plan.Actions {
		if a.Type != contextbudget.ActionKeep {
			actions = append(actions, string(a.Type))
		}
	}
	return FinalizeInput{
		EventID:            result.EventID,
		ReservationID:      reservationID,
		EstimatedCredits:   reservedCredits,
		CompressionActions: actions,
	}
}

// ----------------------------------------------------------------------------
// Streaming wrapper
// ----------------------------------------------------------------------------

// wrapStreamForContextBudget wraps a streaming channel and triggers Finalize
// exactly once on any terminal condition (spec §5.1.2):
//   - IsFinal chunk with Usage (reconcile)
//   - IsFinal chunk with Err (reconcile if usage present, else refund)
//   - Channel close without IsFinal (calibration_skipped)
//   - Context cancellation (refund)
//
// sync.Once guarantees idempotency: double terminal chunks or double close
// never double-trigger Finalize.
func wrapStreamForContextBudget(
	ctx context.Context,
	src <-chan aiservice.ChatChunk,
	deps Deps,
	result *PrepareResult,
	baseFI FinalizeInput,
	_ error, // callErr from next() — not used in stream wrapper (callErr will be nil if stream was returned)
) <-chan aiservice.ChatChunk {
	if src == nil {
		// Nil stream: finalize immediately as error then return closed channel.
		fi := baseFI
		fi.Status = "failed"
		fi.ErrorCode = "nil_stream"
		fi.Refund = true
		if err := deps.ContextBudget.Finalize(ctx, fi); err != nil {
			deps.warnw("ContextBudgetCredits: Finalize error (nil stream)",
				"event_id", result.EventID,
				"error", err,
			)
		}
		finalizeReservationIfNeeded(ctx, deps, fi)
		closed := make(chan aiservice.ChatChunk)
		close(closed)
		return closed
	}

	out := make(chan aiservice.ChatChunk)
	var once sync.Once

	finalize := func(fi FinalizeInput) {
		once.Do(func() {
			if err := deps.ContextBudget.Finalize(ctx, fi); err != nil {
				deps.warnw("ContextBudgetCredits: Finalize error (stream)",
					"event_id", result.EventID,
					"error", err,
				)
			}
			// Reconcile/Refund the credit reservation. Spec §6.4: after provider
			// returns usage, FinalizeReservation flips reservation status from
			// "reserved" to "reconciled" (or refunds on error). Without this,
			// reservations stay stuck in "reserved" forever — caught by S5
			// retest after the LoadUser fix.
			finalizeReservationIfNeeded(ctx, deps, fi)
		})
	}

	go func() {
		defer close(out)
		for {
			// Priority check: if context is already done, refund immediately
			// regardless of whether src is also ready. This ensures cancellation
			// always wins over a coincident channel close (spec §5.1.2).
			select {
			case <-ctx.Done():
				fi := baseFI
				fi.Refund = true
				fi.ErrorCode = "user_cancelled"
				fi.Status = "failed"
				finalize(fi)
				// Drain remaining src so the provider HTTP stream is not leaked.
				for range src {
				}
				return
			default:
			}

			select {
			case <-ctx.Done():
				// Context cancelled before final usage — refund.
				fi := baseFI
				fi.Refund = true
				fi.ErrorCode = "user_cancelled"
				fi.Status = "failed"
				finalize(fi)
				// Drain remaining src so the provider HTTP stream is not leaked.
				for range src {
				}
				return

			case chunk, ok := <-src:
				if !ok {
					// Channel closed without final usage.
					// Re-check ctx cancellation: if both happened simultaneously,
					// honour the cancellation semantics (refund over calibrate).
					if ctx.Err() != nil {
						fi := baseFI
						fi.Refund = true
						fi.ErrorCode = "user_cancelled"
						fi.Status = "failed"
						finalize(fi)
						return
					}
					fi := baseFI
					fi.CalibrationSkipped = true
					fi.Status = "ok"
					finalize(fi)
					return
				}

				// Forward the chunk to downstream consumers.
				select {
				case out <- chunk:
				case <-ctx.Done():
					// Stop sending to out; drain src.
					for range src {
					}
					fi := baseFI
					fi.Refund = true
					fi.ErrorCode = "user_cancelled"
					fi.Status = "failed"
					finalize(fi)
					return
				}

				if chunk.IsFinal {
					if chunk.Err != nil {
						// Error terminal chunk.
						if chunk.Usage != nil {
							// Usage present: reconcile with actual cost.
							fi := baseFI
							fi.ActualPromptTokens = chunk.Usage.PromptTokens
							fi.ActualCompletionTokens = chunk.Usage.CompletionTokens
							fi.Status = "ok"
							fi.ErrorCode = "provider_err"
							finalize(fi)
						} else {
							// No usage: refund.
							fi := baseFI
							fi.Refund = true
							fi.ErrorCode = "provider_err"
							fi.Status = "failed"
							finalize(fi)
						}
					} else if chunk.Usage != nil {
						// Normal end with usage: reconcile.
						fi := baseFI
						fi.ActualPromptTokens = chunk.Usage.PromptTokens
						fi.ActualCompletionTokens = chunk.Usage.CompletionTokens
						fi.Status = "ok"
						finalize(fi)
					} else {
						// IsFinal but no usage: calibration skipped.
						fi := baseFI
						fi.CalibrationSkipped = true
						fi.Status = "ok"
						finalize(fi)
					}
					return
				}
			}
		}
	}()

	return out
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// asChatReq type-asserts req to aiservice.ChatRequest (value or pointer).
func asChatReq(req interface{}) (aiservice.ChatRequest, bool) {
	if req == nil {
		return aiservice.ChatRequest{}, false
	}
	if cr, ok := req.(aiservice.ChatRequest); ok {
		return cr, true
	}
	if cr, ok := req.(*aiservice.ChatRequest); ok && cr != nil {
		return *cr, true
	}
	return aiservice.ChatRequest{}, false
}
