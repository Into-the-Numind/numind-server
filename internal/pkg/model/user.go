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
	OpenID    string `gorm:"uniqueIndex:idx_openid;size:50" json:"openid"`
	UnionID   string `gorm:"uniqueIndex:idx_unionid;size:50;index" json:"unionid"` // 统一使用UnionID作为用户标识
	Phone     string `gorm:"size:20;index" json:"phone"`
	Nickname  string `gorm:"size:100" json:"nickname"`
	AvatarURL string `gorm:"size:512" json:"avatar_url"`
	IsPro     bool   `gorm:"default:false" json:"is_pro"`

	// 客户层级管理字段
	ParentUserID *uint `gorm:"type:int unsigned;index" json:"parent_user_id,omitempty"` // 上级客户ID,NULL表示直接客户
	Parent       *User `gorm:"foreignKey:ParentUserID;references:ID" json:"parent,omitempty"`

	// 会员相关字段
	MembershipType           string     `gorm:"size:20;default:'free';index" json:"membership_type"` // 会员类型：free, subscription, package
	MembershipExpires        *time.Time `gorm:"index" json:"membership_expires"`                     // 会员到期时间
	MembershipStartDate      *time.Time `gorm:"index" json:"membership_start_date"`                  // 会员开始时间（用于计算会员月）
	PackageCount             int        `gorm:"default:0" json:"package_count"`                      // 资源包剩余次数
	BookNum                  int        `gorm:"default:0" json:"book_num"`
	BookAllNum               int64      `gorm:"default:0" json:"book_all_num"`                 // 状态为非failed的书本数量
	MonthlyBookCount         int        `gorm:"default:0" json:"monthly_book_count"`           // 当前会员月内创建的卡册数量
	FreeUserMonthlyBookCount int        `gorm:"default:0" json:"free_user_monthly_book_count"` // 免费用户本月创建的卡册数量
	FreeUserLastResetDate    *time.Time `gorm:"index" json:"free_user_last_reset_date"`        // 免费用户上次重置时间
	CardNum                  int        `gorm:"default:0" json:"card_num"`
	ChatNum                  int        `gorm:"default:0" json:"chat_num"`

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

	// 关联关系
	//Articles        []ArticleM        `gorm:"foreignKey:UserID" json:"articles,omitempty"`
	Favorites []Favorite `gorm:"foreignKey:UserID" json:"favorites,omitempty"`
	Feedbacks []Feedback `gorm:"foreignKey:UserID" json:"feedbacks,omitempty"`
	Images    []ImageM   `gorm:"foreignKey:UserID" json:"images,omitempty"`
	Books     []BookM    `gorm:"foreignKey:UserID" json:"books,omitempty"`
}

func (User) TableName() string {
	return "user"
}

// MembershipType 定义会员类型常量
const (
	MembershipTypeFree         = "free"         // 免费用户
	MembershipTypeSubscription = "subscription" // 订阅会员
	MembershipTypePackage      = "package"      // 付费资源包（次数）
	MembershipTypeBoth         = "both"         // 同时拥有订阅会员和资源包
)

// UserTier 定义用户等级常量（控制SOP运行权限）
const (
	UserTierFree     = "free"     // 免费用户：不可运行SOP
	UserTierStandard = "standard" // 普通会员：每月20次SOP
	UserTierPremium  = "premium"  // 高级会员：无限次SOP
)

// StandardUserMonthlySOPLimit 普通会员每月SOP运行次数上限
const StandardUserMonthlySOPLimit = 20

// IsMembershipActive 检查会员是否有效
func (u *User) IsMembershipActive() bool {
	if u.MembershipType == MembershipTypeFree {
		return false
	}

	if u.MembershipType == MembershipTypePackage {
		return u.PackageCount > 0
	}

	// 订阅会员检查到期时间
	if u.MembershipType == MembershipTypeSubscription && u.MembershipExpires != nil {
		return u.MembershipExpires.After(time.Now())
	}

	// both类型：需要同时满足订阅会员和资源包的条件
	if u.MembershipType == MembershipTypeBoth {
		subscriptionActive := u.MembershipExpires != nil && u.MembershipExpires.After(time.Now())
		packageActive := u.PackageCount > 0
		return subscriptionActive || packageActive // 只要其中一个有效就算有效
	}

	return false
}

// GetActualMembershipType 获取实际的会员类型（考虑过期情况）
// 这个方法会根据当前状态自动判断用户实际应该拥有的会员类型
func (u *User) GetActualMembershipType() string {
	if u.MembershipType == MembershipTypeFree {
		return MembershipTypeFree
	}

	// 检查订阅是否有效
	subscriptionActive := u.MembershipExpires != nil && u.MembershipExpires.After(time.Now())
	// 检查资源包是否有效
	packageActive := u.PackageCount > 0

	if u.MembershipType == MembershipTypeBoth {
		if subscriptionActive && packageActive {
			return MembershipTypeBoth
		} else if subscriptionActive {
			return MembershipTypeSubscription
		} else if packageActive {
			return MembershipTypePackage
		} else {
			return MembershipTypeFree
		}
	}

	if u.MembershipType == MembershipTypeSubscription {
		if subscriptionActive {
			return MembershipTypeSubscription
		}
		// 订阅过期，检查是否有资源包
		if packageActive {
			return MembershipTypePackage
		}
		return MembershipTypeFree
	}

	if u.MembershipType == MembershipTypePackage {
		if packageActive {
			return MembershipTypePackage
		}
		return MembershipTypeFree
	}

	return MembershipTypeFree
}

// GetMembershipStatus 获取会员状态描述
func (u *User) GetMembershipStatus() string {
	if !u.IsMembershipActive() {
		return "免费用户"
	}

	switch u.MembershipType {
	case MembershipTypeSubscription:
		return "订阅会员"
	case MembershipTypePackage:
		return fmt.Sprintf("资源包会员（剩余%d次）", u.PackageCount)
	case MembershipTypeBoth:
		subscriptionActive := u.MembershipExpires != nil && u.MembershipExpires.After(time.Now())
		packageActive := u.PackageCount > 0
		if subscriptionActive && packageActive {
			return fmt.Sprintf("订阅会员+资源包（剩余%d次）", u.PackageCount)
		} else if subscriptionActive {
			return "订阅会员"
		} else if packageActive {
			return fmt.Sprintf("资源包会员（剩余%d次）", u.PackageCount)
		}
		return "免费用户"
	default:
		return "免费用户"
	}
}

// CanUseSubscription 检查是否可以使用订阅会员权益
func (u *User) CanUseSubscription() bool {
	if u.MembershipType == MembershipTypeSubscription || u.MembershipType == MembershipTypeBoth {
		return u.MembershipExpires != nil && u.MembershipExpires.After(time.Now())
	}
	return false
}

// CanUsePackage 检查是否可以使用资源包
func (u *User) CanUsePackage() bool {
	if u.MembershipType == MembershipTypePackage || u.MembershipType == MembershipTypeBoth {
		return u.PackageCount > 0
	}
	return false
}

// GetAvailableUsageCount 获取可用次数（优先返回订阅会员，其次资源包）
func (u *User) GetAvailableUsageCount() (int, string) {
	// 优先使用订阅会员
	if u.CanUseSubscription() {
		return -1, "subscription" // -1 表示无限制
	}

	// 其次使用资源包
	if u.CanUsePackage() {
		return u.PackageCount, "package"
	}

	return 0, "none"
}

// IsInNewMembershipMonth 检查是否进入新的会员月
func (u *User) IsInNewMembershipMonth() bool {
	if u.MembershipStartDate == nil {
		return false
	}

	now := time.Now()
	// 计算当前会员月的开始时间
	currentMonthStart := u.GetCurrentMembershipMonthStart()

	// 如果当前时间已经超过当前会员月，说明进入了新的会员月
	return now.After(currentMonthStart.AddDate(0, 0, 30))
}

// GetCurrentMembershipMonthStart 获取当前会员月的开始时间
func (u *User) GetCurrentMembershipMonthStart() time.Time {
	if u.MembershipStartDate == nil {
		return time.Time{}
	}

	now := time.Now()
	startDate := *u.MembershipStartDate

	// 计算从开始时间到现在过了多少个30天周期
	daysDiff := int(now.Sub(startDate).Hours() / 24)
	monthsPassed := daysDiff / 30

	// 返回当前会员月的开始时间
	return startDate.AddDate(0, 0, monthsPassed*30)
}

// GetCurrentMembershipMonthEnd 获取当前会员月的结束时间
func (u *User) GetCurrentMembershipMonthEnd() time.Time {
	monthStart := u.GetCurrentMembershipMonthStart()
	return monthStart.AddDate(0, 0, 30)
}

// CanCreateBookInCurrentMonth 检查当前会员月内是否可以创建卡册
func (u *User) CanCreateBookInCurrentMonth() bool {
	// 只有订阅会员和both类型才需要检查
	if u.MembershipType != MembershipTypeSubscription && u.MembershipType != MembershipTypeBoth {
		return true
	}

	// 检查是否在会员月内
	if !u.IsMembershipActive() {
		return false
	}

	// 会员无数量限制，直接返回true
	return true
}

// GetRemainingMonthlyBooks 获取当前会员月内剩余可创建的卡册数量
func (u *User) GetRemainingMonthlyBooks() int {
	if u.MembershipType != MembershipTypeSubscription && u.MembershipType != MembershipTypeBoth {
		return -1 // 无限制
	}

	// 会员无数量限制
	return -1
}

// IsInNewFreeUserMonth 检查免费用户是否进入新的日历月
func (u *User) IsInNewFreeUserMonth() bool {
	if u.FreeUserLastResetDate == nil {
		// 如果从未重置过，需要重置
		return true
	}

	now := time.Now()
	lastReset := *u.FreeUserLastResetDate

	// 检查是否跨月了（比较年月）
	return now.Year() != lastReset.Year() || now.Month() != lastReset.Month()
}

// GetCurrentFreeUserMonthStart 获取免费用户当前月的开始时间（每月1号0点）
func (u *User) GetCurrentFreeUserMonthStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
}

// GetCurrentFreeUserMonthEnd 获取免费用户当前月的结束时间（下月1号0点）
func (u *User) GetCurrentFreeUserMonthEnd() time.Time {
	monthStart := u.GetCurrentFreeUserMonthStart()
	return monthStart.AddDate(0, 1, 0)
}

// CanCreateBookAsFreeUser 检查免费用户是否可以创建卡册（月度限制）
func (u *User) CanCreateBookAsFreeUser() bool {
	// 只有免费用户才需要检查月度限制
	if u.MembershipType != MembershipTypeFree {
		return true
	}

	// 检查是否在免费用户月内
	if u.IsInNewFreeUserMonth() {
		// 需要重置计数
		return true
	}

	// 检查月度创建数量限制
	const freeUserMonthlyBookLimit = 5
	return u.FreeUserMonthlyBookCount < freeUserMonthlyBookLimit
}

// GetRemainingFreeUserMonthlyBooks 获取免费用户当前月内剩余可创建的卡册数量
func (u *User) GetRemainingFreeUserMonthlyBooks() int {
	if u.MembershipType != MembershipTypeFree {
		return -1 // 无限制
	}

	const freeUserMonthlyBookLimit = 5
	remaining := freeUserMonthlyBookLimit - u.FreeUserMonthlyBookCount
	if remaining < 0 {
		return 0
	}
	return remaining
}

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
