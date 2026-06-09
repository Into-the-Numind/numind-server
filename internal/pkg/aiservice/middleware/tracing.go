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

			// --- inject budget metadata holder before calling next (F-5 fix) ---
			// ContextBudgetCredits is an inner middleware that appends to ctx via
			// context.WithValue. The resulting child ctx is only visible to further
			// inner middlewares (Billing, Retry, Adapter). We inject an empty
			// *budgetMetadataHolder here so ContextBudgetCredits can write the
			// populated budgetMetadata into it. Because we share the *pointer*
			// (not a value), our close paths below can read from it even though we
			// hold the original ctx.
			ctx, _ = withBudgetMetadataHolder(ctx)

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
					var lastTraceMeta *aiservice.TraceMetadata
					for chunk := range ch {
						if chunk.Usage != nil {
							lastUsage = chunk.Usage
						}
						if chunk.Model != "" {
							lastModel = chunk.Model
						}
						// TraceMetadata is only populated on the terminal chunk
						// (IsFinal=true) per aiservice.ChatChunk contract, but
						// we capture defensively on any chunk that carries it.
						if chunk.TraceMetadata != nil {
							lastTraceMeta = chunk.TraceMetadata
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
						mergeBudgetTracingMeta(ctx, meta)
						outputMap := safeOutput(nil, meta)
						mergeTraceMetadata(outputMap, lastTraceMeta)
						opts := []langfuse.GenOption{
							langfuse.WithGenOutput(outputMap),
						}
						// Dual-channel cached-token observability (gated on cache>0
						// inside the helper; no-cache events stay byte-identical).
						opts = appendCachedUsageGenOption(opts, outputMap, lastUsage)
						if lastModel != "" {
							opts = append(opts, langfuse.WithGenModel(lastModel))
						}
						langfuse.EndGeneration(tc.TraceID, observationID, opts...)
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
				mergeBudgetTracingMeta(ctx, meta)

				if isGeneration {
					if err != nil {
						langfuse.EndGeneration(tc.TraceID, observationID,
							langfuse.WithGenOutput(map[string]interface{}{
								"error":    err.Error(),
								"metadata": meta,
							}),
							langfuse.WithGenError(err.Error()),
						)
					} else {
						usage := extractUsage(resp)
						outputMap := safeOutput(resp, meta)
						// Merge adapter-resolved TraceMetadata (reasoning effort,
						// model family, temp override) into output.metadata. The
						// Langfuse SDK does not expose WithGenMetadata, so we ride
						// on the existing WithGenOutput(map{"metadata":...}) channel.
						if cr, ok := resp.(*aiservice.ChatResponse); ok && cr != nil && cr.TraceMetadata != nil {
							mergeTraceMetadata(outputMap, cr.TraceMetadata)
						} else if cr, ok := resp.(aiservice.ChatResponse); ok && cr.TraceMetadata != nil {
							mergeTraceMetadata(outputMap, cr.TraceMetadata)
						}
						opts := []langfuse.GenOption{
							langfuse.WithGenOutput(outputMap),
						}
						// Dual-channel cached-token observability (gated on cache>0
						// inside the helper; no-cache events stay byte-identical).
						opts = appendCachedUsageGenOption(opts, outputMap, usage)
						langfuse.EndGeneration(tc.TraceID, observationID, opts...)
					}
				} else {
					if err != nil {
						langfuse.EndSpan(tc.TraceID, observationID,
							langfuse.WithSpanOutput(map[string]interface{}{
								"error":    err.Error(),
								"metadata": meta,
							}),
							langfuse.WithSpanError(err.Error()),
						)
					} else {
						langfuse.EndSpan(tc.TraceID, observationID,
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

// appendCachedUsageGenOption appends the usage GenOption for a generation,
// dual-channel for prompt-cache observability (Batch A). It is the single shared
// implementation applied at BOTH usage-carrying EndGeneration sites (stream
// close + non-stream success) so the logic stays consistent.
//
//   - usage == nil ⇒ no option appended (matches the existing `if usage != nil`
//     guard; non-LLM / no-usage events are untouched).
//   - CachedPromptTokens == 0 ⇒ plain WithGenUsage (TODAY's exact behavior) and
//     outputMap is NOT mutated ⇒ non-cache events stay byte-identical.
//   - CachedPromptTokens > 0 ⇒ channel A (WithGenCachedUsage, typed usage field,
//     honored by Langfuse versions that parse it) AND channel B
//     (output.metadata.cached_input_tokens, always rendered by Langfuse v3).
//
// outputMap is the same map already passed to WithGenOutput at the call site;
// channel B only mutates it inside the cache>0 branch.
func appendCachedUsageGenOption(opts []langfuse.GenOption, outputMap map[string]interface{}, usage *aiservice.TokenUsage) []langfuse.GenOption {
	if usage == nil {
		return opts
	}
	if usage.CachedPromptTokens > 0 {
		// Channel A: typed usage field.
		opts = append(opts, langfuse.WithGenCachedUsage(usage.PromptTokens, usage.CompletionTokens, usage.CachedPromptTokens))
		// Channel B: output.metadata key (guaranteed visible on Langfuse v3).
		if outputMap != nil {
			if md, ok := outputMap["metadata"].(map[string]interface{}); ok && md != nil {
				md["cached_input_tokens"] = usage.CachedPromptTokens
			} else {
				outputMap["metadata"] = map[string]interface{}{"cached_input_tokens": usage.CachedPromptTokens}
			}
		}
		return opts
	}
	// No cache hit — emit today's exact event.
	return append(opts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
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

// mergeTraceMetadata merges adapter-resolved TraceMetadata fields into the
// "metadata" sub-map of a Langfuse output payload produced by safeOutput.
//
// The Langfuse Go SDK (internal/pkg/langfuse/helpers.go) does not expose a
// WithGenMetadata option nor an UpdateGeneration call, so we embed the
// resolved fields inside the output.metadata map the adapter already uses.
// Only non-zero fields are written — this keeps old traces (adapters that do
// not populate TraceMetadata) byte-identical.
//
// No-op when tm is nil, preserving backward compatibility with ali/volc
// adapters that do not populate TraceMetadata.
func mergeTraceMetadata(outputMap map[string]interface{}, tm *aiservice.TraceMetadata) {
	if tm == nil || outputMap == nil {
		return
	}
	meta, _ := outputMap["metadata"].(map[string]interface{})
	if meta == nil {
		meta = map[string]interface{}{}
	}
	if tm.ResolvedReasoningEffort != "" {
		meta["resolved_reasoning_effort"] = tm.ResolvedReasoningEffort
	}
	if tm.ResolvedModelFamily != "" {
		meta["resolved_model_family"] = tm.ResolvedModelFamily
	}
	if tm.TempOverridden {
		meta["temp_overridden"] = true
	}
	outputMap["metadata"] = meta
}

// mergeBudgetTracingMeta merges context-budget scalar IDs and counts into the
// Langfuse metadata map built by buildMeta. Called by both the streaming and
// non-streaming observation close paths so that generation metadata in Langfuse
// always includes context-budget observability fields (spec §11.1).
//
// Source priority (F-5 fix):
//  1. *budgetMetadataHolder injected by Tracing into the original ctx — this is
//     the post-mutation source of truth set by ContextBudgetCredits.
//  2. ctx-value path (withBudgetMetadata) — preserved for backward compatibility
//     with unit tests (e.g. tracing_context_budget_test.go) that inject the
//     metadata directly into ctx without wiring the full chain.
//
// Privacy contract (spec §11.3): only scalar IDs, token counts, and flag fields
// are written. Fragment content, rendered prompt text, and user data are NEVER
// included. The function is a no-op when no budgetMetadata was injected into ctx
// via either path (i.e., the ContextBudgetCredits middleware was bypassed or ran
// as passthrough).
func mergeBudgetTracingMeta(ctx context.Context, meta map[string]interface{}) {
	if meta == nil {
		return
	}

	// Prefer the holder (F-5 fix): the holder is the post-mutation value written
	// by ContextBudgetCredits into the *original* ctx that Tracing holds.
	var bm budgetMetadata
	if h, ok := budgetMetadataHolderFromCtx(ctx); ok {
		if hm, set := h.Get(); set {
			bm = hm
			goto merge
		}
	}

	// Fallback: ctx-value path written by withBudgetMetadata. Used by unit tests
	// and code paths that inject budgetMetadata directly into ctx.
	{
		ctxBM, ok := budgetMetadataFromCtx(ctx)
		if !ok {
			return
		}
		bm = ctxBM
	}

merge:
	// Only scalar IDs, counts, and flags — never prompt content.
	if bm.EventID != 0 {
		meta["context_budget_event_id"] = bm.EventID
	}
	if bm.ContextWindow != 0 {
		meta["context_window"] = bm.ContextWindow
	}
	if bm.MaxOutputTokens != 0 {
		meta["max_output_tokens"] = bm.MaxOutputTokens
	}
	if bm.ReservedOutputTokens != 0 {
		meta["reserved_output_tokens"] = bm.ReservedOutputTokens
	}
	if bm.SafeRatio != 0 {
		meta["safe_ratio"] = bm.SafeRatio
	}
	if bm.FixedOverheadTokens != 0 {
		meta["fixed_overhead_tokens"] = bm.FixedOverheadTokens
	}
	if bm.SafeInputBudget != 0 {
		meta["safe_input_budget"] = bm.SafeInputBudget
	}
	if bm.EstimatedPromptBefore != 0 {
		meta["estimated_before"] = bm.EstimatedPromptBefore
	}
	if bm.EstimatedPromptAfter != 0 {
		meta["estimated_after"] = bm.EstimatedPromptAfter
	}
	if len(bm.CompressionActions) > 0 {
		meta["compression_actions"] = bm.CompressionActions
	}
	if bm.DroppedFragmentCount != 0 {
		meta["dropped_fragment_count"] = bm.DroppedFragmentCount
	}
	if bm.SummarizedFragmentCount != 0 {
		meta["summarized_fragment_count"] = bm.SummarizedFragmentCount
	}
	if bm.CriticalFragmentCount != 0 {
		meta["critical_fragment_count"] = bm.CriticalFragmentCount
	}
	if bm.TokenProfileID != 0 {
		meta["token_profile_id"] = bm.TokenProfileID
	}
	// Boolean flags: write unconditionally only when true (zero value = false = omit).
	if bm.TokenProfileFallback {
		meta["token_profile_fallback"] = true
	}
	if bm.CalibrationSkipped {
		meta["calibration_skipped"] = true
	}
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
