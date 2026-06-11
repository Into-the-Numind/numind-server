package middleware

import (
	"context"
	"fmt"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
)

// ----------------------------------------------------------------------------
// Streaming test helpers (shared by stream_reattempt / streaming Retry / Fallback tests)
// ----------------------------------------------------------------------------

// countingStreamHandler returns a Handler that, on each invocation, emits the
// chunks produced by chunksFor(callNumber) on a fresh channel and returns that
// channel (simulating a streaming LLM adapter). *callCount is incremented per
// call so tests can assert how many upstream attempts were made.
func countingStreamHandler(callCount *int, chunksFor func(call int) []aiservice.ChatChunk) Handler {
	return func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		*callCount++
		call := *callCount
		chunks := chunksFor(call)
		ch := make(chan aiservice.ChatChunk, len(chunks)+1)
		go func() {
			defer close(ch)
			for _, c := range chunks {
				ch <- c
			}
		}()
		return (<-chan aiservice.ChatChunk)(ch), nil
	}
}

// idleErrChunk builds a terminal chunk mirroring what adapter.runOAIStream emits
// when the idle watchdog trips before any content: IsFinal + a retryable
// ErrAIProviderTimeout-wrapped error, with no Usage.
func idleErrChunk() aiservice.ChatChunk {
	return aiservice.ChatChunk{
		IsFinal:      true,
		FinishReason: "idle_timeout: no stream data",
		Err:          fmt.Errorf("aiservice stream idle timeout: %w", errno.ErrAIProviderTimeout),
	}
}

// successChunks builds a normal content stream ending in a usage terminal chunk.
func successChunks(text string) []aiservice.ChatChunk {
	return []aiservice.ChatChunk{
		{Delta: text, Index: 0},
		{
			IsFinal:      true,
			FinishReason: "stop",
			Usage:        &aiservice.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
}

// collectStream drains a streaming response into a slice of chunks. Fails the
// test if resp is not a <-chan aiservice.ChatChunk.
func collectStream(t *testing.T, resp interface{}) []aiservice.ChatChunk {
	t.Helper()
	ch, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatalf("expected <-chan aiservice.ChatChunk, got %T", resp)
	}
	var out []aiservice.ChatChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

// concatDelta joins the content deltas of a chunk slice.
func concatDelta(chunks []aiservice.ChatChunk) string {
	s := ""
	for _, c := range chunks {
		s += c.Delta
	}
	return s
}

// ----------------------------------------------------------------------------
// T0 — RED reproduction (Rule 11 first commit)
// ----------------------------------------------------------------------------

// TestRetry_Stream_RetriesIdleTimeoutBeforeFirstChunk reproduces the gap Part B
// fixes: a streaming attempt that fails with a retryable ErrAIProviderTimeout
// BEFORE any content chunk should be retried (handler invoked twice), and the
// downstream consumer should see the retry's successful content — not the error.
//
// On the current (pre-Part-B) code the Retry middleware passes the streaming
// channel through without inspecting the async ChatChunk.Err, so the handler is
// invoked exactly once and the consumer receives only the idle-timeout error
// chunk. This test therefore FAILS until the streaming-aware retry lands.
func TestRetry_Stream_RetriesIdleTimeoutBeforeFirstChunk(t *testing.T) {
	policy := zeroDelayPolicy()
	mw := retryWithPolicy(policy)

	callCount := 0
	inner := countingStreamHandler(&callCount, func(call int) []aiservice.ChatChunk {
		if call == 1 {
			return []aiservice.ChatChunk{idleErrChunk()} // first attempt stalls pre-content
		}
		return successChunks("hello") // retry succeeds
	})
	handler := mw(inner)

	resp, err := handler(context.Background(), buildTestRoute("llm"), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	got := collectStream(t, resp)

	if callCount != 2 {
		t.Errorf("expected 2 upstream attempts (1 initial + 1 retry), got %d", callCount)
	}
	if text := concatDelta(got); text != "hello" {
		t.Errorf("expected retried content %q, got %q", "hello", text)
	}
	// The consumer must NOT see the idle-timeout error after a successful retry.
	for _, c := range got {
		if c.IsFinal && c.Err != nil {
			t.Errorf("retry succeeded but consumer still saw error terminal chunk: %v", c.Err)
		}
	}
}
