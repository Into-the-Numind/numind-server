package model

import (
	"time"

	"gorm.io/gorm"
)

// BookM 卡册表
type BookM struct {
	gorm.Model
	UserID       uint       `gorm:"index;not null" json:"user_id"`            // 创建用户ID
	Title        string     `gorm:"size:255;not null" json:"title"`           // 卡册标题
	CategoryID   *uint      `gorm:"index" json:"category_id"`                 // 分类ID（可为空）
	CategoryName string     `gorm:"size:100" json:"category_name"`            // 分类名称（兼容旧字段）
	TemplateID   string     `gorm:"size:50" json:"template_id"`               // 模板ID
	Tags         string     `gorm:"size:255" json:"tags"`                     // 标签，逗号分隔
	CardCount    int        `gorm:"default:0" json:"card_count"`              // 卡片数量
	ViewTime     *time.Time `gorm:"type:datetime(3)" json:"view_time"`        // 查看时间
	ImageUrl     string     `gorm:"size:255" json:"image_url"`                // 封面图片URL
	Status       string     `gorm:"size:20;default:'creating'" json:"status"` // 创建状态：creating, success, failed

	// 关联关系
	//User     User       `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Category *CategoryM `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	// Cards    []CardM    `gorm:"foreignKey:BookID" json:"cards,omitempty"`
}

func (BookM) TableName() string {
	return "book"
}

// GetCategoryName 获取分类名称
func (b *BookM) GetCategoryName() string {
	if b.Category != nil {
		return b.Category.Name
	}
	return b.CategoryName
}

// BookStatus 定义book状态常量
const (
	BookStatusCreating = "creating" // 创建中
	BookStatusSuccess  = "success"  // 创建成功
	BookStatusFailed   = "failed"   // 创建失败
)
