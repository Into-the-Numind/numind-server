package aiservice

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/viper"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// providerSeedEntry describes a single provider to seed from config into DB.
type providerSeedEntry struct {
	// name is the llm_provider.name value (must match the row inserted by migration).
	name string
	// cfgKeyAPIKey is the viper path for this provider's api_key.
	cfgKeyAPIKey string
	// cfgKeyBaseURL is the viper path for this provider's base_url (optional).
	cfgKeyBaseURL string
	// cfgKeySecretKey is the viper path for an additional secret key (Baidu).
	// When non-empty, it is appended to the api_key field as ":secret_key" to
	// keep both values in the existing single-column schema.
	cfgKeySecretKey string
	// cfgKeyWorkspaceID is the viper path for a workspace / tenant ID (Bailian).
	// When non-empty, it is appended to the base_url field as "|workspace=ID" so
	// that the value is preserved in the DB for future adapter use (Task 9-11).
	cfgKeyWorkspaceID string
}

// providerSeedEntries lists all providers whose credentials are synced from
// config (§11.2) into the llm_provider table at startup.
//
// Provider name values must match the rows inserted by migration
// 20260416_000001_ai_service_manager.sql (see spec §2.4).
var providerSeedEntries = []providerSeedEntry{
	{
		name:          "ali-dashscope",
		cfgKeyAPIKey:  "ai_providers.ali.api_key",
		cfgKeyBaseURL: "ai_providers.ali.base_url",
	},
	{
		name:          "volc-ark",
		cfgKeyAPIKey:  "ai_providers.volc.api_key",
		cfgKeyBaseURL: "ai_providers.volc.base_url",
	},
	{
		name:          "dmxapi",
		cfgKeyAPIKey:  "ai_providers.dmxapi.api_key",
		cfgKeyBaseURL: "ai_providers.dmxapi.base_url",
	},
	{
		name:            "baidu-ocr",
		cfgKeyAPIKey:    "ai_providers.baidu.api_key",
		cfgKeySecretKey: "ai_providers.baidu.secret_key",
	},
	{
		name:          "bailian-file",
		cfgKeyAPIKey:  "ai_providers.bailian.api_key",
		cfgKeyBaseURL: "ai_providers.bailian.base_url",
		// workspace_id is stored in base_url as "|workspace=<id>" suffix so
		// it survives in the DB without a schema change.  Adapters that need it
		// (Tasks 9-11) should parse it from base_url using strings.Split("|").
		cfgKeyWorkspaceID: "ai_providers.bailian.workspace_id",
	},
	{
		name:          "funasr-local",
		cfgKeyBaseURL: "ai_providers.funasr.base_url",
	},
	{
		// 云 ASR(小红书视频转写)：复用 ali 的 DashScope api_key；base_url 不同步（保留 migration
		// 固定的 https://dashscope.aliyuncs.com，ali.base_url 是 compatible-mode 不适用于录音文件识别）。
		name:         "dashscope-asr",
		cfgKeyAPIKey: "ai_providers.ali.api_key",
	},
}

// SyncProviderCredentials syncs provider API keys from application config into the
// llm_provider table (DB). This function is intended to be called once at service
// startup, after the DB connection is established but before routes are registered.
//
// Design rationale (spec §2.6):
//   - Migration SQL inserts provider rows with empty api_key strings.
//   - On each startup, this function UPSERTs api_key values from config into the DB.
//   - This makes key rotation easy: update config → restart service → keys are live.
//   - The DB is the single source of truth at runtime; config is only read at startup.
//
// Failure behaviour: errors are logged but do NOT prevent service startup.
// The /healthz/ai endpoint exposes seed status for monitoring.
//
// Config absence is handled gracefully: if the ai_providers section is absent
// or a particular provider key is empty, that provider is skipped with a WARN log.
func SyncProviderCredentials(ctx context.Context, db *gorm.DB, cfg *viper.Viper) error {
	if db == nil {
		log.Warnw("[aiservice] SyncProviderCredentials: db is nil, skipping")
		return nil
	}
	if cfg == nil {
		return fmt.Errorf("SyncProviderCredentials: cfg required")
	}

	synced := 0
	skipped := 0

	for _, entry := range providerSeedEntries {
		apiKey := cfg.GetString(entry.cfgKeyAPIKey)
		secretKey := ""
		if entry.cfgKeySecretKey != "" {
			secretKey = cfg.GetString(entry.cfgKeySecretKey)
		}
		baseURL := ""
		if entry.cfgKeyBaseURL != "" {
			baseURL = cfg.GetString(entry.cfgKeyBaseURL)
		}
		// Append workspace_id to base_url as "|workspace=<id>" so the value is
		// persisted in the DB without a schema change.  Downstream adapters that
		// require it (Tasks 9-11) should parse it via strings.SplitN(baseURL, "|", 2).
		if entry.cfgKeyWorkspaceID != "" {
			if wsID := cfg.GetString(entry.cfgKeyWorkspaceID); wsID != "" {
				baseURL = strings.TrimRight(baseURL, "/") + "|workspace=" + wsID
			}
		}

		// For Baidu: encode both keys into a single api_key field as "key:secret".
		combinedKey := apiKey
		if secretKey != "" {
			combinedKey = apiKey + ":" + secretKey
		}

		// If both key and base_url are empty for this provider, skip with a warning.
		if combinedKey == "" && baseURL == "" {
			log.Warnw("[aiservice] SyncProviderCredentials: no credentials configured for provider, skipping",
				"provider", entry.name,
				"api_key_path", entry.cfgKeyAPIKey,
			)
			skipped++
			continue
		}

		// Build update map — only set non-empty values to avoid overwriting existing data
		// with empty strings when a key is intentionally omitted.
		updates := map[string]interface{}{}
		if combinedKey != "" {
			updates["api_key"] = combinedKey
		}
		if baseURL != "" {
			updates["base_url"] = baseURL
		}

		// UPSERT: update api_key (and optionally base_url) for existing rows only.
		// We don't INSERT new rows here — that is migration's responsibility.
		// Using Clauses(clause.OnConflict) on Name (uniqueIndex) for idempotency.
		result := db.WithContext(ctx).
			Model(&model.LLMProvider{}).
			Where("name = ?", entry.name).
			Updates(updates)

		if result.Error != nil {
			log.Warnw("[aiservice] SyncProviderCredentials: failed to update provider",
				"provider", entry.name,
				"error", result.Error,
			)
			// Non-fatal — log and continue with remaining providers.
			continue
		}

		if result.RowsAffected == 0 {
			// Row doesn't exist yet (migration not applied or name mismatch).
			// Attempt an INSERT so seed works even when migration runs inline.
			provider := &model.LLMProvider{
				Name:    entry.name,
				APIKey:  combinedKey,
				BaseURL: baseURL,
			}
			if insertErr := db.WithContext(ctx).
				Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "name"}},
					DoUpdates: clause.Assignments(updates),
				}).
				Create(provider).Error; insertErr != nil {
				log.Warnw("[aiservice] SyncProviderCredentials: failed to upsert provider",
					"provider", entry.name,
					"error", insertErr,
				)
				continue
			}
		}

		log.Infow("[aiservice] SyncProviderCredentials: synced provider", "provider", entry.name)
		synced++
	}

	log.Infow("[aiservice] SyncProviderCredentials: completed",
		"synced", synced,
		"skipped", skipped,
	)
	return nil
}
