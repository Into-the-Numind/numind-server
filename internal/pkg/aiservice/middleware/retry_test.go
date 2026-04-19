package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
)

// zeroDelayPolicy returns a RetryPolicy with zero delay for fast tests.
func zeroDelayPolicy() RetryPolicy {
	return RetryPolicy{
		BaseDelay: 1 * time.Microsecond,
		MaxJitter: 1 * time.Microsecond,
	}
}

// TestRetry_SuccessNoRetry verifies that a successful call is not retried.
func TestRetry_SuccessNoRetry(t *testing.T) {
	policy := zeroDelayPolicy()
	mw := retryWithPolicy(policy)

	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		return "ok", nil
	})
	handler := mw(inner)

	_, err := handler(context.Background(), buildTestRoute("llm"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

// TestRetry_OneRetryThenSuccess verifies that a single retryable failure followed
// by a success results in 2 total calls and returns the success response.
func TestRetry_OneRetryThenSuccess(t *testing.T) {
	policy := zeroDelayPolicy()
	mw := retryWithPolicy(policy)

	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		if callCount == 1 {
			return nil, errno.ErrAIProviderTimeout
		}
		return "recovered", nil
	})
	handler := mw(inner)

	resp, err := handler(context.Background(), buildTestRoute("llm"), nil)
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if resp != "recovered" {
		t.Errorf("expected %q, got %v", "recovered", resp)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (1 initial + 1 retry), got %d", callCount)
	}
}

// TestRetry_PersistentFailure verifies that after one retry (total 2 calls)
// the error is propagated unchanged.
func TestRetry_PersistentFailure(t *testing.T) {
	policy := zeroDelayPolicy()
	mw := retryWithPolicy(policy)

	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		return nil, errno.ErrAIProviderError
	})
	handler := mw(inner)

	_, err := handler(context.Background(), buildTestRoute("llm"), nil)
	if !errors.Is(err, errno.ErrAIProviderError) {
		t.Errorf("expected ErrAIProviderError, got %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected exactly 2 calls (initial + 1 retry), got %d", callCount)
	}
}

// TestRetry_NonRetryableError verifies that non-retryable errors (e.g.
// ErrAICapabilityMismatch) do not trigger a retry.
func TestRetry_NonRetryableError(t *testing.T) {
	policy := zeroDelayPolicy()
	mw := retryWithPolicy(policy)

	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		return nil, errno.ErrAICapabilityMismatch
	})
	handler := mw(inner)

	_, err := handler(context.Background(), buildTestRoute("llm"), nil)
	if !errors.Is(err, errno.ErrAICapabilityMismatch) {
		t.Errorf("expected ErrAICapabilityMismatch, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("non-retryable error should produce exactly 1 call, got %d", callCount)
	}
}

// TestRetry_SkipRetry_Passthrough verifies that the Fallback bypass (skip_retry=true
// in ctx) makes the Retry middleware a transparent passthrough.
func TestRetry_SkipRetry_Passthrough(t *testing.T) {
	policy := zeroDelayPolicy()
	mw := retryWithPolicy(policy)

	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		return nil, errno.ErrAIProviderTimeout
	})
	handler := mw(inner)

	ctx := withSkipRetry(context.Background())
	_, err := handler(ctx, buildTestRoute("llm"), nil)
	if !errors.Is(err, errno.ErrAIProviderTimeout) {
		t.Errorf("expected ErrAIProviderTimeout, got %v", err)
	}
	// With skip_retry, only 1 call should be made (no retry).
	if callCount != 1 {
		t.Errorf("skip_retry should suppress retry: expected 1 call, got %d", callCount)
	}
}

// TestRetry_StreamingFirstChunkSent verifies the hard rule:
// once the first streaming chunk has been sent (ctxKeyFirstChunkSent=true),
// subsequent failures must NOT trigger a retry.
func TestRetry_StreamingFirstChunkSent(t *testing.T) {
	policy := zeroDelayPolicy()
	mw := retryWithPolicy(policy)

	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		return nil, errno.ErrAIProviderError
	})
	handler := mw(inner)

	// Simulate: first chunk already delivered.
	ctx := withFirstChunkSent(context.Background())
	_, err := handler(ctx, buildTestRoute("llm"), nil)
	if !errors.Is(err, errno.ErrAIProviderError) {
		t.Errorf("expected ErrAIProviderError, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("post-first-chunk failure must not retry: expected 1 call, got %d", callCount)
	}
}

// TestRetry_ContextCancelled verifies that a context.Canceled error is not retried.
func TestRetry_ContextCancelled(t *testing.T) {
	policy := zeroDelayPolicy()
	mw := retryWithPolicy(policy)

	callCount := 0
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		callCount++
		return nil, context.Canceled
	})
	handler := mw(inner)

	_, err := handler(context.Background(), buildTestRoute("llm"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("context.Canceled must not retry: expected 1 call, got %d", callCount)
	}
}
