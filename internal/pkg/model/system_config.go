package model

import "time"

// SystemConfig 系统配置表
type SystemConfig struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Key         string    `gorm:"size:100;uniqueIndex" json:"key"`
	Value       string    `gorm:"type:text" json:"value"`
	Description string    `gorm:"size:255" json:"description"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime" json:"updated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

func (SystemConfig) TableName() string {
	return "system_configs"
}
