package payment

import (
	"context"
	"fmt"
	"numind-server/internal/pkg/log"
)

// PriceValidator 价格验证器
type PriceValidator struct {
	subscriptionPrices map[int64]int // 订阅价格映射：价格(分) -> 天数
	packagePrices      map[int]int64 // 资源包价格映射：次数 -> 价格(分)
}

// NewPriceValidator 创建价格验证器
func NewPriceValidator() *PriceValidator {
	return &PriceValidator{
		subscriptionPrices: map[int64]int{
			2800:  30,  // 月度订阅：28元 -> 30天
			19800: 365, // 年度订阅：198元 -> 365天
		},
		packagePrices: map[int]int64{
			1:  300,  // 1次 -> 3元
			5:  1200, // 5次 -> 12元
			20: 3800, // 20次 -> 38元
			50: 5000, // 50次 -> 50元
		},
	}
}

// ValidateSubscriptionPrice 验证订阅会员价格
func (pv *PriceValidator) ValidateSubscriptionPrice(amount int64) (int, error) {
	days, exists := pv.subscriptionPrices[amount]
	if !exists {
		return 0, fmt.Errorf("无效的订阅价格: %d分，只支持30天(2800分)和365天(19800分)", amount)
	}
	return days, nil
}

// ValidatePackagePrice 验证资源包价格
func (pv *PriceValidator) ValidatePackagePrice(count int, amount int64) error {
	expectedAmount, exists := pv.packagePrices[count]
	if !exists {
		return fmt.Errorf("无效的资源包次数: %d，只支持1、5、20、50次", count)
	}

	if amount != expectedAmount {
		return fmt.Errorf("资源包价格不匹配: 期望%d分，实际%d分", expectedAmount, amount)
	}

	return nil
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

// GetPackagePrice 获取资源包价格（服务端计算）
func (pv *PriceValidator) GetPackagePrice(count int) (int64, error) {
	price, exists := pv.packagePrices[count]
	if !exists {
		return 0, fmt.Errorf("无效的资源包次数: %d，只支持1、5、20、50次", count)
	}
	return price, nil
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

// GetValidPackagePrices 获取所有有效的资源包价格
func (pv *PriceValidator) GetValidPackagePrices() map[int]int64 {
	// 返回副本，防止外部修改
	result := make(map[int]int64)
	for count, price := range pv.packagePrices {
		result[count] = price
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
