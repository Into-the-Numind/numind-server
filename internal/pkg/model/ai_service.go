package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// JSONMap is a helper type for JSON columns stored as map[string]interface{}.
type JSONMap map[string]interface{}

// Value implements the driver.Valuer interface for database writes.
// A nil JSONMap writes SQL NULL (not "{}") so that optional JSON columns store
// a true NULL rather than an empty-object string for unset values.
func (j JSONMap) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements the sql.Scanner interface for database reads.
func (j *JSONMap) Scan(val interface{}) error {
	if val == nil {
		*j = JSONMap{}
		return nil
	}
	var bytes []byte
	switch v := val.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("JSONMap: unsupported scan type %T", val)
	}
	return json.Unmarshal(bytes, j)
}

// JSONStringSlice is a helper type for JSON columns stored as []string.
type JSONStringSlice []string

// Value implements the driver.Valuer interface for database writes.
// A nil JSONStringSlice writes SQL NULL (not "[]") so that optional JSON columns
// store a true NULL rather than an empty-array string for unset values.
func (j JSONStringSlice) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	b, err := json.Marshal(j)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// Scan implements the sql.Scanner interface for database reads.
func (j *JSONStringSlice) Scan(val interface{}) error {
	if val == nil {
		*j = JSONStringSlice{}
		return nil
	}
	var bytes []byte
	switch v := val.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("JSONStringSlice: unsupported scan type %T", val)
	}
	return json.Unmarshal(bytes, j)
}

// AIService maps to the ai_service table (renamed from llm_model).
// This is the authoritative struct for all AI service entries across all service types
// (llm | ocr | asr). The legacy LLMModel struct still points to the llm_model VIEW
// for backwards-compatible reads.
type AIService struct {
	ID               uint64          `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelKey         string          `gorm:"size:100;not null;uniqueIndex" json:"model_key"`
	DisplayName      string          `gorm:"size:100;not null" json:"display_name"`
	ServiceType      string          `gorm:"size:20;not null;default:'llm'" json:"service_type"` // llm | ocr | asr
	CapabilityJSON   JSONMap         `gorm:"column:capability_json;type:json" json:"capability_json"`
	LatencyTier      string          `gorm:"size:20;default:'standard'" json:"latency_tier"`
	QualityTier      string          `gorm:"size:20;default:'standard'" json:"quality_tier"`
	Tags             JSONStringSlice `gorm:"column:tags;type:json" json:"tags"`
	DeprecatedAt     *time.Time      `gorm:"default:null" json:"deprecated_at"`
	IsThinking       bool            `gorm:"default:false" json:"is_thinking"`
	BaseModelID      *uint64         `gorm:"index:idx_base_model" json:"base_model_id"`
	SupportsThinking bool            `gorm:"default:false" json:"supports_thinking"`
	ThinkingOnly     bool            `gorm:"default:false" json:"thinking_only"`
	// ThinkingStyle selects HOW thinking is activated on the OpenAI-compatible
	// wire when SupportsThinking && !ThinkingOnly. Empty string is the legacy
	// default and behaves identically to "reasoning_effort" (today's behavior).
	//   "" / "reasoning_effort" → reasoning_effort:"medium"
	//   "enable_thinking_kwarg" → chat_template_kwargs:{"enable_thinking":true} (Qwen/vLLM style)
	//   "none"                  → inject nothing (supports thinking but no activation field)
	ThinkingStyle string `gorm:"size:32;default:''" json:"thinking_style"`
	// PromptCachePolicy is the per-model durable knob (Layer 2 of the 3-layer cache
	// toggle) selecting whether/how provider-native prompt caching is used. Caching
	// economics are a property of the MODEL, so the knob lives on ai_service next to
	// thinking_style. Enum: off | claude_ephemeral | gemini_implicit | auto.
	// `not null;default:'off'` keeps the safe default explicit and prevents NULL ever
	// appearing in admin reads — anything not in {claude_ephemeral, gemini_implicit,
	// auto} means caching stays off. Mirrored onto ResolvedRoute via the same pattern
	// thinking_style follows.
	PromptCachePolicy string    `gorm:"size:16;not null;default:'off'" json:"prompt_cache_policy"`
	Icon              string    `gorm:"size:50" json:"icon"`
	SortOrder         int       `gorm:"default:0" json:"sort_order"`
	IsActive          bool      `gorm:"default:true" json:"is_active"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// TableName returns the table name for AIService.
func (AIService) TableName() string { return "ai_service" }

// ScopeOnlyLLM is a GORM scope that filters ai_service to active LLM entries only.
// Usage: db.Scopes(model.ScopeOnlyLLM).Find(&services)
func ScopeOnlyLLM(db *gorm.DB) *gorm.DB {
	return db.Where("service_type = ? AND deprecated_at IS NULL", "llm")
}

// AIServiceRoute maps to the ai_service_route table (renamed from llm_model_provider).
// Stores provider routing mappings for all service types.
type AIServiceRoute struct {
	ID              uint64       `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelID         uint64       `gorm:"not null;uniqueIndex:uk_model_provider" json:"model_id"`
	ProviderID      uint64       `gorm:"not null;uniqueIndex:uk_model_provider" json:"provider_id"`
	ProviderModelID string       `gorm:"size:100;not null" json:"provider_model_id"`
	Priority        int          `gorm:"default:0" json:"priority"`
	IsActive        bool         `gorm:"default:true" json:"is_active"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	Provider        *LLMProvider `gorm:"foreignKey:ProviderID" json:"provider,omitempty"`
	Service         *AIService   `gorm:"foreignKey:ModelID" json:"service,omitempty"`
}

// TableName returns the table name for AIServiceRoute.
func (AIServiceRoute) TableName() string { return "ai_service_route" }
