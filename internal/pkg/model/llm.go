package model

import "time"

// LLMProvider LLM 供应商
type LLMProvider struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"size:50;not null;uniqueIndex" json:"name"`
	DisplayName string    `gorm:"size:100;not null" json:"display_name"`
	BaseURL     string    `gorm:"size:255;not null" json:"base_url"`
	APIKey      string    `gorm:"size:255;not null" json:"-"`
	IsActive    bool      `gorm:"default:true" json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName returns the table name for LLMProvider.
func (LLMProvider) TableName() string { return "llm_provider" }

// MaskedAPIKey 返回脱敏的 API key（仅显示后 4 位）
func (p *LLMProvider) MaskedAPIKey() string {
	if len(p.APIKey) <= 4 {
		return "****"
	}
	return "****" + p.APIKey[len(p.APIKey)-4:]
}

// LLMModel is a pure response DTO used by biz/llmrouter to preserve the v3
// ModelSelector response shape after the ai-service-manager migration.
// The llm_model backing VIEW was dropped in migration 20260417_180000_drop_llm_compat_views.sql
// — this struct must NOT be used with GORM (Find/Where/Create/etc.). Construct
// it explicitly from model.AIService rows (see biz/llmrouter/preference.go:80).
// TableName() is kept so any inadvertent GORM use surfaces the dropped-view
// name in logs rather than the GORM-default "llm_models" pluralization.
type LLMModel struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelKey         string    `gorm:"size:100;not null;uniqueIndex" json:"model_key"`
	DisplayName      string    `gorm:"size:100;not null" json:"display_name"`
	ServiceType      string    `gorm:"size:20;column:service_type" json:"service_type,omitempty"` // "llm" for all rows; zero-value when read from VIEW
	IsThinking       bool      `gorm:"default:false" json:"is_thinking"`
	BaseModelID      *uint64   `gorm:"index:idx_base_model" json:"base_model_id"`
	SupportsThinking bool      `gorm:"default:false" json:"supports_thinking"`
	ThinkingOnly     bool      `gorm:"default:false" json:"thinking_only"`
	Icon             string    `gorm:"size:50" json:"icon"`
	SortOrder        int       `gorm:"default:0" json:"sort_order"`
	IsActive         bool      `gorm:"default:true" json:"is_active"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// TableName returns the table name for LLMModel.
// Points to the llm_model VIEW for backwards-compatible reads.
func (LLMModel) TableName() string { return "llm_model" }

// UserModelPreference 用户模型偏好
type UserModelPreference struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    uint      `gorm:"not null;uniqueIndex:uk_user_feature" json:"user_id"`
	Feature   string    `gorm:"size:20;not null;uniqueIndex:uk_user_feature" json:"feature"`
	ModelKey  string    `gorm:"size:100;not null" json:"model_key"`
	Thinking  bool      `gorm:"default:false" json:"thinking"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName returns the table name for UserModelPreference.
func (UserModelPreference) TableName() string { return "user_model_preference" }
