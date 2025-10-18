package model

import (
	"gorm.io/gorm"
)

// SystemConfigM 系统配置表
type SystemConfigM struct {
	gorm.Model
	Key         string `gorm:"size:100;uniqueIndex" json:"key"`
	Value       string `gorm:"type:text" json:"value"`
	Description string `gorm:"size:255" json:"description"`
}

func (SystemConfigM) TableName() string {
	return "system_config"
}
