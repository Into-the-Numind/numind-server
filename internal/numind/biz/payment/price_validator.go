package payment

import (
	"context"
	"fmt"
	"numind-server/internal/pkg/log"
)

// PriceValidator 价格验证器
type PriceValidator struct {
	subscriptionPrices map[int64]int // 订阅价格映射：价格(分) -> 天数
}

// NewPriceValidator 创建价格验证器
func NewPriceValidator() *PriceValidator {
	return &PriceValidator{
		subscriptionPrices: map[int64]int{
			1600:  30,  // 月度订阅：16元 -> 30天
			11900: 365, // 年度订阅：119元 -> 365天
		},
	}
}

// ValidateSubscriptionPrice 验证订阅会员价格
func (pv *PriceValidator) ValidateSubscriptionPrice(amount int64) (int, error) {
	days, exists := pv.subscriptionPrices[amount]
	if !exists {
		return 0, fmt.Errorf("无效的订阅价格: %d分，只支持30天(1600分)和365天(11900分)", amount)
	}
	return days, nil
}

// GetSubscriptionPrice 获取订阅价格（服务端计算）
func (pv *PriceValidator) GetSubscriptionPrice(days int) (int64, error) {
	for price, dayCount := range pv.subscriptionPrices {
		if dayCount == days {
			return price, nil
		}
	}
	return 0, fmt.Errorf("无效的订阅天数: %d，只支持30天和365天", days)
}

// GetValidSubscriptionPrices 获取所有有效的订阅价格
func (pv *PriceValidator) GetValidSubscriptionPrices() map[int64]int {
	// 返回副本，防止外部修改
	result := make(map[int64]int)
	for price, days := range pv.subscriptionPrices {
		result[price] = days
	}
	return result
}

// LogPriceValidation 记录价格验证日志
func (pv *PriceValidator) LogPriceValidation(ctx context.Context, membershipType string, amount int64, count int, isValid bool, err error) {
	if isValid {
		log.C(ctx).Infow("价格验证通过",
			"membership_type", membershipType,
			"amount", amount,
			"count", count)
	} else {
		log.C(ctx).Warnw("价格验证失败",
			"membership_type", membershipType,
			"amount", amount,
			"count", count,
			"error", err.Error())
	}
}
