package adapter

import (
	"context"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/httpclient"
)

// gemini_native.go is the Gemini-native adapter: it issues calls in Gemini's
// native generateContent format (POST {BaseURL}/v1beta/models/{model}:generateContent
// ?key={APIKey}) so the implicit-cache token count (usageMetadata.cachedContentTokenCount)
// becomes visible for billing. Gemini implicit caching is automatic on 2.5+; there
// is no explicit cachedContents API on DMXAPI (404), so the cache "toggle" for
// Gemini is simply whether a route is pointed at this adapter at all.
//
// T4 scaffold: identity, two-client http split, compile-time guards, and Chat /
// ChatStream STUBS. The ?key= URL build with the fullURL/redactedURL split
// (finding #4), systemInstruction/contents/tools translation, usage parse (D5),
// SSE streaming, and the stateless functionResponse name recovery (finding #6)
// land in T6.

// Compile-time interface guards (see anthropic_native.go).
var _ aiservice.ChatProvider = (*GeminiNativeAdapter)(nil)
var _ ChatAdapter = (*GeminiNativeAdapter)(nil)

// GeminiNativeAdapter speaks the Gemini generateContent wire format.
type GeminiNativeAdapter struct {
	// client serves non-streaming calls. Mirrors DMXAPIAdapter.client.
	client *httpclient.Client
	// streamClient serves streaming calls (no total request timeout). Caution B
	// of D7. Mirrors DMXAPIAdapter.streamClient.
	streamClient *httpclient.Client
}

// NewGeminiNativeAdapter builds the adapter with the two-client http split
// (copied from dmxapi.go:105-114).
func NewGeminiNativeAdapter() *GeminiNativeAdapter {
	return &GeminiNativeAdapter{
		client:       httpclient.NewClient(httpclient.LLMConfig()),
		streamClient: httpclient.NewClient(httpclient.LLMStreamConfig()),
	}
}

// Name returns the adapter identifier. MUST equal the llm_provider.name of the
// native Gemini provider row and the literal in KnownNativeProviderNames().
// "gemini-native" is chosen so "dmxapi" is NOT a prefix of it (D1 naming-hazard
// mitigation).
func (a *GeminiNativeAdapter) Name() string { return "gemini-native" }

// ProviderType returns the provider category.
func (a *GeminiNativeAdapter) ProviderType() string { return "gemini" }

// Capabilities lists the capabilities this adapter supports.
func (a *GeminiNativeAdapter) Capabilities() []string { return []string{"chat"} }

// Chat is a T4 stub; the Gemini generateContent translation lands in T6.
func (a *GeminiNativeAdapter) Chat(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return nil, errno.ErrAICapabilityMismatch.SetMessage("gemini-native: Chat not implemented yet (T6)")
}

// ChatStream is a T4 stub; the Gemini SSE streaming lands in T6.
func (a *GeminiNativeAdapter) ChatStream(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	return nil, errno.ErrAICapabilityMismatch.SetMessage("gemini-native: ChatStream not implemented yet (T6)")
}
