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

	// Step 2: normalise operation. For internal ops (context_compression) we allow
	// them through without mapping to a credit.Operation.
	normalizedOp := normalizeOperation(input.Operation)

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
	policyRow, err := b.store.GetActivePolicy(ctx, input.Operation)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("Prepare: GetActivePolicy: %w", err)
		}
		// No policy configured → use a safe default and record event as "ok".
		policyRow = defaultPolicy(input.Operation, cap)
	}

	policy := policyRowToBudgetPolicy(policyRow)

	// Step 5: load active token profile (exact then fallback).
	profileRow, tokenProfile := b.loadTokenProfile(ctx, input)

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
		fragments, estResult = b.applyPlan(ctx, plan, fragments, tokenProfile, policy.FixedOverheadTokens)

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
	if input.ActualPromptTokens > 0 || !input.CalibrationSkipped {
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
	ContextWindow int
	// MaxOutputTokens is the model's maximum output capacity.
	MaxOutputTokens int
	// ReservedOutputTokens is the output token reservation used.
	ReservedOutputTokens int
	// SafeInputBudget is the maximum tokens for the input side.
	SafeInputBudget int
	// SoftThreshold is the monitoring threshold.
	SoftThreshold int
	// HardThreshold is the compression trigger threshold.
	HardThreshold int
	// Valid is true when all inputs satisfy the spec §2.4 constraints.
	Valid bool
	// Warnings lists advisory messages (e.g. when safe_ratio is near limits).
	Warnings []string
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

// normalizeOperation maps raw billing operation strings to canonical names used
// in event/metadata. Internal ops (context_compression) pass through unchanged.
func normalizeOperation(op string) string {
	if op == "" {
		return "unknown"
	}
	return op
}

// loadTokenProfile attempts to load an exact token profile for the route's
// provider/model, falling back to a fallback profile, then to a built-in default.
func (b *Biz) loadTokenProfile(ctx context.Context, input aimw.PrepareInput) (*model.TokenEstimationProfile, contextbudget.TokenProfile) {
	if input.Route == nil {
		return nil, defaultTokenProfile()
	}

	provider := input.Route.Provider.Name
	model := input.Route.ServiceKey

	// Try exact match.
	row, err := b.store.GetActiveTokenProfile(ctx, store.TokenProfileLookupKey{
		Provider:    provider,
		Model:       model,
		ServiceType: "llm_chat",
		IsFallback:  false,
	})
	if err == nil {
		p, parseErr := parseTokenProfile(row)
		if parseErr == nil {
			return row, p
		}
		b.logger.Warnw("loadTokenProfile: parse error for exact profile",
			"provider", provider, "model", model, "error", parseErr)
	}

	// Try fallback profile.
	fallbackRow, err := b.store.GetActiveTokenProfile(ctx, store.TokenProfileLookupKey{
		Provider:    provider,
		Model:       "",
		ServiceType: "llm_chat",
		IsFallback:  true,
	})
	if err == nil {
		p, parseErr := parseTokenProfile(fallbackRow)
		if parseErr == nil {
			return fallbackRow, p
		}
	}

	// Built-in safe default.
	return nil, defaultTokenProfile()
}

// parseTokenProfile deserialises the JSON profile embedded in a DB row.
func parseTokenProfile(row *model.TokenEstimationProfile) (contextbudget.TokenProfile, error) {
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
		SafetyMultiplier:      1.15,
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

	cache := make(map[string]contextbudget.ContextFragment)
	for _, f := range fragments {
		if f.SourceReference == "" {
			continue
		}
		summary, err := b.store.FindReadySummary(ctx, input.UserID, scopeType, scopeID, f.SourceReference)
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

// applyPlan executes planner actions in memory. For ActionSummarize actions it
// calls the Compressor (if available); ActionDrop removes fragments; other
// actions are handled by the planner's updated fragment content.
// Returns the updated fragment slice and a new EstimateResult.
func (b *Biz) applyPlan(
	ctx context.Context,
	plan contextbudget.Plan,
	fragments []contextbudget.ContextFragment,
	profile contextbudget.TokenProfile,
	fixedOverhead int,
) ([]contextbudget.ContextFragment, contextbudget.EstimateResult) {
	// Build a fast lookup map.
	fragMap := make(map[string]int, len(fragments))
	for i, f := range fragments {
		fragMap[f.ID] = i
	}

	result := make([]contextbudget.ContextFragment, 0, len(fragments))
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

	// Rebuild fragment list.
	for _, f := range fragments {
		if !drop[f.ID] {
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
