package model

import (
	"time"
)

// PaymentAuditLog 支付审计日志
type PaymentAuditLog struct {
	ID               uint       `json:"id" gorm:"primaryKey"`
	OutTradeNo       string     `json:"out_trade_no" gorm:"index;not null;comment:商户订单号"`
	UserID           uint       `json:"user_id" gorm:"index;not null;comment:用户ID"`
	Amount           int64      `json:"amount" gorm:"not null;comment:支付金额(分)"`
	MembershipType   string     `json:"membership_type" gorm:"type:varchar(20);comment:会员类型"`
	PackageCount     int        `json:"package_count" gorm:"default:0;comment:包次数"`
	SubscriptionDays int        `json:"subscription_days" gorm:"default:0;comment:订阅天数"`
	Status           string     `json:"status" gorm:"type:varchar(20);not null;comment:支付状态"`
	TransactionID    string     `json:"transaction_id" gorm:"comment:微信支付订单号"`
	PaidAt           *time.Time `json:"paid_at" gorm:"comment:支付时间"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (PaymentAuditLog) TableName() string {
	return "payment_audit_logs"
}

// PaymentAuditAction 支付审计操作类型
const (
	PaymentAuditActionCreate    = "create"    // 创建支付
	PaymentAuditActionSuccess   = "success"   // 支付成功
	PaymentAuditActionFailed    = "failed"    // 支付失败
	PaymentAuditActionCancelled = "cancelled" // 支付取消
	PaymentAuditActionRefund    = "refund"    // 退款
)

// PaymentAuditLogRequest 创建支付审计日志请求
type PaymentAuditLogRequest struct {
	OutTradeNo       string     `json:"out_trade_no" binding:"required"`
	UserID           uint       `json:"user_id" binding:"required"`
	Amount           int64      `json:"amount" binding:"required"`
	MembershipType   string     `json:"membership_type" binding:"required"`
	PackageCount     int        `json:"package_count,omitempty"`
	SubscriptionDays int        `json:"subscription_days,omitempty"`
	Status           string     `json:"status" binding:"required"`
	TransactionID    string     `json:"transaction_id,omitempty"`
	PaidAt           *time.Time `json:"paid_at,omitempty"`
}
