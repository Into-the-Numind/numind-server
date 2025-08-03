package model

import (
	"time"

	"gorm.io/gorm"
)

// BookM 卡册表
type BookM struct {
	gorm.Model
	UserID       uint       `gorm:"index;not null" json:"user_id"`     // 创建用户ID
	Title        string     `gorm:"size:255;not null" json:"title"`    // 卡册标题
	CategoryID   *uint      `gorm:"index" json:"category_id"`          // 分类ID（可为空）
	CategoryName string     `gorm:"size:100" json:"category_name"`     // 分类名称（兼容旧字段）
	Tags         string     `gorm:"size:255" json:"tags"`              // 标签，逗号分隔
	CardCount    int        `gorm:"default:0" json:"card_count"`       // 卡片数量
	ViewTime     *time.Time `gorm:"type:datetime(3)" json:"view_time"` // 查看时间

	// 关联关系
	User     User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Category *CategoryM `gorm:"foreignKey:CategoryID" json:"category_info,omitempty"`
	Cards    []CardM    `gorm:"foreignKey:BookID" json:"cards,omitempty"`
}

func (BookM) TableName() string {
	return "book"
}
