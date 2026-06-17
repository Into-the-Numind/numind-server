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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
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

// ImageGenProvider is implemented by adapters that support text-to-image generation.
type ImageGenProvider interface {
	Provider
	ImageGen(ctx context.Context, route *registry.ResolvedRoute, req ImageGenRequest) (*ImageGenResponse, error)
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

	// Default max_tokens from the resolved model's configured capability when the
	// caller left it 0, so a thinking model's reasoning cannot exhaust the provider
	// default budget and strand the answer in reasoning_content. Only fills
	// ChatRequest; Embed/Rerank req types are left untouched. See maxtokens.go.
	if chatReq, ok := req.(ChatRequest); ok && chatReq.MaxTokens == 0 {
		chatReq.MaxTokens = defaultMaxTokensFromCapability(primary.Capability.MaxOutputTokens)
		req = chatReq
	}

	// Resolve the PRIMARY provider for the fail-fast capability check below.
	// NOTE: the actual per-call adapter is resolved again inside the handler via
	// lookupProvider(r.Provider.Name) — that is what lets the Fallback middleware
	// dispatch a fallback route to ITS OWN provider's adapter instead of reusing
	// the primary's (cross-provider fallback for rerank/embed/etc).
	p := g.lookupProvider(primary.Provider.Name)
	if p == nil {
		return nil, fmt.Errorf("gateway: no provider registered for %q", primary.Provider.Name)
	}

	g.mu.RLock()
	chainFn := g.chain
	g.mu.RUnlock()

	handler, err := makeHandler(p, primary)
	if err != nil {
		return nil, err
	}

	if chainFn != nil {
		handler = chainFn(handler)
	}
	return handler(ctx, primary, req)
}

// lookupProvider resolves a registered provider by name, applying the same
// prefix-match fallback (findAdapterByPrefix) as the legacy inline resolution.
// It acquires g.mu.RLock internally so it is safe to call from a dispatch
// closure at handler-EXECUTION time (after resolveAndRun's own RLock has been
// released). This per-call resolution is the mechanism that makes the Fallback
// middleware use each route's own adapter — see resolveAndRun.
func (g *Gateway) lookupProvider(name string) Provider {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if pr, ok := g.providers[name]; ok {
		return pr
	}
	// findAdapterByPrefix requires the caller to hold g.mu.RLock — satisfied here.
	return g.findAdapterByPrefix(name)
}

// ----------------------------------------------------------------------------
// Chat
// ----------------------------------------------------------------------------

// Chat performs a non-streaming chat call for the given taskID.
func (g *Gateway) Chat(ctx context.Context, taskID string, req ChatRequest) (*ChatResponse, error) {
	resp, err := g.resolveAndRun(ctx, taskID, req, func(p Provider, route *registry.ResolvedRoute) (GatewayHandler, error) {
		if _, ok := p.(ChatProvider); !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support Chat", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			rp := g.lookupProvider(r.Provider.Name)
			if rp == nil {
				return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name)
			}
			chat, ok := rp.(ChatProvider)
			if !ok {
				return nil, fmt.Errorf("gateway: provider %q does not support Chat: %w", rp.Name(), errno.ErrAICapabilityMismatch)
			}
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

	// Default max_tokens from the resolved model's configured capability when the
	// caller left it 0 (same as resolveAndRun; ChatStream resolves inline). Prevents
	// a thinking model's reasoning from exhausting the provider default budget and
	// stranding the answer in reasoning_content. See maxtokens.go.
	if req.MaxTokens == 0 {
		req.MaxTokens = defaultMaxTokensFromCapability(primary.Capability.MaxOutputTokens)
	}

	// Fail-fast capability check on the PRIMARY provider (mirrors resolveAndRun).
	// The actual per-call adapter is resolved AGAIN inside the handler via
	// lookupProvider(r.Provider.Name) — that is what lets the streaming Fallback
	// middleware dispatch a fallback route to ITS OWN provider's adapter instead of
	// reusing the primary's. (Previously this captured the primary adapter at
	// construction time — NOTE(rerank-routing T1) — which broke cross-provider
	// streaming fallback; fixed in agent-stream-retry to match Chat/Embed.)
	p := g.lookupProvider(primary.Provider.Name)
	if p == nil {
		return nil, fmt.Errorf("gateway: no provider registered for %q", primary.Provider.Name)
	}
	if _, ok := p.(ChatProvider); !ok {
		return nil, fmt.Errorf("gateway: provider %q does not support ChatStream: %w", p.Name(), errno.ErrAICapabilityMismatch)
	}

	g.mu.RLock()
	chainFn := g.chain
	g.mu.RUnlock()

	handler := GatewayHandler(func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
		rp := g.lookupProvider(r.Provider.Name)
		if rp == nil {
			return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name)
		}
		chat, ok := rp.(ChatProvider)
		if !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support ChatStream: %w", rp.Name(), errno.ErrAICapabilityMismatch)
		}
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
		if _, ok := p.(EmbedProvider); !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support Embed", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			rp := g.lookupProvider(r.Provider.Name)
			if rp == nil {
				return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name)
			}
			embed, ok := rp.(EmbedProvider)
			if !ok {
				return nil, fmt.Errorf("gateway: provider %q does not support Embed: %w", rp.Name(), errno.ErrAICapabilityMismatch)
			}
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
		if _, ok := p.(RerankProvider); !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support Rerank", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			rp := g.lookupProvider(r.Provider.Name)
			if rp == nil {
				return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name)
			}
			rerank, ok := rp.(RerankProvider)
			if !ok {
				return nil, fmt.Errorf("gateway: provider %q does not support Rerank: %w", rp.Name(), errno.ErrAICapabilityMismatch)
			}
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
		if _, ok := p.(OCRProvider); !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support OCR", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			rp := g.lookupProvider(r.Provider.Name)
			if rp == nil {
				return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name)
			}
			ocr, ok := rp.(OCRProvider)
			if !ok {
				return nil, fmt.Errorf("gateway: provider %q does not support OCR: %w", rp.Name(), errno.ErrAICapabilityMismatch)
			}
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
		if _, ok := p.(ASRProvider); !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support ASR", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			rp := g.lookupProvider(r.Provider.Name)
			if rp == nil {
				return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name)
			}
			asr, ok := rp.(ASRProvider)
			if !ok {
				return nil, fmt.Errorf("gateway: provider %q does not support ASR: %w", rp.Name(), errno.ErrAICapabilityMismatch)
			}
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
// ImageGen
// ----------------------------------------------------------------------------

// ImageGen performs a text-to-image generation call for the given taskID.
func (g *Gateway) ImageGen(ctx context.Context, taskID string, req ImageGenRequest) (*ImageGenResponse, error) {
	resp, err := g.resolveAndRun(ctx, taskID, req, func(p Provider, route *registry.ResolvedRoute) (GatewayHandler, error) {
		if _, ok := p.(ImageGenProvider); !ok {
			return nil, fmt.Errorf("gateway: provider %q does not support ImageGen", p.Name())
		}
		return func(ctx context.Context, r *registry.ResolvedRoute, rawReq interface{}) (interface{}, error) {
			rp := g.lookupProvider(r.Provider.Name)
			if rp == nil {
				return nil, fmt.Errorf("gateway: no provider registered for %q", r.Provider.Name)
			}
			img, ok := rp.(ImageGenProvider)
			if !ok {
				return nil, fmt.Errorf("gateway: provider %q does not support ImageGen: %w", rp.Name(), errno.ErrAICapabilityMismatch)
			}
			return img.ImageGen(ctx, r, rawReq.(ImageGenRequest))
		}, nil
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.(*ImageGenResponse), nil
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

// ResolveTask exposes the registry's task-routing resolution to callers that
// need to know which model a taskID will route to *before* issuing the Chat
// call — e.g. compactv2 adapter needs route.Capability.ContextWindow to size
// the prevention threshold (85% / 95%) against the actual model.
//
// Returns the primary route. Does not return the fallback list — callers that
// care about fallback (Fallback middleware does) get it during the actual Chat
// invocation.
//
// NOTE: this is a read-only side-channel into the same routing that Chat /
// ChatStream / Embed / etc go through; the result is consistent with what the
// next Chat call would route to (modulo a ModelOverride from the caller, which
// only kicks in inside the Chat path).
func (g *Gateway) ResolveTask(ctx context.Context, taskID string) (*registry.ResolvedRoute, error) {
	if g.registry == nil {
		return nil, errors.New("gateway: no registry configured")
	}
	primary, _, err := g.registry.ResolveTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("gateway.ResolveTask(%s): %w", taskID, err)
	}
	return primary, nil
}

// ResolveTask is a package-level helper around (*Gateway).ResolveTask using
// the Default() singleton. Returns an error (not panic) when no default has
// been set yet, so callers (e.g. unit tests of components that opportunistically
// read the route) can degrade gracefully.
func ResolveTask(ctx context.Context, taskID string) (*registry.ResolvedRoute, error) {
	g := defaultGateway.Load()
	if g == nil {
		return nil, errors.New("aiservice: default gateway not initialized")
	}
	return g.ResolveTask(ctx, taskID)
}
