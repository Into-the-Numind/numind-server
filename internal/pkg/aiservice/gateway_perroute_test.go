package aiservice_test

import (
	"context"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
)

// T1 focused tests for per-route adapter resolution (resolveAndRun + lookupProvider).
// Reuses reproRegistry + recordingRerankProvider from gateway_rerank_repro_test.go.

// recordingEmbedProvider counts Embed invocations so we can prove which adapter served.
type recordingEmbedProvider struct {
	name  string
	resp  *aiservice.EmbedResponse
	err   error
	calls int
}

func (m *recordingEmbedProvider) Name() string           { return m.name }
func (m *recordingEmbedProvider) ProviderType() string   { return "mock" }
func (m *recordingEmbedProvider) Capabilities() []string { return []string{"embed"} }
func (m *recordingEmbedProvider) Embed(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.EmbedRequest) (*aiservice.EmbedResponse, error) {
	m.calls++
	return m.resp, m.err
}

// TestGateway_EmbedFailsOverToDifferentProvider proves the per-route adapter fix
// also covers embed (billing-critical path): on a retryable primary error the
// fallback embed provider's OWN adapter serves the call. (reviewer F8)
func TestGateway_EmbedFailsOverToDifferentProvider(t *testing.T) {
	primaryProv := &recordingEmbedProvider{name: "primary-embed", err: errno.ErrAIProviderError}
	fallbackProv := &recordingEmbedProvider{
		name: "fallback-embed",
		resp: &aiservice.EmbedResponse{Provider: "fallback-embed", Dimension: 3, Embeddings: [][]float32{{0.1, 0.2, 0.3}}},
	}

	primaryRoute := registry.ResolvedRoute{TaskID: "salesrag.embed", ServiceID: 1, Provider: registry.ProviderInfo{Name: "primary-embed"}}
	fallbackRoute := registry.ResolvedRoute{TaskID: "salesrag.embed", ServiceID: 2, Provider: registry.ProviderInfo{Name: "fallback-embed"}}
	reg := &reproRegistry{primary: &primaryRoute, fallbacks: []registry.ResolvedRoute{fallbackRoute}}

	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(middleware.AsGatewayChain(middleware.Fallback(middleware.Deps{Resolver: reg})))
	gw.RegisterProvider(primaryProv)
	gw.RegisterProvider(fallbackProv)

	resp, err := gw.Embed(context.Background(), "salesrag.embed", aiservice.EmbedRequest{Texts: []string{"x"}})
	if err != nil {
		t.Fatalf("expected embed fail-over to succeed, got err: %v", err)
	}
	if resp == nil || resp.Provider != "fallback-embed" {
		t.Fatalf("expected response from fallback-embed, got %+v", resp)
	}
	if fallbackProv.calls != 1 {
		t.Fatalf("expected fallback embed provider's own adapter to serve (calls=1); got %d", fallbackProv.calls)
	}
}

// TestGateway_Rerank_PrimaryLacksCapability reproduces the LITERAL production
// error (Rule-11 Assertion A): the rerank task's primary provider does not
// implement Rerank (the ali-dashscope incident) → gateway returns
// "does not support Rerank". This is the fail-fast path (before the chain).
func TestGateway_Rerank_PrimaryLacksCapability(t *testing.T) {
	// embed-only provider — does NOT implement RerankProvider.
	embedOnly := &recordingEmbedProvider{name: "ali-dashscope", resp: &aiservice.EmbedResponse{}}
	route := registry.ResolvedRoute{TaskID: "salesrag.rerank", ServiceID: 1, Provider: registry.ProviderInfo{Name: "ali-dashscope"}}
	reg := &reproRegistry{primary: &route}

	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(aiservice.MiddlewareChainFunc(func(next aiservice.GatewayHandler) aiservice.GatewayHandler { return next }))
	gw.RegisterProvider(embedOnly)

	_, err := gw.Rerank(context.Background(), "salesrag.rerank", aiservice.RerankRequest{Query: "q", Documents: []string{"a"}})
	if err == nil {
		t.Fatal("expected 'does not support Rerank' error, got nil")
	}
	if !contains(err.Error(), "does not support Rerank") {
		t.Errorf("expected 'does not support Rerank' (production symptom), got: %v", err)
	}
}

// TestGateway_Rerank_UnregisteredProvider_Errors documents the behavior when a
// route points to a provider with no registered adapter and no dmxapi catch-all:
// lookupProvider returns nil and the dispatch handler reports "no provider
// registered". (reviewer F11)
func TestGateway_Rerank_UnregisteredProvider_Errors(t *testing.T) {
	route := registry.ResolvedRoute{TaskID: "salesrag.rerank", ServiceID: 1, Provider: registry.ProviderInfo{Name: "ghost-provider"}}
	reg := &reproRegistry{primary: &route}

	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(aiservice.MiddlewareChainFunc(func(next aiservice.GatewayHandler) aiservice.GatewayHandler { return next }))
	// Intentionally register NO providers (and no dmxapi catch-all).

	_, err := gw.Rerank(context.Background(), "salesrag.rerank", aiservice.RerankRequest{Query: "q", Documents: []string{"a"}})
	if err == nil {
		t.Fatal("expected error for unregistered provider, got nil")
	}
	if !contains(err.Error(), "no provider registered") {
		t.Errorf("expected 'no provider registered' error, got: %v", err)
	}
}

// recordingStreamProvider counts ChatStream invocations so we can prove which
// adapter served a streaming call.
type recordingStreamProvider struct {
	name        string
	streamCalls int
	chunks      []aiservice.ChatChunk
}

func (m *recordingStreamProvider) Name() string           { return m.name }
func (m *recordingStreamProvider) ProviderType() string   { return "mock" }
func (m *recordingStreamProvider) Capabilities() []string { return []string{"chat"} }
func (m *recordingStreamProvider) Chat(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return nil, errno.ErrAICapabilityMismatch // not used by these tests
}
func (m *recordingStreamProvider) ChatStream(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	m.streamCalls++
	ch := make(chan aiservice.ChatChunk, len(m.chunks)+1)
	for _, c := range m.chunks {
		ch <- c
	}
	close(ch)
	return (<-chan aiservice.ChatChunk)(ch), nil
}

// TestGatewayChatStream_DispatchesPerRouteAdapter proves the config-C fix: when
// the middleware chain dispatches a DIFFERENT route than the primary (as the
// streaming Fallback does on failover), ChatStream resolves the adapter PER-ROUTE
// (lookupProvider) instead of reusing the primary adapter captured at
// construction. Before the fix the primary adapter was locked in, so the
// alternate route would have been served by the wrong provider's wire logic.
func TestGatewayChatStream_DispatchesPerRouteAdapter(t *testing.T) {
	provA := &recordingStreamProvider{name: "prov-a"}
	provB := &recordingStreamProvider{
		name:   "prov-b",
		chunks: []aiservice.ChatChunk{{Delta: "hi"}, {IsFinal: true, FinishReason: "stop"}},
	}

	primaryRoute := registry.ResolvedRoute{TaskID: "agent.run", ServiceID: 1, Provider: registry.ProviderInfo{Name: "prov-a"}}
	altRoute := registry.ResolvedRoute{TaskID: "agent.run", ServiceID: 2, Provider: registry.ProviderInfo{Name: "prov-b"}}
	reg := &reproRegistry{primary: &primaryRoute}

	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	// Chain that swaps the route to the alternate provider (mimics a fallback
	// switch) — the handler must then dispatch to prov-b's OWN adapter.
	gw.SetMiddlewareChain(aiservice.MiddlewareChainFunc(func(next aiservice.GatewayHandler) aiservice.GatewayHandler {
		return func(ctx context.Context, _ *registry.ResolvedRoute, req interface{}) (interface{}, error) {
			return next(ctx, &altRoute, req)
		}
	}))
	gw.RegisterProvider(provA)
	gw.RegisterProvider(provB)

	ch, err := gw.ChatStream(context.Background(), "agent.run", aiservice.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch { //nolint:revive // drain
	}

	if provA.streamCalls != 0 {
		t.Errorf("primary adapter must NOT serve the alternate route; prov-a calls=%d", provA.streamCalls)
	}
	if provB.streamCalls != 1 {
		t.Errorf("alternate route must dispatch to ITS OWN adapter (per-route lookup); prov-b calls=%d", provB.streamCalls)
	}
}
