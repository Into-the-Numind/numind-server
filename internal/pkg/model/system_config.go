package model

import (
	"gorm.io/gorm"
)

// SystemConfigM 系统配置表
type SystemConfigM struct {
	gorm.Model
	Key         string `gorm:"size:100;uniqueIndex" json:"key"`
	Title       string `gorm:"size:100" json:"title"` // 配置标题，用于后台显示
	Value       string `gorm:"type:text" json:"value"`
	Description string `gorm:"size:255" json:"description"` // 配置详细描述
}

func (SystemConfigM) TableName() string {
	return "system_config"
}
