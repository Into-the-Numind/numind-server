package model

import (
	"time"

	"gorm.io/gorm"
)

// ProxyServerM 代理服务器表
type ProxyServerM struct {
	gorm.Model
	IPAddress     string     `gorm:"size:50;index;not null" json:"ip_address"`
	Port          int        `gorm:"not null" json:"port"`
	Protocol      string     `gorm:"size:10;default:http" json:"protocol"`
	Username      string     `gorm:"size:100" json:"username"`
	Password      string     `gorm:"size:100" json:"password"`
	Location      string     `gorm:"size:100" json:"location"`
	Status        int        `gorm:"default:1;index" json:"status"`
	LastCheckTime *time.Time `json:"last_check_time"`
	SuccessRate   *int       `json:"success_rate"`
	CheckCount    int        `gorm:"default:0" json:"check_count"`
	SuccessCount  int        `gorm:"default:0" json:"success_count"`
	IsAutoAdded   int        `gorm:"default:0" json:"is_auto_added"`
	Remarks       string     `gorm:"size:255" json:"remarks"`
}

func (ProxyServerM) TableName() string {
	return "proxy_server"
}
