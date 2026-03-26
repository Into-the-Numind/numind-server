package model

import "time"

// CreditAccount 积分账户（1:1 用户，懒创建）
type CreditAccount struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    uint      `gorm:"not null;uniqueIndex:uk_credit_account_user" json:"user_id"`
	Balance   int64     `gorm:"default:0" json:"balance"`               // 可用积分余额（缓存值）
	Status    string    `gorm:"size:20;default:'active'" json:"status"` // active
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (CreditAccount) TableName() string { return "credit_account" }

// CreditPackage 积分包
type CreditPackage struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID        uint      `gorm:"not null;index:idx_cp_user_status_expires" json:"user_id"`
	Type          string    `gorm:"size:20;not null" json:"type"`                                    // trial / subscription / booster
	TotalCredits  int64     `gorm:"not null" json:"total_credits"`                                   // 初始积分
	RemainCredits int64     `gorm:"not null" json:"remain_credits"`                                  // 剩余积分
	ActivatedAt   time.Time `gorm:"not null" json:"activated_at"`                                    // 生效时间
	ExpiresAt     time.Time `gorm:"not null;index:idx_cp_user_status_expires" json:"expires_at"`     // 到期时间
	OrderID       *uint64   `gorm:"index:idx_cp_order" json:"order_id"`                              // 关联支付订单（Admin 手动充值时为 nil）
	Status        string    `gorm:"size:20;not null;index:idx_cp_user_status_expires" json:"status"` // pending / active / exhausted / expired
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (CreditPackage) TableName() string { return "credit_package" }

// CreditPackage Status constants
const (
	CreditPackagePending   = "pending"
	CreditPackageActive    = "active"
	CreditPackageExhausted = "exhausted"
	CreditPackageExpired   = "expired"
)

// CreditPackage Type constants
const (
	CreditTypeTrial        = "trial"
	CreditTypeSubscription = "subscription"
	CreditTypeBooster      = "booster"
)

// CreditTransaction 积分流水
type CreditTransaction struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID        uint      `gorm:"not null;index:idx_ct_user_created" json:"user_id"`
	PackageID     uint64    `gorm:"not null;index:idx_ct_package" json:"package_id"`
	Amount        int64     `gorm:"not null" json:"amount"` // 负数=扣减，正数=退还
	Operation     string    `gorm:"size:100;not null" json:"operation"`
	UsageRecordID *uint64   `json:"usage_record_id"`
	BizRefType    string    `gorm:"size:50" json:"biz_ref_type"`
	BizRefID      string    `gorm:"size:100" json:"biz_ref_id"`
	CreatedAt     time.Time `gorm:"index:idx_ct_user_created" json:"created_at"`
}

func (CreditTransaction) TableName() string { return "credit_transaction" }
