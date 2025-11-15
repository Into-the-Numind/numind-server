package middleware

import (
	"context"
	"fmt"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

// AdminSystemAuthMiddleware 后台管理系统认证中间件
// 专门用于后台管理系统，验证 admin token 并从 admin 表查询管理员信息
// 注意：这个中间件与 middleware.go 中的 AdminAuthMiddleware 不同，专门用于后台管理系统
func AdminSystemAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("未提供认证令牌"), nil)
			c.Abort()
			return
		}

		admin, err := validateAdminToken(c, token)
		if err != nil {
			core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("无效的认证令牌"), nil)
			c.Abort()
			return
		}

		// 检查管理员状态
		if admin.Status != model.AdminStatusEnabled {
			core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("管理员账户已被禁用"), nil)
			c.Abort()
			return
		}

		c.Set("current_admin", admin)
		c.Next()
	}
}

// validateAdminToken 验证后台管理系统的 JWT token 并返回管理员信息
// token 应包含 admin_id 和 username 字段
func validateAdminToken(ctx context.Context, tokenString string) (*model.Admin, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(viper.GetString("jwt.secret")), nil
	})

	if err != nil {
		return nil, fmt.Errorf("解析token失败: %v", err)
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// 安全地获取 admin_id
		adminIDValue, exists := claims["admin_id"]
		if !exists || adminIDValue == nil {
			return nil, fmt.Errorf("token缺少admin_id字段")
		}

		var adminID uint
		switch v := adminIDValue.(type) {
		case float64:
			adminID = uint(v)
		case int:
			adminID = uint(v)
		case uint:
			adminID = v
		default:
			return nil, fmt.Errorf("admin_id类型无效")
		}

		// 从数据库获取管理员信息
		if store.S == nil {
			return nil, fmt.Errorf("数据库连接未初始化")
		}

		admin, err := store.S.AdminAccounts().GetByID(ctx, adminID)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, fmt.Errorf("管理员不存在")
			}
			return nil, fmt.Errorf("查询管理员信息失败: %v", err)
		}

		// 验证 token 中的 username 是否与数据库中的一致（可选，增强安全性）
		if usernameValue, exists := claims["username"]; exists {
			if username, ok := usernameValue.(string); ok && username != admin.Username {
				return nil, fmt.Errorf("token中的用户名与管理员信息不匹配")
			}
		}

		// 清除密码字段
		admin.Password = ""

		return admin, nil
	}

	return nil, fmt.Errorf("无效的token")
}

// GetCurrentAdmin 获取当前管理员信息
func GetCurrentAdmin(c *gin.Context) *model.Admin {
	admin, exists := c.Get("current_admin")
	if !exists {
		return nil
	}
	return admin.(*model.Admin)
}
