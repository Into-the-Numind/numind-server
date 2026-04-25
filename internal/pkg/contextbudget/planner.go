package contextbudget

import (
	"fmt"
	"sort"
)

// ActionType describes what the planner recommends doing with a fragment.
type ActionType string

const (
	// ActionReuseSummary recommends replacing the fragment with an existing cached summary.
	ActionReuseSummary ActionType = "reuse_summary"
	// ActionReference recommends replacing the fragment with a lightweight reference pointer.
	ActionReference ActionType = "reference"
	// ActionSummarize recommends the caller to compress the fragment into an LLM-generated summary.
	ActionSummarize ActionType = "summarize"
	// ActionDrop recommends removing the fragment from the context entirely.
	ActionDrop ActionType = "drop"
	// ActionKeep recommends leaving the fragment as-is.
	ActionKeep ActionType = "keep"
)

// Action is a single recommendation produced by the planner for one fragment.
type Action struct {
	// FragmentID is the ID of the target fragment.
	FragmentID string `json:"fragment_id"`
	// Type is the recommended compression action.
	Type ActionType `json:"type"`
	// Reason is a short human-readable explanation for the recommendation.
	Reason string `json:"reason"`
}

// Plan is the output of PlanCompression.
type Plan struct {
	// Actions is the ordered list of recommendations, one per fragment.
	Actions []Action `json:"actions"`
	// EstimatedAfter is the estimated token count after applying all recommended actions.
	EstimatedAfter int `json:"estimated_after"`
	// Feasible is true when EstimatedAfter <= Budget.SafeInputBudget.
	Feasible bool `json:"feasible"`
	// UnusedBudget is the remaining token budget after compression (negative if infeasible).
	UnusedBudget int `json:"unused_budget"`
}

// PlanInput bundles all inputs required by PlanCompression.
type PlanInput struct {
	// Fragments is the full list of fragments to plan over.
	Fragments []ContextFragment `json:"fragments"`
	// Profile is the token estimation profile.
	Profile TokenProfile `json:"profile"`
	// Budget contains the computed thresholds.
	Budget Budget `json:"budget"`
	// Operation is a human-readable name for the operation (used in action reasons).
	Operation string `json:"operation"`
	// SummaryCache maps a source_hash to a ready-made summary fragment.
	// The planner uses this in Phase 1 to avoid redundant LLM summarization.
	SummaryCache map[string]ContextFragment `json:"summary_cache,omitempty"`
}

// planState tracks the mutable state during a planning run.
type planState struct {
	// fragments are the working copies of input fragments.
	fragments []ContextFragment
	// estimates maps fragment ID to current token estimate.
	estimates map[string]int
	// actions accumulates recommendations keyed by fragment ID.
	actions map[string]Action
	// total is the running estimated token count.
	total int
}

// PlanCompression produces a deterministic compression plan for the given input.
//
// The planner applies 6 phases in sequence (Spec §5.2):
//  1. Reuse summary cache
//  2. Reference large evidence/file fragments
//  3. Summarize compressible durable/recent/tool fragments
//  4. Drop low-value discardable fragments (importance asc, order asc)
//  5. Drop non-critical working fragments (order asc)
//  6. Minimal recent fallback
//
// The planner NEVER drops fragments where isCritical(f)==true.
// The planner NEVER reads Metadata keys for ranking or decision-making.
func PlanCompression(input PlanInput) (Plan, error) {
	if len(input.Profile.Classes) == 0 {
		return Plan{}, fmt.Errorf("%w: token profile has no classes", ErrTokenProfileMissing)
	}

	// Initial token estimation for all fragments.
	estResult := EstimateFragments(input.Fragments, input.Profile, 0, 0)

	state := &planState{
		fragments: make([]ContextFragment, len(input.Fragments)),
		estimates: make(map[string]int, len(input.Fragments)),
		actions:   make(map[string]Action, len(input.Fragments)),
		total:     estResult.PromptTokens,
	}
	copy(state.fragments, input.Fragments)

	for id, est := range estResult.PerFragmentMap {
		state.estimates[id] = est
	}

	// Apply phases in order, stopping early if budget is met.
	if state.total > input.Budget.SafeInputBudget {
		applyPhase1ReuseCache(state, input)
	}
	if state.total > input.Budget.SafeInputBudget {
		applyPhase2Reference(state, input)
	}
	if state.total > input.Budget.SafeInputBudget {
		applyPhase3Summarize(state, input)
	}
	if state.total > input.Budget.SafeInputBudget {
		applyPhase4DropDiscardable(state, input)
	}
	if state.total > input.Budget.SafeInputBudget {
		applyPhase5DropWorking(state, input)
	}
	if state.total > input.Budget.SafeInputBudget {
		applyPhase6MinimalFallback(state, input)
	}

	// Build final action list: assign ActionKeep to any fragment without an action.
	actions := make([]Action, 0, len(input.Fragments))
	for _, f := range input.Fragments {
		if a, ok := state.actions[f.ID]; ok {
			actions = append(actions, a)
		} else {
			actions = append(actions, Action{
				FragmentID: f.ID,
				Type:       ActionKeep,
				Reason:     "within budget",
			})
		}
	}

	feasible := state.total <= input.Budget.SafeInputBudget
	return Plan{
		Actions:        actions,
		EstimatedAfter: state.total,
		Feasible:       feasible,
		UnusedBudget:   input.Budget.SafeInputBudget - state.total,
	}, nil
}

// applyPhase1ReuseCache replaces fragments with matching cached summaries.
// Matching is based on fragment.SourceReference (used as the cache key).
func applyPhase1ReuseCache(state *planState, input PlanInput) {
	if len(input.SummaryCache) == 0 {
		return
	}
	for i, f := range state.fragments {
		if _, alreadyActed := state.actions[f.ID]; alreadyActed {
			continue
		}
		if summary, ok := input.SummaryCache[f.SourceReference]; ok && f.SourceReference != "" {
			oldEst := state.estimates[f.ID]
			newEst := estimateSingleFragment(summary.Content, input.Profile, multiplierSafety(input.Profile), multiplierCalib(input.Profile))
			state.total = state.total - oldEst + newEst
			state.estimates[f.ID] = newEst
			state.fragments[i].Content = summary.Content
			state.actions[f.ID] = Action{
				FragmentID: f.ID,
				Type:       ActionReuseSummary,
				Reason:     fmt.Sprintf("replaced with cached summary (key=%s)", f.SourceReference),
			}
		}
	}
}

// applyPhase2Reference replaces fragments that support reference compression.
func applyPhase2Reference(state *planState, input PlanInput) {
	for i, f := range state.fragments {
		if _, alreadyActed := state.actions[f.ID]; alreadyActed {
			continue
		}
		if f.Compressibility != CompressReference || f.SourceReference == "" {
			continue
		}
		// Replace content with a short reference stub.
		refContent := fmt.Sprintf("[ref: %s]", f.SourceReference)
		oldEst := state.estimates[f.ID]
		newEst := estimateSingleFragment(refContent, input.Profile, multiplierSafety(input.Profile), multiplierCalib(input.Profile))
		state.total = state.total - oldEst + newEst
		state.estimates[f.ID] = newEst
		state.fragments[i].Content = refContent
		state.actions[f.ID] = Action{
			FragmentID: f.ID,
			Type:       ActionReference,
			Reason:     fmt.Sprintf("referenced large fragment (ref=%s)", f.SourceReference),
		}
	}
}

// applyPhase3Summarize marks compressible durable/recent/tool fragments for summarization.
// Actual LLM summarization is performed by the caller; the planner only decides what to summarize.
//
// Spec §5.2 phase 3: eligible fragments are durable, recent, or tool-sourced.
// RoleEvidence is handled in phase 2 (reference compression).
// RoleWorking is handled in phase 5 (drop working).
func applyPhase3Summarize(state *planState, input PlanInput) {
	for _, f := range state.fragments {
		if _, alreadyActed := state.actions[f.ID]; alreadyActed {
			continue
		}
		if f.Compressibility != CompressSummarize {
			continue
		}
		// Eligible: durable role, recent role, or tool-sourced (spec §5.2 phase 3).
		eligible := f.Role == RoleDurable || f.Role == RoleRecent || f.Source == SourceTool
		if !eligible {
			continue
		}
		state.actions[f.ID] = Action{
			FragmentID: f.ID,
			Type:       ActionSummarize,
			Reason:     fmt.Sprintf("fragment role=%s source=%s is compressible via summarization", f.Role, f.Source),
		}
		// Optimistically assume summarization reduces tokens by 60%.
		oldEst := state.estimates[f.ID]
		newEst := int(float64(oldEst) * 0.40)
		if newEst < 1 {
			newEst = 1
		}
		state.total = state.total - oldEst + newEst
		state.estimates[f.ID] = newEst
	}
}

// applyPhase4DropDiscardable drops low-value discardable fragments.
// Sort order: importance ascending (lowest first), then order ascending (oldest first).
// Critical fragments are never dropped.
func applyPhase4DropDiscardable(state *planState, input PlanInput) {
	candidates := collectDropCandidates(state, input, func(f ContextFragment) bool {
		return f.Role == RoleDiscardable && f.Compressibility == CompressDrop
	})

	// Sort: importance asc, order asc (business Metadata is ignored).
	sort.Slice(candidates, func(i, j int) bool {
		fi, fj := candidates[i], candidates[j]
		if fi.Importance != fj.Importance {
			return fi.Importance < fj.Importance
		}
		return fi.Order < fj.Order
	})

	for _, f := range candidates {
		if state.total <= input.Budget.SafeInputBudget {
			break
		}
		dropFragment(state, f, "low-value discardable fragment dropped for budget")
	}
}

// applyPhase5DropWorking drops non-critical working fragments.
// Sort order: order ascending (oldest first).
func applyPhase5DropWorking(state *planState, input PlanInput) {
	candidates := collectDropCandidates(state, input, func(f ContextFragment) bool {
		return f.Role == RoleWorking && f.Compressibility == CompressDrop
	})

	// Sort: order asc (oldest first).
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Order < candidates[j].Order
	})

	for _, f := range candidates {
		if state.total <= input.Budget.SafeInputBudget {
			break
		}
		dropFragment(state, f, "non-critical working fragment dropped for budget")
	}
}

// applyPhase6MinimalFallback retains only immutable/critical/current-request fragments
// and the most recent "recent" fragments, dropping everything else.
func applyPhase6MinimalFallback(state *planState, input PlanInput) {
	// Identify fragments that must survive.
	mustKeep := make(map[string]bool)
	for _, f := range state.fragments {
		if isCritical(f) {
			mustKeep[f.ID] = true
		}
	}

	// Keep the single most recent "recent" fragment.
	var latestRecent *ContextFragment
	for i := range state.fragments {
		f := &state.fragments[i]
		if f.Role == RoleRecent && !isCritical(*f) {
			if latestRecent == nil || f.Order > latestRecent.Order {
				latestRecent = f
			}
		}
	}
	if latestRecent != nil {
		mustKeep[latestRecent.ID] = true
	}

	// Drop everything not in mustKeep that hasn't been acted on yet.
	for _, f := range state.fragments {
		if mustKeep[f.ID] {
			continue
		}
		if _, alreadyActed := state.actions[f.ID]; alreadyActed {
			// Only skip if already dropped (don't double-act).
			if state.actions[f.ID].Type == ActionDrop {
				continue
			}
		}
		if state.total <= input.Budget.SafeInputBudget {
			break
		}
		dropFragment(state, f, "minimal fallback: retaining only critical and most recent fragments")
	}
}

// collectDropCandidates returns fragments that match the predicate and are not critical or already acted on.
func collectDropCandidates(state *planState, input PlanInput, predicate func(ContextFragment) bool) []ContextFragment {
	var result []ContextFragment
	for _, f := range state.fragments {
		if isCritical(f) {
			continue
		}
		if _, alreadyActed := state.actions[f.ID]; alreadyActed {
			continue
		}
		if predicate(f) {
			result = append(result, f)
		}
	}
	return result
}

// dropFragment records a drop action and updates the running total.
func dropFragment(state *planState, f ContextFragment, reason string) {
	est := state.estimates[f.ID]
	state.total -= est
	state.estimates[f.ID] = 0
	state.actions[f.ID] = Action{
		FragmentID: f.ID,
		Type:       ActionDrop,
		Reason:     reason,
	}
}

// multiplierSafety extracts the safety multiplier from a profile, defaulting to 1.0.
func multiplierSafety(profile TokenProfile) float64 {
	if profile.SafetyMultiplier <= 0 {
		return 1.0
	}
	return profile.SafetyMultiplier
}

// multiplierCalib extracts the calibration multiplier from a profile, defaulting to 1.0.
func multiplierCalib(profile TokenProfile) float64 {
	if profile.CalibrationMultiplier <= 0 {
		return 1.0
	}
	return profile.CalibrationMultiplier
}
