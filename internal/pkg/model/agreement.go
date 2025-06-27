package model

import "time"

// Agreement 协议表
type Agreement struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Type      string    `gorm:"size:32;uniqueIndex;not null" json:"type"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

func (Agreement) TableName() string {
	return "agreements"
}
