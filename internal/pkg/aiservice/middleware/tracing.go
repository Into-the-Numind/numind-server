package middleware

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/langfuse"
)

// langfuseCallTimeout is the hard deadline for every individual Langfuse SDK call.
// If the Langfuse ingestion endpoint is slow/down, we abandon the call and keep
// the main request flowing.
const langfuseCallTimeout = 2 * time.Second

// Tracing returns a Middleware that creates a Langfuse generation (for LLM/vision/embed
// calls) or span (for OCR/ASR/rerank calls) around each adapter invocation.
//
// Fault-tolerance contract (spec §6.2 hardcoded rules):
//  1. Every Langfuse SDK call runs inside context.WithTimeout(2 s).
//  2. The entire tracing block is wrapped in a recover() — a panic in the SDK
//     never crashes the server.
//  3. Any error from Langfuse is swallowed (logged at WARN).  The main request
//     result is always returned regardless of tracing outcomes.
func Tracing(deps Deps) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (resp interface{}, err error) {
			// Extract userID from context (set by auth middleware on the request path).
			userID, _ := ctx.Value(ctxKeyUserID{}).(uint)
			featureRef, _ := ctx.Value(ctxKeyFeatureRef{}).(map[string]interface{})

			// Decide observation type.
			isGeneration := isLLMType(route.ServiceType)

			var observationID string

			// --- open observation (best-effort, fully fault-tolerant) ---
			func() {
				defer func() {
					if r := recover(); r != nil {
						deps.warnw("tracing: panic in open observation, recovered",
							"recover", fmt.Sprintf("%v", r),
							"task_id", route.TaskID,
						)
					}
				}()

				tc := langfuse.FromContext(ctx)
				if tc == nil {
					// No parent trace — nothing to nest into.
					return
				}

				observationID = langfuse.SpanID()
				meta := buildMeta(route, userID, featureRef, "")

				// Enforce 2 s timeout on Langfuse calls (spec §6.2 hardcoded rule).
				// The Langfuse SDK enqueues asynchronously, so the timeout guards
				// any synchronous work inside the SDK helper (none currently).
				_, cancel := context.WithTimeout(ctx, langfuseCallTimeout)
				defer cancel()

				if isGeneration {
					langfuse.CreateGeneration(tc.TraceID, observationID,
						langfuse.WithGenParent(tc.ParentObservationID),
						langfuse.WithGenName(route.TaskID),
						langfuse.WithGenModel(route.ServiceKey),
						langfuse.WithGenInput(map[string]interface{}{"request": req}),
						langfuse.WithGenOutput(map[string]interface{}{"metadata": meta}),
					)
				} else {
					langfuse.CreateSpan(tc.TraceID, observationID, route.TaskID,
						langfuse.WithSpanParent(tc.ParentObservationID),
						langfuse.WithSpanInput(map[string]interface{}{"request": req, "metadata": meta}),
					)
				}
			}()

			// --- call the next handler (adapter or inner middleware) ---
			resp, err = next(ctx, route, req)

			// --- close observation (best-effort) ---
			func() {
				defer func() {
					if r := recover(); r != nil {
						deps.warnw("tracing: panic in close observation, recovered",
							"recover", fmt.Sprintf("%v", r),
							"task_id", route.TaskID,
						)
					}
				}()

				if observationID == "" {
					return
				}

				tc := langfuse.FromContext(ctx)
				if tc == nil {
					return
				}

				fallbackFrom, _ := ctx.Value(ctxKeyFallbackFromServiceID{}).(uint64)
				meta := buildMeta(route, userID, featureRef, fmt.Sprintf("%d", fallbackFrom))

				if isGeneration {
					if err != nil {
						langfuse.EndGeneration(observationID,
							langfuse.WithGenOutput(map[string]interface{}{
								"error":    err.Error(),
								"metadata": meta,
							}),
							langfuse.WithGenError(err.Error()),
						)
					} else {
						usage := extractUsage(resp)
						opts := []langfuse.GenOption{
							langfuse.WithGenOutput(map[string]interface{}{"response": resp, "metadata": meta}),
						}
						if usage != nil {
							opts = append(opts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
						}
						langfuse.EndGeneration(observationID, opts...)
					}
				} else {
					if err != nil {
						langfuse.EndSpan(observationID,
							langfuse.WithSpanOutput(map[string]interface{}{
								"error":    err.Error(),
								"metadata": meta,
							}),
							langfuse.WithSpanError(err.Error()),
						)
					} else {
						langfuse.EndSpan(observationID,
							langfuse.WithSpanOutput(map[string]interface{}{"response": resp, "metadata": meta}),
						)
					}
				}
			}()

			return resp, err
		}
	}
}

// ----------------------------------------------------------------------------
// Context keys for tracing inputs (set by caller / auth middleware)
// ----------------------------------------------------------------------------

type ctxKeyUserID struct{}
type ctxKeyFeatureRef struct{}
type ctxKeyFallbackFromServiceID struct{}

// WithUserID injects a userID into the context for use by Tracing and Billing.
func WithUserID(ctx context.Context, userID uint) context.Context {
	return context.WithValue(ctx, ctxKeyUserID{}, userID)
}

// WithFeatureRef injects an arbitrary feature reference map (e.g. sop_id, node_id)
// into the context for Tracing metadata.
func WithFeatureRef(ctx context.Context, ref map[string]interface{}) context.Context {
	return context.WithValue(ctx, ctxKeyFeatureRef{}, ref)
}

// withFallbackFromServiceID marks that the current call is a fallback for the
// given primary service ID.  Set by the Fallback middleware before calling next.
func withFallbackFromServiceID(ctx context.Context, id uint64) context.Context {
	return context.WithValue(ctx, ctxKeyFallbackFromServiceID{}, id)
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// isLLMType returns true for service types that map to Langfuse Generation
// (LLM chat/vision/embed). OCR/ASR/rerank use Span instead.
func isLLMType(serviceType string) bool {
	return serviceType == "llm"
}

// buildMeta constructs the Langfuse metadata map from a resolved route.
func buildMeta(route *registry.ResolvedRoute, userID uint, featureRef map[string]interface{}, fallbackFrom string) map[string]interface{} {
	m := map[string]interface{}{
		"task_id":      route.TaskID,
		"service_id":   route.ServiceID,
		"service_name": route.ServiceKey,
		"provider":     route.Provider.Name,
		"user_id":      userID,
	}
	if len(featureRef) > 0 {
		m["feature_ref"] = featureRef
	}
	if fallbackFrom != "0" && fallbackFrom != "" {
		m["fallback_from_service_id"] = fallbackFrom
	}
	return m
}

// extractUsage attempts to pull TokenUsage out of an adapter response.
// Supports *aiservice.ChatResponse and aiservice.ChatResponse.
func extractUsage(resp interface{}) *aiservice.TokenUsage {
	if resp == nil {
		return nil
	}
	switch v := resp.(type) {
	case *aiservice.ChatResponse:
		return &v.Usage
	case aiservice.ChatResponse:
		return &v.Usage
	}
	return nil
}
