package aiservice_test

import (
	"context"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
)

// ----------------------------------------------------------------------------
// Bug-from-customer reproduction (NDF Rule 11)
//
// Production incident (2026-06-11): DMXAPI dropped qwen3-rerank; the registry
// routed rerank to ali-dashscope, but the ali adapter does not implement Rerank,
// so the gateway returned `provider "ali" does not support Rerank` and rerank
// failed silently in dev + prod. The architectural root cause exposed: the
// gateway cannot fail rerank over to a DIFFERENT-protocol provider, because
//   1. the dispatch closure captured the PRIMARY provider's adapter, so when the
//      Fallback middleware retried with a fallback route it reused the primary
//      adapter against the fallback route (wrong adapter), and
//   2. the ali adapter had no Rerank at all.
//
// This test reproduces (1): when the primary rerank provider returns a retryable
// error, the gateway must fail over to the fallback provider USING THAT PROVIDER'S
// OWN adapter. Before the per-route-adapter fix (T1) the fallback provider's
// Rerank is never invoked (the primary adapter is reused) and the call fails with
// ErrAIFallbackExhausted.
//
// RED before T1/T4; GREEN after. Kept permanently as regression protection.
// ----------------------------------------------------------------------------

// reproRegistry is a minimal Registry that returns a fixed primary + fallback
// list. It embeds the Registry interface (nil) and overrides only the methods
// the rerank dispatch path actually touches (ResolveTask). Any other call would
// nil-panic, which is intentional — it documents the surface this test exercises.
type reproRegistry struct {
	registry.Registry
	primary   *registry.ResolvedRoute
	fallbacks []registry.ResolvedRoute
}

func (r *reproRegistry) ResolveTask(_ context.Context, _ string) (*registry.ResolvedRoute, []registry.ResolvedRoute, error) {
	return r.primary, r.fallbacks, nil
}

// ResolveByModelKey is touched only by the ChatRequest model-override branch,
// never for RerankRequest, but implement it defensively.
func (r *reproRegistry) ResolveByModelKey(_ context.Context, _ string, _ string) (*registry.ResolvedRoute, error) {
	return nil, errno.ErrAIServiceNotFound
}

// recordingRerankProvider counts how many times its Rerank was invoked so the
// test can prove WHICH provider's adapter actually served the call.
type recordingRerankProvider struct {
	name  string
	resp  *aiservice.RerankResponse
	err   error
	calls int
}

func (m *recordingRerankProvider) Name() string           { return m.name }
func (m *recordingRerankProvider) ProviderType() string   { return "mock" }
func (m *recordingRerankProvider) Capabilities() []string { return []string{"rerank"} }
func (m *recordingRerankProvider) Rerank(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.RerankRequest) (*aiservice.RerankResponse, error) {
	m.calls++
	return m.resp, m.err
}

func TestGateway_RerankFailsOverToDifferentProvider(t *testing.T) {
	// Primary returns a retryable provider error (simulates a 429 rate-limit on
	// the free bge model after the T3 fix maps 429 -> ErrAIProviderError).
	primaryProv := &recordingRerankProvider{name: "primary-rerank", err: errno.ErrAIProviderError}
	// Fallback is a DIFFERENT provider (e.g. ali/qwen3-rerank) that succeeds.
	fallbackProv := &recordingRerankProvider{
		name: "fallback-rerank",
		resp: &aiservice.RerankResponse{
			Provider: "fallback-rerank",
			Results:  []aiservice.RerankResult{{Index: 0, Score: 0.9, Document: "a"}},
		},
	}

	primaryRoute := registry.ResolvedRoute{
		TaskID:    "salesrag.rerank",
		ServiceID: 1,
		Provider:  registry.ProviderInfo{Name: "primary-rerank"},
	}
	fallbackRoute := registry.ResolvedRoute{
		TaskID:    "salesrag.rerank",
		ServiceID: 2,
		Provider:  registry.ProviderInfo{Name: "fallback-rerank"},
	}
	reg := &reproRegistry{primary: &primaryRoute, fallbacks: []registry.ResolvedRoute{fallbackRoute}}

	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	// Use the REAL Fallback middleware so this exercises the production path.
	gw.SetMiddlewareChain(middleware.AsGatewayChain(middleware.Fallback(middleware.Deps{Resolver: reg})))
	gw.RegisterProvider(primaryProv)
	gw.RegisterProvider(fallbackProv)

	resp, err := gw.Rerank(context.Background(), "salesrag.rerank", aiservice.RerankRequest{
		Query:     "创业的阶段",
		Documents: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("expected rerank to fail over to the fallback provider and succeed, got err: %v", err)
	}
	if resp == nil || resp.Provider != "fallback-rerank" {
		t.Fatalf("expected response served by fallback-rerank, got %+v", resp)
	}
	if fallbackProv.calls != 1 {
		t.Fatalf("expected fallback provider's OWN adapter to serve the rerank (calls=1); got calls=%d "+
			"— the gateway reused the primary adapter against the fallback route", fallbackProv.calls)
	}
}
