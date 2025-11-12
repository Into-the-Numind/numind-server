package model

import (
	"time"

	"gorm.io/gorm"
)

// Admin 管理员表
type Admin struct {
	gorm.Model
	Username  string     `gorm:"size:50;uniqueIndex;not null" json:"username"`
	Password  string     `gorm:"size:255;not null" json:"-"`
	Nickname  string     `gorm:"size:100" json:"nickname"`
	Email     string     `gorm:"size:100;index" json:"email"`
	Status    int        `gorm:"default:1;comment:状态 0-禁用 1-启用" json:"status"`
	LastLogin *time.Time `json:"last_login"`
	Remark    string     `gorm:"size:255" json:"remark"`
}

func (Admin) TableName() string {
	return "admin"
}

// AdminStatus 管理员状态常量
const (
	AdminStatusDisabled = 0 // 禁用
	AdminStatusEnabled  = 1 // 启用
)
