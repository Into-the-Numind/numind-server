package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

// TestValidateToken_SafeTypeAssertion 测试修复后的类型断言是否安全
func TestValidateToken_SafeTypeAssertion(t *testing.T) {
	// 设置JWT secret用于测试
	viper.Set("jwt.secret", "test-secret-key-for-validation")

	// 测试用例1: 正常的token，包含user_id
	t.Run("正常token", func(t *testing.T) {
		claims := jwt.MapClaims{
			"user_id": float64(123),
			"exp":     time.Now().Add(time.Hour).Unix(),
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenString, err := token.SignedString([]byte("test-secret-key-for-validation"))
		if err != nil {
			t.Fatalf("生成token失败: %v", err)
		}

		user, err := ValidateToken(context.Background(), tokenString)
		if err != nil {
			t.Fatalf("验证token失败: %v", err)
		}

		if user.ID != 123 {
			t.Errorf("期望user_id=123, 实际=%d", user.ID)
		}
	})

	// 测试用例2: token中缺少user_id（应该返回错误而不是panic）
	t.Run("缺少user_id", func(t *testing.T) {
		claims := jwt.MapClaims{
			"exp": time.Now().Add(time.Hour).Unix(),
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenString, err := token.SignedString([]byte("test-secret-key-for-validation"))
		if err != nil {
			t.Fatalf("生成token失败: %v", err)
		}

		user, err := ValidateToken(context.Background(), tokenString)
		if err == nil {
			t.Error("期望返回错误，但没有返回")
		}

		if user != nil {
			t.Error("期望user为nil，但不是nil")
		}

		// 验证错误信息包含"user_id"
		if err.Error() != "user_id not found in token" {
			t.Errorf("期望错误信息包含'user_id not found in token', 实际=%s", err.Error())
		}
	})

	// 测试用例3: user_id类型错误（应该返回错误而不是panic）
	t.Run("user_id类型错误", func(t *testing.T) {
		claims := jwt.MapClaims{
			"user_id": "invalid_type", // 字符串类型，不是数字
			"exp":     time.Now().Add(time.Hour).Unix(),
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenString, err := token.SignedString([]byte("test-secret-key-for-validation"))
		if err != nil {
			t.Fatalf("生成token失败: %v", err)
		}

		user, err := ValidateToken(context.Background(), tokenString)
		if err == nil {
			t.Error("期望返回错误，但没有返回")
		}

		if user != nil {
			t.Error("期望user为nil，但不是nil")
		}

		// 验证错误信息包含"invalid user_id type"
		if err.Error()[:20] != "invalid user_id type" {
			t.Errorf("期望错误信息包含'invalid user_id type', 实际=%s", err.Error())
		}
	})

	// 测试用例4: user_id为int类型（应该正常处理）
	t.Run("user_id为int类型", func(t *testing.T) {
		claims := jwt.MapClaims{
			"user_id": int(456),
			"exp":     time.Now().Add(time.Hour).Unix(),
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenString, err := token.SignedString([]byte("test-secret-key-for-validation"))
		if err != nil {
			t.Fatalf("生成token失败: %v", err)
		}

		user, err := ValidateToken(context.Background(), tokenString)
		if err != nil {
			t.Fatalf("验证token失败: %v", err)
		}

		if user.ID != 456 {
			t.Errorf("期望user_id=456, 实际=%d", user.ID)
		}
	})
}
