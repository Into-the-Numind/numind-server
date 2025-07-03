package model

import (
	"gorm.io/gorm"
)

type CardM struct {
	gorm.Model
	UserID        uint   `gorm:"index;not null" json:"user_id"`              // 创建用户ID
	BookID        uint   `gorm:"index" json:"book_id"`                       // 所属卡册ID
	ImageID       uint   `gorm:"index;not null" json:"image_id"`             // 原始图片ID
	Title         string `gorm:"size:255" json:"title"`                      // 卡片标题
	Content       string `gorm:"type:text" json:"content"`                   // 卡片内容
	OCRText       string `gorm:"type:text" json:"ocr_text"`                  // OCR识别的原始文本
	ProcessedText string `gorm:"type:text" json:"processed_text"`            // AI处理后的文本
	CardType      string `gorm:"size:50;default:'text'" json:"card_type"`    // 卡片类型: text, qa, summary, etc.
	Status        string `gorm:"size:20;default:'processing'" json:"status"` // 状态: processing, completed, failed
	SortOrder     int    `gorm:"default:0" json:"sort_order"`                // 在卡册中的排序
	Tags          string `gorm:"size:255" json:"tags"`                       // 标签，逗号分隔
	Source        string `gorm:"size:255" json:"source"`                     // 来源

	// 关联关系
	User  User   `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Book  BookM  `gorm:"foreignKey:BookID" json:"book,omitempty"`
	Image ImageM `gorm:"foreignKey:ImageID" json:"image,omitempty"`
}

func (CardM) TableName() string {
	return "cards"
}
