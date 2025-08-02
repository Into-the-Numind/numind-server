package model

import (
	"gorm.io/gorm"
)

type CardM struct {
	gorm.Model
	UserID        uint   `gorm:"index;not null" json:"user_id"`   // 创建用户ID
	BookID        uint   `gorm:"index" json:"book_id"`            // 所属卡册ID
	ImageID       uint   `gorm:"index;not null" json:"image_id"`  // 原始图片ID
	OCRText       string `gorm:"type:text" json:"ocr_text"`       // OCR识别的原始文本
	ProcessedText string `gorm:"type:text" json:"processed_text"` // AI处理后的文本
	SortOrder     int    `gorm:"default:0" json:"sort_order"`     // 在卡册中的排序
	Tags          string `gorm:"size:255" json:"tags"`            // 标签，逗号分隔

	// 关联关系
	User  User   `gorm:"foreignKey:UserID;constraintName:fk_card_user_new" json:"user,omitempty"`
	Book  BookM  `gorm:"foreignKey:BookID;constraintName:fk_card_book_new" json:"book,omitempty"`
	Image ImageM `gorm:"foreignKey:ImageID;constraintName:fk_card_image_new" json:"image,omitempty"`
}

func (CardM) TableName() string {
	return "cards"
}
