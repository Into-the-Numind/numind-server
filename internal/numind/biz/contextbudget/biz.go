// Package contextbudget provides the biz-layer implementation of the context
// budget service (spec §5). It orchestrates policy/profile loading, token
// estimation, compression planning, summary cache lookup, event persistence,
// and finalisation (calibration ratio + event patching).
//
// This package does NOT perform credit reservation or reconciliation; those are
// handled by the ContextBudgetCredits middleware using the injected
// ContextBudgetCreditService. Biz.Finalize only patches the event row and
// computes calibration metadata.
package contextbudget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	aimw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Compressor interface
// ---------------------------------------------------------------------------

// Compressor is implemented by any component that can compress a set of
// ContextFragments into a single summary fragment. The real implementation
// (Task 12) will use an LLM call with operation=context_compression.
// Tests inject a stub. When nil, compression is skipped and the planner's
// ActionSummarize actions are treated as best-effort drops.
type Compressor interface {
	Compress(ctx context.Context, fragments []contextbudget.ContextFragment, targetTokens int) (contextbudget.ContextFragment, error)
}

// ---------------------------------------------------------------------------
// Options / Biz
// ---------------------------------------------------------------------------

// Options carries optional collaborators for Biz. All fields may be nil/zero;
// safe defaults are applied in New.
type Options struct {
	// Compressor is the LLM-backed summarisation component.
	// When nil, Summarize actions are skipped (fragments are not compressed).
	Compressor Compressor
	// Clock returns the current time. Defaults to time.Now.
	Clock func() time.Time
	// Logger is a structured logger. When nil package-level log functions are used.
	Logger interface {
		Warnw(msg string, kv ...interface{})
		Errorw(msg string, kv ...interface{})
	}
}

// Biz is the biz-layer context budget service. It implements
// middleware.ContextBudgetService and is intended to be used as a singleton.
type Biz struct {
	store      store.ContextBudgetStore
	compressor Compressor
	clock      func() time.Time
	logger     interface {
		Warnw(msg string, kv ...interface{})
		Errorw(msg string, kv ...interface{})
	}
}

// New constructs a Biz with the given store and options.
//
// The store must be non-nil. Options fields are optional; Clock defaults to
// time.Now if not provided.
func New(s store.ContextBudgetStore, opts Options) *Biz {
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	return &Biz{
		store:      s,
		compressor: opts.Compressor,
		clock:      opts.Clock,
		logger:     opts.logger(),
	}
}

// logger returns the logger from Options, adapting it to the Biz logger type.
// If Logger is nil it falls back to a no-op to avoid nil-pointer panics.
func (o Options) logger() interface {
	Warnw(msg string, kv ...interface{})
	Errorw(msg string, kv ...interface{})
} {
	if o.Logger != nil {
		return o.Logger
	}
	return noopLog{}
}

// noopLog is a zero-allocation logger that discards all messages.
type noopLog struct{}

func (noopLog) Warnw(_ string, _ ...interface{})  {}
func (noopLog) Errorw(_ string, _ ...interface{}) {}

// ---------------------------------------------------------------------------
// Prepare
// ---------------------------------------------------------------------------

// Prepare implements middleware.ContextBudgetService.
//
// Steps (spec §5.1):
//  1. Empty fragments with non-migrated op → skipped event, SkipBudget=true.
//     (The caller already handled the empty-fragments passthrough, but
//     Prepare acts as defence-in-depth.)
//  2. Normalise operation.
//  3. Read route capability.
//  4. Load active policy from store.
//  5. Load active token profile from store (exact then fallback).
//  6. Estimate fragments.
//     7-9. If estimated > safe_input_budget: run planner; for ActionSummarize
//     actions call Compressor if available; re-estimate after compression.
//
// 10. Still over budget → return ErrContextTooLarge.
// 11-12. (Handled by middleware: credit precheck + reserve, metadata inject.)
// 13. Create context_budget_event.
//
// The returned PrepareResult carries fields consumed by ContextBudgetCredits
// (Messages, EventID, PolicyID, TokenProfileID, SafeInputBudget, etc.).
func (b *Biz) Prepare(ctx context.Context, input aimw.PrepareInput) (*aimw.PrepareResult, error) {
	// Step 1: empty fragments — defence-in-depth (middleware already passthrough'd).
	if len(input.Fragments) == 0 {
		ev := b.buildEvent(input, nil, nil, 0, 0, "skipped", "")
		if err := b.store.CreateEvent(ctx, ev); err != nil {
			b.logger.Warnw("Prepare: CreateEvent (skipped) failed", "error", err)
		}
		return &aimw.PrepareResult{
			SkipBudget:   true,
			EventID:      ev.ID,
			NormalizedOp: input.Operation,
		}, nil
	}

	// Step 2: normalise operation.
	normalizedOp, normErr := normalizeOperation(input.Operation, input.UserID != 0)
	if normErr != nil {
		return nil, fmt.Errorf("Prepare: %w", normErr)
	}

	// Spec §5.4: when this Prepare is invoked from the compressor's own LLM call,
	// we must NOT recursively run compression. Detect by the canonical operation name.
	if normalizedOp == "context_compression" {
		return b.prepareWithoutCompression(ctx, input, normalizedOp)
	}

	// Step 3: read route capability.
	cap := contextbudget.ModelCapability{
		ContextWindow:   input.ContextWindow,
		MaxOutputTokens: input.MaxOutputTokens,
	}
	// Prefer values from Route when present.
	if input.Route != nil {
		if input.Route.Capability.ContextWindow > 0 {
			cap.ContextWindow = input.Route.Capability.ContextWindow
		}
		if input.Route.Capability.MaxOutputTokens > 0 {
			cap.MaxOutputTokens = input.Route.Capability.MaxOutputTokens
		}
	}
	if cap.ContextWindow <= 0 {
		cap.ContextWindow = 128000 // sensible fallback
	}
	if cap.MaxOutputTokens <= 0 {
		cap.MaxOutputTokens = 8192 // sensible fallback
	}

	// Step 4: load active policy.
	// Use normalizedOp (canonical key) instead of raw input.Operation so that
	// aliased raw ops (sop_node_execute, chatbot.stream, etc.) correctly hit
	// the seeded policy rows keyed by canonical name (sop_run, chatbot_chat, …).
	policyRow, err := b.store.GetActivePolicy(ctx, normalizedOp)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("Prepare: GetActivePolicy: %w", err)
		}
		// No policy configured → use a safe default and record event as "ok".
		policyRow = defaultPolicy(normalizedOp, cap)
	}

	policy := policyRowToBudgetPolicy(policyRow)

	// Step 5: load active token profile (exact then fallback).
	profileRow, tokenProfile, _ := b.loadTokenProfile(ctx, input)

	// Step 6: estimate fragments.
	budget, err := contextbudget.ComputeBudget(cap, policy)
	if err != nil {
		return nil, fmt.Errorf("Prepare: ComputeBudget: %w", err)
	}

	fragments := make([]contextbudget.ContextFragment, len(input.Fragments))
	copy(fragments, input.Fragments)

	estResult := contextbudget.EstimateFragments(fragments, tokenProfile, policy.FixedOverheadTokens, 0)
	estimatedBefore := estResult.PromptTokens

	// Steps 7–9: compression loop.
	const maxCompressionIter = 3
	for i := 0; i < maxCompressionIter && estResult.PromptTokens > budget.SafeInputBudget; i++ {
		// Load summary cache for planner phase 1 (spec §5.2).
		summaryCache := b.loadSummaryCache(ctx, input, fragments)

		plan, planErr := contextbudget.PlanCompression(contextbudget.PlanInput{
			Fragments:    fragments,
			Profile:      tokenProfile,
			Budget:       budget,
			Operation:    normalizedOp,
			SummaryCache: summaryCache,
		})
		if planErr != nil {
			return nil, fmt.Errorf("Prepare: PlanCompression: %w", planErr)
		}

		// Apply planner actions and, if Compressor is available, run LLM summaries.
		fragments, estResult = b.applyPlan(ctx, plan, fragments, summaryCache, tokenProfile, policy.FixedOverheadTokens)

		if plan.Feasible {
			break
		}
	}

	// Step 10: still over budget after compression.
	if estResult.PromptTokens > budget.SafeInputBudget {
		return nil, fmt.Errorf("%w: estimated=%d safe=%d op=%s",
			contextbudget.ErrContextTooLarge, estResult.PromptTokens, budget.SafeInputBudget, normalizedOp)
	}

	// Determine status.
	status := "ok"
	for _, f := range input.Fragments {
		// If any fragment was compressed / replaced, mark "compressed".
		if findFragment(fragments, f.ID) == nil {
			status = "compressed"
			break
		}
	}
	if status == "ok" && len(fragments) != len(input.Fragments) {
		status = "compressed"
	}

	// Step 13: persist event.
	var profileID *uint64
	if profileRow != nil {
		profileID = &profileRow.ID
	}
	policyID := policyRow.ID

	ev := b.buildEvent(input, &policyID, profileID, estimatedBefore, estResult.PromptTokens, status, "")
	ev.SafeInputBudget = budget.SafeInputBudget
	ev.ReservedOutputTokens = policy.ReservedOutputTokens
	ev.SafeRatio = policy.SafeRatio
	ev.FixedOverheadTokens = policy.FixedOverheadTokens

	if err := b.store.CreateEvent(ctx, ev); err != nil {
		return nil, fmt.Errorf("Prepare: CreateEvent: %w", err)
	}

	// Render fragments to ChatMessages.
	messages := aiservice.RenderContextFragments(fragments)

	var tokenProfileID uint64
	if profileRow != nil {
		tokenProfileID = profileRow.ID
	}

	return &aimw.PrepareResult{
		Fragments:       fragments,
		Messages:        messages,
		EstimatedBefore: estimatedBefore,
		EstimatedAfter:  estResult.PromptTokens,
		SafeInputBudget: budget.SafeInputBudget,
		Policy:          policy,
		PolicyID:        policyID,
		TokenProfileID:  tokenProfileID,
		EventID:         ev.ID,
		NormalizedOp:    normalizedOp,
		SkipBudget:      false,
	}, nil
}

// prepareWithoutCompression is a trimmed Prepare path used when operation ==
// "context_compression" (spec §5.4). It runs estimation and persists an event
// but deliberately skips the compression loop to prevent recursive LLM calls.
// If the estimate still exceeds the budget, ErrContextTooLarge is returned to
// guard against runaway context growth.
func (b *Biz) prepareWithoutCompression(ctx context.Context, input aimw.PrepareInput, normalizedOp string) (*aimw.PrepareResult, error) {
	// Resolve model capability.
	cap := contextbudget.ModelCapability{
		ContextWindow:   input.ContextWindow,
		MaxOutputTokens: input.MaxOutputTokens,
	}
	if input.Route != nil {
		if input.Route.Capability.ContextWindow > 0 {
			cap.ContextWindow = input.Route.Capability.ContextWindow
		}
		if input.Route.Capability.MaxOutputTokens > 0 {
			cap.MaxOutputTokens = input.Route.Capability.MaxOutputTokens
		}
	}
	if cap.ContextWindow <= 0 {
		cap.ContextWindow = 128000
	}
	if cap.MaxOutputTokens <= 0 {
		cap.MaxOutputTokens = 8192
	}

	// Load policy (fall back to default if not configured).
	// Use normalizedOp (already "context_compression") so the lookup is consistent.
	policyRow, err := b.store.GetActivePolicy(ctx, normalizedOp)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("prepareWithoutCompression: GetActivePolicy: %w", err)
		}
		policyRow = defaultPolicy(normalizedOp, cap)
	}
	policy := policyRowToBudgetPolicy(policyRow)

	// Load token profile.
	profileRow, tokenProfile, _ := b.loadTokenProfile(ctx, input)

	// Compute budget and estimate.
	budget, err := contextbudget.ComputeBudget(cap, policy)
	if err != nil {
		return nil, fmt.Errorf("prepareWithoutCompression: ComputeBudget: %w", err)
	}

	fragments := make([]contextbudget.ContextFragment, len(input.Fragments))
	copy(fragments, input.Fragments)

	estResult := contextbudget.EstimateFragments(fragments, tokenProfile, policy.FixedOverheadTokens, 0)
	estimatedBefore := estResult.PromptTokens

	// Guard: even without compression, if input is too large return an error.
	if estResult.PromptTokens > budget.SafeInputBudget {
		return nil, fmt.Errorf("%w: estimated=%d safe=%d op=%s (context_compression: no recursive compression)",
			contextbudget.ErrContextTooLarge, estResult.PromptTokens, budget.SafeInputBudget, normalizedOp)
	}

	// Persist event.
	var profileID *uint64
	if profileRow != nil {
		profileID = &profileRow.ID
	}
	policyID := policyRow.ID

	ev := b.buildEvent(input, &policyID, profileID, estimatedBefore, estResult.PromptTokens, "ok", "")
	ev.SafeInputBudget = budget.SafeInputBudget
	ev.ReservedOutputTokens = policy.ReservedOutputTokens
	ev.SafeRatio = policy.SafeRatio
	ev.FixedOverheadTokens = policy.FixedOverheadTokens

	if err := b.store.CreateEvent(ctx, ev); err != nil {
		return nil, fmt.Errorf("prepareWithoutCompression: CreateEvent: %w", err)
	}

	messages := aiservice.RenderContextFragments(fragments)

	var tokenProfileID uint64
	if profileRow != nil {
		tokenProfileID = profileRow.ID
	}

	return &aimw.PrepareResult{
		Fragments:       fragments,
		Messages:        messages,
		EstimatedBefore: estimatedBefore,
		EstimatedAfter:  estResult.PromptTokens,
		SafeInputBudget: budget.SafeInputBudget,
		Policy:          policy,
		PolicyID:        policyID,
		TokenProfileID:  tokenProfileID,
		EventID:         ev.ID,
		NormalizedOp:    normalizedOp,
		SkipBudget:      false,
	}, nil
}

// deriveOwnerUserID returns the owner user ID for summary cache lookups.
// For B2B2C child accounts (ParentUserID != nil), the owner is the parent.
// For standalone users, the owner is the user themselves (spec §3.4).
func deriveOwnerUserID(in aimw.PrepareInput) uint {
	if in.User != nil && in.User.ParentUserID != nil && *in.User.ParentUserID != 0 {
		return *in.User.ParentUserID
	}
	return in.UserID
}

// ---------------------------------------------------------------------------
// Finalize
// ---------------------------------------------------------------------------

// Finalize implements middleware.ContextBudgetService.
//
// It patches the context_budget_event row with:
//   - actual_prompt_tokens / actual_completion_tokens
//   - calibration_ratio = actual_prompt / estimated_before (when both > 0)
//   - status / error_code
//   - compression_actions list
//
// Credit reservation reconcile/refund is handled by the middleware directly
// via ContextBudgetCreditService; Finalize is only responsible for event
// persistence and calibration metadata.
func (b *Biz) Finalize(ctx context.Context, input aimw.FinalizeInput) error {
	if input.EventID == 0 {
		return nil // nothing to finalize
	}

	patch := store.EventPatch{}

	// Status and error code.
	if input.Status != "" {
		s := input.Status
		patch.Status = &s
	}
	if input.ErrorCode != "" {
		ec := input.ErrorCode
		patch.ErrorCode = &ec
	}

	// Actual usage.
	// Guard: do not write actual_prompt_tokens=0 when status="failed" — it would
	// conflate "zero tokens used" with "call failed before any tokens were consumed".
	if (input.ActualPromptTokens > 0 || !input.CalibrationSkipped) && input.Status != "failed" {
		pt := input.ActualPromptTokens
		ct := input.ActualCompletionTokens
		patch.ActualPromptTokens = &pt
		patch.ActualCompletionTokens = &ct
	}

	// Calibration ratio: actual_prompt_tokens / estimated_before.
	// Load the event from DB to get EstimatedBefore (stored at Prepare time).
	if input.ActualPromptTokens > 0 && !input.CalibrationSkipped {
		calibRatio, ratioErr := b.computeCalibrationRatio(ctx, input)
		if ratioErr == nil && calibRatio > 0 {
			patch.CalibrationRatio = &calibRatio
		}
	}

	// Compression actions JSON.
	if len(input.CompressionActions) > 0 {
		actionsJSON, err := json.Marshal(input.CompressionActions)
		if err == nil {
			patch.CompressionActions = datatypes.JSON(actionsJSON)
		}
	}

	// ReserveAmount and ReconcileDelta (spec verification condition).
	// ReserveAmount = the pre-call credit estimate carried in FinalizeInput.
	// ReconcileDelta = PricingCostCents − EstimatedCredits:
	//   positive → actual cost exceeded estimate (under-reserved)
	//   negative → actual cost less than estimate (over-reserved, partial refund)
	if input.EstimatedCredits > 0 {
		ra := input.EstimatedCredits
		patch.ReserveAmount = &ra
		rd := input.PricingCostCents - input.EstimatedCredits
		patch.ReconcileDelta = &rd
	}

	// Reservation ID if present.
	if input.ReservationID > 0 {
		rid := input.ReservationID
		patch.ReservationID = &rid
	}

	// Usage record ID if present.
	if input.UsageRecordID > 0 {
		uid := input.UsageRecordID
		patch.UsageRecordID = &uid
	}

	if err := b.store.PatchEvent(ctx, input.EventID, patch); err != nil {
		return fmt.Errorf("Finalize: PatchEvent: %w", err)
	}
	return nil
}

// computeCalibrationRatio loads the event's EstimatedBefore from DB and returns
// actual_prompt_tokens / estimated_before (spec §6.4).
// Returns (0, nil) when EstimatedBefore is 0 (no baseline to compare against).
func (b *Biz) computeCalibrationRatio(ctx context.Context, input aimw.FinalizeInput) (float64, error) {
	ev, err := b.store.GetEvent(ctx, input.EventID)
	if err != nil {
		return 0, fmt.Errorf("computeCalibrationRatio: %w", err)
	}
	if ev.EstimatedBefore <= 0 {
		return 0, nil
	}
	ratio := float64(input.ActualPromptTokens) / float64(ev.EstimatedBefore)
	return ratio, nil
}

// ---------------------------------------------------------------------------
// Preview
// ---------------------------------------------------------------------------

// PreviewInput carries the parameters for a budget preview calculation.
// The ModelCapability must be pre-resolved by the caller (e.g. admin biz layer
// that loads the ai_service row by ServiceID). This avoids coupling biz/contextbudget
// to the ai_service registry.
type PreviewInput struct {
	// Capability is the model token limits, resolved by the caller from ServiceID.
	Capability contextbudget.ModelCapability
	// Operation is the billing operation string.
	Operation string
	// FixedOverheadTokens is the constant token overhead for chat formatting.
	FixedOverheadTokens int
	// ReservedOutputTokens is the number of tokens to reserve for model output.
	ReservedOutputTokens int
	// SafeRatio is the safe utilisation fraction [0.50, 0.95].
	SafeRatio float64
	// SoftThresholdRatio is the soft-pressure fraction (default 0.7).
	SoftThresholdRatio float64
	// HardThresholdRatio is the hard-pressure fraction (default 0.85).
	HardThresholdRatio float64
}

// PreviewResult carries the computed budget thresholds.
type PreviewResult struct {
	// ContextWindow is the model's total context window (tokens).
	ContextWindow int `json:"context_window"`
	// MaxOutputTokens is the model's maximum output capacity.
	MaxOutputTokens int `json:"max_output_tokens"`
	// ReservedOutputTokens is the output token reservation used.
	ReservedOutputTokens int `json:"reserved_output_tokens"`
	// SafeInputBudget is the maximum tokens for the input side.
	SafeInputBudget int `json:"safe_input_budget"`
	// SoftThreshold is the monitoring threshold.
	SoftThreshold int `json:"soft_threshold"`
	// HardThreshold is the compression trigger threshold.
	HardThreshold int `json:"hard_threshold"`
	// Valid is true when all inputs satisfy the spec §2.4 constraints.
	Valid bool `json:"valid"`
	// Warnings lists advisory messages (e.g. when safe_ratio is near limits).
	Warnings []string `json:"warnings"`
}

// Preview computes the token budget thresholds for a given model capability and
// policy parameters. This is a helper for the admin preview API (spec §7.3).
// It does NOT touch the database — all parameters come from the caller.
func (b *Biz) Preview(_ context.Context, input PreviewInput) (*PreviewResult, error) {
	policy := contextbudget.BudgetPolicy{
		Operation:            input.Operation,
		ReservedOutputTokens: input.ReservedOutputTokens,
		SafeRatio:            input.SafeRatio,
		FixedOverheadTokens:  input.FixedOverheadTokens,
		SoftThresholdRatio:   input.SoftThresholdRatio,
		HardThresholdRatio:   input.HardThresholdRatio,
	}

	budget, err := contextbudget.ComputeBudget(input.Capability, policy)
	if err != nil {
		return &PreviewResult{
			ContextWindow:        input.Capability.ContextWindow,
			MaxOutputTokens:      input.Capability.MaxOutputTokens,
			ReservedOutputTokens: input.ReservedOutputTokens,
			Valid:                false,
			Warnings:             []string{err.Error()},
		}, nil
	}

	var warnings []string
	if input.SafeRatio >= 0.93 {
		warnings = append(warnings, "safe_ratio is near the maximum (0.95); consider reducing for headroom")
	}

	return &PreviewResult{
		ContextWindow:        input.Capability.ContextWindow,
		MaxOutputTokens:      input.Capability.MaxOutputTokens,
		ReservedOutputTokens: input.ReservedOutputTokens,
		SafeInputBudget:      budget.SafeInputBudget,
		SoftThreshold:        budget.SoftThreshold,
		HardThreshold:        budget.HardThreshold,
		Valid:                true,
		Warnings:             warnings,
	}, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// budgetOperationMap mirrors the spec §6.1.1 mapping table.
// Kept as a plain string→string map to avoid importing biz/credit just for these constants.
var budgetOperationMap = map[string]string{
	"sop_node_execute":              "sop_run",
	"sop_run":                       "sop_run",
	"sop_chat":                      "sop_chat",
	"sop_chat_stream":               "sop_chat",
	"chatbot_chat":                  "chatbot_chat",
	"chatbot.stream":                "chatbot_chat",
	"salesrag_chat":                 "salesrag_chat",
	"salesrag_chat_generate":        "salesrag_chat",
	"salesrag_strategy_select":      "salesrag_chat",
	"salesrag_analyze_profile":      "salesrag_chat",
	"salesrag_analyze_profile_text": "salesrag_chat",
	"salesrag_chat_style_text":      "salesrag_chat",
	"context_compression":           "context_compression",
}

// normalizeOperation maps raw billing operation strings to canonical context-budget
// operation strings per spec §6.1.1.
//
// If the operation is not in the map:
//   - hasUser=true  (caller has user billing context, i.e. would charge): returns
//     ("", ErrContextConfigInvalid) — fail-closed; silently defaulting to
//     "default_llm_chat" for a charged user call would be unsafe.
//   - hasUser=false (internal/admin call, uncharged): returns "default_llm_chat".
func normalizeOperation(op string, hasUser bool) (string, error) {
	if normalized, ok := budgetOperationMap[op]; ok {
		return normalized, nil
	}
	if hasUser {
		return "", fmt.Errorf("normalizeOperation: %w: unknown operation %q for user billing context",
			contextbudget.ErrContextConfigInvalid, op)
	}
	return "default_llm_chat", nil
}

// loadTokenProfile loads a token estimation profile for the route's provider/model.
//
// It performs spec §3.2 lookup levels 1, 3, and 4:
//
//  1. Exact:            (provider, model, service_type, is_fallback=false, is_active=true)
//  2. (family-level skipped — deferred until admin can supply ModelFamily explicitly)
//  3. Provider fallback: (provider, "",    service_type, is_fallback=true,  is_active=true)
//  4. Global fallback:  ("",       "",    service_type, is_fallback=true,  is_active=true)
//
// Returns (row, profile, isFallback). isFallback=true when levels 3 or 4 were used.
// When no DB row is found at any level, returns (nil, defaultTokenProfile(), true).
func (b *Biz) loadTokenProfile(ctx context.Context, input aimw.PrepareInput) (*model.TokenEstimationProfile, contextbudget.TokenProfile, bool) {
	if input.Route == nil {
		return nil, defaultTokenProfile(), true
	}

	provider := input.Route.Provider.Name
	modelKey := input.Route.ServiceKey

	// Level 1: exact match (provider, model).
	row, err := b.store.GetActiveTokenProfile(ctx, store.TokenProfileLookupKey{
		Provider:    provider,
		Model:       modelKey,
		ServiceType: "llm_chat",
		IsFallback:  false,
	})
	if err == nil {
		p, parseErr := parseTokenProfile(row, false)
		if parseErr == nil {
			return row, p, false
		}
		b.logger.Warnw("loadTokenProfile: parse error for exact profile",
			"provider", provider, "model", modelKey, "error", parseErr)
	}

	// Level 3: provider-scoped fallback (provider, "").
	// NOTE: level 2 (model-family lookup) is deferred until admin explicitly
	// supplies a ModelFamily value; family derivation from model strings is
	// unreliable across providers.
	fallbackRow, err := b.store.GetActiveTokenProfile(ctx, store.TokenProfileLookupKey{
		Provider:    provider,
		Model:       "",
		ServiceType: "llm_chat",
		IsFallback:  true,
	})
	if err == nil {
		p, parseErr := parseTokenProfile(fallbackRow, true)
		if parseErr == nil {
			return fallbackRow, p, true
		}
	}

	// Level 4: global fallback ("", "").
	globalRow, err := b.store.GetActiveTokenProfile(ctx, store.TokenProfileLookupKey{
		Provider:    "",
		Model:       "",
		ServiceType: "llm_chat",
		IsFallback:  true,
	})
	if err == nil {
		p, parseErr := parseTokenProfile(globalRow, true)
		if parseErr == nil {
			return globalRow, p, true
		}
	}

	// Built-in safe default (already conservative; treated as fallback).
	return nil, defaultTokenProfile(), true
}

// parseTokenProfile deserialises the JSON profile embedded in a DB row.
// When isFallback is true, the safety multiplier is raised to at least 1.30
// per spec §3.2 to account for the higher estimation uncertainty of non-exact profiles.
func parseTokenProfile(row *model.TokenEstimationProfile, isFallback bool) (contextbudget.TokenProfile, error) {
	var tp contextbudget.TokenProfile
	if err := json.Unmarshal(row.ProfileJSON, &tp); err != nil {
		return contextbudget.TokenProfile{}, err
	}
	// Override multipliers from DB columns (may differ from JSON blob).
	if row.SafetyMultiplier >= 1.0 {
		tp.SafetyMultiplier = row.SafetyMultiplier
	}
	if row.CalibrationMultiplier > 0 {
		tp.CalibrationMultiplier = row.CalibrationMultiplier
	}
	// Spec §3.2: when a fallback row is used, apply a minimum safety boost of 1.30.
	if isFallback && tp.SafetyMultiplier < 1.30 {
		tp.SafetyMultiplier = 1.30
	}
	return tp, nil
}

// defaultTokenProfile returns a built-in conservative estimation profile used
// when no DB profile is configured.
func defaultTokenProfile() contextbudget.TokenProfile {
	return contextbudget.TokenProfile{
		Method:                 "default",
		MessageOverheadTokens:  4,
		FragmentOverheadTokens: 2,
		Classes: map[string]contextbudget.TokenClass{
			"en":     {TokenPerChar: 0.25},
			"zh":     {TokenPerChar: 0.60},
			"code":   {TokenPerChar: 0.30},
			"json":   {TokenPerChar: 0.25},
			"symbol": {TokenPerChar: 0.20},
			"mixed":  {TokenPerChar: 0.45},
		},
		SafetyMultiplier:      1.30, // spec §3.2: fallback paths must apply max(orig, 1.30)
		CalibrationMultiplier: 1.0,
	}
}

// defaultPolicy returns a safe default budget policy when none is configured.
func defaultPolicy(op string, cap contextbudget.ModelCapability) *model.ContextBudgetPolicy {
	reserved := cap.MaxOutputTokens / 2
	if reserved <= 0 {
		reserved = 2048
	}
	return &model.ContextBudgetPolicy{
		ID:                   0,
		Operation:            op,
		ReservedOutputTokens: reserved,
		SafeRatio:            0.85,
		FixedOverheadTokens:  512,
		SoftThresholdRatio:   0.70,
		HardThresholdRatio:   0.85,
		ChargeUser:           false,
		Version:              0,
		IsActive:             false, // synthetic, not persisted
	}
}

// policyRowToBudgetPolicy converts a DB policy row to the contextbudget domain type.
func policyRowToBudgetPolicy(row *model.ContextBudgetPolicy) contextbudget.BudgetPolicy {
	return contextbudget.BudgetPolicy{
		Operation:            row.Operation,
		ReservedOutputTokens: row.ReservedOutputTokens,
		SafeRatio:            row.SafeRatio,
		FixedOverheadTokens:  row.FixedOverheadTokens,
		SoftThresholdRatio:   row.SoftThresholdRatio,
		HardThresholdRatio:   row.HardThresholdRatio,
		ChargeUser:           row.ChargeUser,
	}
}

// loadSummaryCache queries the store for existing summaries matching the current
// request context. The summary cache is keyed by SourceReference (fragment hash)
// and scoped by owner_user_id + scope_type + scope_id extracted from metadata.
func (b *Biz) loadSummaryCache(ctx context.Context, input aimw.PrepareInput, fragments []contextbudget.ContextFragment) map[string]contextbudget.ContextFragment {
	if input.UserID == 0 {
		return nil
	}

	// Extract scope from metadata (sop_run_id, chat_session_id, etc.).
	scopeType, scopeID := extractScope(input.Metadata)
	if scopeType == "" || scopeID == "" {
		return nil
	}

	// Derive owner: for B2B2C child accounts the cache is keyed by the parent
	// (owner_user_id = parent_user_id), so child and parent share summaries (spec §3.4).
	ownerUserID := deriveOwnerUserID(input)

	cache := make(map[string]contextbudget.ContextFragment)
	for _, f := range fragments {
		if f.SourceReference == "" {
			continue
		}
		summary, err := b.store.FindReadySummary(ctx, ownerUserID, scopeType, scopeID, f.SourceReference)
		if err != nil {
			continue // not found or error — skip
		}
		cache[f.SourceReference] = contextbudget.ContextFragment{
			ID:          "summary-" + f.ID,
			Role:        contextbudget.RoleDurable,
			Source:      contextbudget.SourceInternal,
			ContentType: contextbudget.ContentSummary,
			Content:     summary.SummaryText,
			Importance:  f.Importance,
			Order:       f.Order,
		}
	}
	return cache
}

// extractScope derives (scope_type, scope_id) from a metadata map.
// Recognises keys: sop_run_id → ("sop_run", value), chat_session_id → ("chat_session", value).
func extractScope(meta map[string]string) (string, string) {
	if meta == nil {
		return "", ""
	}
	if v := meta["sop_run_id"]; v != "" {
		return "sop_run", v
	}
	if v := meta["chat_session_id"]; v != "" {
		return "chat_session", v
	}
	if v := meta["scope_id"]; v != "" {
		scopeType := meta["scope_type"]
		if scopeType == "" {
			scopeType = "generic"
		}
		return scopeType, v
	}
	return "", ""
}

// applyPlan executes planner actions in memory.
//
//   - ActionDrop:         removes the fragment entirely.
//   - ActionSummarize:    calls the Compressor (if available); on failure drops candidate.
//   - ActionReuseSummary: substitutes the fragment with the pre-loaded cached summary
//     fragment from summaryCache (keyed by fragment.SourceReference).
//   - ActionReference:    rewrites fragment.Content to a short "[ref: <sourceRef>]" form.
//   - ActionKeep:         passes the fragment through unchanged.
//
// summaryCache maps SourceReference → cached summary fragment (loaded by loadSummaryCache).
// Returns the updated fragment slice and a new EstimateResult.
func (b *Biz) applyPlan(
	ctx context.Context,
	plan contextbudget.Plan,
	fragments []contextbudget.ContextFragment,
	summaryCache map[string]contextbudget.ContextFragment,
	profile contextbudget.TokenProfile,
	fixedOverhead int,
) ([]contextbudget.ContextFragment, contextbudget.EstimateResult) {
	// Build a fast lookup map.
	fragMap := make(map[string]int, len(fragments))
	for i, f := range fragments {
		fragMap[f.ID] = i
	}

	// Build an action-type index keyed by FragmentID.
	actionByID := make(map[string]contextbudget.ActionType, len(plan.Actions))
	for _, action := range plan.Actions {
		actionByID[action.FragmentID] = action.Type
	}

	drop := make(map[string]bool)

	// Collect summarize candidates for batch compression.
	var summarizeCandidates []contextbudget.ContextFragment
	var summarizeIDs []string
	for _, action := range plan.Actions {
		switch action.Type {
		case contextbudget.ActionDrop:
			drop[action.FragmentID] = true
		case contextbudget.ActionSummarize:
			if idx, ok := fragMap[action.FragmentID]; ok {
				summarizeCandidates = append(summarizeCandidates, fragments[idx])
				summarizeIDs = append(summarizeIDs, action.FragmentID)
			}
		}
	}

	// Attempt batch summarisation.
	var summaryFrag *contextbudget.ContextFragment
	if len(summarizeCandidates) > 0 && b.compressor != nil {
		targetTokens := 0
		for _, f := range summarizeCandidates {
			targetTokens += f.TokenEstimate
		}
		if targetTokens <= 0 {
			targetTokens = 512
		}
		targetTokens = int(float64(targetTokens) * 0.40)

		sf, compErr := b.compressor.Compress(ctx, summarizeCandidates, targetTokens)
		if compErr == nil {
			summaryFrag = &sf
			for _, id := range summarizeIDs {
				drop[id] = true
			}
		} else {
			// Compression failed — fall back to dropping the candidates.
			b.logger.Warnw("applyPlan: Compress failed, dropping candidates", "error", compErr)
			for _, id := range summarizeIDs {
				drop[id] = true
			}
		}
	} else if len(summarizeCandidates) > 0 {
		// No compressor — drop the candidates as fallback.
		for _, id := range summarizeIDs {
			drop[id] = true
		}
	}

	// Rebuild fragment list, applying ReuseSummary and Reference in-order.
	result := make([]contextbudget.ContextFragment, 0, len(fragments))
	for _, f := range fragments {
		if drop[f.ID] {
			continue
		}
		switch actionByID[f.ID] {
		case contextbudget.ActionReuseSummary:
			// Replace with cached summary fragment if available.
			if cached, ok := summaryCache[f.SourceReference]; ok {
				result = append(result, cached)
			} else {
				// Cache miss (shouldn't happen if planner used cache correctly) — keep original.
				b.logger.Warnw("applyPlan: ActionReuseSummary cache miss, keeping original",
					"fragment_id", f.ID, "source_reference", f.SourceReference)
				result = append(result, f)
			}
		case contextbudget.ActionReference:
			// Replace long content with a short [ref: <sourceRef>] pointer.
			shortened := f
			if shortened.SourceReference != "" {
				shortened.Content = fmt.Sprintf("[ref: %s]", shortened.SourceReference)
			}
			result = append(result, shortened)
		default:
			result = append(result, f)
		}
	}
	if summaryFrag != nil {
		result = append(result, *summaryFrag)
	}

	est := contextbudget.EstimateFragments(result, profile, fixedOverhead, 0)
	return result, est
}

// buildEvent constructs a ContextBudgetEvent for persisting. Fields not known
// at event creation time (actual usage, calibration) are left nil/zero.
func (b *Biz) buildEvent(
	input aimw.PrepareInput,
	policyID *uint64,
	profileID *uint64,
	estimatedBefore, estimatedAfter int,
	status, errorCode string,
) *model.ContextBudgetEvent {
	ev := &model.ContextBudgetEvent{
		Operation:       input.Operation,
		EstimatedBefore: estimatedBefore,
		EstimatedAfter:  estimatedAfter,
		Status:          status,
		ErrorCode:       errorCode,
		BudgetPolicyID:  policyID,
		TokenProfileID:  profileID,
	}

	if input.UserID != 0 {
		uid := input.UserID
		ev.UserID = &uid
	}
	if input.TaskID != "" {
		ev.TaskID = input.TaskID
	}
	if input.Route != nil {
		ev.Provider = input.Route.Provider.Name
		ev.Model = input.Route.ServiceKey
		ev.ContextWindow = input.Route.Capability.ContextWindow
		ev.MaxOutputTokens = input.Route.Capability.MaxOutputTokens
	} else {
		ev.ContextWindow = input.ContextWindow
		ev.MaxOutputTokens = input.MaxOutputTokens
	}

	return ev
}

// findFragment returns the fragment with the given ID from fragments, or nil.
func findFragment(fragments []contextbudget.ContextFragment, id string) *contextbudget.ContextFragment {
	for i := range fragments {
		if fragments[i].ID == id {
			return &fragments[i]
		}
	}
	return nil
}
