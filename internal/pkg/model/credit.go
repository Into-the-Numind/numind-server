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
	Type          string    `gorm:"size:20;not null" json:"type"`                                           // trial / subscription / booster
	TotalCredits  int64     `gorm:"not null" json:"total_credits"`                                          // 初始积分
	RemainCredits int64     `gorm:"not null" json:"remain_credits"`                                         // 剩余积分
	ActivatedAt   time.Time `gorm:"not null;index:idx_grant_source_granter,priority:3" json:"activated_at"` // 生效时间
	ExpiresAt     time.Time `gorm:"not null;index:idx_cp_user_status_expires" json:"expires_at"`            // 到期时间
	OrderID       *uint64   `gorm:"index:idx_cp_order" json:"order_id"`                                     // 关联支付订单（Admin 手动充值时为 nil）
	Status        string    `gorm:"size:20;not null;index:idx_cp_user_status_expires" json:"status"`        // pending / active / exhausted / expired

	// Q1 B2B2C 会员赋予字段（spec §Q1.1）:
	// GrantSource='b2b_grant' 表示父账户通过"帮开通"赋予（不走支付），
	//             'self_purchase' 表示 C 端自购（加量包路径）。
	// GranterUserID 在 b2b_grant 时必填，指向 parent user ID；self_purchase 为 NULL。
	//
	// 注: DDL 的 MySQL ENUM 约束在 migration 20260420_100000 中定义；此 GORM tag
	// 只声明字段存在性 + 索引，避免 SQLite 测试数据库无法识别 MySQL 的 ENUM 语法。
	GrantSource   string `gorm:"size:20;not null;default:'self_purchase';index:idx_grant_source_granter,priority:1" json:"grant_source"`
	GranterUserID *uint  `gorm:"index:idx_grant_source_granter,priority:2" json:"granter_user_id,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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

// CreditPackage GrantSource constants (Q1 B2B2C)
const (
	GrantSourceSelfPurchase = "self_purchase"
	GrantSourceB2BGrant     = "b2b_grant"
)

// CreditTransaction Operation 前缀。普通扣减 / 退还使用业务 op 名（如
// "sop_run"）；reconcile top-up 补扣使用 "reconcile:" 前缀；reconcile 补扣
// 时余额不足产生的债记账使用 "reconcile_debt:" 前缀（spec §5.3）。
// 查询债记账：WHERE operation LIKE 'reconcile_debt:%'。
const (
	CreditTxOpPrefixReconcile     = "reconcile:"
	CreditTxOpPrefixReconcileDebt = "reconcile_debt:"
)

// CreditTransaction 积分流水
type CreditTransaction struct {
	ID        uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    uint   `gorm:"not null;index:idx_ct_user_created" json:"user_id"`
	PackageID uint64 `gorm:"not null;index:idx_ct_package" json:"package_id"`
	// SourceType identifies which pool this deduction targets.
	// Values: "trial" (trial_grant), "cycle" (credit_cycle), "booster" (user_booster_balance).
	// NULL for legacy credit_package rows and reconcile_debt entries (package_id=0).
	// Added by migration 20260515_100000 (T1).
	SourceType *string `gorm:"column:source_type;size:20;default:null;index:idx_ct_source,priority:1" json:"source_type,omitempty"`
	// SourceID is the PK of the pool row identified by SourceType:
	//   trial   → trial_grant.id
	//   cycle   → credit_cycle.id
	//   booster → user_booster_balance.user_id (the table PK)
	// NULL when SourceType is NULL.
	SourceID      *uint64   `gorm:"column:source_id;default:null;index:idx_ct_source,priority:2" json:"source_id,omitempty"`
	Amount        int64     `gorm:"not null" json:"amount"` // 负数=扣减，正数=退还
	Operation     string    `gorm:"size:100;not null" json:"operation"`
	UsageRecordID *uint64   `json:"usage_record_id"`
	BizRefType    string    `gorm:"size:50" json:"biz_ref_type"`
	BizRefID      string    `gorm:"size:100" json:"biz_ref_id"`
	CreatedAt     time.Time `gorm:"index:idx_ct_user_created" json:"created_at"`
}

// CreditUserTypeConfig per-user-type credit burn-rate multipliers (admin-configurable).
// The multiplier is applied at Reserve time and snapshotted onto credit_reservation.user_type_multiplier.
// Reconcile re-applies the snapshot so delta computation is consistent with the original reservation.
type CreditUserTypeConfig struct {
	UserType         string  `gorm:"primaryKey;size:30" json:"user_type"`                              // trial | subscription | ...
	CreditMultiplier float64 `gorm:"type:decimal(5,2);not null;default:1.00" json:"credit_multiplier"` // <1 = slower burn
	Description      string  `gorm:"size:200;not null;default:''" json:"description"`
	// IsActive: if admin CRUD Create is ever added, apply the UpdateColumn two-step fix (see database.md §6 GORM default:true gotcha).
	IsActive  bool      `gorm:"not null;default:true" json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (CreditUserTypeConfig) TableName() string { return "credit_user_type_config" }

func (CreditTransaction) TableName() string { return "credit_transaction" }
