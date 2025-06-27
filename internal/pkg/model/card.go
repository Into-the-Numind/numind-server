package model

import "time"

type CardM struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Title     string    `gorm:"size:255" json:"title"`
	Content   string    `gorm:"type:text" json:"content"`
	Images    string    `gorm:"type:text" json:"images"` // 图片URL列表，逗号分隔或JSON
	Tags      string    `gorm:"size:255" json:"tags"`
	Source    string    `gorm:"size:255" json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (CardM) TableName() string {
	return "cards"
}
