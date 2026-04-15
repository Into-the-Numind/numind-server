package middleware

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
)

// RetryPolicy controls the retry behaviour.
type RetryPolicy struct {
	// BaseDelay is the initial back-off delay before the first retry.
	// Defaults to 200 ms when zero.
	BaseDelay time.Duration

	// MaxJitter is the upper bound of random jitter added to BaseDelay.
	// Defaults to BaseDelay when zero (i.e. up to 2× BaseDelay).
	MaxJitter time.Duration
}

const defaultBaseDelay = 200 * time.Millisecond

func (p RetryPolicy) baseDelay() time.Duration {
	if p.BaseDelay > 0 {
		return p.BaseDelay
	}
	return defaultBaseDelay
}

func (p RetryPolicy) maxJitter() time.Duration {
	if p.MaxJitter > 0 {
		return p.MaxJitter
	}
	return p.baseDelay()
}

// retryDelay returns the back-off duration for the first (and only) retry:
// baseDelay + random jitter in [0, maxJitter).
func (p RetryPolicy) retryDelay() time.Duration {
	jitter := time.Duration(rand.Int63n(int64(p.maxJitter())))
	return p.baseDelay() + jitter
}

// retryableError reports whether err is a transient error that should be retried.
// Non-retryable errors include: capability mismatches, 4xx client errors, and
// context cancellation.
func retryableError(err error) bool {
	if err == nil {
		return false
	}
	// Context cancelled / deadline exceeded — do not retry.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Capability mismatches are configuration errors — do not retry.
	if errors.Is(err, errno.ErrAICapabilityMismatch) {
		return false
	}
	// Provider timeout or 5xx / network error — retry.
	if errors.Is(err, errno.ErrAIProviderTimeout) || errors.Is(err, errno.ErrAIProviderError) {
		return true
	}
	return false
}

// ----------------------------------------------------------------------------
// Context keys
// ----------------------------------------------------------------------------

type ctxKeySkipRetry struct{}

// withSkipRetry injects the skip_retry flag so that the Retry middleware
// becomes a passthrough for a given call.  Used by Fallback to ensure the
// fallback service is called at most once (no recursive retry).
func withSkipRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeySkipRetry{}, true)
}

func shouldSkipRetry(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeySkipRetry{}).(bool)
	return v
}

// ----------------------------------------------------------------------------
// Middleware
// ----------------------------------------------------------------------------

// Retry returns a Middleware that re-invokes next at most once on a retryable
// error (spec §6.4: single retry, exponential back-off + jitter).
//
// Streaming constraint (hard rule, spec §6.4):
//   - Retry is only triggered before the first streaming chunk has been delivered.
//   - Once ctxKeyFirstChunkSent is true in the context, failures are propagated
//     directly without retrying.
//
// Fallback bypass:
//   - When ctxKeySkipRetry is present in ctx, Retry is a transparent passthrough.
//     The Fallback middleware injects this flag when invoking the fallback service
//     so that total upstream calls remain ≤ 3.
//
// Retry constructs the middleware using the default RetryPolicy (200 ms base
// delay + random jitter).  Tests should use retryWithPolicy directly to inject
// a zero-delay policy for speed.
func Retry(_ Deps) Middleware {
	return retryWithPolicy(RetryPolicy{})
}

// retryWithPolicy is the testable core of the Retry middleware.
func retryWithPolicy(policy RetryPolicy) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error) {
			// Skip retry when instructed (Fallback bypass or streaming post-first-chunk).
			if shouldSkipRetry(ctx) {
				return next(ctx, route, req)
			}

			resp, err := next(ctx, route, req)
			if err == nil {
				return resp, nil
			}

			// Do not retry if the first chunk has already been sent (streaming).
			if firstChunkSent, _ := ctx.Value(ctxKeyFirstChunkSent{}).(bool); firstChunkSent {
				return resp, err
			}

			if !retryableError(err) {
				return resp, err
			}

			// Perform one retry after a back-off delay.
			delay := policy.retryDelay()
			select {
			case <-ctx.Done():
				return resp, ctx.Err()
			case <-time.After(delay):
			}

			return next(ctx, route, req)
		}
	}
}
