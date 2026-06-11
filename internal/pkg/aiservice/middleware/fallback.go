package middleware

import (
	"context"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
)

// Fallback returns a Middleware that fails the request over to the configured
// fallback services when the primary service fails after exhausting Retry
// attempts (spec §6.5).
//
// Contract (rerank-routing T4 — multi-level cascade):
//   - Fallback is only triggered after the primary service has failed with a
//     retryable error (ErrAIProviderTimeout or ErrAIProviderError).
//   - ALL configured fallback services are tried in priority order (highest
//     priority first); the first success wins. Any candidate failure (retryable
//     or not) advances to the next candidate.
//   - Each fallback call has Retry disabled (skip_retry=true injected into ctx,
//     re-derived from the original ctx per candidate so it is not compounded),
//     so each candidate makes a single upstream attempt.
//   - If every fallback also fails, ErrAIFallbackExhausted is returned with the
//     list of tried service IDs in its message (provenance for debugging/billing).
//   - The Langfuse generation/span for each fallback call carries the metadata
//     key fallback_from_service_id = the PRIMARY service id (root cause).
//
// When Deps.Resolver is nil the middleware becomes a transparent passthrough.
func Fallback(deps Deps) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error) {
			// Attempt the primary service path (which includes Retry inside the chain).
			resp, err := next(ctx, route, req)

			// Streaming-aware fallback (Part B): a successfully-established stream
			// returns (channel, nil); the failure surfaces asynchronously as a
			// ChatChunk.Err terminal chunk that the synchronous cascade below never
			// sees. Wrap the channel so a retryable error BEFORE the first content
			// chunk cascades to the SAME MODEL's alternate-provider routes (e.g.
			// deepseek-v4-pro: aihubmix → dmxapi). This wrapper sits OUTSIDE
			// ContextBudgetCredits, so each alternate gets its own fresh Reserve
			// (correct for a different provider); the primary's reservation was
			// already refunded by ContextBudgetCredits when its error terminal
			// flowed up through it.
			if ch, isStream := resp.(<-chan aiservice.ChatChunk); isStream && err == nil {
				if deps.Resolver == nil {
					return ch, nil // no resolver → no fallback, transparent passthrough
				}
				return fallbackStream(ctx, route, req, ch, next, deps), nil
			}

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

			// Cascade through ALL fallbacks in priority order (rerank-routing T4).
			// fallbacks is ordered highest-priority-first. Previously only
			// fallbacks[0] was tried; configuring >1 backup silently ignored the
			// rest. Now each candidate is attempted until one succeeds.
			deps.warnw("fallback: engaging cascade",
				"task_id", route.TaskID,
				"primary_service_id", route.ServiceID,
				"primary_error", err,
				"candidates", len(fallbacks),
			)

			triedIDs := make([]uint64, 0, len(fallbacks))
			for i := range fallbacks {
				fbRoute := fallbacks[i]
				triedIDs = append(triedIDs, fbRoute.ServiceID)

				// Inject skip_retry + fallback metadata into ctx for the inner chain.
				// (Re-derived from the ORIGINAL ctx each iteration so skip_retry is
				// not compounded across candidates.)
				fbCtx := withSkipRetry(ctx)
				fbCtx = withFallbackFromServiceID(fbCtx, route.ServiceID)

				fbResp, fbErr := next(fbCtx, &fbRoute, req)
				if fbErr == nil {
					return fbResp, nil
				}
				// Any failure (retryable OR not, incl. capability mismatch) on a
				// last-resort fallback → try the next candidate.
				deps.warnw("fallback: candidate failed",
					"task_id", route.TaskID,
					"fallback_service_id", fbRoute.ServiceID,
					"fallback_service_key", fbRoute.ServiceKey,
					"fallback_error", fbErr,
				)
			}

			// All candidates failed — return with provenance for debugging/billing:
			// the primary error (root cause) + the fallback service IDs we tried.
			return nil, errno.ErrAIFallbackExhausted.SetMessage("所有 AI 服务（含 fallback）均不可用 (primary err: %v; tried %d fallback(s): %v)", err, len(triedIDs), triedIDs)
		}
	}
}

// fallbackStream wraps a streaming response so that a retryable error before the
// first content chunk cascades to the primary model's same-model
// alternate-provider routes (cross-provider streaming fallback). Each alternate
// is invoked with skip_retry (no same-provider stream retry — total upstream
// calls stay ≤ 3: primary attempt + primary retry + one alternate) and a
// fallback_from_service_id marker for tracing/billing provenance. Alternates are
// resolved lazily (only on the failover path) so the common success path takes
// no extra DB hit. Once any content chunk is forwarded, no fallback is attempted
// (handled inside wrapStreamWithReattempt).
func fallbackStream(
	ctx context.Context,
	route *registry.ResolvedRoute,
	req interface{},
	firstCh <-chan aiservice.ChatChunk,
	next Handler,
	deps Deps,
) <-chan aiservice.ChatChunk {
	var (
		alts     []registry.ResolvedRoute
		resolved bool
		idx      int
	)
	reattempt := func(rctx context.Context) (<-chan aiservice.ChatChunk, error, bool) {
		if !resolved {
			resolved = true
			a, e := deps.Resolver.ResolveModelAlternates(rctx, route.TaskID, route.ServiceID, route.Provider.ID)
			if e != nil {
				deps.warnw("fallback: ResolveModelAlternates failed (streaming)",
					"task_id", route.TaskID, "primary_service_id", route.ServiceID, "error", e)
				return nil, nil, false
			}
			alts = a
			if len(alts) > 0 {
				deps.warnw("fallback: engaging streaming cross-provider cascade",
					"task_id", route.TaskID, "primary_service_id", route.ServiceID,
					"from_provider", route.Provider.Name, "candidates", len(alts))
			}
		}
		// Cascade through alternates, skipping any that fail to establish a stream.
		for idx < len(alts) {
			alt := alts[idx]
			idx++
			// Re-derive from the ORIGINAL ctx each iteration so skip_retry is not
			// compounded across candidates.
			fbCtx := withSkipRetry(ctx)
			fbCtx = withFallbackFromServiceID(fbCtx, route.ServiceID)
			r2, e2 := next(fbCtx, &alt, req)
			if e2 != nil {
				deps.warnw("fallback: streaming candidate failed to start",
					"task_id", route.TaskID, "to_provider", alt.Provider.Name,
					"to_service_id", alt.ServiceID, "error", e2)
				continue
			}
			c2, ok := r2.(<-chan aiservice.ChatChunk)
			if !ok {
				continue
			}
			deps.warnw("fallback: streaming failover to alternate provider",
				"task_id", route.TaskID, "from_provider", route.Provider.Name,
				"to_provider", alt.Provider.Name, "to_service_id", alt.ServiceID)
			return c2, nil, true
		}
		return nil, nil, false // alternates exhausted → forward the held primary error
	}
	return wrapStreamWithReattempt(ctx, firstCh, reattempt, nil)
}
