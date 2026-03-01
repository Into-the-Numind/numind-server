package model

import (
	"fmt"
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
	UserTier    string     `gorm:"size:20;default:'free';index" json:"user_tier"` // 用户等级：free, standard, premium
	TierExpires *time.Time `gorm:"index" json:"tier_expires"`                     // 等级到期时间

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

// UserTier 定义用户等级常量（控制SOP运行权限）
const (
	UserTierFree     = "free"     // 免费用户：不可运行SOP
	UserTierStandard = "standard" // 普通会员：每月20次SOP
	UserTierPremium  = "premium"  // 高级会员：无限次SOP
)

// StandardUserMonthlySOPLimit 普通会员每月SOP运行次数上限
const StandardUserMonthlySOPLimit = 20

// ============================================================================
// SOP 运行权限相关方法（用户等级控制）
// ============================================================================

// GetActualUserTier 获取实际的用户等级（考虑过期自动降级）
// 如果会员已过期，返回 UserTierFree
func (u *User) GetActualUserTier() string {
	// 如果是免费用户，直接返回
	if u.UserTier == "" || u.UserTier == UserTierFree {
		return UserTierFree
	}

	// 检查是否过期
	if u.TierExpires != nil && u.TierExpires.Before(time.Now()) {
		return UserTierFree // 已过期，降级为免费用户
	}

	return u.UserTier
}

// CanRunSOP 检查用户是否可以运行SOP
// 返回值：是否可运行，不可运行时的原因
func (u *User) CanRunSOP() (bool, string) {
	actualTier := u.GetActualUserTier()

	switch actualTier {
	case UserTierFree:
		return false, "免费用户无法运行SOP，请升级为会员"

	case UserTierStandard:
		// 检查是否过期
		if u.TierExpires != nil && u.TierExpires.Before(time.Now()) {
			return false, "会员已过期，请续费"
		}
		// 检查是否需要重置月度次数（跨自然月）
		if u.IsInNewSOPMonth() {
			// 需要重置，说明本月还未使用过，可以运行
			return true, ""
		}
		// 检查月度运行次数限制
		if u.MonthlySopRuns >= StandardUserMonthlySOPLimit {
			return false, fmt.Sprintf("本月运行次数已达上限（%d次），请升级为高级会员或等待下月重置", StandardUserMonthlySOPLimit)
		}
		return true, ""

	case UserTierPremium:
		// 检查是否过期
		if u.TierExpires != nil && u.TierExpires.Before(time.Now()) {
			return false, "会员已过期，请续费"
		}
		return true, ""

	default:
		return false, "未知用户等级"
	}
}

// GetRemainingSOPRuns 获取用户剩余可运行SOP次数
// 返回值：剩余次数（-1 表示无限次，0 表示无法运行）
func (u *User) GetRemainingSOPRuns() int {
	actualTier := u.GetActualUserTier()

	switch actualTier {
	case UserTierFree:
		return 0

	case UserTierStandard:
		// 检查是否已过期
		if u.TierExpires != nil && u.TierExpires.Before(time.Now()) {
			return 0
		}
		// 如果跨月了，返回满额次数（IncrementSopRunCount 会自动重置）
		if u.IsInNewSOPMonth() {
			return StandardUserMonthlySOPLimit
		}
		// 计算剩余次数
		remaining := StandardUserMonthlySOPLimit - u.MonthlySopRuns
		if remaining < 0 {
			return 0
		}
		return remaining

	case UserTierPremium:
		// 检查是否已过期
		if u.TierExpires != nil && u.TierExpires.Before(time.Now()) {
			return 0
		}
		return -1 // 无限次

	default:
		return 0
	}
}

// IsInNewSOPMonth 检查是否已进入新的30天周期（需要重置月度SOP运行次数）
// 周期从用户成为会员那天开始计算，每30天重置一次
func (u *User) IsInNewSOPMonth() bool {
	if u.MonthlyResetAt == nil {
		// 从未重置过，需要重置
		return true
	}

	now := time.Now()
	lastReset := *u.MonthlyResetAt

	// 检查是否已过30天周期
	return now.After(lastReset.AddDate(0, 0, 30))
}

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
// free=0, standard=1, premium=2
func TierRank(tier string) int {
	switch tier {
	case UserTierStandard:
		return 1
	case UserTierPremium:
		return 2
	default:
		return 0
	}
}

// GetUserTierDisplayName 获取用户等级的显示名称
func (u *User) GetUserTierDisplayName() string {
	actualTier := u.GetActualUserTier()

	switch actualTier {
	case UserTierFree:
		return "免费用户"
	case UserTierStandard:
		return "普通会员"
	case UserTierPremium:
		return "高级会员"
	default:
		return "免费用户"
	}
}
