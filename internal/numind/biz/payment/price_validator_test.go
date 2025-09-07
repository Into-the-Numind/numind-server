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
			amount:   2800,
			expected: 30,
			hasError: false,
		},
		{
			name:     "有效的年度订阅价格",
			amount:   19800,
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

func TestPriceValidator_ValidatePackagePrice(t *testing.T) {
	validator := NewPriceValidator()

	tests := []struct {
		name     string
		count    int
		amount   int64
		hasError bool
	}{
		{
			name:     "有效的1次包价格",
			count:    1,
			amount:   300,
			hasError: false,
		},
		{
			name:     "有效的5次包价格",
			count:    5,
			amount:   1200,
			hasError: false,
		},
		{
			name:     "有效的20次包价格",
			count:    20,
			amount:   3800,
			hasError: false,
		},
		{
			name:     "有效的50次包价格",
			count:    50,
			amount:   5000,
			hasError: false,
		},
		{
			name:     "无效的包次数",
			count:    10,
			amount:   3000,
			hasError: true,
		},
		{
			name:     "价格不匹配",
			count:    5,
			amount:   1000, // 应该是1200
			hasError: true,
		},
		{
			name:     "恶意篡改的价格",
			count:    1,
			amount:   1,
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidatePackagePrice(tt.count, tt.amount)
			if tt.hasError {
				if err == nil {
					t.Errorf("期望有错误，但没有错误")
				}
			} else {
				if err != nil {
					t.Errorf("不期望有错误，但有错误: %v", err)
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
			expected: 2800,
			hasError: false,
		},
		{
			name:     "365天订阅",
			days:     365,
			expected: 19800,
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

func TestPriceValidator_GetPackagePrice(t *testing.T) {
	validator := NewPriceValidator()

	tests := []struct {
		name     string
		count    int
		expected int64
		hasError bool
	}{
		{
			name:     "1次包",
			count:    1,
			expected: 300,
			hasError: false,
		},
		{
			name:     "5次包",
			count:    5,
			expected: 1200,
			hasError: false,
		},
		{
			name:     "20次包",
			count:    20,
			expected: 3800,
			hasError: false,
		},
		{
			name:     "50次包",
			count:    50,
			expected: 5000,
			hasError: false,
		},
		{
			name:     "无效的次数",
			count:    10,
			expected: 0,
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			price, err := validator.GetPackagePrice(tt.count)
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
