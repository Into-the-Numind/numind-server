package payment

import (
	"context"
	"fmt"
	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// PriceValidator 价格验证器
type PriceValidator struct {
	subscriptionPrices map[int64]int // 订阅价格映射：价格(分) -> 天数
}

// NewPriceValidator 创建价格验证器
func NewPriceValidator() *PriceValidator {
	// 检查是否为开发环境（debug模式）
	runmode := viper.GetString("runmode")
	isDev := runmode == "debug"

	prices := make(map[int64]int)
	if isDev {
		// 开发环境：1分钱用于测试（30天和365天都是1分）
		// 注意：由于map的key不能重复，我们只存储1分 -> 30天
		// 在ValidateSubscriptionPrice中会特殊处理365天的情况
		prices[1] = 30 // 月度订阅：1分 -> 30天
		log.C(context.Background()).Infow("价格验证器初始化（开发模式）", "runmode", runmode, "price", 1)
	} else {
		// 生产环境：正常价格
		prices[1600] = 30   // 月度订阅：16元 -> 30天
		prices[11900] = 365 // 年度订阅：119元 -> 365天
	}

	return &PriceValidator{
		subscriptionPrices: prices,
	}
}

// ValidateSubscriptionPrice 验证订阅会员价格
func (pv *PriceValidator) ValidateSubscriptionPrice(amount int64) (int, error) {
	// 检查是否为开发环境
	runmode := viper.GetString("runmode")
	if runmode == "debug" {
		// 开发环境：1分钱，需要根据其他信息判断天数
		// 由于无法从价格区分30天和365天，这里返回30天作为默认值
		// 实际天数应该从payment.SubscriptionDays字段获取
		if amount == 1 {
			return 30, nil // 开发环境默认返回30天，实际天数由SubscriptionDays字段决定
		}
		return 0, fmt.Errorf("无效的订阅价格: %d分，开发环境只支持1分", amount)
	}

	// 生产环境：正常验证
	days, exists := pv.subscriptionPrices[amount]
	if !exists {
		return 0, fmt.Errorf("无效的订阅价格: %d分，只支持30天(1600分)和365天(11900分)", amount)
	}
	return days, nil
}

// GetSubscriptionPrice 获取订阅价格（服务端计算）
func (pv *PriceValidator) GetSubscriptionPrice(days int) (int64, error) {
	// 检查是否为开发环境
	runmode := viper.GetString("runmode")
	if runmode == "debug" {
		// 开发环境：无论多少天都返回1分
		if days == 30 || days == 365 {
			return 1, nil
		}
		return 0, fmt.Errorf("无效的订阅天数: %d，只支持30天和365天", days)
	}

	// 生产环境：正常价格
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
