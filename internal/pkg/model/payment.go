package model

import (
	"time"
)

// PaymentM 支付记录模型
type PaymentM struct {
	ID            uint       `json:"id" gorm:"primaryKey"`
	OutTradeNo    string     `json:"out_trade_no" gorm:"uniqueIndex:idx_out_trade_no,length:100;not null;comment:商户订单号"`
	TransactionID string     `json:"transaction_id" gorm:"index:idx_transaction_id,length:100;comment:微信支付订单号"`
	UserID        uint       `json:"user_id" gorm:"index;not null;comment:用户ID"`
	Amount        int64      `json:"amount" gorm:"not null;comment:支付金额(分)"`
	Description   string     `json:"description" gorm:"type:varchar(500);comment:商品描述"`
	Channel       string     `json:"channel" gorm:"type:varchar(20);not null;comment:支付渠道(wechat,alipay)"`
	Status        string     `json:"status" gorm:"type:varchar(20);not null;default:pending;comment:支付状态(pending,success,failed,cancelled)"`
	PayMethod     string     `json:"pay_method" gorm:"type:varchar(20);comment:支付方式(native,miniprogram,jsapi)"`
	OpenID        string     `json:"openid" gorm:"type:varchar(100);comment:用户openid"`
	PrepayID      string     `json:"prepay_id" gorm:"type:varchar(100);comment:预支付ID"`
	CodeURL       string     `json:"code_url" gorm:"type:varchar(500);comment:二维码链接"`
	NotifyData    string     `json:"notify_data" gorm:"type:text;comment:回调数据"`
	PaidAt        *time.Time `json:"paid_at" gorm:"comment:支付时间"`
	ExpireAt      *time.Time `json:"expire_at" gorm:"comment:过期时间"`
	// 会员相关字段
	MembershipType string    `json:"membership_type" gorm:"type:varchar(20);comment:会员类型(monthly,yearly,package)"`
	PackageCount   int       `json:"package_count" gorm:"default:0;comment:包次数"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TableName 指定表名
func (PaymentM) TableName() string {
	return "payments"
}

// PaymentStatus 支付状态常量
const (
	PaymentStatusPending   = "pending"   // 待支付
	PaymentStatusSuccess   = "success"   // 支付成功
	PaymentStatusFailed    = "failed"    // 支付失败
	PaymentStatusCancelled = "cancelled" // 已取消
)

// PaymentChannel 支付渠道常量
const (
	PaymentChannelWechat = "wechat" // 微信支付
	PaymentChannelAlipay = "alipay" // 支付宝
)

// PaymentMethod 支付方式常量
const (
	PaymentMethodNative      = "native"      // 扫码支付
	PaymentMethodMiniProgram = "miniprogram" // 小程序支付
	PaymentMethodJSAPI       = "jsapi"       // JSAPI支付
)

// CreatePaymentRequest 创建支付请求
type CreatePaymentRequest struct {
	OutTradeNo     string `json:"out_trade_no" binding:"required"`
	Description    string `json:"description" binding:"required"`
	Amount         int64  `json:"amount" binding:"required"`
	OpenID         string `json:"openid,omitempty"` // 小程序支付必填
	PayMethod      string `json:"pay_method" binding:"required,oneof=native miniprogram jsapi"`
	MembershipType string `json:"membership_type" binding:"required,oneof=monthly yearly package"` // 会员类型
	PackageCount   int    `json:"package_count,omitempty"`                                         // 包次数（仅当membership_type为package时使用）
}

// CreatePaymentResponse 创建支付响应
type CreatePaymentResponse struct {
	OutTradeNo    string `json:"out_trade_no"`
	TransactionID string `json:"transaction_id,omitempty"`
	PrepayID      string `json:"prepay_id,omitempty"`
	CodeURL       string `json:"code_url,omitempty"`
	PaySign       string `json:"pay_sign,omitempty"`
	TimeStamp     string `json:"time_stamp,omitempty"`
	NonceStr      string `json:"nonce_str,omitempty"`
	Package       string `json:"package,omitempty"`
	SignType      string `json:"sign_type,omitempty"`
}

// PaymentNotifyRequest 支付回调请求
type PaymentNotifyRequest struct {
	OutTradeNo    string `json:"out_trade_no"`
	TransactionID string `json:"transaction_id"`
	Amount        int64  `json:"amount"`
	Status        string `json:"status"`
	PaidAt        string `json:"paid_at"`
	NotifyData    string `json:"notify_data"`
}

// PaymentListRequest 支付列表请求
type PaymentListRequest struct {
	UserID    uint   `form:"user_id"`
	Status    string `form:"status"`
	Channel   string `form:"channel"`
	StartDate string `form:"start_date"`
	EndDate   string `form:"end_date"`
	Page      int    `form:"page,default=1"`
	PageSize  int    `form:"page_size,default=20"`
}

// PaymentListResponse 支付列表响应
type PaymentListResponse struct {
	Total    int64       `json:"total"`
	Payments []*PaymentM `json:"payments"`
}
