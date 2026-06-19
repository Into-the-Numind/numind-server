package middleware

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
)

// ----------------------------------------------------------------------------
// T2 (native-cache-adapters): cache-CREATION token copy + RW cost wiring
//
// The Billing middleware must copy CacheCreationTokens from the provider Usage
// (chunk.Usage on the stream path, chatResp.Usage on the non-stream path) into
// record.CacheCreationTokens at the four explicit copy sites, and feed it into
// the cost path via CalculateCostWithCacheRW(..., cachedTokens, cacheWriteTokens).
//
// ZERO REGRESSION: every existing flow has CacheCreationTokens==0, so the spy
// observes cacheWriteTokens==0 and cost is byte-identical to today. A native
// Claude route that writes a non-zero creation count proves the value is
// carried via the EXPLICIT copy (not via billing.TokenUsage.Normalize(), which
// is a deliberate no-op for this field — finding #7).
// ----------------------------------------------------------------------------

// spyRWCalc is a pricing.ICalculator that records the two cache-bucket args it
// was last called with on CalculateCostWithCacheRW, so a test can prove the
// middleware copied record.CacheCreationTokens and passed it into the RW path.
type spyRWCalc struct {
	costCents int64

	rwCalled             bool
	lastCachedTokens     int
	lastCacheWriteTokens int
}

func (s *spyRWCalc) CalculateCost(_ context.Context, _, _, _ string, _, _ int) (int64, error) {
	return s.costCents, nil
}

func (s *spyRWCalc) IsFreeModel(_ context.Context, _, _, _ string) (bool, error) {
	return false, nil
}

// CalculateCostWithCache delegates to the RW form with cacheWriteTokens=0, so the
// spy still records the call (mirrors the production delegate discipline).
func (s *spyRWCalc) CalculateCostWithCache(ctx context.Context, st, p, m string, prompt, completion, cached int) (int64, error) {
	return s.CalculateCostWithCacheRW(ctx, st, p, m, prompt, completion, cached, 0)
}

func (s *spyRWCalc) CalculateCostWithCacheRW(_ context.Context, _, _, _ string, _, _, cached, cacheWrite int) (int64, error) {
	s.rwCalled = true
	s.lastCachedTokens = cached
	s.lastCacheWriteTokens = cacheWrite
	return s.costCents, nil
}

// TestBilling_NonStream_CopiesCacheCreationTokens proves the non-stream copy
// site (billing.go after :499) writes chatResp.Usage.CacheCreationTokens into
// record.CacheCreationTokens, and that publishCostToHolder feeds it into the RW
// cost path as cacheWriteTokens.
func TestBilling_NonStream_CopiesCacheCreationTokens(t *testing.T) {
	store := newMockStoreWithPricing()
	calc := &spyRWCalc{costCents: 42}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}, PricingCalc: calc}
	mw := Billing(deps)

	chatResp := &aiservice.ChatResponse{
		Content: "answer",
		Usage: aiservice.TokenUsage{
			PromptTokens:        1000,
			CompletionTokens:    50,
			TotalTokens:         1050,
			CachedPromptTokens:  200,
			CacheCreationTokens: 300,
		},
	}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	})
	handler := mw(inner)

	// Inject a finalCostHolder so publishCostToHolder actually invokes the calc
	// (publishCostToHolder is a no-op when no holder is present in ctx).
	holder := &finalCostHolder{}
	ctx := withFinalCostHolder(WithUserID(context.Background(), 7), holder)
	if _, err := handler(ctx, llmRoute(), "req"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(store.records))
	}
	r := store.records[0]
	if r.CacheCreationTokens != 300 {
		t.Errorf("record.CacheCreationTokens: got %d, want 300 (non-stream copy site missing)", r.CacheCreationTokens)
	}
	if r.CachedPromptTokens != 200 {
		t.Errorf("record.CachedPromptTokens: got %d, want 200", r.CachedPromptTokens)
	}
	// publishCostToHolder must route through the RW cost path with the creation count.
	if !calc.rwCalled {
		t.Fatalf("CalculateCostWithCacheRW was not called")
	}
	if calc.lastCacheWriteTokens != 300 {
		t.Errorf("RW cacheWriteTokens: got %d, want 300 (record.CacheCreationTokens not passed to ...RW)", calc.lastCacheWriteTokens)
	}
	if calc.lastCachedTokens != 200 {
		t.Errorf("RW cachedTokens: got %d, want 200", calc.lastCachedTokens)
	}
	// The spy's cost (42) must have landed in the holder via the RW path.
	if got, ok := holder.Get(); !ok || got != 42 {
		t.Errorf("holder cost: got (%d, %v), want (42, true)", got, ok)
	}
}

// TestBilling_Stream_CopiesCacheCreationTokens proves the stream copy site
// (billing.go after :113) writes chunk.Usage.CacheCreationTokens into
// record.CacheCreationTokens, and that the streaming publishCostToHolder feeds
// it into the RW cost path BEFORE the IsFinal chunk is forwarded.
func TestBilling_Stream_CopiesCacheCreationTokens(t *testing.T) {
	store := newMockStoreWithPricing()
	calc := &spyRWCalc{costCents: 99}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}, PricingCalc: calc}
	mw := Billing(deps)

	chunks := []aiservice.ChatChunk{
		{Delta: "Hello", Index: 0},
		{
			Delta:        "",
			Index:        1,
			IsFinal:      true,
			FinishReason: "stop",
			Usage: &aiservice.TokenUsage{
				PromptTokens:        1000,
				CompletionTokens:    23,
				TotalTokens:         1023,
				CachedPromptTokens:  100,
				CacheCreationTokens: 250,
			},
		},
	}

	handler := mw(streamHandler(chunks))
	holder := &finalCostHolder{}
	ctx := withFinalCostHolder(WithUserID(context.Background(), 7), holder)
	resp, err := handler(ctx, llmRoute(), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ch, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatalf("expected <-chan ChatChunk, got %T", resp)
	}
	for range ch { // channel close is the sync point: record is persisted before close.
	}

	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record after stream close, got %d", len(store.records))
	}
	r := store.records[0]
	if r.CacheCreationTokens != 250 {
		t.Errorf("record.CacheCreationTokens: got %d, want 250 (stream copy site missing)", r.CacheCreationTokens)
	}
	if r.CachedPromptTokens != 100 {
		t.Errorf("record.CachedPromptTokens: got %d, want 100", r.CachedPromptTokens)
	}
	if !calc.rwCalled {
		t.Fatalf("CalculateCostWithCacheRW was not called on the stream path")
	}
	if calc.lastCacheWriteTokens != 250 {
		t.Errorf("RW cacheWriteTokens: got %d, want 250 (record.CacheCreationTokens not passed to ...RW)", calc.lastCacheWriteTokens)
	}
	if calc.lastCachedTokens != 100 {
		t.Errorf("RW cachedTokens: got %d, want 100", calc.lastCachedTokens)
	}
	if got, ok := holder.Get(); !ok || got != 99 {
		t.Errorf("holder cost: got (%d, %v), want (99, true)", got, ok)
	}
}

// TestBilling_NonStream_ZeroCreation_ByteIdentical is the zero-regression control:
// every existing dmxapi OpenAI-compat call reports CacheCreationTokens==0, so the
// RW spy must observe cacheWriteTokens==0 (cost collapses to today's value).
func TestBilling_NonStream_ZeroCreation_ByteIdentical(t *testing.T) {
	store := newMockStoreWithPricing()
	calc := &spyRWCalc{costCents: 7}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}, PricingCalc: calc}
	mw := Billing(deps)

	chatResp := &aiservice.ChatResponse{
		Content: "answer",
		Usage: aiservice.TokenUsage{
			PromptTokens:     1000,
			CompletionTokens: 50,
			TotalTokens:      1050,
		},
	}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	})
	handler := mw(inner)

	holder := &finalCostHolder{}
	ctx := withFinalCostHolder(WithUserID(context.Background(), 7), holder)
	if _, err := handler(ctx, llmRoute(), "req"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(store.records))
	}
	if store.records[0].CacheCreationTokens != 0 {
		t.Errorf("record.CacheCreationTokens: got %d, want 0 (no creation reported)", store.records[0].CacheCreationTokens)
	}
	if !calc.rwCalled {
		t.Fatalf("CalculateCostWithCacheRW was not called")
	}
	if calc.lastCacheWriteTokens != 0 {
		t.Errorf("RW cacheWriteTokens: got %d, want 0 (zero-regression broken)", calc.lastCacheWriteTokens)
	}
}

// TestBillingTokenUsage_Normalize_DoesNotTouchCreation proves the value reaches
// the record via the EXPLICIT copy sites, NOT via billing.TokenUsage.Normalize()
// — which is a deliberate no-op for CacheCreationTokens (finding #7). Normalize
// must neither populate nor zero the field.
func TestBillingTokenUsage_Normalize_DoesNotTouchCreation(t *testing.T) {
	// Non-zero stays untouched.
	u := billing.TokenUsage{PromptTokens: 1000, CacheCreationTokens: 300}
	u.Normalize()
	if u.CacheCreationTokens != 300 {
		t.Errorf("Normalize altered CacheCreationTokens: got %d, want 300 (must be a no-op)", u.CacheCreationTokens)
	}
	// Zero stays zero — Normalize must not synthesize a creation count from any
	// nested/aliased field (there is none; Claude reports it top-level).
	z := billing.TokenUsage{
		PromptTokens:         1000,
		PromptCacheHitTokens: 200,
	}
	z.Normalize()
	if z.CacheCreationTokens != 0 {
		t.Errorf("Normalize synthesized CacheCreationTokens: got %d, want 0 (no nested source)", z.CacheCreationTokens)
	}
}
