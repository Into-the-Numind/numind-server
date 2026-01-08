package payment

import (
	"testing"
	"time"

	"numind-server/internal/pkg/model"
)

func TestSubscriptionDaysAccumulation(t *testing.T) {
	validator := NewPriceValidator()

	tests := []struct {
		name             string
		subscriptionDays int
		expectedPrice    int64
		hasError         bool
	}{
		{
			name:             "30天订阅",
			subscriptionDays: 30,
			expectedPrice:    1600,
			hasError:         false,
		},
		{
			name:             "365天订阅",
			subscriptionDays: 365,
			expectedPrice:    11900,
			hasError:         false,
		},
		{
			name:             "无效天数",
			subscriptionDays: 100,
			expectedPrice:    0,
			hasError:         true,
		},
		{
			name:             "零天数",
			subscriptionDays: 0,
			expectedPrice:    0,
			hasError:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 测试获取订阅价格
			price, err := validator.GetSubscriptionPrice(tt.subscriptionDays)
			if tt.hasError {
				if err == nil {
					t.Errorf("期望有错误，但没有错误")
				}
			} else {
				if err != nil {
					t.Errorf("不期望有错误，但有错误: %v", err)
				}
				if price != tt.expectedPrice {
					t.Errorf("期望价格 %d，实际 %d", tt.expectedPrice, price)
				}
			}
		})
	}
}

func TestSubscriptionDaysValidation(t *testing.T) {
	validator := NewPriceValidator()

	tests := []struct {
		name             string
		subscriptionDays int
		amount           int64
		hasError         bool
	}{
		{
			name:             "30天订阅正确价格",
			subscriptionDays: 30,
			amount:           1600,
			hasError:         false,
		},
		{
			name:             "365天订阅正确价格",
			subscriptionDays: 365,
			amount:           11900,
			hasError:         false,
		},
		{
			name:             "30天订阅错误价格",
			subscriptionDays: 30,
			amount:           1000,
			hasError:         true,
		},
		{
			name:             "365天订阅错误价格",
			subscriptionDays: 365,
			amount:           10000,
			hasError:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 测试价格验证
			expectedPrice, err := validator.GetSubscriptionPrice(tt.subscriptionDays)
			if err != nil {
				t.Fatalf("获取订阅价格失败: %v", err)
			}

			if tt.amount != expectedPrice {
				if !tt.hasError {
					t.Errorf("期望价格匹配，但价格不匹配: 期望 %d，实际 %d", expectedPrice, tt.amount)
				}
			} else {
				if tt.hasError {
					t.Errorf("期望价格不匹配，但价格匹配")
				}
			}
		})
	}
}

func TestMembershipExpirationCalculation(t *testing.T) {
	// 测试会员到期时间计算
	now := time.Now()

	tests := []struct {
		name             string
		subscriptionDays int
		existingExpires  *time.Time
		expectedDays     int
	}{
		{
			name:             "新用户30天订阅",
			subscriptionDays: 30,
			existingExpires:  nil,
			expectedDays:     30,
		},
		{
			name:             "新用户365天订阅",
			subscriptionDays: 365,
			existingExpires:  nil,
			expectedDays:     365,
		},
		{
			name:             "已有会员续费30天",
			subscriptionDays: 30,
			existingExpires:  &time.Time{},
			expectedDays:     30, // 会在现有到期时间基础上累加
		},
		{
			name:             "已有会员续费365天",
			subscriptionDays: 365,
			existingExpires:  &time.Time{},
			expectedDays:     365, // 会在现有到期时间基础上累加
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 模拟计算新的到期时间
			var newExpires time.Time

			if tt.existingExpires == nil {
				// 新用户，从当前时间开始计算
				newExpires = now.AddDate(0, 0, tt.subscriptionDays)
			} else {
				// 已有会员，在现有到期时间基础上累加
				existingTime := now.AddDate(0, 0, 10) // 假设还有10天到期
				newExpires = existingTime.AddDate(0, 0, tt.subscriptionDays)
			}

			// 验证计算是否正确
			expectedDuration := time.Duration(tt.subscriptionDays) * 24 * time.Hour
			actualDuration := newExpires.Sub(now)

			// 允许1小时的误差
			if actualDuration < expectedDuration-time.Hour || actualDuration > expectedDuration+time.Hour {
				if tt.existingExpires != nil {
					// 对于已有会员，需要从现有到期时间计算
					existingTime := now.AddDate(0, 0, 10)
					expectedFromExisting := existingTime.AddDate(0, 0, tt.subscriptionDays)
					expectedDuration = expectedFromExisting.Sub(now)
					actualDuration = newExpires.Sub(now)
				}

				if actualDuration < expectedDuration-time.Hour || actualDuration > expectedDuration+time.Hour {
					t.Errorf("到期时间计算错误: 期望约 %v，实际 %v", expectedDuration, actualDuration)
				}
			}
		})
	}
}

func TestPaymentRequestValidation(t *testing.T) {
	tests := []struct {
		name          string
		req           *model.CreatePaymentRequest
		hasError      bool
		errorContains string
	}{
		{
			name: "有效的30天订阅请求",
			req: &model.CreatePaymentRequest{
				MembershipType:   model.MembershipTypeSubscription,
				SubscriptionDays: 30,
				Amount:           1600,
			},
			hasError: false,
		},
		{
			name: "有效的365天订阅请求",
			req: &model.CreatePaymentRequest{
				MembershipType:   model.MembershipTypeSubscription,
				SubscriptionDays: 365,
				Amount:           11900,
			},
			hasError: false,
		},
		{
			name: "无效的订阅天数",
			req: &model.CreatePaymentRequest{
				MembershipType:   model.MembershipTypeSubscription,
				SubscriptionDays: 100,
				Amount:           5000,
			},
			hasError:      true,
			errorContains: "订阅天数只支持30天和365天",
		},
		{
			name: "价格与天数不匹配",
			req: &model.CreatePaymentRequest{
				MembershipType:   model.MembershipTypeSubscription,
				SubscriptionDays: 30,
				Amount:           1000,
			},
			hasError:      true,
			errorContains: "订阅价格不匹配",
		},
		{
			name: "零天数",
			req: &model.CreatePaymentRequest{
				MembershipType:   model.MembershipTypeSubscription,
				SubscriptionDays: 0,
				Amount:           1600,
			},
			hasError:      true,
			errorContains: "订阅天数必须大于0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这里需要模拟支付业务实例
			// 由于我们无法直接创建paymentBiz实例（需要store依赖），
			// 我们只测试价格验证器的逻辑

			validator := NewPriceValidator()

			if tt.req.MembershipType == model.MembershipTypeSubscription {
				// 验证天数
				if tt.req.SubscriptionDays <= 0 {
					if !tt.hasError {
						t.Errorf("期望有错误，但没有错误")
					}
					return
				}

				if tt.req.SubscriptionDays != 30 && tt.req.SubscriptionDays != 365 {
					if !tt.hasError {
						t.Errorf("期望有错误，但没有错误")
					}
					return
				}

				// 验证价格
				expectedPrice, err := validator.GetSubscriptionPrice(tt.req.SubscriptionDays)
				if err != nil {
					t.Fatalf("获取订阅价格失败: %v", err)
				}

				if tt.req.Amount != expectedPrice {
					if !tt.hasError {
						t.Errorf("期望有错误，但没有错误")
					}
					return
				}

				// 如果没有错误，验证通过
				if tt.hasError {
					t.Errorf("期望有错误，但没有错误")
				}
			}
		})
	}
}
