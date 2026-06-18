package aiservice

import (
	"testing"

	"github.com/spf13/viper"
)

// TestPromptCacheGloballyEnabled verifies the Layer-1 global kill-switch reads
// viper key features.provider_prompt_cache.enabled, and that an absent key reads
// as false (the safe default — prod ships without the key ⇒ caching OFF).
func TestPromptCacheGloballyEnabled(t *testing.T) {
	const key = "features.provider_prompt_cache.enabled"

	// Snapshot + restore so the test does not leak global viper state.
	orig := viper.Get(key)
	t.Cleanup(func() { viper.Set(key, orig) })

	cases := []struct {
		name string
		set  bool
		want bool
	}{
		{name: "enabled", set: true, want: true},
		{name: "disabled", set: false, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viper.Set(key, tc.set)
			if got := PromptCacheGloballyEnabled(); got != tc.want {
				t.Fatalf("PromptCacheGloballyEnabled() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("absent_defaults_false", func(t *testing.T) {
		// Reset to a fresh viper instance so the key is genuinely absent.
		saved := viper.GetViper()
		viper.Reset()
		t.Cleanup(func() {
			// viper has no public swap; restore by re-setting the package singleton
			// state we care about. The global Reset is fine for an isolated unit test,
			// but re-apply the snapshot key so later tests see the original value.
			viper.Set(key, saved.Get(key))
		})
		if got := PromptCacheGloballyEnabled(); got != false {
			t.Fatalf("PromptCacheGloballyEnabled() with absent key = %v, want false", got)
		}
	})
}
