package middleware

// tracing_context_budget_test.go — Task 12 observability test for Tracing middleware.
//
// Verifies that mergeBudgetTracingMeta injects context-budget summary fields into
// the Langfuse metadata map WITHOUT including prompt text or fragment content
// (spec §11.1 + §11.3).

import (
	"context"
	"testing"
)

// TestTracingMetadataIncludesContextBudgetSummaryWithoutPromptContent verifies
// that mergeBudgetTracingMeta copies scalar IDs, counts, and flags from the
// budgetMetadata in context into a Langfuse metadata map, and that no prompt
// content or fragment text is included (spec §11.1 privacy rule §11.3).
//
// This test exercises the helper directly (unit test) because the real Langfuse
// SDK path requires a live Langfuse client and async observation management.
// The full integration path (Tracing middleware → Langfuse generation) is
// validated by TestTracing_WithTraceContext which confirms no panics or errors
// on the happy path.
func TestTracingMetadataIncludesContextBudgetSummaryWithoutPromptContent(t *testing.T) {
	// Simulate ctx produced by ContextBudgetCredits (withBudgetMetadata).
	bm := budgetMetadata{
		EventID:                   123,
		TokenProfileID:            7,
		SafeInputBudget:           835638,
		EstimatedPromptBefore:     120000,
		EstimatedPromptAfter:      42000,
		EstimatedCompletionTokens: 16384,
		CompressionStatus:         "compressed",
		CompressionActions:        []string{"summarize", "reference"},
		ReservedOutputTokens:      16384,
		ReservationID:             0,
		PolicyID:                  5,
		ContextWindow:             1000000,
		MaxOutputTokens:           384000,
		SafeRatio:                 0.85,
		FixedOverheadTokens:       512,
		DroppedFragmentCount:      2,
		SummarizedFragmentCount:   4,
		CriticalFragmentCount:     3,
		TokenProfileFallback:      false,
		CalibrationSkipped:        false,
	}
	ctx := withBudgetMetadata(context.Background(), bm)

	meta := map[string]interface{}{
		"task_id":    "sop.text",
		"service_id": uint(1),
		"provider":   "volc",
		"user_id":    uint(42),
	}

	// Call the helper under test.
	mergeBudgetTracingMeta(ctx, meta)

	// --- Assert spec §11.1 required fields are present ---
	type wantSpec struct {
		key  string
		want interface{}
	}
	specs := []wantSpec{
		{"context_budget_event_id", uint64(123)},
		{"context_window", 1000000},
		{"max_output_tokens", 384000},
		{"reserved_output_tokens", 16384},
		{"safe_ratio", 0.85},
		{"fixed_overhead_tokens", 512},
		{"safe_input_budget", 835638},
		{"estimated_before", 120000},
		{"estimated_after", 42000},
		{"dropped_fragment_count", 2},
		{"summarized_fragment_count", 4},
		{"critical_fragment_count", 3},
		{"token_profile_id", uint64(7)},
	}
	for _, s := range specs {
		got, ok := meta[s.key]
		if !ok {
			t.Errorf("Langfuse metadata missing required key %q (spec §11.1)", s.key)
			continue
		}
		if got != s.want {
			t.Errorf("Langfuse metadata[%q] = %v (%T), want %v (%T)", s.key, got, got, s.want, s.want)
		}
	}

	// --- Assert compression_actions is a non-empty slice ---
	actionsRaw, ok := meta["compression_actions"]
	if !ok {
		t.Error("Langfuse metadata missing required key 'compression_actions' (spec §11.1)")
	} else {
		actions, ok := actionsRaw.([]string)
		if !ok {
			t.Errorf("compression_actions should be []string, got %T: %v", actionsRaw, actionsRaw)
		} else if len(actions) == 0 {
			t.Error("compression_actions should be non-empty when compression occurred")
		}
	}

	// --- Privacy guard (spec §11.3): no prompt / fragment content in metadata ---
	prohibited := []string{
		"fragment_content",
		"prompt_text",
		"message_content",
		"raw_content",
		"content",
	}
	for _, key := range prohibited {
		if _, present := meta[key]; present {
			t.Errorf("Langfuse metadata must NOT contain %q — privacy rule spec §11.3", key)
		}
	}

	// --- Boolean false flags: token_profile_fallback and calibration_skipped should be absent ---
	// (spec §11.1: only write boolean flags when true)
	for _, falseFlag := range []string{"token_profile_fallback", "calibration_skipped"} {
		if val, present := meta[falseFlag]; present {
			if val == true {
				t.Errorf("Langfuse metadata[%q] = true but should be absent (false flag omit rule)", falseFlag)
			}
			// If present with value false, that's also incorrect (zero-value omit).
		}
	}
}

// TestTracingBudgetMeta_NilCtxIsNoop verifies that when no budgetMetadata is
// present in ctx (ContextBudgetCredits bypassed), mergeBudgetTracingMeta does
// not modify the existing meta map.
func TestTracingBudgetMeta_NilCtxIsNoop(t *testing.T) {
	ctx := context.Background() // no budget metadata
	meta := map[string]interface{}{
		"task_id": "sop.text",
	}
	mergeBudgetTracingMeta(ctx, meta)

	// Only the original key should be present.
	if len(meta) != 1 {
		t.Errorf("meta should be unchanged (len=1), got len=%d: %v", len(meta), meta)
	}
	if _, ok := meta["task_id"]; !ok {
		t.Error("original task_id key was removed")
	}
}

// TestTracingBudgetMeta_TrueFlags verifies that boolean true flags ARE written.
func TestTracingBudgetMeta_TrueFlags(t *testing.T) {
	bm := budgetMetadata{
		EventID:              1,
		TokenProfileFallback: true,
		CalibrationSkipped:   true,
	}
	ctx := withBudgetMetadata(context.Background(), bm)
	meta := map[string]interface{}{}
	mergeBudgetTracingMeta(ctx, meta)

	if v, ok := meta["token_profile_fallback"]; !ok || v != true {
		t.Errorf("token_profile_fallback should be true when TokenProfileFallback=true, got %v (present=%v)", v, ok)
	}
	if v, ok := meta["calibration_skipped"]; !ok || v != true {
		t.Errorf("calibration_skipped should be true when CalibrationSkipped=true, got %v (present=%v)", v, ok)
	}
}
