package model

import (
	"gorm.io/gorm"
)

// CategoryM 分类表
type CategoryM struct {
	gorm.Model
	UserID uint   `gorm:"index;not null" json:"user_id"`          // 用户ID
	Name   string `gorm:"size:50;not null" json:"name"`           // 分类名称
	Color  string `gorm:"size:20;default:'#1890ff'" json:"color"` // 分类颜色
	Sort   int    `gorm:"default:0" json:"sort"`                  // 排序

	// 关联关系
	User  User    `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Books []BookM `gorm:"foreignKey:CategoryID" json:"books,omitempty"`
}

func (CategoryM) TableName() string {
	return "category"
}
