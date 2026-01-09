package model

import (
	"gorm.io/gorm"
)

// UserTemplatePermission 用户模板权限表(白名单模式)
type UserTemplatePermission struct {
	gorm.Model
	ParentUserID uint `gorm:"not null;index:idx_parent_sub" json:"parent_user_id"`                                    // 直接客户ID
	SubUserID    uint `gorm:"not null;index:idx_sub_template;uniqueIndex:idx_sub_template_unique" json:"sub_user_id"` // 二级客户ID
	TemplateID   uint `gorm:"not null;index:idx_template;uniqueIndex:idx_sub_template_unique" json:"template_id"`     // 模板ID

	// 关联
	ParentUser *User        `gorm:"foreignKey:ParentUserID;references:ID" json:"parent_user,omitempty"`
	SubUser    *User        `gorm:"foreignKey:SubUserID;references:ID" json:"sub_user,omitempty"`
	Template   *SopTemplate `gorm:"foreignKey:TemplateID;references:ID" json:"template,omitempty"`
}

func (UserTemplatePermission) TableName() string {
	return "user_template_permission"
}
