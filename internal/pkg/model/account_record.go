package model

import (
	"time"

	"gorm.io/gorm"
)

// AccountRecord 账户记录表 - 记录用户的支付历史
type AccountRecord struct {
	gorm.Model
	UserID      uint      `gorm:"index;not null" json:"user_id"`              // 用户ID
	OrderID     uint      `gorm:"index;not null" json:"order_id"`             // 关联的订单ID
	OutTradeNo  string    `gorm:"size:64;not null;index" json:"out_trade_no"` // 微信支付订单号
	Amount      int64     `gorm:"not null" json:"amount"`                     // 支付金额（分）
	AmountYuan  float64   `gorm:"not null" json:"amount_yuan"`                // 支付金额（元）
	Type        string    `gorm:"size:32;not null" json:"type"`               // 记录类型：payment(支付), refund(退款), bonus(赠送)
	Status      string    `gorm:"size:32;not null" json:"status"`             // 状态：success, failed, pending
	Description string    `gorm:"size:255" json:"description"`                // 描述信息
	PaymentAt   time.Time `gorm:"not null" json:"payment_at"`                 // 支付时间
	Channel     string    `gorm:"size:32;not null" json:"channel"`            // 支付渠道：wechat, alipay等
	Remark      string    `gorm:"size:500" json:"remark"`                     // 备注信息

	// 关联关系
	User  User  `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Order Order `gorm:"foreignKey:OrderID" json:"order,omitempty"`
}

func (AccountRecord) TableName() string {
	return "account_records"
}

// BeforeCreate 创建前的钩子函数
func (ar *AccountRecord) BeforeCreate(tx *gorm.DB) error {
	// 如果支付时间为零值，设置为当前时间
	if ar.PaymentAt.IsZero() {
		ar.PaymentAt = time.Now()
	}
	return nil
}
