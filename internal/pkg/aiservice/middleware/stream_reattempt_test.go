package middleware

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// ----------------------------------------------------------------------------
// T1 — streaming Retry behaviour lock
// ----------------------------------------------------------------------------

// countTerminals counts IsFinal chunks; the consumer must see exactly one.
func countTerminals(chunks []aiservice.ChatChunk) int {
	n := 0
	for _, c := range chunks {
		if c.IsFinal {
			n++
		}
	}
	return n
}

// TestRetryStream_NormalStreamPassthrough (AC9 zero-regression): a stream whose
// first chunk is content must pass through unchanged with no reattempt.
func TestRetryStream_NormalStreamPassthrough(t *testing.T) {
	mw := retryWithPolicy(zeroDelayPolicy())
	callCount := 0
	inner := countingStreamHandler(&callCount, func(_ int) []aiservice.ChatChunk {
		return successChunks("hello world")
	})
	resp, err := mw(inner)(context.Background(), buildTestRoute("llm"), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := collectStream(t, resp)
	if callCount != 1 {
		t.Errorf("normal stream must not reattempt: expected 1 call, got %d", callCount)
	}
	if concatDelta(got) != "hello world" {
		t.Errorf("content altered: got %q", concatDelta(got))
	}
	if countTerminals(got) != 1 || got[len(got)-1].Usage == nil {
		t.Errorf("terminal usage chunk not preserved: %+v", got)
	}
}

// TestRetryStream_PostContentErrorNoRetry (AC4): an error AFTER content was
// forwarded must not retry; the error terminal passes through.
func TestRetryStream_PostContentErrorNoRetry(t *testing.T) {
	mw := retryWithPolicy(zeroDelayPolicy())
	callCount := 0
	inner := countingStreamHandler(&callCount, func(_ int) []aiservice.ChatChunk {
		return []aiservice.ChatChunk{{Delta: "partial", Index: 0}, idleErrChunk()}
	})
	got := runStream(t, mw(inner))
	if callCount != 1 {
		t.Errorf("post-content error must not retry: expected 1 call, got %d", callCount)
	}
	if concatDelta(got) != "partial" {
		t.Errorf("expected forwarded content %q, got %q", "partial", concatDelta(got))
	}
	if got[len(got)-1].Err == nil {
		t.Errorf("post-content error terminal should pass through to consumer")
	}
}

// TestRetryStream_ReasoningDeltaCountsAsContent (P0-3): a thinking model that
// emits reasoning then stalls has already "started streaming" — no retry.
func TestRetryStream_ReasoningDeltaCountsAsContent(t *testing.T) {
	mw := retryWithPolicy(zeroDelayPolicy())
	callCount := 0
	inner := countingStreamHandler(&callCount, func(_ int) []aiservice.ChatChunk {
		return []aiservice.ChatChunk{{ReasoningDelta: "thinking...", Index: 0}, idleErrChunk()}
	})
	_ = runStream(t, mw(inner))
	if callCount != 1 {
		t.Errorf("error after reasoning delta must not retry (firstContentForwarded), got %d calls", callCount)
	}
}

// TestRetryStream_RetryExhaustedPropagatesSingleError (AC3 + P0-1): when both
// the initial attempt and the retry fail pre-content, the consumer sees exactly
// ONE terminal error chunk (so the outer Billing/ContextBudget wrappers refund
// exactly once).
func TestRetryStream_RetryExhaustedPropagatesSingleError(t *testing.T) {
	mw := retryWithPolicy(zeroDelayPolicy())
	callCount := 0
	inner := countingStreamHandler(&callCount, func(_ int) []aiservice.ChatChunk {
		return []aiservice.ChatChunk{idleErrChunk()}
	})
	got := runStream(t, mw(inner))
	if callCount != 2 {
		t.Errorf("expected 2 attempts (initial + 1 retry) then give up, got %d", callCount)
	}
	if countTerminals(got) != 1 {
		t.Errorf("expected exactly 1 terminal chunk to reach consumer, got %d (%+v)", countTerminals(got), got)
	}
	if len(got) == 0 || got[len(got)-1].Err == nil {
		t.Errorf("expected a final error terminal chunk after exhaustion")
	}
}

// TestRetryStream_SkipRetryNoStreamRetry (P0-4): when skip_retry is set (Fallback
// bypass), a streaming pre-content error must NOT trigger a same-provider retry.
func TestRetryStream_SkipRetryNoStreamRetry(t *testing.T) {
	mw := retryWithPolicy(zeroDelayPolicy())
	callCount := 0
	inner := countingStreamHandler(&callCount, func(_ int) []aiservice.ChatChunk {
		return []aiservice.ChatChunk{idleErrChunk()}
	})
	ctx := withSkipRetry(context.Background())
	resp, err := mw(inner)(ctx, buildTestRoute("llm"), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = collectStream(t, resp)
	if callCount != 1 {
		t.Errorf("skip_retry must not same-provider-retry streams: expected 1 call, got %d", callCount)
	}
}

// TestRetryStream_DrainsAbandonedAttempt (P0-2): the failed attempt's channel is
// fully drained so the producer goroutine / provider HTTP body is released; its
// stragglers must not leak to the consumer.
func TestRetryStream_DrainsAbandonedAttempt(t *testing.T) {
	mw := retryWithPolicy(zeroDelayPolicy())
	producerDone := make(chan struct{})
	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		call := callCount
		ch := make(chan aiservice.ChatChunk) // unbuffered → drain must actively consume
		go func() {
			defer close(ch)
			if call == 1 {
				ch <- idleErrChunk()
				ch <- aiservice.ChatChunk{Delta: "straggler"} // only delivered if drained
				close(producerDone)
				return
			}
			for _, c := range successChunks("ok") {
				ch <- c
			}
		}()
		return (<-chan aiservice.ChatChunk)(ch), nil
	})
	got := runStream(t, mw(inner))

	select {
	case <-producerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned attempt channel was not drained (producer blocked) — goroutine/HTTP leak")
	}
	if callCount != 2 {
		t.Errorf("expected 2 attempts, got %d", callCount)
	}
	if concatDelta(got) != "ok" {
		t.Errorf("expected retried content %q, got %q", "ok", concatDelta(got))
	}
	if strings.Contains(concatDelta(got), "straggler") {
		t.Error("abandoned attempt content leaked to consumer")
	}
}

// runStream invokes a streaming Handler with a default route and drains the
// resulting channel into a slice of chunks.
func runStream(t *testing.T, h Handler) []aiservice.ChatChunk {
	t.Helper()
	resp, err := h(context.Background(), buildTestRoute("llm"), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return collectStream(t, resp)
}
