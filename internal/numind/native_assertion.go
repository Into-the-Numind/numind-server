package numind

import (
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

// assertNativeAdaptersRegistered is the startup guard for the provider-native
// cache adapters (native-cache-adapters spec D1 / review finding #1 — P0).
//
// Threat it closes: the T8 migration inserts permanent llm_provider rows named
// "claude-native"/"gemini-native", but the binary deploy is a SEPARATE,
// non-atomic step. If an ai_service_route points at a native name while the
// deployed binary lacks that adapter in its provider map, the gateway's
// exact-map lookup misses, findAdapterByPrefix fails to prefix-match the native
// name, and the hard-coded dmxapi fallback (gateway.go) silently routes an
// Anthropic / Gemini body to /chat/completions → a malformed 400 or a 200 with
// lost cache tokens. Panic-at-boot is correct here: a half-deployed binary
// serving native bodies to the OAI endpoint is worse than downtime.
//
// Behaviour: for each KnownNativeProviderNames() entry that has an is_active=true
// row in llm_provider, assert the gateway has that adapter registered via the
// EXACT lookup (LookupProviderExact, NO prefix fallback — so the dmxapi fallback
// can never mask a missing native adapter). Returns a non-nil error if any active
// native row lacks its adapter; the caller (numind.go) log.Fatalw's on a non-nil
// return. When NO native rows are active — the default state and the
// deploy-before-activate window — this is a no-op, so it has zero impact on every
// existing deploy.
//
// Returns an error (rather than calling log.Fatalw directly) so it is unit
// testable with an injected gateway + in-memory db; the boot path turns the error
// into a fatal.
func assertNativeAdaptersRegistered(g *aiservice.Gateway, db *gorm.DB) error {
	if g == nil || db == nil {
		// Defensive: a nil gateway/db means startup wiring is broken upstream;
		// nothing to assert here, let the broader startup path surface it.
		return nil
	}

	known := aiservice.KnownNativeProviderNames()
	if len(known) == 0 {
		return nil
	}

	var activeNames []string
	if err := db.Model(&model.LLMProvider{}).
		Where("name IN ? AND is_active = ?", known, true).
		Pluck("name", &activeNames).Error; err != nil {
		// A query failure here must not silently pass the guard: a misconfigured
		// DB could hide an active native row. Surface it as a startup error.
		return fmt.Errorf("assertNativeAdaptersRegistered: query active native providers: %w", err)
	}

	for _, name := range activeNames {
		if g.LookupProviderExact(name) == nil {
			return fmt.Errorf(
				"assertNativeAdaptersRegistered: llm_provider %q is is_active=true but no adapter is registered in this binary "+
					"(half-deploy detected; refusing to start — the dmxapi fallback would silently route a native body to /chat/completions)",
				name,
			)
		}
	}
	return nil
}
