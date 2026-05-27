package model

import (
	"time"

	"gorm.io/datatypes"
)

// AgentDefinition 用户配置的 agent 定义（每个父账户可建多个）。
// is_active 带 gorm:"default:1"（default:true bool 踩坑场景，见 database.md §6）。
// Create 路径必须用 UpdateColumn fixup；Update 路径用 db.Save() 或 Updates(map) 安全。
type AgentDefinition struct {
	ID                   uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	ParentUserID         uint           `gorm:"type:int unsigned;not null;index:idx_ad_parent_active" json:"parent_user_id"`
	Name                 string         `gorm:"size:50;not null" json:"name"`
	Description          string         `gorm:"size:150" json:"description"`
	IconURL              string         `gorm:"size:512" json:"icon_url"`
	WelcomeMessage       string         `gorm:"type:text" json:"welcome_message"`
	Starters             datatypes.JSON `json:"starters"`
	QuestionnaireAnswers datatypes.JSON `json:"questionnaire_answers"`
	GeneratedSkillBody   string         `gorm:"type:text" json:"generated_skill_body"`
	AdvancedMode         bool           `gorm:"type:tinyint(1);not null;default:0" json:"advanced_mode"`
	CustomSkillBody      string         `gorm:"type:text" json:"custom_skill_body"`
	// SystemPrompt 是机构方在 AgentBuilder 写的"行为指引"大文本框内容。
	// 非空时走新 4 段拼装（BuildSystemPromptV2）；空字符串时 fallback 到
	// BuildSystemPromptLegacy（沿用现有 6+ 段拼装逻辑）。
	// 上限：64KB（后端 biz 层校验），DB 列 MEDIUMTEXT 16MB 仅兜底。
	SystemPrompt        string         `gorm:"type:mediumtext;not null;default:''" json:"system_prompt"`
	ToolFlags           datatypes.JSON `json:"tool_flags"`
	CreditCapPerSession *uint          `gorm:"type:int unsigned" json:"credit_cap_per_session"`
	DailyCreditCap      *uint          `gorm:"type:int unsigned" json:"daily_credit_cap"`
	Version             uint           `gorm:"type:int unsigned;not null;default:1" json:"version"`
	IsActive            bool           `gorm:"type:tinyint(1);not null;default:1;index:idx_ad_parent_active" json:"is_active"`
	SourceTemplateID    *uint64        `gorm:"type:bigint unsigned;index:idx_ad_template" json:"source_template_id"`
	CreatedBy           uint           `gorm:"type:int unsigned;not null" json:"created_by"`
	CreatedAt           time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"created_at"`
	UpdatedAt           time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoUpdateTime" json:"updated_at"`
}

func (AgentDefinition) TableName() string { return "agent_definition" }
