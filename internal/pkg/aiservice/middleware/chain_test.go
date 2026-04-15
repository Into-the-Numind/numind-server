package middleware

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/aiservice/registry"
)

// buildTestRoute returns a minimal ResolvedRoute for use in tests.
func buildTestRoute(serviceType string) *registry.ResolvedRoute {
	unit := "per_1m_tokens"
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
		Pricing: registry.PricingInfo{
			Unit:               unit,
			InputPricePerMTok:  1.0,
			OutputPricePerMTok: 4.0,
		},
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
