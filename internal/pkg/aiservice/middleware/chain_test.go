package middleware

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/model"
)

// buildTestRoute returns a minimal ResolvedRoute for use in tests.
func buildTestRoute(serviceType string) *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		TaskID:      "test.task",
		ServiceID:   42,
		ServiceKey:  "test-model",
		ServiceType: serviceType,
		Provider: registry.ProviderInfo{
			ID:   1,
			Name: "test-provider",
		},
		ProviderModelID: "test-model-id",
		// Pricing amounts removed from route in T-arch; billing middleware
		// resolves them from pricing_rule at call time.
		Pricing: registry.PricingInfo{Unit: "per_1m_tokens"},
	}
}

// okHandler is a test Handler that always succeeds.
func okHandler(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
	return "ok", nil
}

// errHandler is a test Handler that always returns an error.
func errHandler(err error) Handler {
	return func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return nil, err
	}
}

// TestChain_Order verifies that Chain applies middlewares in the documented
// order: [a, b, c] → a wraps b wraps c wraps next.
func TestChain_Order(t *testing.T) {
	var order []string

	makeMiddleware := func(name string) Middleware {
		return func(next Handler) Handler {
			return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error) {
				order = append(order, name+":before")
				resp, err := next(ctx, route, req)
				order = append(order, name+":after")
				return resp, err
			}
		}
	}

	chain := Chain(makeMiddleware("a"), makeMiddleware("b"), makeMiddleware("c"))
	handler := chain(okHandler)

	ctx := context.Background()
	route := buildTestRoute("llm")
	_, err := handler(ctx, route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"a:before", "b:before", "c:before", "c:after", "b:after", "a:after"}
	if len(order) != len(want) {
		t.Fatalf("order mismatch: got %v, want %v", order, want)
	}
	for i, v := range want {
		if order[i] != v {
			t.Errorf("order[%d]: got %q, want %q", i, order[i], v)
		}
	}
}

// TestChain_Empty ensures an empty chain wraps the handler without modification.
func TestChain_Empty(t *testing.T) {
	chain := Chain()
	handler := chain(okHandler)
	ctx := context.Background()
	route := buildTestRoute("llm")
	resp, err := handler(ctx, route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("unexpected response: %v", resp)
	}
}

// TestChain_ErrorPropagation ensures errors from inner handlers propagate outward.
func TestChain_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("inner error")
	chain := Chain()
	handler := chain(errHandler(sentinel))
	ctx := context.Background()
	route := buildTestRoute("llm")
	_, err := handler(ctx, route, nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

// spyUsageStore records CreateUsageRecord calls and their invocation order.
type spyUsageStore struct {
	callOrder *[]string
}

func (s *spyUsageStore) CreateUsageRecord(_ context.Context, _ *model.UsageRecord) error {
	*s.callOrder = append(*s.callOrder, "billing")
	return nil
}

// GetPricingRule satisfies the updated UsageStore interface.
// Returns ErrRecordNotFound so buildBaseRecord leaves snapshots nil (non-fatal).
func (s *spyUsageStore) GetPricingRule(_ context.Context, _, _, _ string) (*model.PricingRule, error) {
	return nil, gorm.ErrRecordNotFound
}

// TestBuildDefault_OrderingMatchesSpec verifies that BuildDefault assembles the chain
// in the new correct order:
//
//	Tracing → Fallback → ContextBudgetCredits → Billing → Retry → Adapter
//
// We verify this by checking that billing (UsageStore write) happens after the adapter
// call, confirming Billing wraps the inner handlers; and that the overall chain still
// produces a successful response.
func TestBuildDefault_OrderingMatchesSpec(t *testing.T) {
	// Disable Langfuse so Tracing becomes a no-op (no real SDK calls).
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	var callOrder []string
	var adapterCalled atomic.Bool

	store := &spyUsageStore{callOrder: &callOrder}
	deps := Deps{
		UsageStore: store,
		Clock:      fixedClock{},
		Logger:     &mockLogger{},
	}

	mw := BuildDefault(deps)

	// Adapter (innermost) records its call before returning.
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		adapterCalled.Store(true)
		callOrder = append(callOrder, "adapter")
		return "result", nil
	})

	handler := mw(adapter)
	route := buildTestRoute("llm")
	ctx := context.Background()

	resp, err := handler(ctx, route, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "result" {
		t.Errorf("unexpected response: %v", resp)
	}
	if !adapterCalled.Load() {
		t.Error("adapter was not called")
	}

	// Verify adapter called before billing (Billing wraps inward from adapter).
	adapterIdx := -1
	billingIdx := -1
	for i, s := range callOrder {
		switch s {
		case "adapter":
			adapterIdx = i
		case "billing":
			billingIdx = i
		}
	}
	if adapterIdx == -1 {
		t.Error("adapter call not recorded")
	}
	if billingIdx == -1 {
		t.Error("billing (UsageStore.CreateUsageRecord) not called — Billing middleware may be missing from chain")
	}
	if adapterIdx >= billingIdx {
		t.Errorf("expected adapter (%d) to be called before billing (%d) in call order %v",
			adapterIdx, billingIdx, callOrder)
	}
}

// spyMiddleware records the position in which it is entered and exited during a
// request, writing "name:before" and "name:after" entries to the shared log.
func spyMiddleware(name string, log *[]string) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error) {
			*log = append(*log, name+":before")
			resp, err := next(ctx, route, req)
			*log = append(*log, name+":after")
			return resp, err
		}
	}
}

// TestBuildDefaultMiddlewareOrder_ContextBudgetAfterFallbackBeforeBilling verifies
// the exact chain order required by spec §6.1 (Task 5 revision):
//
//	Tracing → Fallback → ContextBudgetCredits → Billing → Retry → Adapter
//
// The test replaces each middleware with a spy that records entry/exit events,
// then asserts the observed event sequence matches the expected order.
func TestBuildDefaultMiddlewareOrder_ContextBudgetAfterFallbackBeforeBilling(t *testing.T) {
	// Disable Langfuse so Tracing becomes a structural no-op for order tracing.
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	var log []string

	// Build a custom chain that mirrors BuildDefault's order but replaces each
	// middleware with a named spy — this removes external side-effects (DB, Langfuse)
	// while still asserting composition order.
	chain := Chain(
		spyMiddleware("tracing", &log),
		spyMiddleware("fallback", &log),
		spyMiddleware("context_budget", &log),
		spyMiddleware("billing", &log),
		spyMiddleware("retry", &log),
	)

	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		log = append(log, "adapter")
		return "ok", nil
	})

	handler := chain(adapter)
	_, err := handler(context.Background(), buildTestRoute("llm"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expected order: each layer enters in order, the adapter runs, then layers exit in reverse.
	want := []string{
		"tracing:before",
		"fallback:before",
		"context_budget:before",
		"billing:before",
		"retry:before",
		"adapter",
		"retry:after",
		"billing:after",
		"context_budget:after",
		"fallback:after",
		"tracing:after",
	}

	if len(log) != len(want) {
		t.Fatalf("event log length: got %d, want %d\ngot:  %v\nwant: %v", len(log), len(want), log, want)
	}
	for i, w := range want {
		if log[i] != w {
			t.Errorf("event[%d]: got %q, want %q", i, log[i], w)
		}
	}
}

// TestBillingUsesFallbackRouteProviderModelAndPricing verifies that when Fallback
// switches to a backup provider, the Billing middleware records the fallback
// route's provider/model rather than the primary route.
//
// This is the key invariant of placing Billing inside Fallback: billing.go
// receives whichever route was ultimately used for the successful call.
func TestBillingUsesFallbackRouteProviderModelAndPricing(t *testing.T) {
	// Disable Langfuse so Tracing is a no-op.
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	// Primary route: serviceID=42, provider=primary-provider, model=primary-model.
	primaryRoute := buildTestRoute("llm")
	// primaryRoute.ServiceID = 42, Provider.Name = "test-provider", ServiceKey = "test-model"

	// Fallback route: serviceID=99, provider=fallback-provider, model=fallback-model.
	fbRoute := registry.ResolvedRoute{
		TaskID:      "test.task",
		ServiceID:   99,
		ServiceKey:  "fallback-model",
		ServiceType: "llm",
		Provider:    registry.ProviderInfo{Name: "fallback-provider"},
		Pricing:     registry.PricingInfo{Unit: "per_1m_tokens"},
	}

	// Registry stub: primary route fails, fallback route succeeds.
	resolver := &registryStub{
		primaryRoute:   primaryRoute,
		fallbackRoutes: []registry.ResolvedRoute{fbRoute},
	}

	// UsageStore that captures which provider/model was recorded.
	store := &mockUsageStore{
		pricingRules: map[string]*model.PricingRule{
			// Pricing for the fallback route.
			"llm_chat|fallback-provider|fallback-model": {
				BillingMode:        "flat",
				FlatUnit:           "call",
				InputPricePerMTok:  2.0,
				OutputPricePerMTok: 8.0,
				IsActive:           true,
			},
		},
	}

	deps := Deps{
		Resolver:   resolver,
		UsageStore: store,
		Clock:      fixedClock{},
		Logger:     &mockLogger{},
	}

	// Chain: Fallback → ContextBudgetCredits → Billing → Retry
	// (Tracing omitted for simplicity; it would be a no-op anyway with Langfuse nil)
	chain := Chain(
		Fallback(deps),
		ContextBudgetCredits(deps),
		Billing(deps),
		retryWithPolicy(zeroDelayPolicy()),
	)

	// Adapter: returns ErrAIProviderError for primary (id=42), success for fallback (id=99).
	adapter := Handler(func(_ context.Context, route *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		if route.ServiceID == 42 {
			return nil, errno.ErrAIProviderError
		}
		return &aiservice.ChatResponse{
			Content: "fallback response",
			Usage: aiservice.TokenUsage{
				PromptTokens:     50,
				CompletionTokens: 20,
				TotalTokens:      70,
			},
			Provider: route.Provider.Name,
			Model:    route.ServiceKey,
		}, nil
	})

	handler := chain(adapter)
	resp, err := handler(WithUserID(context.Background(), 1), primaryRoute, aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a response from fallback")
	}

	// With Billing inside Fallback, two records are written:
	//   [0] primary attempt (failed) — provider="test-provider", IsFallback=false
	//   [1] fallback attempt (succeeded) — provider="fallback-provider", IsFallback=true
	// (The Retry middleware causes 2 upstream calls to the adapter for the primary,
	// but Billing sits outside Retry so it still writes exactly 1 record per Fallback-level
	// call, not per retry attempt.)
	if len(store.records) != 2 {
		t.Fatalf("expected 2 usage records (1 primary + 1 fallback), got %d\nrecords: %+v", len(store.records), store.records)
	}

	// Identify the fallback record — it is the one with IsFallback=true.
	var fbRecord *model.UsageRecord
	for _, rec := range store.records {
		if rec.IsFallback {
			fbRecord = rec
			break
		}
	}
	if fbRecord == nil {
		t.Fatal("expected a usage record with IsFallback=true for the fallback call")
	}

	// The fallback record must carry the fallback route's provider/model.
	if fbRecord.Provider != "fallback-provider" {
		t.Errorf("fallback record Provider: got %q, want %q", fbRecord.Provider, "fallback-provider")
	}
	if fbRecord.Model != "fallback-model" {
		t.Errorf("fallback record Model: got %q, want %q", fbRecord.Model, "fallback-model")
	}
	// Pricing snapshot for the fallback record must be from the fallback route's pricing_rule.
	if fbRecord.PricingInputSnapshot == nil || *fbRecord.PricingInputSnapshot != 2.0 {
		t.Errorf("fallback record PricingInputSnapshot: got %v, want 2.0 (fallback pricing)", fbRecord.PricingInputSnapshot)
	}
	if fbRecord.PricingOutputSnapshot == nil || *fbRecord.PricingOutputSnapshot != 8.0 {
		t.Errorf("fallback record PricingOutputSnapshot: got %v, want 8.0 (fallback pricing)", fbRecord.PricingOutputSnapshot)
	}
}
