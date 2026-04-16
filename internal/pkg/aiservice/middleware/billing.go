package middleware

import (
	"context"
	"math"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/model"
)

// Billing returns a Middleware that writes a UsageRecord for every AI service
// call.  The record captures the pricing snapshot at call time and fills the
// usage fields appropriate for the service_type:
//   - llm  → tokens_input / tokens_output (from ChatResponse.Usage)
//   - ocr  → call_count = 1
//   - asr  → duration_seconds (from ASRResponse.DurationSeconds)
//
// Failure contract (spec §6.3):
//   - DB write failures are logged at ERROR but never propagated to the caller.
//   - The main request response is always returned regardless of billing outcome.
//
// Streaming interruption:
//   - When the caller's context is cancelled after at least one chunk was sent,
//     the token count is estimated from the response content length (chars/2)
//     and IsEstimated is set to true.
func Billing(deps Deps) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (resp interface{}, err error) {
			userID, _ := ctx.Value(ctxKeyUserID{}).(uint)

			// Prepare the skeleton UsageRecord from the route's pricing snapshot.
			record := buildBaseRecord(route, userID, deps.clock(), ctx)

			// Call the next handler.
			resp, err = next(ctx, route, req)

			// Populate usage fields from the response.
			populateUsage(ctx, record, route.ServiceType, resp, err)

			// Persist — best-effort, non-blocking.
			if deps.UsageStore != nil {
				if writeErr := deps.UsageStore.CreateUsageRecord(ctx, record); writeErr != nil {
					deps.errorw("billing: failed to write usage record",
						"task_id", route.TaskID,
						"service_id", route.ServiceID,
						"user_id", userID,
						"error", writeErr,
					)
				}
			} else {
				deps.warnw("billing: no UsageStore configured, skipping usage record",
					"task_id", route.TaskID,
				)
			}

			return resp, err
		}
	}
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// buildBaseRecord constructs a UsageRecord skeleton from route metadata.
// Usage fields (tokens, call_count, duration_seconds) are left at zero / nil
// until the response is available.
// IsFallback is set to true when ctx carries a non-zero ctxKeyFallbackFromServiceID,
// i.e. this call was triggered by the Fallback middleware as a backup for a failed primary.
func buildBaseRecord(route *registry.ResolvedRoute, userID uint, clk Clock, ctx context.Context) *model.UsageRecord {
	taskID := route.TaskID
	unit := route.Pricing.Unit
	svcType := route.ServiceType

	isFallback := false
	if fallbackFrom, _ := ctx.Value(ctxKeyFallbackFromServiceID{}).(uint64); fallbackFrom != 0 {
		isFallback = true
	}

	r := &model.UsageRecord{
		UserID:      userID,
		ServiceType: svcType,
		Provider:    route.Provider.Name,
		Model:       route.ServiceKey,
		Operation:   route.TaskID,
		IsFallback:  isFallback,
		// AI Service Manager extension fields.
		TaskID: &taskID,
		Unit:   &unit,
	}

	// Populate pricing snapshots based on billing unit.
	switch unit {
	case "per_1m_tokens":
		r.PricingInputSnapshot = ptrFloat64(route.Pricing.InputPricePerMTok)
		r.PricingOutputSnapshot = ptrFloat64(route.Pricing.OutputPricePerMTok)
	case "per_call":
		if route.Pricing.PricePerCall != nil {
			r.PricingCallSnapshot = route.Pricing.PricePerCall
		}
	case "per_second":
		if route.Pricing.PricePerSecond != nil {
			r.PricingSecondSnapshot = route.Pricing.PricePerSecond
		}
	}

	r.CreatedAt = clk.Now()
	return r
}

// populateUsage fills in the usage-specific fields of a UsageRecord once the
// response (or error) from the adapter is known.
func populateUsage(ctx context.Context, r *model.UsageRecord, serviceType string, resp interface{}, callErr error) {
	switch serviceType {
	case "llm":
		populateLLMUsage(r, resp, callErr, ctx)
	case "ocr":
		c := 1
		r.CallCount = &c
	case "asr":
		if asr, ok := resp.(*aiservice.ASRResponse); ok && asr != nil {
			r.DurationSeconds = &asr.DurationSeconds
		} else if asr, ok := resp.(aiservice.ASRResponse); ok {
			r.DurationSeconds = &asr.DurationSeconds
		}
	}
}

// populateLLMUsage fills in token counts for LLM calls.
// On streaming interruption (context cancelled after first chunk), it falls
// back to character-count estimation (chars / 2) and sets IsEstimated = true.
func populateLLMUsage(r *model.UsageRecord, resp interface{}, callErr error, ctx context.Context) {
	// Non-streaming: extract from ChatResponse.
	if chatResp, ok := asChatResponse(resp); ok {
		r.PromptTokens = chatResp.Usage.PromptTokens
		r.CompletionTokens = chatResp.Usage.CompletionTokens
		r.TotalTokens = chatResp.Usage.TotalTokens
		r.ReasoningTokens = chatResp.Usage.ReasoningTokens
		return
	}

	// Streaming interruption: context cancelled after first chunk was sent.
	if ctx.Err() != nil {
		if firstChunkSent, _ := ctx.Value(ctxKeyFirstChunkSent{}).(bool); firstChunkSent {
			// Estimate from accumulated content length via pointer.
			estimated := int(math.Ceil(float64(accumulatedContentLen(ctx)) / 2.0))
			r.CompletionTokens = estimated
			r.TotalTokens = r.PromptTokens + estimated
			r.IsEstimated = true
		}
	}
}

// asChatResponse type-asserts resp to a ChatResponse (pointer or value).
func asChatResponse(resp interface{}) (*aiservice.ChatResponse, bool) {
	if resp == nil {
		return nil, false
	}
	if cr, ok := resp.(*aiservice.ChatResponse); ok {
		return cr, true
	}
	if cr, ok := resp.(aiservice.ChatResponse); ok {
		return &cr, true
	}
	return nil, false
}

// ptrFloat64 returns a pointer to a copy of v.
func ptrFloat64(v float64) *float64 {
	cp := v
	return &cp
}

// ----------------------------------------------------------------------------
// Additional context keys used by Billing for streaming
// ----------------------------------------------------------------------------

// Note: ctxKeyFirstChunkSent and WithFirstChunkSent are defined in retry.go
// (same package) because they are semantically a retry-layer concern.
// Billing reads ctxKeyFirstChunkSent to decide whether to estimate token counts
// on streaming interruption.

type ctxKeyAccumulatedContentLen struct{}

// WithAccumulatedContentLen stores a pointer to the caller's content-length counter
// for streaming estimation when the context is cancelled.
//
// Usage by adapter streaming wrappers (Task 5/6):
//
//	var n int
//	ctx = aiservice.WithAccumulatedContentLen(ctx, &n)
//	// inside chunk loop:
//	n += len(chunkContent)
//
// The Billing middleware reads *ptr when ctx.Done triggers to get the most
// up-to-date byte count. Passing a *int (not int) ensures mutations made by the
// adapter after this call are visible to the middleware.
func WithAccumulatedContentLen(ctx context.Context, lenPtr *int) context.Context {
	return context.WithValue(ctx, ctxKeyAccumulatedContentLen{}, lenPtr)
}

// accumulatedContentLen reads the current byte count from the pointer stored in ctx.
// Returns 0 when no pointer is present or the pointer is nil.
func accumulatedContentLen(ctx context.Context) int {
	if p, ok := ctx.Value(ctxKeyAccumulatedContentLen{}).(*int); ok && p != nil {
		return *p
	}
	return 0
}
