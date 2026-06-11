package middleware

import (
	"context"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// streamCall records one upstream streaming invocation.
type streamCall struct {
	provider  string
	skipRetry bool
}

// streamingProviderHandler returns a Handler that, per call, emits the chunks
// configured for route.Provider.Name and records the call (provider + whether
// skip_retry was injected into ctx). Used to exercise the streaming Fallback
// cascade across providers.
func streamingProviderHandler(calls *[]streamCall, chunksByProvider map[string][]aiservice.ChatChunk) Handler {
	return func(ctx context.Context, route *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		*calls = append(*calls, streamCall{provider: route.Provider.Name, skipRetry: shouldSkipRetry(ctx)})
		chunks := chunksByProvider[route.Provider.Name]
		ch := make(chan aiservice.ChatChunk, len(chunks)+1)
		for _, c := range chunks {
			ch <- c
		}
		close(ch)
		return (<-chan aiservice.ChatChunk)(ch), nil
	}
}

func dsPrimaryRoute() *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		TaskID: "agent.run", ServiceID: 24, ServiceKey: "deepseek-v4-pro",
		ServiceType: "llm", Provider: registry.ProviderInfo{ID: 3, Name: "aihubmix"},
	}
}

func dsAltRoute() registry.ResolvedRoute {
	return registry.ResolvedRoute{
		TaskID: "agent.run", ServiceID: 24, ServiceKey: "deepseek-v4-pro",
		ServiceType: "llm", Provider: registry.ProviderInfo{ID: 1, Name: "dmxapi"},
	}
}

// TestFallbackStream_CrossProviderFailover (AC2): a streaming primary that stalls
// before the first content chunk fails over to the SAME model on a DIFFERENT
// provider; the alternate is invoked with skip_retry and its content reaches the
// consumer transparently (one terminal, no error).
func TestFallbackStream_CrossProviderFailover(t *testing.T) {
	primary := dsPrimaryRoute()
	resolver := &registryStub{primaryRoute: primary, alternates: []registry.ResolvedRoute{dsAltRoute()}}
	mw := Fallback(Deps{Resolver: resolver, Logger: &mockLogger{}})

	var calls []streamCall
	inner := streamingProviderHandler(&calls, map[string][]aiservice.ChatChunk{
		"aihubmix": {idleErrChunk()},           // primary stalls pre-content
		"dmxapi":   successChunks("recovered"), // alternate succeeds
	})

	resp, err := mw(inner)(context.Background(), primary, aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := collectStream(t, resp)

	if concatDelta(got) != "recovered" {
		t.Errorf("expected alternate content %q, got %q", "recovered", concatDelta(got))
	}
	if countTerminals(got) != 1 {
		t.Errorf("expected exactly 1 terminal chunk, got %d", countTerminals(got))
	}
	for _, c := range got {
		if c.IsFinal && c.Err != nil {
			t.Errorf("failover succeeded but consumer saw error terminal: %v", c.Err)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 upstream calls (primary + alternate), got %d (%+v)", len(calls), calls)
	}
	if calls[0].provider != "aihubmix" || calls[1].provider != "dmxapi" {
		t.Errorf("expected [aihubmix dmxapi], got [%s %s]", calls[0].provider, calls[1].provider)
	}
	if !calls[1].skipRetry {
		t.Errorf("alternate must be invoked with skip_retry (no same-provider stream retry → upstream ≤3)")
	}
}

// TestFallbackStream_NoAlternates_PropagatesPrimaryError (AC3): when no alternate
// provider exists, the primary error terminal passes through unchanged.
func TestFallbackStream_NoAlternates_PropagatesPrimaryError(t *testing.T) {
	primary := dsPrimaryRoute()
	resolver := &registryStub{primaryRoute: primary, alternates: nil}
	mw := Fallback(Deps{Resolver: resolver, Logger: &mockLogger{}})

	var calls []streamCall
	inner := streamingProviderHandler(&calls, map[string][]aiservice.ChatChunk{
		"aihubmix": {idleErrChunk()},
	})

	resp, err := mw(inner)(context.Background(), primary, aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := collectStream(t, resp)

	if len(calls) != 1 {
		t.Errorf("no alternates → exactly 1 upstream call, got %d", len(calls))
	}
	if len(got) == 0 || got[len(got)-1].Err == nil {
		t.Errorf("expected the primary error terminal to pass through")
	}
}

// TestFallbackStream_PostContentError_NoFailover (AC4): once content has been
// forwarded, an error must NOT trigger cross-provider failover.
func TestFallbackStream_PostContentError_NoFailover(t *testing.T) {
	primary := dsPrimaryRoute()
	resolver := &registryStub{primaryRoute: primary, alternates: []registry.ResolvedRoute{dsAltRoute()}}
	mw := Fallback(Deps{Resolver: resolver, Logger: &mockLogger{}})

	var calls []streamCall
	inner := streamingProviderHandler(&calls, map[string][]aiservice.ChatChunk{
		"aihubmix": {{Delta: "partial", Index: 0}, idleErrChunk()}, // content THEN error
	})

	resp, err := mw(inner)(context.Background(), primary, aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := collectStream(t, resp)

	if len(calls) != 1 {
		t.Errorf("post-content error must not fail over: expected 1 upstream call, got %d", len(calls))
	}
	if concatDelta(got) != "partial" {
		t.Errorf("expected forwarded content %q, got %q", "partial", concatDelta(got))
	}
}

// TestFallbackStream_CascadesThroughMultipleAlternates: the first alternate also
// stalls pre-content → the cascade advances to the second alternate.
func TestFallbackStream_CascadesThroughMultipleAlternates(t *testing.T) {
	primary := dsPrimaryRoute()
	alt1 := registry.ResolvedRoute{TaskID: "agent.run", ServiceID: 24, Provider: registry.ProviderInfo{ID: 1, Name: "dmxapi"}}
	alt2 := registry.ResolvedRoute{TaskID: "agent.run", ServiceID: 24, Provider: registry.ProviderInfo{ID: 2, Name: "dmxapi-ssvip"}}
	resolver := &registryStub{primaryRoute: primary, alternates: []registry.ResolvedRoute{alt1, alt2}}
	mw := Fallback(Deps{Resolver: resolver, Logger: &mockLogger{}})

	var calls []streamCall
	inner := streamingProviderHandler(&calls, map[string][]aiservice.ChatChunk{
		"aihubmix":     {idleErrChunk()},
		"dmxapi":       {idleErrChunk()},           // first alternate also stalls
		"dmxapi-ssvip": successChunks("from-alt2"), // second alternate succeeds
	})

	resp, err := mw(inner)(context.Background(), primary, aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := collectStream(t, resp)

	if concatDelta(got) != "from-alt2" {
		t.Errorf("expected content from the second alternate, got %q", concatDelta(got))
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 upstream calls (primary + 2 alternates), got %d (%+v)", len(calls), calls)
	}
	if calls[1].provider != "dmxapi" || calls[2].provider != "dmxapi-ssvip" {
		t.Errorf("expected cascade [dmxapi dmxapi-ssvip], got [%s %s]", calls[1].provider, calls[2].provider)
	}
}

// TestFallbackStream_NormalStreamPassthrough (AC9 zero-regression): a primary that
// streams content with no error never resolves alternates / never fails over.
func TestFallbackStream_NormalStreamPassthrough(t *testing.T) {
	primary := dsPrimaryRoute()
	resolver := &registryStub{primaryRoute: primary, alternates: []registry.ResolvedRoute{dsAltRoute()}}
	mw := Fallback(Deps{Resolver: resolver, Logger: &mockLogger{}})

	var calls []streamCall
	inner := streamingProviderHandler(&calls, map[string][]aiservice.ChatChunk{
		"aihubmix": successChunks("all good"),
	})

	resp, err := mw(inner)(context.Background(), primary, aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := collectStream(t, resp)

	if len(calls) != 1 || calls[0].provider != "aihubmix" {
		t.Errorf("normal stream must not fail over: calls=%+v", calls)
	}
	if concatDelta(got) != "all good" {
		t.Errorf("content altered: got %q", concatDelta(got))
	}
}
