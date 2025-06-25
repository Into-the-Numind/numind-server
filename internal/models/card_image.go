package models

import "time"

type CardImage struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CardID    uint      `gorm:"index" json:"card_id"`
	URL       string    `gorm:"size:512;not null" json:"url"`
	OCRText   string    `gorm:"type:text" json:"ocr_text"`
	CreatedAt time.Time `json:"created_at"`
}

func (CardImage) TableName() string {
	return "card_images"
}
