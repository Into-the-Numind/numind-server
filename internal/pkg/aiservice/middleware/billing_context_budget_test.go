package middleware

// billing_context_budget_test.go — Task 12 observability test for Billing middleware.
//
// Verifies that usage_record.metadata includes context-budget scalar IDs and counts
// but does NOT include fragment content or rendered prompt text (spec §11.2 + §11.3).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// TestBillingMetadataIncludesContextBudgetIDsWithoutPromptContent verifies that
// when ContextBudgetCredits injects budget metadata into ctx, the Billing
// middleware merges the scalar IDs and counts into usage_record.metadata, and
// that the merged metadata does NOT contain fragment content or prompt text.
//
// This test covers the spec §11.2 usage_record.metadata contract and the
// spec §11.3 privacy rule that forbids logging prompt / fragment content.
func TestBillingMetadataIncludesContextBudgetIDsWithoutPromptContent(t *testing.T) {
	store := &mockUsageStore{}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	// Inject budget metadata that a real ContextBudgetCredits run would inject.
	// FragmentContent is intentionally omitted from budgetMetadata (privacy rule).
	bm := budgetMetadata{
		EventID:                   42,
		TokenProfileID:            7,
		SafeInputBudget:           835638,
		EstimatedPromptBefore:     120000,
		EstimatedPromptAfter:      42000,
		EstimatedCompletionTokens: 16384,
		CompressionStatus:         "compressed",
		CompressionActions:        []string{"summarize", "reference"},
		ReservedOutputTokens:      16384,
		ReservationID:             0, // no reservation (legacy tier)
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
	ctx := WithUserID(context.Background(), 99)
	ctx = withBudgetMetadata(ctx, bm)

	chatResp := &aiservice.ChatResponse{
		Content: "answer",
		Usage: aiservice.TokenUsage{
			PromptTokens:     42000,
			CompletionTokens: 1000,
			TotalTokens:      43000,
		},
	}
	inner := func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	}
	handler := mw(Handler(inner))

	_, err := handler(ctx, llmRoute(), aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hello"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(store.records))
	}

	raw := store.records[0].Metadata
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("metadata is not valid JSON: %v — raw: %q", err, raw)
	}

	// --- Assert required scalar IDs are present ---
	requiredScalars := []struct {
		key  string
		want interface{}
	}{
		{"context_budget_event_id", float64(42)},
		{"token_profile_id", float64(7)},
		{"safe_input_budget", float64(835638)},
		{"estimated_prompt_tokens_before", float64(120000)},
		{"estimated_prompt_tokens_after", float64(42000)},
		{"estimated_completion_tokens", float64(16384)},
		{"reserved_output_tokens", float64(16384)},
		{"budget_policy_id", float64(5)},
		{"compression_status", "compressed"},
	}
	for _, sc := range requiredScalars {
		got, ok := meta[sc.key]
		if !ok {
			t.Errorf("metadata missing required key %q", sc.key)
			continue
		}
		if got != sc.want {
			t.Errorf("metadata[%q] = %v (%T), want %v (%T)", sc.key, got, got, sc.want, sc.want)
		}
	}

	// --- Assert compression_actions is a non-empty array ---
	actionsRaw, ok := meta["compression_actions"]
	if !ok {
		t.Error("metadata missing required key 'compression_actions'")
	} else {
		actions, ok := actionsRaw.([]interface{})
		if !ok {
			t.Errorf("compression_actions should be array, got %T: %v", actionsRaw, actionsRaw)
		} else if len(actions) == 0 {
			t.Error("compression_actions should be non-empty")
		}
	}

	// --- Privacy guard: metadata must NOT contain prompt text / fragment content ---
	// The budgetMetadata struct never carries Content strings; this assertion guards
	// against future regressions where someone inadvertently passes content through.
	prohibited := []string{
		"fragment_content",
		"prompt_text",
		"message_content",
		"raw_content",
		"content",
	}
	for _, key := range prohibited {
		if _, present := meta[key]; present {
			t.Errorf("metadata must NOT contain %q — privacy rule spec §11.3", key)
		}
	}

	// Verify the raw JSON string itself does not contain the literal request text
	// (defence-in-depth: guards against safeInput or other helpers leaking text).
	requestText := "hello"
	if strings.Contains(raw, requestText) {
		t.Errorf("metadata JSON must not contain prompt text %q — privacy rule spec §11.3", requestText)
	}
}
