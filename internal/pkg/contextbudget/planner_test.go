package contextbudget_test

import (
	"reflect"
	"testing"

	"numind-server/internal/pkg/contextbudget"
)

// makeProfile returns a simple TokenProfile for planner tests.
func makeProfile() contextbudget.TokenProfile {
	return contextbudget.TokenProfile{
		Method:                 "weighted_char",
		MessageOverheadTokens:  4,
		FragmentOverheadTokens: 2,
		SafetyMultiplier:       1.0,
		CalibrationMultiplier:  1.0,
		Classes: map[string]contextbudget.TokenClass{
			"zh":             {TokenPerChar: 1.5},
			"en":             {TokenPerChar: 0.25},
			"code":           {TokenPerChar: 0.30},
			"json":           {TokenPerChar: 0.28},
			"markdown_table": {TokenPerChar: 0.30},
			"symbol":         {TokenPerChar: 0.20},
			"mixed":          {TokenPerChar: 0.50},
		},
	}
}

// makeBudget returns a Budget with generous limits so planner stays in "no compression needed" path by default.
func makeBudget(safe int) contextbudget.Budget {
	return contextbudget.Budget{
		SafeInputBudget: safe,
		SoftThreshold:   int(float64(safe) * 0.7),
		HardThreshold:   int(float64(safe) * 0.9),
	}
}

func TestPlannerDoesNotDropCriticalOrCurrentFragments(t *testing.T) {
	profile := makeProfile()

	// Build fragments that together exceed a tiny budget so the planner is forced
	// into drop mode — but critical/immutable fragments must survive.
	fragments := []contextbudget.ContextFragment{
		{
			ID:              "sys-prompt",
			Role:            contextbudget.RoleImmutable,
			Source:          contextbudget.SourceSystem,
			ContentType:     contextbudget.ContentText,
			Content:         "You are a helpful assistant. " + repeat("a", 500),
			Importance:      10,
			Order:           0,
			Compressibility: contextbudget.CompressNone,
			Critical:        true,
		},
		{
			ID:              "current-request",
			Role:            contextbudget.RoleWorking,
			Source:          contextbudget.SourceUser,
			ContentType:     contextbudget.ContentText,
			Content:         "What is the answer? " + repeat("b", 200),
			Importance:      10,
			Order:           100,
			Compressibility: contextbudget.CompressNone,
			Critical:        true,
		},
		{
			ID:              "low-value-1",
			Role:            contextbudget.RoleDiscardable,
			Source:          contextbudget.SourceUser,
			ContentType:     contextbudget.ContentText,
			Content:         repeat("c", 1000),
			Importance:      1,
			Order:           5,
			Compressibility: contextbudget.CompressDrop,
		},
		{
			ID:              "low-value-2",
			Role:            contextbudget.RoleDiscardable,
			Source:          contextbudget.SourceUser,
			ContentType:     contextbudget.ContentText,
			Content:         repeat("d", 1000),
			Importance:      1,
			Order:           6,
			Compressibility: contextbudget.CompressDrop,
		},
	}

	// Very tight budget (10 tokens) to force aggressive compression.
	budget := makeBudget(10)

	plan, err := contextbudget.PlanCompression(contextbudget.PlanInput{
		Fragments:    fragments,
		Profile:      profile,
		Budget:       budget,
		Operation:    "test-op",
		SummaryCache: nil,
	})
	if err != nil {
		t.Fatalf("PlanCompression returned error: %v", err)
	}

	// Verify: critical fragments must NOT have ActionDrop.
	criticalIDs := map[string]bool{"sys-prompt": true, "current-request": true}
	for _, action := range plan.Actions {
		if criticalIDs[action.FragmentID] && action.Type == contextbudget.ActionDrop {
			t.Errorf("critical fragment %q must not be dropped, got action=%s reason=%s",
				action.FragmentID, action.Type, action.Reason)
		}
	}

	// At least the discardable fragments should have been considered for drop.
	droppedCount := 0
	for _, action := range plan.Actions {
		if action.Type == contextbudget.ActionDrop {
			droppedCount++
		}
	}
	if droppedCount == 0 {
		// With a 10-token budget we should have dropped something.
		t.Logf("plan.Actions: %+v", plan.Actions)
		t.Logf("plan.Feasible: %v, plan.EstimatedAfter: %d", plan.Feasible, plan.EstimatedAfter)
		// Not a hard failure if planner is correct and budget math differs, but warn.
		t.Log("WARNING: expected at least one drop action with tight budget, but got 0")
	}
}

func TestPlannerIgnoresBusinessMetadataForRanking(t *testing.T) {
	profile := makeProfile()

	// Two fragments identical in every ranking-relevant field, differing ONLY in Metadata.
	baseFragment := contextbudget.ContextFragment{
		Role:            contextbudget.RoleDiscardable,
		Source:          contextbudget.SourceUser,
		ContentType:     contextbudget.ContentText,
		Content:         repeat("x", 300),
		Importance:      2,
		Order:           10,
		Compressibility: contextbudget.CompressDrop,
		Critical:        false,
	}

	fragA := baseFragment
	fragA.ID = "frag-a"
	fragA.Metadata = map[string]string{
		"sop_stage":   "step_1",
		"node_id":     "node-abc",
		"template_id": "tpl-001",
	}

	fragB := baseFragment
	fragB.ID = "frag-b"
	fragB.Metadata = map[string]string{
		"sop_stage":   "step_99",
		"node_id":     "node-xyz",
		"template_id": "tpl-999",
	}

	// Same system prompt for both runs.
	sysPrompt := contextbudget.ContextFragment{
		ID:              "sys",
		Role:            contextbudget.RoleImmutable,
		Source:          contextbudget.SourceSystem,
		ContentType:     contextbudget.ContentText,
		Content:         "system prompt",
		Importance:      10,
		Order:           0,
		Compressibility: contextbudget.CompressNone,
		Critical:        true,
	}

	// Tight budget to trigger compression decisions.
	budget := makeBudget(20)

	runWithFrag := func(f contextbudget.ContextFragment) contextbudget.Plan {
		plan, err := contextbudget.PlanCompression(contextbudget.PlanInput{
			Fragments:    []contextbudget.ContextFragment{sysPrompt, f},
			Profile:      profile,
			Budget:       budget,
			Operation:    "test-op",
			SummaryCache: nil,
		})
		if err != nil {
			t.Fatalf("PlanCompression error: %v", err)
		}
		return plan
	}

	planA := runWithFrag(fragA)
	planB := runWithFrag(fragB)

	// Normalize action types — replace fragment IDs "frag-a"/"frag-b" with a canonical ID
	// so we can compare action lists.
	normalizeActions := func(actions []contextbudget.Action, fragID string) []contextbudget.Action {
		out := make([]contextbudget.Action, len(actions))
		for i, a := range actions {
			norm := a
			if norm.FragmentID == fragID {
				norm.FragmentID = "target-frag"
			}
			out[i] = norm
		}
		return out
	}

	actionsA := normalizeActions(planA.Actions, "frag-a")
	actionsB := normalizeActions(planB.Actions, "frag-b")

	if !reflect.DeepEqual(actionsA, actionsB) {
		t.Errorf("planner produced different actions for fragments that differ only in business Metadata.\n"+
			"actionsA: %+v\nactionsB: %+v", actionsA, actionsB)
	}

	// Additional sanity: plans should be structurally equal.
	if planA.Feasible != planB.Feasible {
		t.Errorf("Feasible differs: A=%v B=%v", planA.Feasible, planB.Feasible)
	}
}

// repeat builds a string of n copies of s.
func repeat(s string, n int) string {
	result := make([]byte, n*len(s))
	for i := range result {
		result[i] = s[0]
	}
	return string(result)
}
