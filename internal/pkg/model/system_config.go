package model

import (
	"time"

	"gorm.io/gorm"
)

// SystemConfigM 系统配置表
type SystemConfigM struct {
	gorm.Model
	Key         string    `gorm:"size:100;uniqueIndex" json:"key"`
	Value       string    `gorm:"type:text" json:"value"`
	Description string    `gorm:"size:255" json:"description"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime" json:"updated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

func (SystemConfigM) TableName() string {
	return "system_config"
}
