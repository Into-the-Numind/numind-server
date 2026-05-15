package model

import "time"

// CreditAccount 积分账户（1:1 用户，懒创建）
//
// T11 (credits-cleanup): the `balance` column has been dropped from the
// credit_account table. GetBalance now reads from the three-pool SOT:
// credit_cycle (subscription), user_booster_balance (booster), and
// trial_grant (trial). The CreditAccount row is kept for identity/status
// purposes only. See docs/legacy_credit_package_archive_README.md.
type CreditAccount struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    uint      `gorm:"not null;uniqueIndex:uk_credit_account_user" json:"user_id"`
	Status    string    `gorm:"size:20;default:'active'" json:"status"` // active
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (CreditAccount) TableName() string { return "credit_account" }

// T11 (credits-cleanup): CreditPackage struct has been deleted.
// The credit_package table was archived to legacy_credit_package_archive_20260515
// on 2026-05-15 and then dropped. Historical data is preserved for 7 years
// per accounting retention policy. See:
//   - migrations/20260515_200000_t11_archive_credit_package.sql
//   - docs/legacy_credit_package_archive_README.md

// CreditPackage Type constants — retained for ledger source_type vocabulary
// (credit_transaction.source_type values) even after credit_package is dropped.
// These strings appear in credit_transaction rows and archive queries.
const (
	CreditTypeTrial        = "trial"
	CreditTypeSubscription = "subscription"
	CreditTypeBooster      = "booster"
)

// CreditPackage GrantSource constants — retained for archive table queries
// (legacy_credit_package_archive_20260515.grant_source) and membership_event
// source labelling.
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
