package model

import "time"

type CardShare struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CardID    uint      `gorm:"index" json:"card_id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	ShareURL  string    `gorm:"size:512" json:"share_url"`
	CreatedAt time.Time `json:"created_at"`
}

func (CardShare) TableName() string {
	return "card_shares"
}
