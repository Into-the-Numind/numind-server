package adapter

import (
	"context"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/httpclient"
)

// anthropic_native.go is the Claude-native adapter: it issues calls in
// Anthropic's native Messages format (POST {BaseURL}/v1/messages) so cache_control
// can be attached to a stable system-block prefix and the response cache token
// buckets (cache_creation_input_tokens / cache_read_input_tokens) become visible
// for billing — neither of which the OpenAI-compatible /chat/completions path can
// surface.
//
// T4 scaffold: identity, two-client http split, compile-time guards, and Chat /
// ChatStream STUBS that return a clean error. Nothing routes to this adapter yet
// (the T8 migration inserts its llm_provider row with is_active=0, and activation
// is a separate manual op after /healthz/ai confirms registration). The full
// request build, response parse, SSE streaming, tool translation, and the
// 3-layer cache_control gating land in T5.

// Compile-time interface guards. These prove the struct SATISFIES the interfaces
// (D1 belt-and-suspenders against a missing-registration silent degrade); the
// runtime startup assertion (assertNativeAdaptersRegistered) proves it was
// actually registered into the running gateway.
var _ aiservice.ChatProvider = (*ClaudeNativeAdapter)(nil)
var _ ChatAdapter = (*ClaudeNativeAdapter)(nil)

// ClaudeNativeAdapter speaks the Anthropic Messages wire format.
type ClaudeNativeAdapter struct {
	// client serves non-streaming calls (one-shot body — the 600s LLMConfig total
	// timeout is a safe cap). Mirrors DMXAPIAdapter.client.
	client *httpclient.Client
	// streamClient serves streaming calls with LLMStreamConfig (no total request
	// timeout) so a long Claude thinking stream is not cut at 600s (prod incident
	// 2026-06-16). Caution B of D7. Mirrors DMXAPIAdapter.streamClient.
	streamClient *httpclient.Client
}

// NewClaudeNativeAdapter builds the adapter with the same two-client http split
// the dmxapi adapter uses (copied from dmxapi.go:105-114).
func NewClaudeNativeAdapter() *ClaudeNativeAdapter {
	return &ClaudeNativeAdapter{
		client:       httpclient.NewClient(httpclient.LLMConfig()),
		streamClient: httpclient.NewClient(httpclient.LLMStreamConfig()),
	}
}

// Name returns the adapter identifier. MUST equal the llm_provider.name of the
// native Claude provider row and the literal in KnownNativeProviderNames() — the
// gateway keys the per-route adapter on Provider.Name. "claude-native" is chosen
// so "dmxapi" is NOT a prefix of it (D1 naming-hazard mitigation).
func (a *ClaudeNativeAdapter) Name() string { return "claude-native" }

// ProviderType returns the provider category.
func (a *ClaudeNativeAdapter) ProviderType() string { return "anthropic" }

// Capabilities lists the capabilities this adapter supports.
func (a *ClaudeNativeAdapter) Capabilities() []string { return []string{"chat"} }

// Chat is a T4 stub; the Anthropic Messages request/response translation lands in
// T5. Returns a clean error so a (not-yet-possible) mis-activated route fails
// loudly rather than serving a malformed body.
func (a *ClaudeNativeAdapter) Chat(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return nil, errno.ErrAICapabilityMismatch.SetMessage("claude-native: Chat not implemented yet (T5)")
}

// ChatStream is a T4 stub; the Anthropic SSE streaming lands in T5.
func (a *ClaudeNativeAdapter) ChatStream(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	return nil, errno.ErrAICapabilityMismatch.SetMessage("claude-native: ChatStream not implemented yet (T5)")
}
