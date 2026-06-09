package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Mock pricing.ICalculator
// ----------------------------------------------------------------------------

// mockPricingCalc satisfies pricing.ICalculator for tests that need a
// deterministic CalculateCost result without hitting the real DB.
type mockPricingCalc struct {
	costCents int64
	err       error
}

func (m *mockPricingCalc) CalculateCost(_ context.Context, _, _, _ string, _, _ int) (int64, error) {
	return m.costCents, m.err
}

// CalculateCostWithCache satisfies pricing.ICalculator; the cached-token arg is
// ignored (this mock returns a fixed cost regardless of inputs).
func (m *mockPricingCalc) CalculateCostWithCache(_ context.Context, _, _, _ string, _, _, _ int) (int64, error) {
	return m.costCents, m.err
}

// ----------------------------------------------------------------------------
// Mock UsageStore
// ----------------------------------------------------------------------------

type mockUsageStore struct {
	records      []*model.UsageRecord
	err          error
	pricingRules map[string]*model.PricingRule // key: "serviceType|provider|model"
}

func (m *mockUsageStore) CreateUsageRecord(_ context.Context, r *model.UsageRecord) error {
	if m.err != nil {
		return m.err
	}
	m.records = append(m.records, r)
	return nil
}

func (m *mockUsageStore) GetPricingRule(_ context.Context, serviceType, provider, modelName string) (*model.PricingRule, error) {
	if m.pricingRules == nil {
		return nil, gorm.ErrRecordNotFound
	}
	key := serviceType + "|" + provider + "|" + modelName
	rule, ok := m.pricingRules[key]
	if !ok {
		// Try provider-level default (empty model).
		key = serviceType + "|" + provider + "|"
		rule, ok = m.pricingRules[key]
	}
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return rule, nil
}

// newMockStoreWithPricing creates a mockUsageStore pre-seeded with the three
// pricing rules used by the default test routes (llm, ocr, asr).
func newMockStoreWithPricing() *mockUsageStore {
	pcall := 0.03
	return &mockUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm|dmxapi|deepseek-v3": {
				BillingMode:        "flat",
				FlatUnit:           "call",
				InputPricePerMTok:  1.0,
				OutputPricePerMTok: 4.0,
				IsActive:           true,
			},
			"ocr|baidu|baidu-ocr-accurate": {
				BillingMode:  "flat",
				FlatUnit:     "call",
				PricePerCall: pcall,
				IsActive:     true,
			},
			"asr|funasr|funasr-paraformer": {
				BillingMode:  "flat",
				FlatUnit:     "call",
				PricePerCall: 0.002,
				IsActive:     true,
			},
		},
	}
}

// ----------------------------------------------------------------------------
// Fixed clock for deterministic tests
// ----------------------------------------------------------------------------

type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

// ----------------------------------------------------------------------------
// Route builders
// ----------------------------------------------------------------------------

func llmRoute() *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		TaskID:      "sop.text",
		ServiceID:   1,
		ServiceKey:  "deepseek-v3",
		ServiceType: "llm",
		Provider:    registry.ProviderInfo{Name: "dmxapi"},
		// Pricing amounts resolved from pricing_rule at call time (T-arch).
		Pricing: registry.PricingInfo{},
	}
}

func ocrRoute() *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		TaskID:      "ocr.baidu",
		ServiceID:   2,
		ServiceKey:  "baidu-ocr-accurate",
		ServiceType: "ocr",
		Provider:    registry.ProviderInfo{Name: "baidu"},
		// Pricing amounts resolved from pricing_rule at call time (T-arch).
		Pricing: registry.PricingInfo{},
	}
}

func asrRoute() *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		TaskID:      "monitor.transcribe",
		ServiceID:   3,
		ServiceKey:  "funasr-paraformer",
		ServiceType: "asr",
		Provider:    registry.ProviderInfo{Name: "funasr"},
		// Pricing amounts resolved from pricing_rule at call time (T-arch).
		Pricing: registry.PricingInfo{},
	}
}

// ----------------------------------------------------------------------------
// LLM billing tests
// ----------------------------------------------------------------------------

// TestBilling_LLM_Success verifies that a successful LLM call persists the
// correct token counts and pricing snapshot (resolved from pricing_rule mock).
func TestBilling_LLM_Success(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	chatResp := &aiservice.ChatResponse{
		Content: "answer",
		Usage: aiservice.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}

	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	})
	handler := mw(inner)

	ctx := WithUserID(context.Background(), 7)
	route := llmRoute()
	resp, err := handler(ctx, route, "req")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != chatResp {
		t.Errorf("response mismatch")
	}

	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(store.records))
	}
	r := store.records[0]
	// Check service type.
	if r.ServiceType != "llm" {
		t.Errorf("ServiceType: got %q, want %q", r.ServiceType, "llm")
	}
	// Check token counts.
	if r.PromptTokens != 100 {
		t.Errorf("PromptTokens: got %d, want 100", r.PromptTokens)
	}
	if r.CompletionTokens != 50 {
		t.Errorf("CompletionTokens: got %d, want 50", r.CompletionTokens)
	}
	// Check pricing snapshot.
	if r.PricingInputSnapshot == nil || *r.PricingInputSnapshot != 1.0 {
		t.Errorf("PricingInputSnapshot: got %v, want 1.0", r.PricingInputSnapshot)
	}
	if r.PricingOutputSnapshot == nil || *r.PricingOutputSnapshot != 4.0 {
		t.Errorf("PricingOutputSnapshot: got %v, want 4.0", r.PricingOutputSnapshot)
	}
	// Unit field.
	if r.Unit == nil || *r.Unit != "per_1m_tokens" {
		t.Errorf("Unit: got %v, want per_1m_tokens", r.Unit)
	}
	// task_id.
	if r.TaskID == nil || *r.TaskID != "sop.text" {
		t.Errorf("TaskID: got %v, want sop.text", r.TaskID)
	}
}

// TestBilling_LLM_AdapterError verifies that when the adapter fails, a usage
// record is still attempted (with zero tokens) and the error is propagated.
func TestBilling_LLM_AdapterError(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	adapterErr := errors.New("provider 503")
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return nil, adapterErr
	})
	handler := mw(inner)

	_, err := handler(WithUserID(context.Background(), 1), llmRoute(), nil)
	if !errors.Is(err, adapterErr) {
		t.Errorf("expected adapterErr, got %v", err)
	}
	// A record should still be written (zero tokens).
	if len(store.records) != 1 {
		t.Fatalf("expected 1 record even on error, got %d", len(store.records))
	}
}

// ----------------------------------------------------------------------------
// OCR billing tests
// ----------------------------------------------------------------------------

// TestBilling_OCR_Success verifies per_call billing for OCR.
func TestBilling_OCR_Success(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	ocrResp := &aiservice.OCRResponse{Text: "hello world"}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return ocrResp, nil
	})
	handler := mw(inner)

	_, err := handler(WithUserID(context.Background(), 5), ocrRoute(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(store.records))
	}
	r := store.records[0]
	if r.ServiceType != "ocr" {
		t.Errorf("ServiceType: got %q", r.ServiceType)
	}
	if r.CallCount == nil || *r.CallCount != 1 {
		t.Errorf("CallCount: got %v, want 1", r.CallCount)
	}
	if r.PricingCallSnapshot == nil {
		t.Errorf("PricingCallSnapshot should be set")
	}
	if r.Unit == nil || *r.Unit != "per_call" {
		t.Errorf("Unit: got %v, want per_call", r.Unit)
	}
}

// TestBilling_OCR_AdapterError verifies that OCR errors still write a record.
func TestBilling_OCR_AdapterError(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	inner := errHandler(errors.New("ocr timeout"))
	handler := mw(inner)

	_, err := handler(WithUserID(context.Background(), 3), ocrRoute(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(store.records) != 1 {
		t.Errorf("expected 1 record on error, got %d", len(store.records))
	}
}

// ----------------------------------------------------------------------------
// ASR billing tests
// ----------------------------------------------------------------------------

// TestBilling_ASR_Success verifies per_call billing for ASR and that DurationSeconds
// is written as business-analysis metadata (not used in billing calculation).
func TestBilling_ASR_Success(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	dur := 35.5
	asrResp := &aiservice.ASRResponse{Text: "transcribed", DurationSeconds: dur}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return asrResp, nil
	})
	handler := mw(inner)

	_, err := handler(WithUserID(context.Background(), 9), asrRoute(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(store.records))
	}
	r := store.records[0]
	if r.ServiceType != "asr" {
		t.Errorf("ServiceType: got %q", r.ServiceType)
	}
	if r.DurationSeconds == nil || *r.DurationSeconds != dur {
		t.Errorf("DurationSeconds: got %v, want %v", r.DurationSeconds, dur)
	}
	// ASR billing uses per_call via pricing_rule (no price_per_second column exists).
	// PricingCallSnapshot must be set; DurationSeconds is metadata-only.
	if r.PricingCallSnapshot == nil {
		t.Errorf("PricingCallSnapshot should be set for ASR (maps to per_call in pricing_rule)")
	}
	if r.Unit == nil || *r.Unit != "per_call" {
		t.Errorf("Unit: got %v, want per_call", r.Unit)
	}
}

// TestBilling_ASR_AdapterError verifies that ASR errors still write a record.
func TestBilling_ASR_AdapterError(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	inner := errHandler(errors.New("asr offline"))
	handler := mw(inner)

	_, err := handler(WithUserID(context.Background(), 2), asrRoute(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(store.records) != 1 {
		t.Errorf("expected 1 record on error, got %d", len(store.records))
	}
}

// TestBilling_StoreError_DoesNotBlockResponse ensures that a DB write failure
// does not prevent the caller from receiving the adapter's response.
func TestBilling_StoreError_DoesNotBlockResponse(t *testing.T) {
	store := &mockUsageStore{err: errors.New("db down")}
	logger := &mockLogger{}
	// No pricingRules → GetPricingRule returns ErrRecordNotFound (silent, non-fatal).
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: logger}
	mw := Billing(deps)

	expected := &aiservice.ChatResponse{Content: "result"}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return expected, nil
	})
	handler := mw(inner)

	resp, err := handler(WithUserID(context.Background(), 1), llmRoute(), nil)
	if err != nil {
		t.Fatalf("store error should not propagate: %v", err)
	}
	if resp != expected {
		t.Errorf("response mismatch")
	}
	if len(logger.errors) == 0 {
		t.Error("expected at least one error log for store failure")
	}
}

// mockBillingStore adapts mockUsageStore to the billing.UsageStore interface
// (which requires extra methods used by the async recorder path).
type mockBillingStore struct {
	mockUsageStore
	batches [][]*model.UsageRecord
}

func (m *mockBillingStore) CreateUsageRecords(_ context.Context, recs []*model.UsageRecord) error {
	cp := append([]*model.UsageRecord(nil), recs...)
	m.batches = append(m.batches, cp)
	return nil
}

// GetPricingRule on mockBillingStore satisfies billing.UsageStore (used by the
// async recorder path). Returns ErrRecordNotFound so calculateCostAndRevenue
// short-circuits to 0/0 without panicking on a nil rule.
func (m *mockBillingStore) GetPricingRule(_ context.Context, _, _, _ string) (*model.PricingRule, error) {
	return nil, gorm.ErrRecordNotFound
}

func (m *mockBillingStore) GetPricingRuleTiers(_ context.Context, _ uint) ([]model.PricingRuleTier, error) {
	return nil, nil
}

func (m *mockBillingStore) GetProviderModelID(_ context.Context, _, _ string) (string, error) {
	return "", errors.New("no provider model id (mock)")
}

// TestBilling_PrefersRecorderWhenInitialized verifies the unification contract:
// when billing.R is initialized, the middleware submits the prebuilt UsageRecord
// to the async batched recorder instead of calling deps.UsageStore directly.
// This guarantees LLM billing shares the same pipeline as VectorDB / COS billing.
func TestBilling_PrefersRecorderWhenInitialized(t *testing.T) {
	// Global singleton protection: reset on cleanup.
	prev := billing.R
	t.Cleanup(func() { billing.R = prev })

	recorderStore := &mockBillingStore{}
	billing.InitRecorder(recorderStore)
	t.Cleanup(func() {
		if billing.R != nil {
			billing.R.Stop()
		}
	})

	// The sync store should NOT receive any writes when R is initialized.
	syncStore := &mockUsageStore{}
	deps := Deps{UsageStore: syncStore, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	chatResp := &aiservice.ChatResponse{
		Content: "answer",
		Usage: aiservice.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	})
	handler := mw(inner)

	_, err := handler(WithUserID(context.Background(), 42), llmRoute(), "req")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Flush + verify: the sync store stays empty; the recorder received the record.
	billing.R.Stop()
	billing.R = nil // prevent second Stop via cleanup

	if len(syncStore.records) != 0 {
		t.Errorf("sync UsageStore should receive 0 records when recorder is active; got %d", len(syncStore.records))
	}
	// Batches may be split across flushes; flatten.
	var flat []*model.UsageRecord
	for _, b := range recorderStore.batches {
		flat = append(flat, b...)
	}
	if len(flat) != 1 {
		t.Fatalf("recorder store: expected 1 record, got %d", len(flat))
	}
	if flat[0].PromptTokens != 10 || flat[0].CompletionTokens != 20 {
		t.Errorf("record tokens mismatch: got %d/%d, want 10/20", flat[0].PromptTokens, flat[0].CompletionTokens)
	}
	if flat[0].TaskID == nil || *flat[0].TaskID != "sop.text" {
		t.Errorf("TaskID lost through recorder path")
	}
}

// TestBilling_StreamingInterruption_EstimatesFromPointer verifies that when a streaming
// call is interrupted (context cancelled after first chunk), the Billing middleware
// reads the accumulated byte count via the *int pointer and estimates completion tokens
// as ceil(bytes/2), and sets IsEstimated=true.
func TestBilling_StreamingInterruption_EstimatesFromPointer(t *testing.T) {
	// No pricing rules needed — the cancelled ctx will short-circuit the lookup,
	// leaving snapshots nil; this test only asserts on IsEstimated + token count.
	store := &mockUsageStore{}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	// Simulate: adapter accumulated 200 bytes before interruption.
	accLen := 200

	// Use a cancellable context so ctx.Err() != nil after cancel.
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithUserID(ctx, 10)
	ctx = withFirstChunkSent(ctx) // mark that at least one chunk was sent
	ctx = WithAccumulatedContentLen(ctx, &accLen)

	// Cancel immediately so ctx.Err() is non-nil when Billing reads it.
	cancel()

	// Adapter returns nil (streaming interrupted — no ChatResponse).
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return nil, nil
	})
	handler := mw(inner)

	_, _ = handler(ctx, llmRoute(), nil)

	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(store.records))
	}
	r := store.records[0]
	// ceil(200 / 2) = 100
	if r.CompletionTokens != 100 {
		t.Errorf("CompletionTokens: got %d, want 100 (estimated from 200 bytes)", r.CompletionTokens)
	}
	if !r.IsEstimated {
		t.Error("IsEstimated should be true for streaming interruption")
	}
}

// TestBilling_IsFallback_SetWhenFallbackCtxPresent verifies that UsageRecords created
// during a fallback call (ctxKeyFallbackFromServiceID set) have IsFallback=true.
func TestBilling_IsFallback_SetWhenFallbackCtxPresent(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	// Inject fallback-from context key (simulates Fallback middleware).
	ctx := WithUserID(context.Background(), 1)
	ctx = withFallbackFromServiceID(ctx, 99) // 99 = primary service ID

	chatResp := &aiservice.ChatResponse{
		Content: "fallback answer",
		Usage:   aiservice.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	})
	handler := mw(inner)

	_, err := handler(ctx, llmRoute(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(store.records))
	}
	if !store.records[0].IsFallback {
		t.Error("IsFallback should be true when ctxKeyFallbackFromServiceID is set")
	}
}

// TestBilling_ChatText_ServiceType verifies end-to-end wiring between the Billing
// middleware and classifyServiceType: passing a real aiservice.ChatRequest with
// text-only messages must produce a UsageRecord.ServiceType of "llm_chat".
// This exercises the actual call-site in buildBaseRecord, so if someone breaks
// the classifyServiceType call the test fails regardless of unit coverage.
func TestBilling_ChatText_ServiceType(t *testing.T) {
	store := &mockUsageStore{}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	chatResp := &aiservice.ChatResponse{
		Content: "text-only answer",
		Usage:   aiservice.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30},
	}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	})
	handler := mw(inner)

	// Pass a real ChatRequest with text-only messages (no image parts).
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role:    aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{Text: "What is the capital of France?"},
			},
		},
	}

	ctx := WithUserID(context.Background(), 11)
	_, err := handler(ctx, llmRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(store.records))
	}
	if got := store.records[0].ServiceType; got != "llm_chat" {
		t.Errorf("ServiceType: got %q, want %q", got, "llm_chat")
	}
	if store.records[0].PromptTokens != 20 {
		t.Errorf("PromptTokens: got %d, want 20", store.records[0].PromptTokens)
	}
}

// TestBilling_ChatVision_ServiceType verifies end-to-end wiring between the Billing
// middleware and classifyServiceType: passing a real aiservice.ChatRequest that
// contains an image_url part must produce a UsageRecord.ServiceType of "llm_vision".
// This guards the call site in buildBaseRecord — if classifyServiceType is
// accidentally removed or bypassed, the record will have "llm" (coarse fallback)
// instead of "llm_vision", and this test will fail.
func TestBilling_ChatVision_ServiceType(t *testing.T) {
	store := &mockUsageStore{}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	chatResp := &aiservice.ChatResponse{
		Content: "vision answer",
		Usage:   aiservice.TokenUsage{PromptTokens: 500, CompletionTokens: 100, TotalTokens: 600},
	}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	})
	handler := mw(inner)

	// Pass a real ChatRequest with an image_url part to trigger vision path.
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Parts: []aiservice.MessagePart{
						{Type: aiservice.MessagePartTypeText, Text: "Describe this image"},
						{
							Type:     aiservice.MessagePartTypeImageURL,
							ImageURL: &aiservice.ImageURL{URL: "https://example.com/photo.jpg"},
						},
					},
				},
			},
		},
	}

	ctx := WithUserID(context.Background(), 12)
	_, err := handler(ctx, llmRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(store.records))
	}
	if got := store.records[0].ServiceType; got != "llm_vision" {
		t.Errorf("ServiceType: got %q, want %q", got, "llm_vision")
	}
	if store.records[0].PromptTokens != 500 {
		t.Errorf("PromptTokens: got %d, want 500", store.records[0].PromptTokens)
	}
}

// TestBilling_AllBackendsUnconfigured_LogsError verifies graceful degradation
// when NEITHER billing.R nor deps.UsageStore is set — a pure-misconfig scenario.
// Silent billing drop in prod is a data loss event, so the middleware logs at
// ERROR (raised from WARN in A4 so alerting catches it).
//
// Guards billing.R to isolate from other tests in this package that may init it.
func TestBilling_AllBackendsUnconfigured_LogsError(t *testing.T) {
	prev := billing.R
	billing.R = nil
	t.Cleanup(func() { billing.R = prev })

	logger := &mockLogger{}
	deps := Deps{Logger: logger} // no UsageStore, no recorder
	mw := Billing(deps)

	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return "ok", nil
	})
	handler := mw(inner)

	resp, err := handler(context.Background(), llmRoute(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("unexpected response: %v", resp)
	}
	if len(logger.errors) == 0 {
		t.Error("expected error log when both recorder and UsageStore are unconfigured")
	}
}

// ============================================================================
// Phase 3 (T-arch): pricing snapshot populated from pricing_rule
// ============================================================================

// TestBuildBaseRecord_PricingSnapshot_Flat verifies that buildBaseRecord reads
// the pricing_rule table at call time and writes the correct snapshot fields for
// a flat-billed LLM service (billing_mode = "flat", input/output prices > 0).
func TestBuildBaseRecord_PricingSnapshot_Flat(t *testing.T) {
	inputPrice := 2.5
	outputPrice := 7.0
	store := &mockUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm|volc|my-model": {
				BillingMode:        "flat",
				FlatUnit:           "call",
				InputPricePerMTok:  inputPrice,
				OutputPricePerMTok: outputPrice,
				IsActive:           true,
			},
		},
	}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}

	route := &registry.ResolvedRoute{
		TaskID:      "sop.text",
		ServiceID:   10,
		ServiceKey:  "my-model",
		ServiceType: "llm",
		Provider:    registry.ProviderInfo{Name: "volc"},
	}
	r := buildBaseRecord(route, 1, deps, context.Background(), nil)

	if r.PricingInputSnapshot == nil || *r.PricingInputSnapshot != inputPrice {
		t.Errorf("PricingInputSnapshot: got %v, want %v", r.PricingInputSnapshot, inputPrice)
	}
	if r.PricingOutputSnapshot == nil || *r.PricingOutputSnapshot != outputPrice {
		t.Errorf("PricingOutputSnapshot: got %v, want %v", r.PricingOutputSnapshot, outputPrice)
	}
	if r.PricingCallSnapshot != nil {
		t.Errorf("PricingCallSnapshot: expected nil for per_1m_tokens billing, got %v", r.PricingCallSnapshot)
	}
	if r.Unit == nil || *r.Unit != "per_1m_tokens" {
		t.Errorf("Unit: got %v, want per_1m_tokens", r.Unit)
	}
}

// TestBuildBaseRecord_PricingSnapshot_Tiered verifies that buildBaseRecord leaves
// all pricing snapshot fields nil for a tiered_token service, because the actual
// cost is computed at flush time by calculateTieredCost in billing.Recorder.
func TestBuildBaseRecord_PricingSnapshot_Tiered(t *testing.T) {
	store := &mockUsageStore{
		pricingRules: map[string]*model.PricingRule{
			"llm|volc|tiered-model": {
				BillingMode:       "tiered_token",
				FlatUnit:          "call",
				InputPricePerMTok: 1.0, // ignored for snapshot when tiered
				IsActive:          true,
			},
		},
	}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}

	route := &registry.ResolvedRoute{
		TaskID:      "sop.text",
		ServiceID:   11,
		ServiceKey:  "tiered-model",
		ServiceType: "llm",
		Provider:    registry.ProviderInfo{Name: "volc"},
	}
	r := buildBaseRecord(route, 1, deps, context.Background(), nil)

	if r.PricingInputSnapshot != nil {
		t.Errorf("PricingInputSnapshot: expected nil for tiered_token billing, got %v", r.PricingInputSnapshot)
	}
	if r.PricingOutputSnapshot != nil {
		t.Errorf("PricingOutputSnapshot: expected nil for tiered_token billing, got %v", r.PricingOutputSnapshot)
	}
	if r.PricingCallSnapshot != nil {
		t.Errorf("PricingCallSnapshot: expected nil for tiered_token billing, got %v", r.PricingCallSnapshot)
	}
	// Unit is still set (derived from pricing_rule) even for tiered mode.
	if r.Unit == nil || *r.Unit != "per_1m_tokens" {
		t.Errorf("Unit: got %v, want per_1m_tokens", r.Unit)
	}
}

// ============================================================================
// Streaming billing tests (wrapStreamForBilling)
// ============================================================================

// streamHandler returns a Handler that emits the given chunks on a channel and
// returns that channel as the response (simulating a streaming LLM adapter).
func streamHandler(chunks []aiservice.ChatChunk) Handler {
	return func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		ch := make(chan aiservice.ChatChunk, len(chunks))
		for _, c := range chunks {
			ch <- c
		}
		close(ch)
		// Return as read-only channel — matches the real ChatStream return type.
		return (<-chan aiservice.ChatChunk)(ch), nil
	}
}

// TestBilling_Stream_CaptureFinalUsage verifies that token counts from the
// final chunk's Usage field are written into the UsageRecord.
func TestBilling_Stream_CaptureFinalUsage(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	chunks := []aiservice.ChatChunk{
		{Delta: "Hello", Index: 0},
		{Delta: " world", Index: 1},
		{
			Delta:        "",
			Index:        2,
			IsFinal:      true,
			FinishReason: "stop",
			Usage: &aiservice.TokenUsage{
				PromptTokens:     724,
				CompletionTokens: 23,
				TotalTokens:      747,
			},
		},
	}

	handler := mw(streamHandler(chunks))
	ctx := WithUserID(context.Background(), 7)

	resp, err := handler(ctx, llmRoute(), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain the wrapped channel (channel close is the synchronisation point:
	// the goroutine calls persistRecord then closes dst, so store.records is
	// visible without a race once the range exits).
	ch, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatalf("expected <-chan ChatChunk, got %T", resp)
	}
	for range ch {
	}

	if len(store.records) != 1 {
		t.Fatalf("expected 1 usage record after stream close, got %d", len(store.records))
	}
	r := store.records[0]
	if r.PromptTokens != 724 {
		t.Errorf("PromptTokens: got %d, want 724", r.PromptTokens)
	}
	if r.CompletionTokens != 23 {
		t.Errorf("CompletionTokens: got %d, want 23", r.CompletionTokens)
	}
	if r.TotalTokens != 747 {
		t.Errorf("TotalTokens: got %d, want 747", r.TotalTokens)
	}
	if r.IsEstimated {
		t.Error("IsEstimated should be false when final Usage chunk was received")
	}
}

// TestBilling_Stream_ForwardsAllChunks verifies the wrapper forwards every
// chunk from the inner channel to the caller without dropping or modifying them.
func TestBilling_Stream_ForwardsAllChunks(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	chunks := []aiservice.ChatChunk{
		{Delta: "chunk0", Index: 0},
		{Delta: "chunk1", Index: 1},
		{Delta: "chunk2", Index: 2, IsFinal: true, Usage: &aiservice.TokenUsage{
			PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
		}},
	}

	handler := mw(streamHandler(chunks))
	resp, err := handler(WithUserID(context.Background(), 1), llmRoute(), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatalf("expected <-chan ChatChunk, got %T", resp)
	}

	var received []aiservice.ChatChunk
	for c := range ch {
		received = append(received, c)
	}

	if len(received) != len(chunks) {
		t.Fatalf("forwarded %d chunks, want %d", len(received), len(chunks))
	}
	for i, want := range chunks {
		got := received[i]
		if got.Delta != want.Delta || got.Index != want.Index || got.IsFinal != want.IsFinal {
			t.Errorf("chunk[%d] mismatch: got %+v, want %+v", i, got, want)
		}
	}
}

// TestBilling_Stream_PersistsAfterClose verifies that the usage record is
// persisted exactly once after the inner channel closes.
func TestBilling_Stream_PersistsAfterClose(t *testing.T) {
	store := newMockStoreWithPricing()
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	chunks := []aiservice.ChatChunk{
		{Delta: "Hi", Index: 0},
		{Delta: "", Index: 1, IsFinal: true, Usage: &aiservice.TokenUsage{
			PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60,
		}},
	}

	handler := mw(streamHandler(chunks))
	resp, err := handler(WithUserID(context.Background(), 3), llmRoute(), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch := resp.(<-chan aiservice.ChatChunk)
	// Draining the wrapped channel until close is the synchronisation point:
	// the billing goroutine calls persistRecord and then closes the channel,
	// so after this loop returns, store.records is visible without a race.
	for range ch {
	}

	if len(store.records) != 1 {
		t.Fatalf("expected exactly 1 record after stream close, got %d", len(store.records))
	}
}

// TestBilling_Stream_InterruptionEstimatesTokens verifies that when the caller
// context is cancelled before the final Usage chunk arrives, the middleware
// falls back to char-count estimation and sets IsEstimated=true.
func TestBilling_Stream_InterruptionEstimatesTokens(t *testing.T) {
	store := &mockUsageStore{}
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}
	mw := Billing(deps)

	// Buffered inner channel: we'll close it after cancel to simulate the
	// provider side closing the stream once it notices the disconnection.
	innerCh := make(chan aiservice.ChatChunk, 1)
	slowHandler := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return (<-chan aiservice.ChatChunk)(innerCh), nil
	})

	// Accumulated byte count (200 bytes → estimated 100 tokens).
	accLen := 200
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithUserID(ctx, 5)
	ctx = withFirstChunkSent(ctx)
	ctx = WithAccumulatedContentLen(ctx, &accLen)

	handler := mw(slowHandler)
	resp, err := handler(ctx, llmRoute(), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch := resp.(<-chan aiservice.ChatChunk)

	// Send one non-final chunk (buffered, won't block).
	innerCh <- aiservice.ChatChunk{Delta: "Hello", Index: 0}

	// Cancel context so the wrapper goroutine takes the ctx.Done() path.
	cancel()

	// Close the inner channel so the wrapper goroutine's drain loop exits.
	close(innerCh)

	// Drain the wrapper channel — it will close once the goroutine exits.
	// Channel close is the synchronisation point: persistRecord completes
	// before close(dst), so store.records is visible without a race.
	for range ch {
	}

	if len(store.records) != 1 {
		t.Fatalf("expected 1 record after interruption, got %d", len(store.records))
	}
	r := store.records[0]
	if r.CompletionTokens != 100 {
		t.Errorf("CompletionTokens: got %d, want 100 (ceil(200/2))", r.CompletionTokens)
	}
	if !r.IsEstimated {
		t.Error("IsEstimated should be true for streaming interruption")
	}
}

// TestBuildBaseRecord_PricingSnapshot_NoMatch verifies that buildBaseRecord leaves
// all snapshot fields nil and Unit nil when no pricing_rule matches the route.
func TestBuildBaseRecord_PricingSnapshot_NoMatch(t *testing.T) {
	store := &mockUsageStore{} // no pricing rules configured
	deps := Deps{UsageStore: store, Clock: fixedClock{t: time.Now()}, Logger: &mockLogger{}}

	route := &registry.ResolvedRoute{
		TaskID:      "sop.text",
		ServiceID:   12,
		ServiceKey:  "unknown-model",
		ServiceType: "llm",
		Provider:    registry.ProviderInfo{Name: "volc"},
	}
	r := buildBaseRecord(route, 1, deps, context.Background(), nil)

	if r.PricingInputSnapshot != nil {
		t.Errorf("PricingInputSnapshot: expected nil on no-match, got %v", r.PricingInputSnapshot)
	}
	if r.PricingOutputSnapshot != nil {
		t.Errorf("PricingOutputSnapshot: expected nil on no-match, got %v", r.PricingOutputSnapshot)
	}
	if r.PricingCallSnapshot != nil {
		t.Errorf("PricingCallSnapshot: expected nil on no-match, got %v", r.PricingCallSnapshot)
	}
	if r.Unit != nil {
		t.Errorf("Unit: expected nil on no-match, got %v", r.Unit)
	}
}

// TestPopulateLLMUsage_TypedNilResponse_NoPanic is a regression test for the
// 2026-04-19 SalesRAG incident where ali adapter returned (nil, error) and
// the middleware panicked dereferencing chatResp.Usage on a typed-nil.
//
// Scenario: adapter returns (typed-nil *ChatResponse, non-nil error). Middleware
// must NOT panic and MUST leave token fields zero.
func TestPopulateLLMUsage_TypedNilResponse_NoPanic(t *testing.T) {
	r := &model.UsageRecord{}

	// Simulate Go typed-nil interface pattern:
	//   var cr *aiservice.ChatResponse // nil
	//   var resp interface{} = cr      // interface{} wrapping typed-nil — NOT == nil
	var cr *aiservice.ChatResponse //nolint:staticcheck
	var resp interface{} = cr      //nolint:staticcheck
	if resp == nil {               //nolint:staticcheck
		t.Fatal("sanity: typed-nil interface should not equal nil (Go semantics)")
	}

	// Must not panic.
	populateLLMUsage(r, resp, errors.New("ali.Chat: provider error: quota exceeded"), context.Background())

	// Token fields must stay zero (no response to read from).
	if r.PromptTokens != 0 || r.CompletionTokens != 0 || r.TotalTokens != 0 {
		t.Errorf("expected zero tokens on adapter error, got prompt=%d completion=%d total=%d",
			r.PromptTokens, r.CompletionTokens, r.TotalTokens)
	}
}

// TestAsChatResponse_TypedNilGuard verifies the typed-nil guard at the
// assertion layer (defensive second layer beside callErr check).
func TestAsChatResponse_TypedNilGuard(t *testing.T) {
	var cr *aiservice.ChatResponse // nil
	var resp interface{} = cr

	got, ok := asChatResponse(resp)
	if ok {
		t.Errorf("expected ok=false for typed-nil *ChatResponse, got ok=true")
	}
	if got != nil {
		t.Errorf("expected nil return for typed-nil *ChatResponse, got %v", got)
	}
}

// TestPopulateLLMUsage_TypedNilResponse_NoError_NoPanic covers the path where
// callErr IS nil but resp is still a typed-nil (e.g. a middleware transformation
// or retry layer that unwraps errors into an alternate success shape). The
// callErr != nil early-return does NOT fire here; the asChatResponse typed-nil
// guard is the only thing preventing panic on chatResp.Usage deref.
//
// Without this test, one of the two defensive fixes would be untested — and a
// future refactor removing the callErr early-return (thinking it's redundant)
// would silently re-open the panic.
func TestPopulateLLMUsage_TypedNilResponse_NoError_NoPanic(t *testing.T) {
	r := &model.UsageRecord{}

	var cr *aiservice.ChatResponse // nil
	var resp interface{} = cr

	// callErr == nil — early-return at top of populateLLMUsage does NOT fire.
	// The only remaining guard is asChatResponse's cr == nil check.
	populateLLMUsage(r, resp, nil, context.Background())

	// Token fields must stay zero (no response to read from).
	if r.PromptTokens != 0 || r.CompletionTokens != 0 || r.TotalTokens != 0 {
		t.Errorf("expected zero tokens on typed-nil resp with nil err, got prompt=%d completion=%d total=%d",
			r.PromptTokens, r.CompletionTokens, r.TotalTokens)
	}
}

// ============================================================================
// cost-calibration plumbing: finalCostHolder population (F-3 hotfix)
// ============================================================================

// TestBillingSetsFinalCostInHolderWhenPresent verifies that when a
// *finalCostHolder is present in ctx (injected by ContextBudgetCredits after
// Reserve), the Billing middleware's non-streaming path calls
// publishCostToHolder with the real pricing-rule cost and the holder is
// populated before the handler returns.
//
// This is the Billing side of the F-3 cost-calibration plumbing fix.
func TestBillingSetsFinalCostInHolderWhenPresent(t *testing.T) {
	store := &mockUsageStore{} // no pricing rules — we're using PricingCalc
	calc := &mockPricingCalc{costCents: 42}
	deps := Deps{
		UsageStore:  store,
		PricingCalc: calc,
		Clock:       fixedClock{t: time.Now()},
		Logger:      &mockLogger{},
	}
	mw := Billing(deps)

	chatResp := &aiservice.ChatResponse{
		Content: "answer",
		Usage: aiservice.TokenUsage{
			PromptTokens:     1000,
			CompletionTokens: 200,
			TotalTokens:      1200,
		},
	}
	inner := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return chatResp, nil
	})
	handler := mw(inner)

	// Pre-inject a finalCostHolder (simulates ContextBudgetCredits step 5b).
	holder := &finalCostHolder{}
	ctx := WithUserID(context.Background(), 5)
	ctx = withFinalCostHolder(ctx, holder)

	_, err := handler(ctx, llmRoute(), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The holder must be populated with the value returned by PricingCalc.
	gotCost, gotSet := holder.Get()
	if !gotSet {
		t.Error("finalCostHolder was not set (Set() was never called)")
	}
	if gotCost != 42 {
		t.Errorf("finalCostHolder cost = %d, want 42 (from mockPricingCalc)", gotCost)
	}
}

// TestBillingSetsFinalCostInHolder_StreamPath verifies that the streaming path
// also populates the finalCostHolder before forwarding the IsFinal chunk, so
// that the outer ContextBudgetCredits goroutine can read the real cost when it
// processes the same IsFinal event.
//
// Memory ordering guarantee: publishCostToHolder runs before the channel send
// (dst <- chunk); the ContextBudget goroutine reads after the channel receive —
// Go memory model channel synchronisation ensures the write is visible.
func TestBillingSetsFinalCostInHolder_StreamPath(t *testing.T) {
	store := &mockUsageStore{}
	calc := &mockPricingCalc{costCents: 77}
	deps := Deps{
		UsageStore:  store,
		PricingCalc: calc,
		Clock:       fixedClock{t: time.Now()},
		Logger:      &mockLogger{},
	}
	mw := Billing(deps)

	chunks := []aiservice.ChatChunk{
		{Delta: "Hello", Index: 0},
		{
			Delta:   "",
			Index:   1,
			IsFinal: true,
			Usage: &aiservice.TokenUsage{
				PromptTokens:     500,
				CompletionTokens: 100,
				TotalTokens:      600,
			},
		},
	}
	handler := mw(streamHandler(chunks))

	holder := &finalCostHolder{}
	ctx := WithUserID(context.Background(), 8)
	ctx = withFinalCostHolder(ctx, holder)

	resp, err := handler(ctx, llmRoute(), aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatalf("expected <-chan ChatChunk, got %T", resp)
	}

	// Drain the stream; channel close is the synchronisation point.
	var gotIFinal bool
	for c := range ch {
		if c.IsFinal {
			gotIFinal = true
		}
	}

	if !gotIFinal {
		t.Error("expected IsFinal chunk to be forwarded")
	}

	// After draining, holder must be populated (set before IsFinal was forwarded).
	gotCost, gotSet := holder.Get()
	if !gotSet {
		t.Error("finalCostHolder was not set (Set() was never called) on streaming path")
	}
	if gotCost != 77 {
		t.Errorf("finalCostHolder cost = %d, want 77 (streaming path)", gotCost)
	}
}

func (*mockPricingCalc) IsFreeModel(ctx context.Context, serviceType, provider, model string) (bool, error) {
	return false, nil
}
