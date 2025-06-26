package model

import (
	"time"
)

// Feedback 用户反馈表
type Feedback struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	Type      string    `gorm:"size:50;not null" json:"type"`
	Status    int       `gorm:"default:0" json:"status"`
	Reply     string    `gorm:"type:text" json:"reply"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`

	// 关联关系
	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (Feedback) TableName() string {
	return "feedbacks"
}
