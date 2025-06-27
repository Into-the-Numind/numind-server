package model

import "time"

type CardRelImageM struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CardID    uint      `gorm:"index" json:"card_id"`
	URL       string    `gorm:"size:512;not null" json:"url"`
	OCRText   string    `gorm:"type:text" json:"ocr_text"`
	CreatedAt time.Time `json:"created_at"`
}

func (CardRelImageM) TableName() string {
	return "card_rel_image"
}
