package models

import "time"

type AIAnswer struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Question  string    `gorm:"type:text;not null" json:"question"`
	Answer    string    `gorm:"type:text;not null" json:"answer"`
	ArticleID *uint     `gorm:"index" json:"article_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (AIAnswer) TableName() string {
	return "ai_answers"
}
