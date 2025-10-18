package model

import (
	"time"

	"gorm.io/gorm"
)

type CardRelImageM struct {
	gorm.Model
	CardID    uint      `gorm:"index" json:"card_id"`
	URL       string    `gorm:"size:512;not null" json:"url"`
	OCRText   string    `gorm:"type:text" json:"ocr_text"`
	CreatedAt time.Time `json:"created_at"`
}

func (CardRelImageM) TableName() string {
	return "card_rel_image"
}
