package adapter

import (
	"context"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// TestClaudeNativeAdapter_Identity verifies the Claude native adapter's identity
// contract: the Name() MUST equal "claude-native" exactly (the gateway keys the
// per-route adapter by Provider.Name and the startup assertion / migration rows
// reference this literal), ProviderType() == "anthropic", and Capabilities() ==
// ["chat"].
func TestClaudeNativeAdapter_Identity(t *testing.T) {
	a := NewClaudeNativeAdapter()
	if a == nil {
		t.Fatal("NewClaudeNativeAdapter returned nil")
	}
	if got := a.Name(); got != "claude-native" {
		t.Errorf("Name()=%q want claude-native", got)
	}
	if got := a.ProviderType(); got != "anthropic" {
		t.Errorf("ProviderType()=%q want anthropic", got)
	}
	caps := a.Capabilities()
	if len(caps) != 1 || caps[0] != "chat" {
		t.Errorf("Capabilities()=%v want [chat]", caps)
	}
}

// TestGeminiNativeAdapter_Identity verifies the Gemini native adapter's identity
// contract: Name() == "gemini-native", ProviderType() == "gemini",
// Capabilities() == ["chat"].
func TestGeminiNativeAdapter_Identity(t *testing.T) {
	a := NewGeminiNativeAdapter()
	if a == nil {
		t.Fatal("NewGeminiNativeAdapter returned nil")
	}
	if got := a.Name(); got != "gemini-native" {
		t.Errorf("Name()=%q want gemini-native", got)
	}
	if got := a.ProviderType(); got != "gemini" {
		t.Errorf("ProviderType()=%q want gemini", got)
	}
	caps := a.Capabilities()
	if len(caps) != 1 || caps[0] != "chat" {
		t.Errorf("Capabilities()=%v want [chat]", caps)
	}
}

// TestNativeAdapters_SatisfyChatProvider is a runtime mirror of the compile-time
// guards: both scaffolds must satisfy aiservice.ChatProvider AND ChatAdapter so
// the gateway registration loop accepts them and lookupProvider returns a usable
// ChatProvider. (The compile-time `var _ ...` lines make this redundant for
// builds, but the runtime assertion documents the contract for readers.)
func TestNativeAdapters_SatisfyChatProvider(t *testing.T) {
	var _ aiservice.ChatProvider = NewClaudeNativeAdapter()
	var _ aiservice.ChatProvider = NewGeminiNativeAdapter()
	var _ ChatAdapter = NewClaudeNativeAdapter()
	var _ ChatAdapter = NewGeminiNativeAdapter()
}

// TestNativeAdapters_ChatStubReturnsError confirms the T4 scaffold contract:
// until T5/T6 land, Chat/ChatStream must return a clean error (not panic, not a
// silent nil) so a mis-activated route fails loudly rather than serving a
// malformed body. Nothing routes to these adapters yet (no active provider row),
// but the stub must be safe if reached.
func TestNativeAdapters_ChatStubReturnsError(t *testing.T) {
	route := &registry.ResolvedRoute{TaskID: "t", Provider: registry.ProviderInfo{Name: "claude-native"}}
	ctx := context.Background()

	for _, a := range []aiservice.ChatProvider{NewClaudeNativeAdapter(), NewGeminiNativeAdapter()} {
		if _, err := a.Chat(ctx, route, aiservice.ChatRequest{}); err == nil {
			t.Errorf("%s.Chat stub returned nil error; want a non-nil stub error", a.Name())
		}
		if _, err := a.ChatStream(ctx, route, aiservice.ChatRequest{}); err == nil {
			t.Errorf("%s.ChatStream stub returned nil error; want a non-nil stub error", a.Name())
		}
	}
}
