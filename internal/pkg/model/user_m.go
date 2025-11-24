package model

import (
	"gorm.io/gorm"
)

// UserM 后台管理用户表（用于旧的管理系统，现在主要使用 Admin 模型）
type UserM struct {
	gorm.Model
	Username string `gorm:"size:50;uniqueIndex;not null" json:"username"`
	Password string `gorm:"size:255;not null" json:"-"`
	Nickname string `gorm:"size:100" json:"nickname"`
	Email    string `gorm:"size:100" json:"email"`
	Phone    string `gorm:"size:20" json:"phone"`
	Status   int    `gorm:"default:1" json:"status"`
}

func (UserM) TableName() string {
	return "user_m"
}
