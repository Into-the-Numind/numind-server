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
	OpenID    string `gorm:"uniqueIndex;size:50" json:"openid"`
	Phone     string `gorm:"size:20;index" json:"phone"`
	Nickname  string `gorm:"size:100" json:"nickname"`
	AvatarURL string `gorm:"size:255" json:"avatar_url"`
	IsPro     bool   `gorm:"default:false" json:"is_pro"`
	// 会员相关字段
	MembershipType    string     `gorm:"size:20;default:'free';index" json:"membership_type"` // 会员类型：free, subscription, package
	MembershipExpires *time.Time `gorm:"index" json:"membership_expires"`                     // 会员到期时间
	PackageCount      int        `gorm:"default:0" json:"package_count"`                      // 资源包剩余次数
	BookNum           int        `gorm:"default:0" json:"book_num"`
	BookAllNum        int64      `gorm:"default:0" json:"book_all_num"` // 状态为非failed的书本数量
	CardNum           int        `gorm:"default:0" json:"card_num"`
	ChatNum           int        `gorm:"default:0" json:"chat_num"`

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
)

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

	return false
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
	default:
		return "免费用户"
	}
}

// CanUseSubscription 检查是否可以使用订阅会员权益
func (u *User) CanUseSubscription() bool {
	return u.MembershipType == MembershipTypeSubscription && u.IsMembershipActive()
}

// CanUsePackage 检查是否可以使用资源包
func (u *User) CanUsePackage() bool {
	return u.MembershipType == MembershipTypePackage && u.PackageCount > 0
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
