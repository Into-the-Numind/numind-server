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

// LLMModel LLM 逻辑模型
type LLMModel struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelKey         string    `gorm:"size:100;not null;uniqueIndex" json:"model_key"`
	DisplayName      string    `gorm:"size:100;not null" json:"display_name"`
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
func (LLMModel) TableName() string { return "llm_model" }

// LLMModelProvider 模型×供应商路由映射
type LLMModelProvider struct {
	ID                 uint64       `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelID            uint64       `gorm:"not null;uniqueIndex:uk_model_provider" json:"model_id"`
	ProviderID         uint64       `gorm:"not null;uniqueIndex:uk_model_provider" json:"provider_id"`
	ProviderModelID    string       `gorm:"size:100;not null" json:"provider_model_id"`
	Priority           int          `gorm:"default:0" json:"priority"`
	InputPricePerMTok  float64      `gorm:"column:input_price_per_mtok;type:decimal(10,4);default:0" json:"input_price_per_mtok"`
	OutputPricePerMTok float64      `gorm:"column:output_price_per_mtok;type:decimal(10,4);default:0" json:"output_price_per_mtok"`
	IsActive           bool         `gorm:"default:true" json:"is_active"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
	Provider           *LLMProvider `gorm:"foreignKey:ProviderID" json:"provider,omitempty"`
	Model              *LLMModel    `gorm:"foreignKey:ModelID" json:"model,omitempty"`
}

// TableName returns the table name for LLMModelProvider.
func (LLMModelProvider) TableName() string { return "llm_model_provider" }

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
