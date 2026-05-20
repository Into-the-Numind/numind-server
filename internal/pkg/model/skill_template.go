package model

import (
	"time"

	"gorm.io/datatypes"
)

// SkillTemplate 平台预置技能模板，所有父账户共享（无 parent_user_id）。
// is_active 带 gorm:"default:1"（default:true bool 踩坑场景，见 database.md §6）。
// CRUD 端点只暴露 GET（list / by-id）；不暴露 POST/PATCH/DELETE。
type SkillTemplate struct {
	ID                   uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Name                 string         `gorm:"size:50;not null" json:"name"`
	Description          string         `gorm:"size:300" json:"description"`
	IconURL              string         `gorm:"size:512" json:"icon_url"`
	CategoryTags         datatypes.JSON `json:"category_tags"`
	QuestionnaireAnswers datatypes.JSON `gorm:"not null" json:"questionnaire_answers"`
	DefaultToolFlags     datatypes.JSON `json:"default_tool_flags"`
	DisplayOrder         int            `gorm:"not null;default:100;index:idx_st_active_order,priority:2" json:"display_order"`
	IsActive             bool           `gorm:"type:tinyint(1);not null;default:1;index:idx_st_active_order,priority:1" json:"is_active"`
	CreatedAt            time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime" json:"created_at"`
	UpdatedAt            time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP;autoUpdateTime" json:"updated_at"`
}

func (SkillTemplate) TableName() string { return "skill_template" }
