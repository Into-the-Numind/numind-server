package middleware

import (
	"context"
	"fmt"
	"sync"

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
	// LoadUser resolves a *model.User by its primary key.
	// Returns the user (with BillingMode + UserTier + TierExpires populated)
	// so that downstream methods (CheckAndEstimateBudget / ReserveBudget) can
	// route correctly via isEffectiveLegacy(user). Required because the
	// middleware only carries userID through ctx.
	LoadUser(ctx context.Context, userID uint) (*model.User, error)

	// CheckAndEstimateBudget runs the pre-call balance check using token-based
	// estimates. Returns SkipDeduction=true for legacy-tier users.
	CheckAndEstimateBudget(ctx context.Context, user *model.User, input credit.BudgetPrecheckInput) (*credit.PreCheckResult, error)

	// ReserveBudget creates a credit_reservation with estimation_source='context_budget'.
	// Legacy-tier users (SkipDeduction=true per precheck) are a no-op: returns (nil, nil).
	ReserveBudget(ctx context.Context, user *model.User, input credit.BudgetReservationInput) (*credit.Reservation, error)

	// FinalizeReservation reconciles an existing reservation with the actual cost
	// (in credits). Idempotent — calling on a terminal reservation is a no-op.
	FinalizeReservation(ctx context.Context, reservationID uint64, actualCredits int64, reason string) error

	// Refund fully refunds an existing reservation. Idempotent.
	Refund(ctx context.Context, reservationID uint64, reason string) error
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

// budgetMetadata is the structured metadata attached to the context.
type budgetMetadata struct {
	EventID               uint64 `json:"event_id,omitempty"`
	TokenProfileID        uint64 `json:"token_profile_id,omitempty"`
	SafeInputBudget       int    `json:"safe_input_budget,omitempty"`
	EstimatedPromptBefore int    `json:"estimated_prompt_tokens_before,omitempty"`
	EstimatedPromptAfter  int    `json:"estimated_prompt_tokens_after,omitempty"`
	// EstimatedCompletionTokens mirrors ReservedOutputTokens today (both equal to
	// policy.ReservedOutputTokens). They are kept as separate fields because
	// spec §11.2 contracts on `estimated_completion_tokens` while older docs use
	// `reserved_output_tokens`. TODO(S7): decouple when actual completion
	// estimate becomes available from biz layer.
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

			// ----------------------------------------------------------------
			// Step 1: if no fragments, passthrough (non-migrated caller).
			// ----------------------------------------------------------------
			if len(chatReq.ContextFragments) == 0 {
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
			// Step 3: call service.Prepare.
			// ----------------------------------------------------------------
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

			result, err := deps.ContextBudget.Prepare(ctx, prepIn)
			if err != nil {
				// Typed budget errors propagate before provider call.
				return nil, fmt.Errorf("ContextBudgetCredits: %w", err)
			}

			// SkipBudget means this is a migrated operation with no fragments
			// (already handled in Step 1, but defence-in-depth).
			if result.SkipBudget {
				return next(ctx, route, req)
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
			var reservationID uint64
			if result.Policy.ChargeUser && deps.CreditService != nil && userID != 0 {
				reservationID, err = doReserveBudget(ctx, deps, result, userID, route)
				if err != nil {
					// Insufficient credits or reserve error — don't call provider.
					return nil, fmt.Errorf("ContextBudgetCredits: %w", err)
				}
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
			ctx = withBudgetMetadata(ctx, budgetMetadata{
				EventID:                   result.EventID,
				TokenProfileID:            result.TokenProfileID,
				SafeInputBudget:           result.SafeInputBudget,
				EstimatedPromptBefore:     result.EstimatedBefore,
				EstimatedPromptAfter:      result.EstimatedAfter,
				EstimatedCompletionTokens: result.Policy.ReservedOutputTokens,
				CompressionStatus:         compressionStatus,
				CompressionActions:        compressionActions,
				ReservedOutputTokens:      result.Policy.ReservedOutputTokens,
				ReservationID:             reservationID,
				PolicyID:                  result.PolicyID,
				ContextWindow:             prepIn.ContextWindow,
				MaxOutputTokens:           prepIn.MaxOutputTokens,
				SafeRatio:                 result.Policy.SafeRatio,
				FixedOverheadTokens:       result.Policy.FixedOverheadTokens,
				DroppedFragmentCount:      droppedCount,
				SummarizedFragmentCount:   summarizedCount,
				CriticalFragmentCount:     criticalCount,
				// TokenProfileFallback: true when TokenProfileID == 0 (default profile used)
				TokenProfileFallback: result.TokenProfileID == 0,
			})

			// Build the base FinalizeInput that will be used by the finalizer.
			baseFI := buildBaseFinalizeInput(result, reservationID)

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

			return resp, callErr
		}
	}
}

// doReserveBudget performs the credit precheck and reserves the budget.
// Returns the reservationID (0 if no reservation was created).
func doReserveBudget(
	ctx context.Context,
	deps Deps,
	result *PrepareResult,
	userID uint,
	route *registry.ResolvedRoute,
) (uint64, error) {
	precheckIn := credit.BudgetPrecheckInput{
		UserID:                    userID,
		Operation:                 result.NormalizedOp,
		EstimatedPromptTokens:     result.EstimatedAfter,
		EstimatedCompletionTokens: result.Policy.ReservedOutputTokens,
		Provider:                  route.Provider.Name,
		Model:                     route.ServiceKey,
		TokenProfileID:            result.TokenProfileID,
		ContextBudgetEventID:      result.EventID,
	}

	// Spec §6.1.2 step 1: load a fresh user before Reserve so isEffectiveLegacy
	// can dispatch credits vs legacy correctly. The middleware only has userID
	// in ctx; the credit facade resolves it via the user store.
	user, err := deps.CreditService.LoadUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("LoadUser: %w", err)
	}

	precheck, err := deps.CreditService.CheckAndEstimateBudget(ctx, user, precheckIn)
	if err != nil {
		return 0, err
	}

	// SkipDeduction: legacy-tier user — no reservation.
	if precheck.SkipDeduction {
		return 0, nil
	}

	reserveIn := credit.BudgetReservationInput{
		BudgetPrecheckInput: precheckIn,
		EstimatedCredits:    precheck.EstimatedCredits,
	}
	rsv, err := deps.CreditService.ReserveBudget(ctx, user, reserveIn)
	if err != nil {
		return 0, err
	}
	if rsv == nil {
		return 0, nil
	}
	return rsv.ID, nil
}

// buildBaseFinalizeInput builds the common FinalizeInput skeleton from a
// PrepareResult and reservation ID. The caller fills in Status/Refund/tokens.
func buildBaseFinalizeInput(result *PrepareResult, reservationID uint64) FinalizeInput {
	actions := make([]string, 0, len(result.Plan.Actions))
	for _, a := range result.Plan.Actions {
		if a.Type != contextbudget.ActionKeep {
			actions = append(actions, string(a.Type))
		}
	}
	return FinalizeInput{
		EventID:            result.EventID,
		ReservationID:      reservationID,
		EstimatedCredits:   int64(result.Policy.ReservedOutputTokens), // approximation; biz layer recalculates
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
