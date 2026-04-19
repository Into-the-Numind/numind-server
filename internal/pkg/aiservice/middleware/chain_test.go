package middleware

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice/registry"
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
// in the correct order: Tracing → Billing → Fallback → Retry → Adapter.
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
