package aiservice

import "github.com/spf13/viper"

// ProviderConfig holds the bootstrap credentials for a single AI provider.
// These values are read once at startup by SyncProviderCredentials and written
// into the llm_provider DB table.  At runtime the Gateway reads from the DB
// (single source of truth); config is only used for the initial seed.
type ProviderConfig struct {
	// APIKey is the provider API key (required for most providers).
	APIKey string `mapstructure:"api_key"`
	// SecretKey is an additional secret (used by Baidu OCR).
	SecretKey string `mapstructure:"secret_key"`
	// BaseURL overrides the default endpoint URL stored in the DB row.
	BaseURL string `mapstructure:"base_url"`
	// WorkspaceID is used by Alibaba Bailian file service.
	WorkspaceID string `mapstructure:"workspace_id"`
}

// AIProvidersConfig holds bootstrap credentials for all supported AI providers.
// Corresponds to the ai_providers: section in config_*.yaml (spec §11.2).
//
// Actual config values are set by Task 15a; this struct is defined now so that
// SyncProviderCredentials can type-check against it and the app can start without
// error even when the section is absent (all fields zero-value).
type AIProvidersConfig struct {
	Ali     ProviderConfig `mapstructure:"ali"`
	Volc    ProviderConfig `mapstructure:"volc"`
	DMXAPI  ProviderConfig `mapstructure:"dmxapi"`
	Baidu   ProviderConfig `mapstructure:"baidu"`
	Bailian ProviderConfig `mapstructure:"bailian"`
	FunASR  ProviderConfig `mapstructure:"funasr"`
}

// LoadAIProvidersConfig reads the ai_providers section from the global Viper instance.
// If the section is absent all fields are zero-value (empty strings), which causes
// SyncProviderCredentials to skip those providers gracefully.
func LoadAIProvidersConfig() *AIProvidersConfig {
	var cfg AIProvidersConfig
	if err := viper.UnmarshalKey("ai_providers", &cfg); err != nil {
		// Non-fatal: return zero-value config rather than propagating.
		return &AIProvidersConfig{}
	}
	return &cfg
}
