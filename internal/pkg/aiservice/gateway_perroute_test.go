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
