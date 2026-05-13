package model

import "time"

// CreditReservation 预扣记录
// 状态机：reserved → reconciled | refunded | expired
// 一次 LLM 调用级操作会生成一条 Reservation，Finalize 时切换终态。
// 同一 idempotency_key 唯一（允许 NULL，退化为非幂等）。
// 详见 spec §2.4 / §2.9。
type CreditReservation struct {
	ID              uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID          uint       `gorm:"not null;index:idx_user_status,priority:1" json:"user_id"`
	ReferenceType   string     `gorm:"size:50;not null" json:"reference_type"`
	ReferenceID     string     `gorm:"size:100;not null" json:"reference_id"`
	Operation       string     `gorm:"size:50;not null" json:"operation"`
	ReservedCredits int64      `gorm:"not null" json:"reserved_credits"`
	CoefficientID   *uint64    `gorm:"index:idx_coefficient" json:"coefficient_id"`
	Status          string     `gorm:"type:enum('reserved','reconciled','refunded','expired');not null;default:'reserved';index:idx_user_status,priority:2;index:idx_status_created,priority:1" json:"status"`
	ActualCostCents *int64     `json:"actual_cost_cents,omitempty"`
	Delta           *int64     `json:"delta,omitempty"`
	FinalizeReason  *string    `gorm:"type:enum('normal','op_failed','user_cancelled','provider_timeout','no_actual_cost','expired_by_cron','manual_refund','provider_err','context_budget_refund','nil_stream')" json:"finalize_reason,omitempty"`
	IdempotencyKey  *string    `gorm:"size:64;uniqueIndex:uk_idempotency_key" json:"idempotency_key,omitempty"`
	ReconciledAt    *time.Time `json:"reconciled_at,omitempty"`
	CreatedAt       time.Time  `gorm:"autoCreateTime:milli;index:idx_user_status,priority:3;index:idx_status_created,priority:2" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"autoUpdateTime:milli" json:"updated_at"`

	// Context Budget extension fields (spec §3.6 / feature: context-budget-compression)
	// estimation_source distinguishes legacy R2 coefficient path from new context budget path.
	// New reservations use estimation_source='context_budget' with coefficient_id=NULL.
	EstimationSource          string  `gorm:"size:30;not null;default:'credit_coefficient'" json:"estimation_source"`
	TokenProfileID            *uint64 `gorm:"index:idx_cr_token_profile" json:"token_profile_id"`
	EstimatedPromptTokens     int     `gorm:"not null;default:0" json:"estimated_prompt_tokens"`
	EstimatedCompletionTokens int     `gorm:"not null;default:0" json:"estimated_completion_tokens"`
	Provider                  string  `gorm:"size:50;not null;default:''" json:"provider"`
	Model                     string  `gorm:"size:100;not null;default:''" json:"model"`
	ContextBudgetEventID      *uint64 `gorm:"index:idx_cr_budget_event" json:"context_budget_event_id"`

	// UserTypeMultiplier is the per-user-type burn-rate multiplier snapshotted at
	// Reserve time. Reconcile must apply the same factor to actualCostCents so
	// the delta computation stays consistent with the original reservation.
	// 1.00 = no discount (default for subscription / normal users).
	UserTypeMultiplier float64 `gorm:"column:user_type_multiplier;type:decimal(5,2);not null;default:1.00" json:"user_type_multiplier"`

	// Items 外键关联（应用层保证 FK 到 credit_reservation_item.reservation_id）
	Items []CreditReservationItem `gorm:"foreignKey:ReservationID" json:"items,omitempty"`
}

// TableName 指定表名
func (CreditReservation) TableName() string { return "credit_reservation" }

// CreditReservationItem FIFO 扣减明细（一个 Reservation 可能按 FIFO 扣多个 Package）
// Seq 为 FIFO 扣减顺序号（1, 2, ...），非 INSERT 顺序。
// 唯一索引 uk_reservation_seq 保证同一 reservation 下 seq 唯一。
// 详见 spec §2.5 / §2.9。
type CreditReservationItem struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ReservationID    uint64    `gorm:"not null;index:idx_reservation;uniqueIndex:uk_reservation_seq,priority:1" json:"reservation_id"`
	PackageID        uint64    `gorm:"not null;index:idx_package,priority:1" json:"package_id"`
	Credits          int64     `gorm:"not null" json:"credits"`
	PackageType      string    `gorm:"size:20;not null" json:"package_type"`
	PackageExpiresAt time.Time `gorm:"not null" json:"package_expires_at"`
	Seq              int       `gorm:"not null;uniqueIndex:uk_reservation_seq,priority:2" json:"seq"`
	CreatedAt        time.Time `gorm:"autoCreateTime:milli;index:idx_package,priority:2" json:"created_at"`
}

// TableName 指定表名
func (CreditReservationItem) TableName() string { return "credit_reservation_item" }
