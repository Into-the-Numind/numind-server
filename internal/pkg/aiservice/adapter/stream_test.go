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

// TestRunOAIStream_TransportsCachedPromptTokens feeds a synthetic SSE stream
// whose usage trailer carries prompt_tokens_details.cached_tokens (the Batch A
// DeepSeek / GPT auto-prefix-cache wire path via DMXAPI) and asserts the
// terminal ChatChunk surfaces CachedPromptTokens on Usage.
func TestRunOAIStream_TransportsCachedPromptTokens(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"x","model":"deepseek-v3-2-251201","choices":[{"delta":{"content":"hi"}}]}`,
		``,
		`data: {"id":"x","model":"deepseek-v3-2-251201","choices":[{"finish_reason":"stop","delta":{}}]}`,
		``,
		`data: {"id":"x","model":"deepseek-v3-2-251201","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":768}}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	r := io.NopCloser(strings.NewReader(sse))
	ch := make(chan aiservice.ChatChunk, 16)

	go runOAIStream(r, ch, "dmxapi", "deepseek-v3-2-251201", nil)

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
		t.Fatal("terminal.Usage is nil; expected usage with cached tokens")
	}
	if terminal.Usage.CachedPromptTokens != 768 {
		t.Errorf("Usage.CachedPromptTokens = %d; want 768", terminal.Usage.CachedPromptTokens)
	}
	if terminal.Usage.PromptTokens != 1000 {
		t.Errorf("Usage.PromptTokens = %d; want 1000", terminal.Usage.PromptTokens)
	}
}

// TestRunOAIStream_NoCachedTokens asserts the zero-regression guarantee: when
// the provider sends no cache fields, the terminal Usage.CachedPromptTokens is 0.
func TestRunOAIStream_NoCachedTokens(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"x","model":"test-model","choices":[{"finish_reason":"stop","delta":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	r := io.NopCloser(strings.NewReader(sse))
	ch := make(chan aiservice.ChatChunk, 16)

	go runOAIStream(r, ch, "ali", "test-model", nil)

	var terminal aiservice.ChatChunk
	for c := range ch {
		if c.IsFinal {
			terminal = c
		}
	}
	if terminal.Usage == nil {
		t.Fatal("terminal.Usage is nil; want non-nil")
	}
	if terminal.Usage.CachedPromptTokens != 0 {
		t.Errorf("Usage.CachedPromptTokens = %d; want 0 (no cache fields)", terminal.Usage.CachedPromptTokens)
	}
}

// TestRunOAIStream_StreamsToolCallsAndReasoningDelta REPRODUCES the bug
// observed on dev 2026-05-28 (agent_run 48/54): when the LLM (deepseek-v4-pro
// via dmxapi) decides to invoke a tool (e.g. web_search), it emits a stream
// of:
//  1. reasoning_content deltas (thinking phase, 84 chunks)
//  2. tool_calls deltas with incremental function.arguments JSON fragments
//     (73 chunks)
//  3. finish_reason=tool_calls (with empty content/reasoning final chunk)
//  4. [DONE]
//
// Direct curl against dmxapi /chat/completions confirms the wire protocol.
// But the backend's runOAIStream:
//   - oaiStreamChunk.Choices[].Delta has NO ToolCalls field — JSON unmarshal
//     silently drops all 73 tool_calls deltas.
//   - emit logic `if delta != "" || reasoningDelta != ""` ignores tool_calls.
//   - ChatChunk has no ToolCalls field anyway.
//
// Result: eino agent receives 0 schema.Message chunks → react loop terminates
// immediately with step_count=0 → frontend shows an empty assistant bubble.
//
// Contract: runOAIStream MUST forward both reasoning_content deltas (as
// ReasoningDelta) AND accumulate tool_calls.function.arguments across chunks,
// emitting the completed ToolCall(s) on the terminal chunk (or whichever chunk
// carries finish_reason="tool_calls").
func TestRunOAIStream_StreamsToolCallsAndReasoningDelta(t *testing.T) {
	// Synthetic stream that mirrors the real dmxapi shape captured 2026-05-28.
	// Three reasoning chunks, then 4 tool_calls chunks (id+name in first,
	// arguments split across the rest), then finish_reason=tool_calls.
	sse := strings.Join([]string{
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":null,"reasoning_content":"用户"}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":null,"reasoning_content":"需要调研"}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"que"}}]}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ry\":\""}}]}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"教培 AI\"}"}}]}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":40,"total_tokens":140}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	r := io.NopCloser(strings.NewReader(sse))
	ch := make(chan aiservice.ChatChunk, 32)
	go runOAIStream(r, ch, "dmxapi", "deepseek-v4-pro", nil)

	var (
		reasoningChunks []aiservice.ChatChunk
		toolCallChunks  []aiservice.ChatChunk
		terminal        aiservice.ChatChunk
		sawTerminal     bool
	)
	for c := range ch {
		switch {
		case c.IsFinal:
			terminal = c
			sawTerminal = true
		case c.ReasoningDelta != "":
			reasoningChunks = append(reasoningChunks, c)
		case len(c.ToolCalls) > 0:
			toolCallChunks = append(toolCallChunks, c)
		}
	}

	if !sawTerminal {
		t.Fatal("no IsFinal=true chunk emitted")
	}
	if len(reasoningChunks) == 0 {
		t.Errorf("expected at least one reasoning_delta chunk, got 0")
	}

	// Either an interim chunk OR the terminal chunk MUST carry the assembled
	// tool_calls so the eino consumer can dispatch the function call.
	var assembled []aiservice.ToolCall
	if len(toolCallChunks) > 0 {
		assembled = toolCallChunks[len(toolCallChunks)-1].ToolCalls
	} else if len(terminal.ToolCalls) > 0 {
		assembled = terminal.ToolCalls
	}
	if len(assembled) == 0 {
		t.Fatalf("no tool_calls forwarded — runOAIStream dropped them silently (bug). reasoningChunks=%d toolCallChunks=%d terminal.ToolCalls=%v",
			len(reasoningChunks), len(toolCallChunks), terminal.ToolCalls)
	}
	if assembled[0].Function.Name != "web_search" {
		t.Errorf("tool_call[0].function.name = %q; want %q", assembled[0].Function.Name, "web_search")
	}
	if assembled[0].Function.Arguments != `{"query":"教培 AI"}` {
		t.Errorf("tool_call[0].function.arguments = %q; want %q", assembled[0].Function.Arguments, `{"query":"教培 AI"}`)
	}
	if assembled[0].ID != "call_abc" {
		t.Errorf("tool_call[0].id = %q; want %q", assembled[0].ID, "call_abc")
	}
	if terminal.FinishReason != "tool_calls" {
		t.Errorf("terminal.FinishReason = %q; want %q", terminal.FinishReason, "tool_calls")
	}
}

// TestRunOAIStream_EmitsToolCallArgsDelta verifies the BE-1 side-channel: every
// streamed function.arguments fragment is surfaced as a non-final ChatChunk with
// ToolCallArgsDelta populated (carrying id + name + the fragment), the
// concatenation of all ArgsDelta values equals the full arguments, AND the
// terminal chunk still carries the fully-assembled ToolCall (execution contract
// is untouched).
func TestRunOAIStream_EmitsToolCallArgsDelta(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"run_python","arguments":""}}]}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"code\":\""}}]}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"print(1)"}}]}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]}}]}`,
		``,
		`data: {"id":"r1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	r := io.NopCloser(strings.NewReader(sse))
	ch := make(chan aiservice.ChatChunk, 32)
	go runOAIStream(r, ch, "dmxapi", "deepseek-v4-pro", nil)

	var (
		argsDeltas  []aiservice.ToolCallArgsDelta
		terminal    aiservice.ChatChunk
		sawTerminal bool
	)
	for c := range ch {
		if c.IsFinal {
			terminal = c
			sawTerminal = true
			continue
		}
		if c.ToolCallArgsDelta != nil {
			argsDeltas = append(argsDeltas, *c.ToolCallArgsDelta)
		}
	}

	if !sawTerminal {
		t.Fatal("no IsFinal=true chunk emitted")
	}
	// 3 arguments fragments were sent (the first id+name chunk had empty args).
	if len(argsDeltas) != 3 {
		t.Fatalf("len(argsDeltas) = %d; want 3", len(argsDeltas))
	}
	var assembled strings.Builder
	for _, ad := range argsDeltas {
		assembled.WriteString(ad.ArgsDelta)
		if ad.FunctionName != "run_python" {
			t.Errorf("ArgsDelta.FunctionName = %q; want run_python", ad.FunctionName)
		}
		if ad.ToolCallID != "call_x" {
			t.Errorf("ArgsDelta.ToolCallID = %q; want call_x", ad.ToolCallID)
		}
	}
	if assembled.String() != `{"code":"print(1)"}` {
		t.Errorf("assembled args = %q; want %q", assembled.String(), `{"code":"print(1)"}`)
	}
	// Execution contract intact: terminal still carries the full ToolCall.
	if len(terminal.ToolCalls) != 1 {
		t.Fatalf("terminal.ToolCalls len = %d; want 1", len(terminal.ToolCalls))
	}
	if terminal.ToolCalls[0].Function.Arguments != `{"code":"print(1)"}` {
		t.Errorf("terminal full args = %q; want %q",
			terminal.ToolCalls[0].Function.Arguments, `{"code":"print(1)"}`)
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
