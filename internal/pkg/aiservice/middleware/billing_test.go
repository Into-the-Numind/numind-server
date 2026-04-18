package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Mock UsageStore
// ----------------------------------------------------------------------------

type mockUsageStore struct {
	records []*model.UsageRecord
	err     error
}

func (m *mockUsageStore) CreateUsageRecord(_ context.Context, r *model.UsageRecord) error {
	if m.err != nil {
		return m.err
	}
	m.records = append(m.records, r)
	return nil
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
		Pricing: registry.PricingInfo{
			Unit:               "per_1m_tokens",
			InputPricePerMTok:  1.0,
			OutputPricePerMTok: 4.0,
		},
	}
}

func ocrRoute() *registry.ResolvedRoute {
	pc := 0.03
	return &registry.ResolvedRoute{
		TaskID:      "ocr.baidu",
		ServiceID:   2,
		ServiceKey:  "baidu-ocr-accurate",
		ServiceType: "ocr",
		Provider:    registry.ProviderInfo{Name: "baidu"},
		Pricing: registry.PricingInfo{
			Unit:         "per_call",
			PricePerCall: &pc,
		},
	}
}

func asrRoute() *registry.ResolvedRoute {
	ps := 0.002
	return &registry.ResolvedRoute{
		TaskID:      "monitor.transcribe",
		ServiceID:   3,
		ServiceKey:  "funasr-paraformer",
		ServiceType: "asr",
		Provider:    registry.ProviderInfo{Name: "funasr"},
		Pricing: registry.PricingInfo{
			Unit:           "per_second",
			PricePerSecond: &ps,
		},
	}
}

// ----------------------------------------------------------------------------
// LLM billing tests
// ----------------------------------------------------------------------------

// TestBilling_LLM_Success verifies that a successful LLM call persists the
// correct token counts and pricing snapshot.
func TestBilling_LLM_Success(t *testing.T) {
	store := &mockUsageStore{}
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
	store := &mockUsageStore{}
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
	store := &mockUsageStore{}
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
	store := &mockUsageStore{}
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

// TestBilling_ASR_Success verifies per_second billing for ASR.
func TestBilling_ASR_Success(t *testing.T) {
	store := &mockUsageStore{}
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
	if r.PricingSecondSnapshot == nil {
		t.Errorf("PricingSecondSnapshot should be set")
	}
	if r.Unit == nil || *r.Unit != "per_second" {
		t.Errorf("Unit: got %v, want per_second", r.Unit)
	}
}

// TestBilling_ASR_AdapterError verifies that ASR errors still write a record.
func TestBilling_ASR_AdapterError(t *testing.T) {
	store := &mockUsageStore{}
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

func (m *mockBillingStore) GetPricingRule(_ context.Context, _, _, _ string) (*model.PricingRule, error) {
	// Return a sentinel error so calculateCostAndRevenue short-circuits to 0/0
	// without trying to dereference a nil rule.
	return nil, errors.New("no pricing rule (mock)")
}

func (m *mockBillingStore) GetPricingRuleTiers(_ context.Context, _ uint) ([]model.PricingRuleTier, error) {
	return nil, nil
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
	store := &mockUsageStore{}
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
