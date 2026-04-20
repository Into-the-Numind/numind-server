package adapter

import (
	"io"
	"strings"
	"testing"

	"numind-server/internal/pkg/aiservice"
)

// TestRunOAIStream_TransportsReasoningTokens feeds a synthetic SSE stream
// whose final usage chunk carries completion_tokens_details.reasoning_tokens
// (the OpenAI / Gemini / DeepSeek wire path) and asserts the terminal ChatChunk
// surfaces both the reasoning_tokens count on Usage and the adapter-provided
// TraceMetadata record.
func TestRunOAIStream_TransportsReasoningTokens(t *testing.T) {
	// SSE body with a content chunk, a finish_reason chunk, and a usage-only
	// trailer containing nested completion_tokens_details.reasoning_tokens=42.
	sse := strings.Join([]string{
		`data: {"id":"x","model":"test-model","choices":[{"delta":{"content":"hi"}}]}`,
		``,
		`data: {"id":"x","model":"test-model","choices":[{"finish_reason":"stop","delta":{}}]}`,
		``,
		`data: {"id":"x","model":"test-model","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":100,"total_tokens":110,"completion_tokens_details":{"reasoning_tokens":42}}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	r := io.NopCloser(strings.NewReader(sse))
	ch := make(chan aiservice.ChatChunk, 16)
	traceMeta := &aiservice.TraceMetadata{
		ResolvedReasoningEffort: "medium",
		ResolvedModelFamily:     "claude",
		TempOverridden:          true,
	}

	go runOAIStream(r, ch, "test-provider", "test-model", traceMeta)

	var terminal aiservice.ChatChunk
	sawTerminal := false
	for c := range ch {
		if c.IsFinal {
			terminal = c
			sawTerminal = true
		}
	}

	if !sawTerminal {
		t.Fatal("no IsFinal=true chunk emitted")
	}
	if terminal.Usage == nil {
		t.Fatal("terminal.Usage is nil; expected usage with reasoning tokens")
	}
	if terminal.Usage.ReasoningTokens != 42 {
		t.Errorf("Usage.ReasoningTokens = %d; want 42", terminal.Usage.ReasoningTokens)
	}
	if terminal.Usage.PromptTokens != 10 {
		t.Errorf("Usage.PromptTokens = %d; want 10", terminal.Usage.PromptTokens)
	}
	if terminal.Usage.CompletionTokens != 100 {
		t.Errorf("Usage.CompletionTokens = %d; want 100", terminal.Usage.CompletionTokens)
	}
	if terminal.TraceMetadata == nil {
		t.Fatal("terminal.TraceMetadata is nil; want non-nil")
	}
	if terminal.TraceMetadata.ResolvedReasoningEffort != "medium" {
		t.Errorf("TraceMetadata.ResolvedReasoningEffort = %q; want %q",
			terminal.TraceMetadata.ResolvedReasoningEffort, "medium")
	}
	if terminal.TraceMetadata.ResolvedModelFamily != "claude" {
		t.Errorf("TraceMetadata.ResolvedModelFamily = %q; want %q",
			terminal.TraceMetadata.ResolvedModelFamily, "claude")
	}
	if !terminal.TraceMetadata.TempOverridden {
		t.Errorf("TraceMetadata.TempOverridden = false; want true")
	}
	if terminal.FinishReason != "stop" {
		t.Errorf("FinishReason = %q; want %q", terminal.FinishReason, "stop")
	}
}

// TestRunOAIStream_NilTraceMeta verifies that callers who do not populate
// TraceMetadata (ali, volc) can safely pass nil. The terminal chunk must
// have TraceMetadata=nil and no panic occurs during stream processing.
func TestRunOAIStream_NilTraceMeta(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"x","model":"test-model","choices":[{"delta":{"content":"hi"}}]}`,
		``,
		`data: {"id":"x","model":"test-model","choices":[{"finish_reason":"stop","delta":{}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	r := io.NopCloser(strings.NewReader(sse))
	ch := make(chan aiservice.ChatChunk, 16)

	// Pass nil traceMeta — this is the ali/volc adapter path.
	go runOAIStream(r, ch, "ali", "test-model", nil)

	var terminal aiservice.ChatChunk
	sawTerminal := false
	for c := range ch {
		if c.IsFinal {
			terminal = c
			sawTerminal = true
		}
	}

	if !sawTerminal {
		t.Fatal("no IsFinal=true chunk emitted")
	}
	if terminal.TraceMetadata != nil {
		t.Errorf("terminal.TraceMetadata = %+v; want nil (caller passed nil)", terminal.TraceMetadata)
	}
	if terminal.Usage == nil || terminal.Usage.TotalTokens != 8 {
		t.Errorf("Usage = %+v; want TotalTokens=8", terminal.Usage)
	}
	// Legacy reasoning_tokens field: not present in this stream.
	if terminal.Usage != nil && terminal.Usage.ReasoningTokens != 0 {
		t.Errorf("Usage.ReasoningTokens = %d; want 0", terminal.Usage.ReasoningTokens)
	}
}
