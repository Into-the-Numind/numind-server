package aiservice

import "github.com/spf13/viper"

// PromptCacheGloballyEnabled reports whether provider-native prompt caching is
// enabled at the GLOBAL level (Layer 1 of the 3-layer cache toggle — the operator
// kill-switch). It reads the viper key `features.provider_prompt_cache.enabled`.
//
// An ABSENT key reads as false: prod ships without the key ⇒ caching stays OFF,
// preserving today's behaviour. dev sets it true (config_dev.yaml). This is the
// single source of truth for the global gate; the native Claude adapter ANDs it with
// the per-model PromptCachePolicy (Layer 2) and the per-call EnablePromptCache
// (Layer 3) before emitting `cache_control`.
func PromptCacheGloballyEnabled() bool {
	return viper.GetBool("features.provider_prompt_cache.enabled")
}
