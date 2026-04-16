// Package aiservice provides the unified AI Gateway for all AI capability calls.
// This file contains the Gateway struct and its Build function.
//
// # Import topology note
//
// The aiservice/adapter and aiservice/middleware sub-packages both import the
// parent "aiservice" package for request/response type definitions.  To avoid
// import cycles, gateway.go does NOT import those sub-packages directly.
// Instead:
//
//   - Capability provider interfaces (ChatProvider, EmbedProvider, …) are
//     defined here using aiservice.* types, so adapter structs satisfy them
//     without aiservice importing adapter.
//
//   - The middleware chain is stored as a locally-defined MiddlewareChainFunc
//     type and wired in from numind.go (which can import both aiservice AND
//     aiservice/middleware since it is not itself imported by either).
//
// See also: providers.go (wired from numind.go) for the six concrete adapters.
package aiservice

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"numind-server/internal/pkg/aiservice/registry"
)

// ----------------------------------------------------------------------------
// Local handler / middleware types (cycle-free)
// ----------------------------------------------------------------------------

// GatewayHandler is the core function type passed through the middleware chain.
// It receives the resolved route and the caller's request, returning an opaque
// response or an error.
type GatewayHandler func(ctx context.Context, route *registry.ResolvedRoute, req interface{}) (interface{}, error)

// MiddlewareChainFunc is the type of a composed middleware chain as returned by
// middleware.BuildDefault(). Storing it here as a named function type allows
// Gateway to use it without importing the middleware sub-package.
type MiddlewareChainFunc func(next GatewayHandler) GatewayHandler

// ----------------------------------------------------------------------------
// Local capability interfaces (cycle-free; mirror adapter sub-interfaces)
// ----------------------------------------------------------------------------

// Provider is the minimal interface every registered AI provider adapter must satisfy.
type Provider interface {
	Name() string
	ProviderType() string
	Capabilities() []string
}

// ChatProvider is implemented by adapters that support text/vision chat.
type ChatProvider interface {
	Provider
	Chat(ctx context.Context, route *registry.ResolvedRoute, req ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, route *registry.ResolvedRoute, req ChatRequest) (<-chan ChatChunk, error)
}

// EmbedProvider is implemented by adapters that support text embedding.
type EmbedProvider interface {
	Provider
	Embed(ctx context.Context, route *registry.ResolvedRoute, req EmbedRequest) (*EmbedResponse, error)
}

// RerankProvider is implemented by adapters that support passage reranking.
type RerankProvider interface {
	Provider
	Rerank(ctx context.Context, route *registry.ResolvedRoute, req RerankRequest) (*RerankResponse, error)
}

// OCRProvider is implemented by adapters that support optical character recognition.
type OCRProvider interface {
	Provider
	OCR(ctx context.Context, route *registry.ResolvedRoute, req OCRRequest) (*OCRResponse, error)
}

// ASRProvider is implemented by adapters that support automatic speech recognition.
type ASRProvider interface {
	Provider
	ASR(ctx context.Context, route *registry.ResolvedRoute, req ASRRequest) (*ASRResponse, error)
}

// ----------------------------------------------------------------------------
// Deps
// ----------------------------------------------------------------------------

// Deps carries the injected dependencies required to build a Gateway.
// Build() creates the registry and allocates the provider map.
// The middleware chain is injected separately via SetMiddlewareChain.
type Deps struct {
	// Registry is the resolved task-profile registry (from aiservice/registry).
	// Typically created via registry.New(db). If nil, all calls return errors.
	Registry registry.Registry
}

// ----------------------------------------------------------------------------
// Gateway
// ----------------------------------------------------------------------------

// Gateway is the process-wide AI service entry point.  Business layers call
// the top-level functions (Chat, Embed, Rerank, OCR, ASR) which delegate to
// the process singleton returned by Default().
//
// Gateway is safe for concurrent use.
type Gateway struct {
	registry  registry.Registry
	chain     MiddlewareChainFunc // set by SetMiddlewareChain after Build
	providers map[string]Provider // keyed by Provider.Name()
	mu        sync.RWMutex        // guards providers map (read-mostly)
}

// Build constructs a new Gateway from the provided Deps.
//
// After Build(), callers must:
//  1. Call SetMiddlewareChain() to wire the middleware stack.
//  2. Call RegisterProvider() for each AI provider adapter.
//  3. Call SetDefault() to install as the process singleton.
func Build(deps Deps) *Gateway {
	return &Gateway{
		registry:  deps.Registry,
		providers: make(map[string]Provider),
	}
}

// SetMiddlewareChain installs the composed middleware chain on the Gateway.
// The chain parameter is the value returned by middleware.BuildDefault(deps).
//
// Because MiddlewareChainFunc and middleware.Middleware have the same underlying
// function signature, callers can convert between them:
//
//	chain := middleware.BuildDefault(mwDeps)
//	g.SetMiddlewareChain(aiservice.MiddlewareChainFunc(chain))
func (g *Gateway) SetMiddlewareChain(chain MiddlewareChainFunc) {
	g.mu.Lock()
	g.chain = chain
	g.mu.Unlock()
}

// RegisterProvider registers a Provider with the Gateway.
// Must be called before any AI capability calls are made.
// Safe to call multiple times (last registration wins for duplicate names).
func (g *Gateway) RegisterProvider(p Provider) {
	g.mu.Lock()
	g.providers[p.Name()] = p
	g.mu.Unlock()
}

// RegisterProviderAlias registers an additional name mapping to an existing provider.
// Useful when multiple llm_provider rows (dmxapi, dmxapi-ssvip, aihubmix) share
// the same adapter protocol (OpenAI-compatible).
func (g *Gateway) RegisterProviderAlias(alias string, adapterName string) {
	g.mu.Lock()
	if p, ok := g.providers[adapterName]; ok {
		g.providers[alias] = p
	}
	g.mu.Unlock()
}

// findAdapterByPrefix returns the first adapter whose name is a prefix of providerName.
// E.g., provider "dmxapi-ssvip" matches adapter "dmxapi".
// Caller must hold g.mu.RLock.
func (g *Gateway) findAdapterByPrefix(providerName string) Provider {
	for name, p := range g.providers {
		if len(name) > 0 && len(providerName) > len(name) && providerName[:len(name)] == name {
			return p
		}
	}
	// Fallback: all OpenAI-compatible providers can use dmxapi adapter
	if p, ok := g.providers["dmxapi"]; ok {
		return p
	}
	return nil
}

// AdapterNames returns the names of all registered providers (for health checks).
func (g *Gateway) AdapterNames() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.providers))
	for k := range g.providers {
		names = append(names, k)
	}
	return names
}

// ----------------------------------------------------------------------------
// Capability dispatch helpers
// ----------------------------------------------------------------------------

// resolveAndRun is the core dispatch logic used by all capability methods.
func (g *Gateway) resolveAndRun(
	ctx context.Context,
	taskID string,
	req interface{},
	makeHandler func(p Provider, route *registry.ResolvedRoute) (GatewayHandler, error),
) (interface{}, error) {
	if g.registry == nil {
		return nil, fmt.Errorf("gateway: no registry configured")
	}

	primary, _, err := g.registry.ResolveTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("gateway.ResolveTask(%s): %w", taskID, err)
	}

	// Check for model override (user selected a specific model via ModelSelector).
	// If override resolution fails, fall through silently to the profile default.
	if chatReq, ok := req.(ChatRequest); ok && chatReq.ModelOverride != "" {
		if overrideRoute, overrideErr := g.registry.ResolveByModelKey(ctx, taskID, chatReq.ModelOverride); overrideErr == nil {
			primary = overrideRoute
		}
	}

	g.mu.RLock()
	p, ok := g.providers[primary.Provider.Name]
	if !ok {
		p = g.findAdapterByPrefix(primary.Provider.Name)
		ok = p != nil
	}
	chainFn := g.chain
	g.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("gateway: no provider registered for %q", primary.Provider.Name)
	}

	handler, err := makeHandler(p, primary)
	if err != nil {
		return nil, err
	}

	if chainFn != nil {
		handler = chainFn(handler)
	}
	return handler(ctx, primary, req)
}

// ----------------------------------------------------------------------------
// Chat
// ----------------------------------------------------------------------------

// Chat performs a non-streaming chat call for the given taskID.
func (g *Gateway) Chat(ctx context.Context, taskID string, req ChatRequest) (*ChatResponse, error) {
	resp, err := g.resolveAndRun(ctx, taskID, req, func(p Provider, route *registry.ResolvedRoute) (GatewayHandler, error) {
		chat, ok := p.(ChatProvider)
		if !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support Chat", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			return chat.Chat(ctx, r, rawReq.(ChatRequest))
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.(*ChatResponse), nil
}

// ChatStream starts a streaming chat call for the given taskID.
func (g *Gateway) ChatStream(ctx context.Context, taskID string, req ChatRequest) (<-chan ChatChunk, error) {
	if g.registry == nil {
		return nil, fmt.Errorf("gateway: no registry configured")
	}

	primary, _, err := g.registry.ResolveTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("gateway.ResolveTask(%s): %w", taskID, err)
	}

	// Check for model override (user selected a specific model via ModelSelector).
	// If override resolution fails, fall through silently to the profile default.
	if req.ModelOverride != "" {
		if overrideRoute, overrideErr := g.registry.ResolveByModelKey(ctx, taskID, req.ModelOverride); overrideErr == nil {
			primary = overrideRoute
		}
	}

	g.mu.RLock()
	p, ok := g.providers[primary.Provider.Name]
	if !ok {
		p = g.findAdapterByPrefix(primary.Provider.Name)
		ok = p != nil
	}
	chainFn := g.chain
	g.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("gateway: no provider registered for %q", primary.Provider.Name)
	}

	chat, ok := p.(ChatProvider)
	if !ok {
		return nil, fmt.Errorf("gateway: provider %q does not support ChatStream", p.Name())
	}

	handler := GatewayHandler(func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
		ch, err := chat.ChatStream(ctx, r, rawReq.(ChatRequest))
		if err != nil {
			return nil, err
		}
		return ch, nil
	})

	if chainFn != nil {
		handler = chainFn(handler)
	}

	resp, err := handler(ctx, primary, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.(<-chan ChatChunk), nil
}

// ----------------------------------------------------------------------------
// Embed
// ----------------------------------------------------------------------------

// Embed performs a text embedding call for the given taskID.
func (g *Gateway) Embed(ctx context.Context, taskID string, req EmbedRequest) (*EmbedResponse, error) {
	resp, err := g.resolveAndRun(ctx, taskID, req, func(p Provider, route *registry.ResolvedRoute) (GatewayHandler, error) {
		embed, ok := p.(EmbedProvider)
		if !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support Embed", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			return embed.Embed(ctx, r, rawReq.(EmbedRequest))
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.(*EmbedResponse), nil
}

// ----------------------------------------------------------------------------
// Rerank
// ----------------------------------------------------------------------------

// Rerank performs a reranking call for the given taskID.
func (g *Gateway) Rerank(ctx context.Context, taskID string, req RerankRequest) (*RerankResponse, error) {
	resp, err := g.resolveAndRun(ctx, taskID, req, func(p Provider, route *registry.ResolvedRoute) (GatewayHandler, error) {
		rerank, ok := p.(RerankProvider)
		if !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support Rerank", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			return rerank.Rerank(ctx, r, rawReq.(RerankRequest))
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.(*RerankResponse), nil
}

// ----------------------------------------------------------------------------
// OCR
// ----------------------------------------------------------------------------

// OCR performs an optical character recognition call for the given taskID.
func (g *Gateway) OCR(ctx context.Context, taskID string, req OCRRequest) (*OCRResponse, error) {
	resp, err := g.resolveAndRun(ctx, taskID, req, func(p Provider, route *registry.ResolvedRoute) (GatewayHandler, error) {
		ocr, ok := p.(OCRProvider)
		if !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support OCR", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			return ocr.OCR(ctx, r, rawReq.(OCRRequest))
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.(*OCRResponse), nil
}

// ----------------------------------------------------------------------------
// ASR
// ----------------------------------------------------------------------------

// ASR performs an automatic speech recognition call for the given taskID.
func (g *Gateway) ASR(ctx context.Context, taskID string, req ASRRequest) (*ASRResponse, error) {
	resp, err := g.resolveAndRun(ctx, taskID, req, func(p Provider, route *registry.ResolvedRoute) (GatewayHandler, error) {
		asr, ok := p.(ASRProvider)
		if !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support ASR", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			return asr.ASR(ctx, r, rawReq.(ASRRequest))
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.(*ASRResponse), nil
}

// ----------------------------------------------------------------------------
// Global singleton
// ----------------------------------------------------------------------------

// defaultGateway holds the process-wide Gateway singleton.
// Uses atomic.Pointer for type-safe, race-free load/store without unsafe.
var defaultGateway atomic.Pointer[Gateway]

// SetDefault installs g as the process-wide Gateway singleton.
// Must be called once during startup (before any AI calls are made).
// Safe for concurrent use.
func SetDefault(g *Gateway) { defaultGateway.Store(g) }

// Default returns the process-wide Gateway singleton.
// Panics if SetDefault has not been called yet — panic (not log.Fatalw) so
// test runners can recover from it rather than having the process killed.
func Default() *Gateway {
	g := defaultGateway.Load()
	if g == nil {
		panic("aiservice.Default() called before SetDefault() — check startup order")
	}
	return g
}
