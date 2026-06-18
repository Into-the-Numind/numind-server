package aiservice_test

import (
	"testing"

	"numind-server/internal/pkg/aiservice"
)

// TestKnownNativeProviderNames asserts the static source-of-truth list shared by
// the startup assertion and the tests. It MUST contain exactly the two native
// adapter names (claude-native, gemini-native) — the same literals the adapters'
// Name() returns and the T8 migration inserts.
func TestKnownNativeProviderNames(t *testing.T) {
	names := aiservice.KnownNativeProviderNames()
	want := map[string]bool{"claude-native": false, "gemini-native": false}
	if len(names) != len(want) {
		t.Fatalf("KnownNativeProviderNames()=%v want exactly %d entries", names, len(want))
	}
	for _, n := range names {
		if _, ok := want[n]; !ok {
			t.Errorf("unexpected native name %q", n)
		}
		want[n] = true
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("missing native name %q", n)
		}
	}
}

// TestLookupProviderExact_ExactHitVsNil locks the core anti-regression lever
// (spec D1 / finding #1): LookupProviderExact does ONLY the exact-map lookup —
// it must NOT apply the dmxapi prefix fallback. An exact-registered native
// adapter is returned; an unregistered name returns nil (so the startup
// assertion can detect a half-deployed binary and refuse to start, rather than
// the dmxapi fallback silently masking the gap).
func TestLookupProviderExact_ExactHitVsNil(t *testing.T) {
	gw := aiservice.Build(aiservice.Deps{})
	claude := &mockProvider{name: "claude-native"}
	dmxapi := &mockProvider{name: "dmxapi"}
	gw.RegisterProvider(claude)
	gw.RegisterProvider(dmxapi)

	// Exact hit returns the registered adapter, NOT the dmxapi fallback.
	if got := gw.LookupProviderExact("claude-native"); got != aiservice.Provider(claude) {
		t.Errorf("LookupProviderExact(claude-native)=%v want the claude adapter", got)
	}
	// dmxapi itself still resolves exactly.
	if got := gw.LookupProviderExact("dmxapi"); got != aiservice.Provider(dmxapi) {
		t.Errorf("LookupProviderExact(dmxapi)=%v want the dmxapi adapter", got)
	}
	// Unregistered name returns nil — NO prefix fallback to dmxapi.
	if got := gw.LookupProviderExact("gemini-native"); got != nil {
		t.Errorf("LookupProviderExact(gemini-native)=%v want nil (not the dmxapi fallback)", got)
	}
	if got := gw.LookupProviderExact("nonexistent"); got != nil {
		t.Errorf("LookupProviderExact(nonexistent)=%v want nil", got)
	}
}

// TestLookupProviderExact_NoPrefixFallback_Contrast documents the contrast with
// the production lookupProvider path (which DOES fall back to dmxapi for unknown
// names via findAdapterByPrefix). Here we verify that even when a dmxapi adapter
// is registered, an unregistered native name resolves to nil through the EXACT
// helper — the property the startup assertion depends on.
func TestLookupProviderExact_NoPrefixFallback_Contrast(t *testing.T) {
	gw := aiservice.Build(aiservice.Deps{})
	dmxapi := &mockProvider{name: "dmxapi"}
	gw.RegisterProvider(dmxapi)

	// "dmxapi-claude" would prefix-fall-back to dmxapi via findAdapterByPrefix,
	// but the EXACT helper must return nil because it was never registered.
	if got := gw.LookupProviderExact("dmxapi-claude"); got != nil {
		t.Errorf("LookupProviderExact(dmxapi-claude)=%v want nil (exact-only, no prefix fallback)", got)
	}
}
