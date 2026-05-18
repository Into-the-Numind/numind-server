package model

import (
	"fmt"
	"time"
)

// Order 支付订单。
//
// 字段语义说明：
//   - Months: 月订阅订单（product_type=monthly）中表示购买的月数（1-12）。
//     在 booster 订单中无业务意义，保留为兼容字段（不再被业务代码读取）。
//   - Quantity: booster 订单（product_type=booster）中表示购买的份数，
//     每份 600 积分，有效期 90 天。月订阅/trial 订单此字段无意义，默认为 1。
type Order struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	OrderNo     string     `gorm:"size:64;not null;uniqueIndex:uk_order_no" json:"order_no"`
	UserID      uint       `gorm:"not null;index:idx_order_user" json:"user_id"`
	PayerID     uint       `gorm:"not null;index:idx_order_payer" json:"payer_id"`
	ProductType string     `gorm:"size:20;not null" json:"product_type"`
	Months      int        `gorm:"not null;default:0" json:"months"`   // 月订阅用：购买月数；booster 订单此字段无业务意义（保留兼容）
	Quantity    int        `gorm:"not null;default:1" json:"quantity"` // booster 用：购买份数（每份 600 积分）；非 booster 订单保持默认 1
	Amount      int64      `gorm:"not null" json:"amount"`
	PayChannel  string     `gorm:"size:20" json:"pay_channel"`
	PayStatus   string     `gorm:"size:20;not null;default:'pending'" json:"pay_status"`
	TradeNo     string     `gorm:"size:128" json:"trade_no"`
	CodeURL     string     `gorm:"type:text" json:"code_url"`
	PaidAt      *time.Time `json:"paid_at"`
	ExpiredAt   time.Time  `gorm:"not null" json:"expired_at"`
	// IdempotencyKey is the value of the Idempotency-Key header on POST /v1/orders.
	// Persisted so a double-submitted retry with the same key returns the existing
	// pending order instead of creating a stranded duplicate. Pointer + uniqueIndex
	// allow historical rows to be NULL (MySQL's unique-index treats multiple NULLs
	// as distinct, so back-fill is unnecessary).
	IdempotencyKey *string   `gorm:"column:idempotency_key;size:64;uniqueIndex:uniq_order_idempotency_key" json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `gorm:"index:idx_order_payer" json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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

// GetProductAmount 返回产品金额（分）。
// 对 monthly 类型，months 表示购买月数。
// 对 booster 类型，quantity 参数无效（传 0 即可）；booster 多份金额请用 GetBoosterAmount。
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
		return 2990 // 单份价格（¥29.9）
	default:
		return 0
	}
}

// GetBoosterAmount 返回 booster 订单的总金额（分），quantity 为购买份数。
// quantity < 1 时按 1 份计算。
func GetBoosterAmount(quantity int) int64 {
	if quantity < 1 {
		quantity = 1
	}
	return int64(quantity) * 2990
}

// GetProductName 返回产品展示名称。
// 对 booster 多份订单，使用 GetBoosterProductName。
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

// GetBoosterProductName 返回 booster 订单的展示名称，含购买份数。
func GetBoosterProductName(quantity int) string {
	if quantity <= 1 {
		return "有数AI工作台-加量包"
	}
	return fmt.Sprintf("有数AI工作台-加量包(%d份)", quantity)
}
