package middleware

import (
	"context"
	"encoding/json"
	"fmt"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/langfuse"
)

// Tracing returns a Middleware that creates a Langfuse generation (for LLM/vision/embed
// calls) or span (for OCR/ASR/rerank calls) around each adapter invocation.
//
// Fault-tolerance contract (spec §6.2 hardcoded rules):
//  1. The entire tracing block is wrapped in a recover() — a panic in the SDK
//     never crashes the server.
//  2. Any error from Langfuse is swallowed (logged at WARN).  The main request
//     result is always returned regardless of tracing outcomes.
//
// Note: The Langfuse SDK enqueues observations asynchronously, so a synchronous
// timeout is not needed here. If the SDK adds synchronous flushing in the future,
// add a context.WithTimeout(ctx, 2s) around the SDK calls at that time.
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

				// Langfuse SDK is async enqueue; recover() guards against panics
				// from nil/internal errors. No synchronous timeout needed.
				if isGeneration {
					langfuse.CreateGeneration(tc.TraceID, observationID,
						langfuse.WithGenParent(tc.ParentObservationID),
						langfuse.WithGenName(route.TaskID),
						langfuse.WithGenModel(route.ServiceKey),
						langfuse.WithGenInput(safeInput(req)),
						langfuse.WithGenOutput(map[string]interface{}{"metadata": meta}),
					)
				} else {
					langfuse.CreateSpan(tc.TraceID, observationID, route.TaskID,
						langfuse.WithSpanParent(tc.ParentObservationID),
						langfuse.WithSpanInput(safeInput(req)),
					)
				}
			}()

			// --- call the next handler (adapter or inner middleware) ---
			resp, err = next(ctx, route, req)

			// --- For stream responses, wrap the channel to capture usage on the final chunk ---
			// next() returns immediately for streams — real usage data arrives in the last chunk.
			// We wrap the channel and close the Langfuse observation only after draining it.
			if ch, ok := resp.(<-chan aiservice.ChatChunk); ok && err == nil {
				wrappedCh := make(chan aiservice.ChatChunk, 1)
				go func() {
					defer close(wrappedCh)
					var lastUsage *aiservice.TokenUsage
					var lastModel string
					for chunk := range ch {
						if chunk.Usage != nil {
							lastUsage = chunk.Usage
						}
						if chunk.Model != "" {
							lastModel = chunk.Model
						}
						wrappedCh <- chunk
					}
					// Stream fully consumed — close the Langfuse generation with actual usage.
					func() {
						defer func() {
							if r := recover(); r != nil {
								deps.warnw("tracing: panic in stream close observation, recovered",
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
						opts := []langfuse.GenOption{
							langfuse.WithGenOutput(safeOutput(nil, meta)),
						}
						if lastUsage != nil {
							opts = append(opts, langfuse.WithGenUsage(lastUsage.PromptTokens, lastUsage.CompletionTokens))
						}
						if lastModel != "" {
							opts = append(opts, langfuse.WithGenModel(lastModel))
						}
						langfuse.EndGeneration(observationID, opts...)
					}()
				}()
				resp = (<-chan aiservice.ChatChunk)(wrappedCh)
				return resp, nil
			}

			// --- close observation (best-effort) for non-stream responses ---
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
							langfuse.WithGenOutput(safeOutput(resp, meta)),
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

// safeInput converts a request to a JSON-safe map for Langfuse input.
// Channels and other non-serializable types are excluded.
func safeInput(req interface{}) map[string]interface{} {
	if req == nil {
		return map[string]interface{}{}
	}
	// Try JSON marshal to check if it's safe; if not, return type name only
	data, err := json.Marshal(req)
	if err != nil {
		return map[string]interface{}{"type": fmt.Sprintf("%T", req), "note": "not serializable"}
	}
	var result map[string]interface{}
	if json.Unmarshal(data, &result) == nil {
		return result
	}
	return map[string]interface{}{"raw": string(data)}
}

// safeOutput converts a response to a JSON-safe map for Langfuse output.
// Channels (<-chan ChatChunk) are replaced with a placeholder.
func safeOutput(resp interface{}, meta map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{"metadata": meta}
	if resp == nil {
		return out
	}
	data, err := json.Marshal(resp)
	if err != nil {
		out["type"] = fmt.Sprintf("%T", resp)
		out["note"] = "not serializable (stream response)"
		return out
	}
	var parsed interface{}
	if json.Unmarshal(data, &parsed) == nil {
		out["response"] = parsed
	} else {
		out["raw"] = string(data)
	}
	return out
}

// buildMeta constructs the Langfuse metadata map from a resolved route.
// service_name prefers DisplayName (human-readable) and falls back to ServiceKey.
func buildMeta(route *registry.ResolvedRoute, userID uint, featureRef map[string]interface{}, fallbackFrom string) map[string]interface{} {
	serviceName := route.DisplayName
	if serviceName == "" {
		serviceName = route.ServiceKey
	}
	m := map[string]interface{}{
		"task_id":      route.TaskID,
		"service_id":   route.ServiceID,
		"service_name": serviceName,
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
