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

	// TotalSopRuns: ⚠️ DEPRECATED — T2 deprecation (2026-05-18, commit 61926f48) 删了
	// store/customer.go 里的 gorm.Expr("total_sop_runs + ?", 1) increment 函数，此字段
	// 不再被任何代码写入。DB 值是 2026-05-18 前的历史快照，不能作为"当前 SOP 运行次数"。
	//
	// 准确数据：COUNT sop_run 表（store/customer.go GetSubUserRunCounts 已批量实现）。
	//
	// 字段保留是为了向后兼容仍在 marshal 它的 4 处：
	//   - controller/v1/admin_user/user.go:87,133 (admin 后台用户列表/详情)
	//   - controller/v1/user/get.go:64 (用户自查接口)
	//   - biz/agent/tool_learner_data_query.go:50 (agent learner 工具)
	// 这 4 处的迁移列入 tech debt follow-up，迁完后整体 drop 列。新代码禁止读此字段。
	TotalSopRuns int `gorm:"default:0;index" json:"total_sop_runs"`

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
