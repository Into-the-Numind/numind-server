package model

import "gorm.io/gorm"

type Template struct {
	gorm.Model
	Name         string `gorm:"size:50;not null;uniqueIndex" json:"name" valid:"required"`
	File         string `gorm:"type:text;not null" json:"file" valid:"required"`
	Preview      string `gorm:"type:text" json:"preview"`                     // 预览图片路径
	IsMemberOnly bool   `gorm:"default:false;not null" json:"is_member_only"` // 是否仅会员可用
}

func (Template) TableName() string {
	return "template"
}
