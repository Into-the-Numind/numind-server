package model

import "time"

// AboutUs 关于我们表
type AboutUsM struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Content   string    `gorm:"type:longtext;not null" json:"content"`
	UpdatedAt time.Time `gorm:"autoUpdateTime;not null" json:"updated_at"`
}

func (AboutUsM) TableName() string {
	return "about_us"
}
