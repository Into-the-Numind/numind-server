package model

import (
	"time"

	"gorm.io/gorm"
)

// LanguageStyle 用户语言风格
type LanguageStyle struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UserID    uint           `gorm:"uniqueIndex;not null" json:"user_id"`
	Style     string         `gorm:"type:text;not null" json:"style"` // Markdown 格式的分析结果
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (LanguageStyle) TableName() string {
	return "language_styles"
}
