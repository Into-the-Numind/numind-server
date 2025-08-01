package model

import (
	"gorm.io/gorm"
)

// BookM 卡册表
type BookM struct {
	gorm.Model
	UserID       uint   `gorm:"index;not null" json:"user_id"`         // 创建用户ID
	Title        string `gorm:"size:255;not null" json:"title"`        // 卡册标题
	Description  string `gorm:"type:text" json:"description"`          // 卡册描述
	Content      string `gorm:"type:text" json:"content"`              // 卡册内容
	CoverURL     string `gorm:"size:512" json:"cover_url"`             // 封面图片URL
	CategoryID   *uint  `gorm:"index" json:"category_id"`              // 分类ID
	CategoryName string `gorm:"size:100" json:"category_name"`         // 分类名称（兼容旧字段）
	Tags         string `gorm:"size:255" json:"tags"`                  // 标签，逗号分隔
	Status       string `gorm:"size:20;default:'draft'" json:"status"` // 状态: draft, published, archived
	IsPublic     bool   `gorm:"default:false" json:"is_public"`        // 是否公开
	CardCount    int    `gorm:"default:0" json:"card_count"`           // 卡片数量
	ViewCount    int    `gorm:"default:0" json:"view_count"`           // 浏览次数
	LikeCount    int    `gorm:"default:0" json:"like_count"`           // 点赞次数

	// 关联关系
	User     User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Category *CategoryM `gorm:"foreignKey:CategoryID" json:"category_info,omitempty"`
	Cards    []CardM    `gorm:"foreignKey:BookID" json:"cards,omitempty"`
}

func (BookM) TableName() string {
	return "book"
}
