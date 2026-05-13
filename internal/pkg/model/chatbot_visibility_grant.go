package model

import "gorm.io/gorm"

// ChatbotVisibilityGrant Chatbot 可见范围授权表（白名单）.
//
// 与 chatbot_config.visibility_restricted 短路字段配合使用 (语义对称 SopVisibilityGrant):
//   - visibility_restricted=false 时, grant 表中的记录被忽略 (D3 保留语义)
//   - visibility_restricted=true  时, 仅 grant 表中 sub_user_id 对应的子用户可见
//
// 唯一索引 (sub_user_id, chatbot_id) 不含 deleted_at, 配合 biz 层
// Unscoped().Delete 物理删模式, 避免软删记录与新插入记录的 NULL 共存冲突.
// 详见 spec §2.3 / §4.1.7 / §12 I-6.
type ChatbotVisibilityGrant struct {
	gorm.Model
	ParentUserID uint `gorm:"not null;index:idx_cvg_parent_sub" json:"parent_user_id"`                                     // 父账户 ID
	SubUserID    uint `gorm:"not null;uniqueIndex:idx_cvg_sub_chatbot_unique;index:idx_cvg_parent_sub" json:"sub_user_id"` // 被授权可见的子用户 ID
	ChatbotID    uint `gorm:"not null;uniqueIndex:idx_cvg_sub_chatbot_unique;index:idx_cvg_chatbot" json:"chatbot_id"`     // 受限可见的 chatbot ID

	// 关联
	ParentUser *User          `gorm:"foreignKey:ParentUserID;references:ID" json:"parent_user,omitempty"`
	SubUser    *User          `gorm:"foreignKey:SubUserID;references:ID" json:"sub_user,omitempty"`
	Chatbot    *ChatbotConfig `gorm:"foreignKey:ChatbotID;references:ID" json:"chatbot,omitempty"`
}

// TableName 返回 ChatbotVisibilityGrant 对应的表名.
func (ChatbotVisibilityGrant) TableName() string { return "chatbot_visibility_grant" }
