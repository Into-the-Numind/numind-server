package middleware

import (
	"context"

	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
)

// Fallback returns a Middleware that switches to the first available fallback
// service when the primary service fails after exhausting Retry attempts
// (spec §6.5).
//
// Contract:
//   - Fallback is only triggered after the primary service has failed with a
//     retryable error (ErrAIProviderTimeout or ErrAIProviderError).
//   - At most one fallback service is tried (no cascading fallbacks).
//   - The fallback service call has Retry disabled (skip_retry=true injected
//     into ctx) so the total upstream call count stays ≤ 3.
//   - If the fallback service also fails, ErrAIFallbackExhausted is returned.
//   - The Langfuse generation/span for the fallback call carries the metadata
//     key fallback_from_service_id (injected via ctx before calling next).
//
// When Deps.Resolver is nil the middleware becomes a transparent passthrough.
func Fallback(deps Deps) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error) {
			// Attempt the primary service path (which includes Retry inside the chain).
			resp, err := next(ctx, route, req)
			if err == nil {
				return resp, nil
			}

			// Only engage fallback for retryable (transient) provider errors.
			// Capability mismatches, context cancellation, etc. propagate directly.
			if !retryableError(err) {
				return resp, err
			}

			// No resolver — cannot look up fallbacks.
			if deps.Resolver == nil {
				return resp, err
			}

			// Fetch fallback routes from the Registry.
			_, fallbacks, resolveErr := deps.Resolver.ResolveTask(ctx, route.TaskID)
			if resolveErr != nil || len(fallbacks) == 0 {
				// No fallbacks configured — propagate the original error.
				return resp, err
			}

			// Take the highest-priority fallback (first in the ordered slice).
			// fallbacks[0] is the highest-priority backup (Task 3 ResolveTask sorts by priority ASC).
			fbRoute := fallbacks[0]

			// Log fallback trigger.
			deps.warnw("fallback: switching to fallback service",
				"task_id", route.TaskID,
				"primary_service_id", route.ServiceID,
				"fallback_service_id", fbRoute.ServiceID,
				"fallback_service_key", fbRoute.ServiceKey,
				"primary_error", err,
			)

			// Inject skip_retry + fallback metadata into ctx for the inner chain.
			fbCtx := withSkipRetry(ctx)
			fbCtx = withFallbackFromServiceID(fbCtx, route.ServiceID)

			fbResp, fbErr := next(fbCtx, &fbRoute, req)
			if fbErr != nil {
				// Both primary and fallback failed.
				deps.warnw("fallback: fallback service also failed",
					"task_id", route.TaskID,
					"fallback_service_id", fbRoute.ServiceID,
					"fallback_error", fbErr,
				)
				return nil, errno.ErrAIFallbackExhausted
			}

			return fbResp, nil
		}
	}
}
