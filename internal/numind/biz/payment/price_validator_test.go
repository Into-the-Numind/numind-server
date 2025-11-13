package payment

import (
	"testing"
)

func TestPriceValidator_ValidateSubscriptionPrice(t *testing.T) {
	validator := NewPriceValidator()

	tests := []struct {
		name     string
		amount   int64
		expected int
		hasError bool
	}{
		{
			name:     "有效的月度订阅价格",
			amount:   1600,
			expected: 30,
			hasError: false,
		},
		{
			name:     "有效的年度订阅价格",
			amount:   11900,
			expected: 365,
			hasError: false,
		},
		{
			name:     "无效的订阅价格",
			amount:   1000,
			expected: 0,
			hasError: true,
		},
		{
			name:     "恶意篡改的价格",
			amount:   1,
			expected: 0,
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			days, err := validator.ValidateSubscriptionPrice(tt.amount)
			if tt.hasError {
				if err == nil {
					t.Errorf("期望有错误，但没有错误")
				}
			} else {
				if err != nil {
					t.Errorf("不期望有错误，但有错误: %v", err)
				}
				if days != tt.expected {
					t.Errorf("期望天数 %d，实际 %d", tt.expected, days)
				}
			}
		})
	}
}

func TestPriceValidator_GetSubscriptionPrice(t *testing.T) {
	validator := NewPriceValidator()

	tests := []struct {
		name     string
		days     int
		expected int64
		hasError bool
	}{
		{
			name:     "30天订阅",
			days:     30,
			expected: 1600,
			hasError: false,
		},
		{
			name:     "365天订阅",
			days:     365,
			expected: 11900,
			hasError: false,
		},
		{
			name:     "无效的天数",
			days:     100,
			expected: 0,
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, err := validator.GetSubscriptionPrice(tt.days)
			if tt.hasError {
				if err == nil {
					t.Errorf("期望有错误，但没有错误")
				}
			} else {
				if err != nil {
					t.Errorf("不期望有错误，但有错误: %v", err)
				}
				if price != tt.expected {
					t.Errorf("期望价格 %d，实际 %d", tt.expected, price)
				}
			}
		})
	}
}
