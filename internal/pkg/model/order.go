package model

import (
	"fmt"
	"time"
)

// Order 支付订单
type Order struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	OrderNo     string     `gorm:"size:64;not null;uniqueIndex:uk_order_no" json:"order_no"`
	UserID      uint       `gorm:"not null;index:idx_order_user" json:"user_id"`
	PayerID     uint       `gorm:"not null;index:idx_order_payer" json:"payer_id"`
	ProductType string     `gorm:"size:20;not null" json:"product_type"`
	Months      int        `gorm:"not null;default:0" json:"months"`
	Amount      int64      `gorm:"not null" json:"amount"`
	PayChannel  string     `gorm:"size:20" json:"pay_channel"`
	PayStatus   string     `gorm:"size:20;not null;default:'pending'" json:"pay_status"`
	TradeNo     string     `gorm:"size:128" json:"trade_no"`
	CodeURL     string     `gorm:"type:text" json:"code_url"`
	PaidAt      *time.Time `json:"paid_at"`
	ExpiredAt   time.Time  `gorm:"not null" json:"expired_at"`
	CreatedAt   time.Time  `gorm:"index:idx_order_payer" json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (Order) TableName() string { return "payment_order" }

const (
	OrderStatusPending  = "pending"
	OrderStatusPaid     = "paid"
	OrderStatusRefunded = "refunded"
	OrderStatusClosed   = "closed"
)

const (
	ProductTypeTrial   = "trial"
	ProductTypeMonthly = "monthly"
	ProductTypeYearly  = "yearly"
	ProductTypeBooster = "booster"
)

const (
	PayChannelWechat = "wechat"
	PayChannelAlipay = "alipay"
)

type ProductConfig struct {
	Credits  int64
	Duration time.Duration
	Months   int
}

func GetProductConfig(productType string, months int) *ProductConfig {
	switch productType {
	case ProductTypeTrial:
		return &ProductConfig{Credits: 200, Duration: 3 * 24 * time.Hour}
	case ProductTypeMonthly:
		if months < 1 {
			months = 1
		}
		return &ProductConfig{Months: months}
	case ProductTypeYearly:
		return &ProductConfig{Months: 12}
	case ProductTypeBooster:
		return &ProductConfig{Credits: 600, Duration: 90 * 24 * time.Hour}
	default:
		return nil
	}
}

func GetProductAmount(productType string, months int) int64 {
	switch productType {
	case ProductTypeTrial:
		return 990 // ¥9.9
	case ProductTypeMonthly:
		if months < 1 {
			months = 1
		}
		return int64(months) * 9900
	case ProductTypeYearly:
		return 94900
	case ProductTypeBooster:
		// TEMPORARY FOR TESTING — MUST REVERT TO 2990 BEFORE PROD
		// 原价：2990 (¥29.9)。临时改为 1 分便于 dev 扫码联调，测完立即 revert 本 commit。
		return 1
	default:
		return 0
	}
}

func GetProductName(productType string, months int) string {
	switch productType {
	case ProductTypeTrial:
		return "有数AI工作台-体验卡"
	case ProductTypeMonthly:
		if months == 1 {
			return "有数AI工作台-月卡"
		}
		return fmt.Sprintf("有数AI工作台-月卡(%d个月)", months)
	case ProductTypeYearly:
		return "有数AI工作台-年卡"
	case ProductTypeBooster:
		return "有数AI工作台-加量包"
	default:
		return "有数AI工作台"
	}
}
