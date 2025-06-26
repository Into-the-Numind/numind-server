package model

import "time"

// AboutUs 关于我们表
type AboutUs struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Content   string    `gorm:"type:longtext;not null" json:"content"`
	UpdatedAt time.Time `gorm:"autoUpdateTime;not null" json:"updated_at"`
}

func (AboutUs) TableName() string {
	return "about_us"
}
