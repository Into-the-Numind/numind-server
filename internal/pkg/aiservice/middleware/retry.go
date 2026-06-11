package middleware

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
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
	jitter := time.Duration(rand.Int64N(int64(p.maxJitter())))
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

// ctxKeyFirstChunkSent is the context key that marks whether the first
// streaming chunk has been delivered to the caller.  It lives here (retry.go)
// because it is semantically a retry-layer concern: once the first chunk is
// sent, the response can no longer be retried (the stream has started).
// billing.go reads this key to decide whether to estimate token counts on
// streaming interruption.
type ctxKeyFirstChunkSent struct{}

// WithFirstChunkSent returns a derived context that signals the first
// streaming chunk has been delivered.  Exported so that streaming adapter
// wrappers (and billing.go) can mark and read this flag.
func WithFirstChunkSent(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyFirstChunkSent{}, true)
}

// withFirstChunkSent is the package-internal alias used by the Retry middleware.
func withFirstChunkSent(ctx context.Context) context.Context {
	return WithFirstChunkSent(ctx)
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
			// Skip retry when instructed (Fallback bypass). Passthrough for both
			// streaming and non-streaming calls — fallback candidates must not be
			// same-provider-retried (keeps total upstream calls ≤ 3, ADR P0-4).
			if shouldSkipRetry(ctx) {
				return next(ctx, route, req)
			}

			resp, err := next(ctx, route, req)

			// Streaming-aware retry (Part B): a successfully-established stream
			// returns (channel, nil); the failure arrives asynchronously as a
			// ChatChunk.Err terminal chunk that the synchronous logic below never
			// sees. Wrap the channel so a retryable error BEFORE the first content
			// chunk re-establishes the same-route stream once. Sits below Billing
			// and ContextBudgetCredits (Reserve) → the reattempt re-invokes only
			// the adapter, never a second Reserve / UsageRecord (ADR §billing).
			if ch, isStream := resp.(<-chan aiservice.ChatChunk); isStream && err == nil {
				return retryStream(ctx, route, req, ch, next, policy), nil
			}

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

// retryStream wraps a streaming response with a single same-route reattempt on a
// retryable error that occurs before the first content chunk. The reattempt
// re-invokes next (the adapter) on the SAME route — it does NOT re-enter the
// outer Billing / ContextBudgetCredits middlewares, so no second Reserve or
// UsageRecord is produced (ADR 0001 §billing). Once any content/reasoning chunk
// is forwarded, retries are disabled (handled inside wrapStreamWithReattempt).
func retryStream(
	ctx context.Context,
	route *registry.ResolvedRoute,
	req interface{},
	firstCh <-chan aiservice.ChatChunk,
	next Handler,
	policy RetryPolicy,
) <-chan aiservice.ChatChunk {
	attempted := false
	reattempt := func(rctx context.Context) (<-chan aiservice.ChatChunk, error, bool) {
		if attempted {
			return nil, nil, false // single retry budget
		}
		attempted = true
		log.Infow("aiservice: streaming retry (same provider) after retryable pre-first-chunk stream error",
			"task_id", route.TaskID, "service_id", route.ServiceID, "provider", route.Provider.Name)
		resp, err := next(rctx, route, req)
		if err != nil {
			return nil, err, true // start error → wrapper forwards the held error chunk
		}
		ch, ok := resp.(<-chan aiservice.ChatChunk)
		if !ok {
			return nil, nil, false
		}
		return ch, nil, true
	}
	// policy.retryDelay is called once (single retry budget). If the streaming
	// retry budget is ever raised >1, swap this for a per-attempt exponential
	// back-off — retryDelay() currently returns an independent jittered base
	// delay per call, not a growing one.
	return wrapStreamWithReattempt(ctx, firstCh, reattempt, policy.retryDelay)
}
