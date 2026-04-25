package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/model"
)

// Billing returns a Middleware that writes a UsageRecord for every AI service
// call.  The record captures the pricing snapshot at call time and fills the
// usage fields appropriate for the service_type:
//   - llm  → tokens_input / tokens_output (from ChatResponse.Usage or final ChatChunk.Usage)
//   - ocr  → call_count = 1
//   - asr  → duration_seconds (from ASRResponse.DurationSeconds)
//
// Failure contract (spec §6.3):
//   - DB write failures are logged at ERROR but never propagated to the caller.
//   - The main request response is always returned regardless of billing outcome.
//
// Streaming:
//   - For streaming LLM calls, the response from next() is an <-chan ChatChunk.
//     The middleware wraps the channel and inspects each chunk; when the final
//     chunk (IsFinal=true) carries Usage, those token counts are written to the
//     record and the record is persisted after the inner channel closes.
//
// Streaming interruption:
//   - When the caller's context is cancelled after at least one chunk was sent,
//     the token count is estimated from the response content length (chars/2)
//     and IsEstimated is set to true.
func Billing(deps Deps) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (resp interface{}, err error) {
			userID, _ := ctx.Value(ctxKeyUserID{}).(uint)

			// Prepare the skeleton UsageRecord from route metadata + live pricing_rule lookup.
			// classifyServiceType is called inside buildBaseRecord (before the adapter call) so the
			// fine-grained service_type is stored in the record and used for the pricing_rule lookup.
			record := buildBaseRecord(route, userID, deps, ctx, req)

			// Call the next handler.
			resp, err = next(ctx, route, req)

			// Streaming LLM path: next() returned a read-only chunk channel.
			// Wrap the channel so we can observe the final Usage chunk and
			// persist the record after the stream completes.
			if ch, isStream := resp.(<-chan aiservice.ChatChunk); isStream {
				wrapped := wrapStreamForBilling(ctx, ch, record, route, userID, deps)
				return wrapped, err
			}

			// Non-streaming path (existing behaviour): populate + persist synchronously.
			populateUsage(ctx, record, record.ServiceType, resp, err)
			persistRecord(ctx, record, route, userID, deps)

			return resp, err
		}
	}
}

// wrapStreamForBilling wraps src to capture the final Usage chunk into record
// and persist the billing record once the inner channel closes.
// It forwards all chunks unmodified to the returned channel so downstream
// consumers (biz layer) see the identical stream.
func wrapStreamForBilling(
	ctx context.Context,
	src <-chan aiservice.ChatChunk,
	record *model.UsageRecord,
	route *registry.ResolvedRoute,
	userID uint,
	deps Deps,
) <-chan aiservice.ChatChunk {
	// Defensive guard: if src is typed-nil (upstream handler returned
	// `return typedNilChan, err` without nil-wrapping), ranging over it
	// blocks forever → goroutine leak per failed call. gateway.go:303-308
	// currently launders this via `return nil, err`, but a future refactor
	// there would silently break us. Short-circuit with a closed empty channel.
	if src == nil {
		persistRecord(ctx, record, route, userID, deps)
		closed := make(chan aiservice.ChatChunk)
		close(closed)
		return closed
	}

	// Buffer matches src so we don't add artificial back-pressure.
	dst := make(chan aiservice.ChatChunk, cap(src))
	go func() {
		var finalSeen bool
		for chunk := range src {
			// Capture token usage from the final chunk before forwarding.
			if chunk.IsFinal && chunk.Usage != nil {
				record.PromptTokens = chunk.Usage.PromptTokens
				record.CompletionTokens = chunk.Usage.CompletionTokens
				record.TotalTokens = chunk.Usage.TotalTokens
				record.ReasoningTokens = chunk.Usage.ReasoningTokens
				finalSeen = true
			}
			// Forward the chunk to the downstream consumer.
			// If the caller's context was already cancelled, drain src to
			// completion so the provider HTTP stream is not leaked, but stop
			// sending to dst (nobody is listening).
			select {
			case dst <- chunk:
			case <-ctx.Done():
				// Drain remaining src without blocking.
				for range src {
				}
				// Fall through to persist with whatever we have.
				goto persist
			}
		}
	persist:
		// src closed (or ctx cancelled). If we never saw a final Usage chunk,
		// fall back to the streaming-interruption char-count estimate.
		if !finalSeen {
			if firstChunkSent, _ := ctx.Value(ctxKeyFirstChunkSent{}).(bool); firstChunkSent {
				estimated := int(math.Ceil(float64(accumulatedContentLen(ctx)) / 2.0))
				record.CompletionTokens = estimated
				record.TotalTokens = record.PromptTokens + estimated
				record.IsEstimated = true
			}
		}
		// Persist the billing record before closing dst.
		// Closing dst is the synchronisation point for callers that drain the
		// channel and then read billing state (e.g. tests). persistRecord must
		// complete before close(dst) so that billing data is visible to any
		// code that synchronises on channel close.
		persistRecord(ctx, record, route, userID, deps)
		close(dst)
	}()
	return dst
}

// persistRecord submits record to the async recorder (billing.R) when
// initialised, or falls back to a synchronous UsageStore write.
// Errors are logged but never returned — billing failures must not affect the
// caller's response.
func persistRecord(ctx context.Context, record *model.UsageRecord, route *registry.ResolvedRoute, userID uint, deps Deps) {
	// Persist — unified with RecordCOS / RecordVectorDB on the async
	// batched recorder path. Submitting a Prebuilt record lets the
	// recorder skip its UsageEvent→UsageRecord mapping and only fill
	// in cost/revenue + CreatedAt.
	//
	// Fallback to deps.UsageStore.CreateUsageRecord (sync) when the
	// global recorder isn't initialised — this keeps unit tests that
	// inject a fake UsageStore working without needing a recorder.
	switch {
	case billing.R != nil:
		billing.R.Record(&billing.UsageEvent{Prebuilt: record})
	case deps.UsageStore != nil:
		if writeErr := deps.UsageStore.CreateUsageRecord(ctx, record); writeErr != nil {
			deps.errorw("billing: failed to write usage record",
				"task_id", route.TaskID,
				"service_id", route.ServiceID,
				"user_id", userID,
				"error", writeErr,
			)
		}
	default:
		// Prod misconfiguration: silently dropping billing is a data
		// loss event for operations. Emit at ERROR so alerting catches it.
		deps.errorw("billing: no recorder or UsageStore configured, skipping usage record",
			"task_id", route.TaskID,
		)
	}
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// classifyServiceType maps a raw AI request to the fine-grained service_type
// used by pricing_rule. This normalises the vocabulary drift between
// ai_service.service_type (coarse: llm/ocr/asr) and pricing_rule.service_type
// (fine: llm_chat/llm_vision/embedding/rerank/ocr/asr/...).
//
// Returns one of: llm_chat | llm_vision | embedding | rerank | ocr | asr
//
// Mapping rules:
//   - OCRRequest    → "ocr"
//   - ASRRequest    → "asr"
//   - EmbedRequest  → "embedding"
//   - RerankRequest → "rerank"
//   - ChatRequest with any image_url part in any message → "llm_vision"
//   - ChatRequest text-only → "llm_chat"
//
// Unknown request types fall back to fallbackServiceType (the coarse value
// from the registry entry). This is safe: if the coarse value already matches
// a pricing_rule row the calculation succeeds; if not, cost defaults to 0
// and a more specific mapping can be added later.
func classifyServiceType(req any, fallbackServiceType string) string {
	switch r := req.(type) {
	case aiservice.OCRRequest:
		return "ocr"
	case *aiservice.OCRRequest:
		if r != nil {
			return "ocr"
		}
	case aiservice.ASRRequest:
		return "asr"
	case *aiservice.ASRRequest:
		if r != nil {
			return "asr"
		}
	case aiservice.EmbedRequest:
		return "embedding"
	case *aiservice.EmbedRequest:
		if r != nil {
			return "embedding"
		}
	case aiservice.RerankRequest:
		return "rerank"
	case *aiservice.RerankRequest:
		if r != nil {
			return "rerank"
		}
	case aiservice.ChatRequest:
		return classifyChatRequest(r)
	case *aiservice.ChatRequest:
		if r != nil {
			return classifyChatRequest(*r)
		}
	}
	return fallbackServiceType
}

// classifyChatRequest returns "llm_vision" if any message contains an
// image_url part; otherwise "llm_chat".
func classifyChatRequest(r aiservice.ChatRequest) string {
	for _, msg := range r.Messages {
		for _, part := range msg.Content.Parts {
			if part.Type == aiservice.MessagePartTypeImageURL {
				return "llm_vision"
			}
		}
	}
	return "llm_chat"
}

// buildBaseRecord constructs a UsageRecord skeleton from route metadata.
// Usage fields (tokens, call_count, duration_seconds) are left at zero / nil
// until the response is available.
// IsFallback is set to true when ctx carries a non-zero ctxKeyFallbackFromServiceID,
// i.e. this call was triggered by the Fallback middleware as a backup for a failed primary.
//
// req is used by classifyServiceType to derive the fine-grained service_type
// (llm_chat/llm_vision/embedding/rerank/ocr/asr) that pricing_rule uses,
// normalising the vocabulary drift from ai_service.service_type (coarse: llm/ocr/asr).
//
// Pricing snapshots are populated by querying pricing_rule at call time.
// Pricing columns were removed from ai_service_route in T-arch; the inline lookup
// uses route.ProviderModelID (already resolved by the registry) as a fallback key,
// which is equivalent to billing.ResolvePricingRule without the extra DB round-trip.
func buildBaseRecord(route *registry.ResolvedRoute, userID uint, deps Deps, ctx context.Context, req interface{}) *model.UsageRecord {
	taskID := route.TaskID
	svcType := classifyServiceType(req, route.ServiceType)

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
		// Metadata is a json-typed column; MySQL rejects empty string, so
		// default to "{}" (valid JSON null-object). Consumers that want to
		// attach structured metadata overwrite this before the recorder flush.
		Metadata: "{}",
		// AI Service Manager extension fields.
		TaskID: &taskID,
	}

	// Populate pricing snapshots by reading pricing_rule at call time.
	// This replaces the old behaviour of reading dead columns from ai_service_route.
	if deps.UsageStore != nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()

		rule, err := deps.UsageStore.GetPricingRule(lookupCtx, svcType, route.Provider.Name, route.ServiceKey)

		// T-arch-prereq fixed the dev-DB so all active routes now have a matching
		// pricing_rule.  On ErrRecordNotFound we attempt a second lookup using the
		// provider_model_id (the identifier actually sent to the provider) as a
		// fallback key.  This handles the edge case where pricing_rule.model stores
		// a provider-native key rather than ai_service.model_key.  Using
		// route.ProviderModelID (already resolved by the registry) avoids an extra
		// DB round-trip compared to billing.ResolvePricingRule.
		if err != nil && isNotFoundErr(err) {
			rule, err = deps.UsageStore.GetPricingRule(lookupCtx, svcType, route.Provider.Name, route.ProviderModelID)
		}

		if err == nil && rule != nil {
			// Derive billing unit from the rule's flat_unit / billing_mode combination.
			// pricing_rule.flat_unit = "call" → per_call, "gb" → per_gb (not used here).
			// For token-based pricing we check input/output prices > 0.
			var unit string
			switch {
			case rule.BillingMode == "tiered_token" || rule.InputPricePerMTok > 0 || rule.OutputPricePerMTok > 0:
				unit = "per_1m_tokens"
			case rule.PricePerCall > 0:
				unit = "per_call"
			default:
				unit = rule.FlatUnit
			}
			r.Unit = &unit

			// Write pricing snapshots.  For tiered_token mode the actual cost is
			// computed at flush time by calculateTieredCost; snapshotting a single
			// tier range here would be misleading — leave all snapshots nil.
			if rule.BillingMode != "tiered_token" {
				switch unit {
				case "per_1m_tokens":
					if rule.InputPricePerMTok > 0 {
						r.PricingInputSnapshot = ptrFloat64(rule.InputPricePerMTok)
					}
					if rule.OutputPricePerMTok > 0 {
						r.PricingOutputSnapshot = ptrFloat64(rule.OutputPricePerMTok)
					}
				case "per_call":
					if rule.PricePerCall > 0 {
						r.PricingCallSnapshot = ptrFloat64(rule.PricePerCall)
					}
				}
			}
		}
		// On no match: leave all snapshots nil and Unit nil (0 cost; non-fatal).
	}

	r.CreatedAt = deps.clock().Now()

	// Merge context-budget metadata when ContextBudgetCredits middleware ran.
	// The budget metadata is attached by ContextBudgetCredits via withBudgetMetadata
	// before calling the inner chain so that Billing can attach event_id,
	// token_profile_id, and compression metrics to usage_record.metadata.
	if bm, ok := budgetMetadataFromCtx(ctx); ok {
		r.Metadata = mergeBudgetMetadata(r.Metadata, bm)
	}

	return r
}

// mergeBudgetMetadata merges the budget metadata fields into the existing
// metadata JSON string. Returns the merged JSON or the original on failure.
//
// Existing metadata is expected to be a valid JSON object (or "{}").
// The budget fields are merged at the top level (shallow merge).
func mergeBudgetMetadata(existing string, bm budgetMetadata) string {
	// Decode existing metadata object.
	existing = normalizeMetadataJSON(existing)
	var base map[string]interface{}
	if err := json.Unmarshal([]byte(existing), &base); err != nil {
		base = make(map[string]interface{})
	}

	// Merge budget fields (only non-zero values to avoid polluting records
	// where ContextBudgetCredits is a passthrough).
	if bm.EventID != 0 {
		base["context_budget_event_id"] = bm.EventID
	}
	if bm.TokenProfileID != 0 {
		base["token_profile_id"] = bm.TokenProfileID
	}
	if bm.SafeInputBudget != 0 {
		base["safe_input_budget"] = bm.SafeInputBudget
	}
	if bm.EstimatedPromptBefore != 0 {
		base["estimated_prompt_tokens_before"] = bm.EstimatedPromptBefore
	}
	if bm.EstimatedPromptAfter != 0 {
		base["estimated_prompt_tokens_after"] = bm.EstimatedPromptAfter
	}
	if bm.CompressionStatus != "" {
		base["compression_status"] = bm.CompressionStatus
	}

	merged, err := json.Marshal(base)
	if err != nil {
		return existing
	}
	return string(merged)
}

// normalizeMetadataJSON ensures the input is a parseable JSON object string.
// Returns "{}" when input is empty or not a JSON object.
func normalizeMetadataJSON(s string) string {
	if s == "" {
		return "{}"
	}
	return s
}

// isNotFoundErr reports whether err wraps gorm.ErrRecordNotFound.
// Uses errors.Is which handles both single-error wraps (Go 1.13+) and
// joined errors (Go 1.20+ errors.Join / Unwrap() []error).
func isNotFoundErr(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// populateUsage fills in the usage-specific fields of a UsageRecord once the
// response (or error) from the adapter is known.
//
// serviceType must be the fine-grained value from classifyServiceType
// (llm_chat/llm_vision/embedding/rerank/ocr/asr). The legacy coarse values
// "llm", "ocr", "asr" are also handled for backwards compatibility.
func populateUsage(ctx context.Context, r *model.UsageRecord, serviceType string, resp interface{}, callErr error) {
	switch serviceType {
	case "llm", "llm_chat", "llm_vision":
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
	// When the adapter returned an error, resp is a typed-nil *ChatResponse
	// (Go idiom: `return nil, err`). Skip usage extraction — there's no
	// response to read tokens from, and asChatResponse below would still
	// succeed on typed-nil then crash on chatResp.Usage dereference.
	if callErr != nil {
		return
	}

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
		// Typed-nil guard: an interface wrapping a nil concrete pointer is
		// NOT == nil above, but dereferencing fields on it panics.
		// This happens whenever an adapter returns (nil, err) — the caller
		// wraps the typed-nil *ChatResponse into an interface{} parameter.
		if cr == nil {
			return nil, false
		}
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
