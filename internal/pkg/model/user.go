package model

import (
	"time"

	"gorm.io/gorm"
)

// User 用户表
type User struct {
	// 用户相关字段
	gorm.Model
	Phone     string `gorm:"size:20;index" json:"phone"`
	Nickname  string `gorm:"size:100" json:"nickname"`
	AvatarURL string `gorm:"size:512" json:"avatar_url"`

	// 客户层级管理字段
	ParentUserID *uint `gorm:"type:int unsigned;index" json:"parent_user_id,omitempty"` // 上级客户ID,NULL表示直接客户
	Parent       *User `gorm:"foreignKey:ParentUserID;references:ID" json:"parent,omitempty"`

	// SOP运行统计字段
	TotalSopRuns   int        `gorm:"default:0;index" json:"total_sop_runs"` // 总SOP运行次数
	MonthlySopRuns int        `gorm:"default:0" json:"monthly_sop_runs"`     // 当月SOP运行次数
	MonthlyResetAt *time.Time `gorm:"index" json:"monthly_reset_at"`         // 上次月度重置时间

	// 用户等级字段（控制SOP运行权限）
	UserTier    string     `gorm:"size:20;default:'free';index" json:"user_tier"` // 用户等级：free, trial, standard, premium
	TierExpires *time.Time `gorm:"index" json:"tier_expires"`                     // 等级到期时间

	// 计费模式字段（credits-system feature）
	// credits = 新积分制（预扣 / 对账 / 退款机制）
	// 详见 spec §2.2
	BillingMode string `gorm:"column:billing_mode;type:enum('legacy_tier','credits');not null;default:'credits';index:idx_user_billing_mode,priority:1" json:"billing_mode"`

	// 管理员相关字段
	Username  string     `gorm:"size:50;uniqueIndex" json:"username,omitempty"`
	Password  string     `gorm:"size:255" json:"-"`
	IsAdmin   bool       `gorm:"default:false" json:"is_admin,omitempty"`
	Status    int        `gorm:"default:0" json:"status,omitempty"`
	LastLogin *time.Time `json:"last_login,omitempty"`
}

func (User) TableName() string {
	return "user"
}

// BillingMode 定义用户计费模式常量（credits-system feature）
// credits: 积分制（默认）——Reserve/Reconcile 预扣对账 + FIFO 扣减。
// 详见 spec §2.2。
const (
	BillingModeCredits = "credits"
)

// TierChangeLog 等级变更日志
type TierChangeLog struct {
	ID             uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ParentUserID   uint       `gorm:"type:int unsigned;not null;index:idx_parent" json:"parent_user_id"`
	SubUserID      uint       `gorm:"type:int unsigned;not null;index:idx_sub" json:"sub_user_id"`
	OldTier        string     `gorm:"size:20;not null" json:"old_tier"`
	NewTier        string     `gorm:"size:20;not null" json:"new_tier"`
	Months         int        `gorm:"not null;default:1" json:"months"`
	OldTierExpires *time.Time `json:"old_tier_expires"`
	NewTierExpires time.Time  `gorm:"not null" json:"new_tier_expires"`
	CreatedAt      time.Time  `gorm:"not null;index:idx_created" json:"created_at"`
}

func (TierChangeLog) TableName() string {
	return "tier_change_log"
}

// TierRank 返回等级的数值排名（用于升级校验）
// free=0, trial=1, standard=2, premium=3
func TierRank(tier string) int {
	switch tier {
	case "trial":
		return 1
	case "standard":
		return 2
	case "premium":
		return 3
	default:
		return 0
	}
}
