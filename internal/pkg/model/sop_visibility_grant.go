package model

import "gorm.io/gorm"

// SopVisibilityGrant SOP 可见范围授权表（白名单）.
//
// 与 sop_template.visibility_restricted 短路字段配合使用:
//   - visibility_restricted=false 时, grant 表中的记录被忽略 (D3 保留语义)
//   - visibility_restricted=true  时, 仅 grant 表中 sub_user_id 对应的子用户可见
//
// 唯一索引 (sub_user_id, sop_template_id) 不含 deleted_at, 配合 biz 层
// Unscoped().Delete 物理删模式, 避免软删记录与新插入记录的 NULL 共存冲突.
// 详见 spec §2.2 / §4.1.6 / §12 I-6.
type SopVisibilityGrant struct {
	gorm.Model
	ParentUserID  uint `gorm:"not null;index:idx_svg_parent_sub" json:"parent_user_id"`                                        // 父账户 ID
	SubUserID     uint `gorm:"not null;uniqueIndex:idx_svg_sub_template_unique;index:idx_svg_parent_sub" json:"sub_user_id"`   // 被授权可见的子用户 ID
	SopTemplateID uint `gorm:"not null;uniqueIndex:idx_svg_sub_template_unique;index:idx_svg_template" json:"sop_template_id"` // 受限可见的 SOP 模板 ID

	// 关联
	ParentUser  *User        `gorm:"foreignKey:ParentUserID;references:ID" json:"parent_user,omitempty"`
	SubUser     *User        `gorm:"foreignKey:SubUserID;references:ID" json:"sub_user,omitempty"`
	SopTemplate *SopTemplate `gorm:"foreignKey:SopTemplateID;references:ID" json:"sop_template,omitempty"`
}

// TableName 返回 SopVisibilityGrant 对应的表名.
func (SopVisibilityGrant) TableName() string { return "sop_visibility_grant" }
