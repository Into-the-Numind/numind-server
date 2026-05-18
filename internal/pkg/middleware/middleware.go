package middleware

import (
	"context"
	"fmt"
	stdlog "log"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"gorm.io/gorm"
)

// CheckFeaturePermissionFunc 是 FeaturePermission 中间件使用的功能权限检查函数类型。
// 由 numind.go 在 NewBiz 完成后注入，避免 middleware → biz → salesrag → middleware 循环依赖。
var CheckFeaturePermissionFunc func(ctx context.Context, userID uint, featureKey string) (bool, error)

// Logger 日志中间件
func Logger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		return fmt.Sprintf("%s - [%s] \"%s %s %s %d %s \"%s\" %s\"\n",
			param.ClientIP,
			param.TimeStamp.Format(time.RFC1123),
			param.Method,
			param.Path,
			param.Request.Proto,
			param.StatusCode,
			param.Latency,
			param.Request.UserAgent(),
			param.ErrorMessage,
		)
	})
}

// ErrorHandler 错误处理中间件
func ErrorHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// 检查是否有错误
		if len(c.Errors) > 0 {
			err := c.Errors.Last()
			stdlog.Printf("Error: %v", err.Error())

			// 返回统一的错误响应
			core.WriteResponse(c, errno.ErrInternalServer, nil)
		}
	}
}

// AuthMiddleware 认证中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("未提供认证令牌"), nil)
			c.Abort()
			return
		}

		user, err := ValidateToken(c.Request.Context(), token)
		if err != nil {
			core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("无效的认证令牌"), nil)
			c.Abort()
			return
		}

		c.Set("current_user", user)
		c.Next()
	}
}

// OptionalAuthMiddleware 可选认证中间件
func OptionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token != "" {
			if user, err := ValidateToken(c.Request.Context(), token); err == nil {
				c.Set("current_user", user)
			}
		}
		c.Next()
	}
}

// AdminAuthMiddleware 管理员认证中间件
func AdminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("未提供认证令牌"), nil)
			c.Abort()
			return
		}

		user, err := ValidateToken(c.Request.Context(), token)
		if err != nil {
			core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("无效的认证令牌"), nil)
			c.Abort()
			return
		}

		if !user.IsAdmin {
			core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("需要管理员权限"), nil)
			c.Abort()
			return
		}

		c.Set("current_user", user)
		c.Next()
	}
}

// extractToken 从请求头中提取token
func extractToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return ""
	}

	return parts[1]
}

// ValidateToken 验证JWT token并返回用户信息
// 从数据库验证用户是否存在
func ValidateToken(ctx context.Context, tokenString string) (*model.User, error) {
	// 检查token是否在黑名单中
	blacklist := GetTokenBlacklist()
	if blacklist.IsTokenBlacklisted(tokenString) {
		return nil, fmt.Errorf("token已失效")
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(viper.GetString("jwt.secret")), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// 再次检查黑名单（防止在解析过程中token被加入黑名单）
		if blacklist.IsTokenBlacklisted(tokenString) {
			return nil, fmt.Errorf("token已失效")
		}

		// 安全地获取 user_id（避免panic）
		userIDValue, exists := claims["user_id"]
		if !exists {
			return nil, fmt.Errorf("user_id not found in token")
		}

		var userID uint
		switch v := userIDValue.(type) {
		case float64:
			userID = uint(v)
		case int:
			userID = uint(v)
		case uint:
			userID = v
		case int64:
			userID = uint(v)
		default:
			return nil, fmt.Errorf("invalid user_id type in token: %T", v)
		}

		// 从数据库验证用户是否存在
		if store.S == nil {
			log.C(ctx).Warnw("store.S未初始化，跳过数据库验证")
			// 如果store未初始化，返回简化的用户对象（向后兼容）
			user := &model.User{}
			user.ID = userID
			return user, nil
		}

		// 从数据库查询用户
		user, err := store.S.Users().GetUserByID(ctx, userID)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, fmt.Errorf("用户不存在")
			}
			log.C(ctx).Errorw("查询用户失败", "user_id", userID, "error", err)
			return nil, fmt.Errorf("查询用户失败: %v", err)
		}

		return user, nil
	}

	return nil, fmt.Errorf("invalid token")
}

// GetCurrentUser 获取当前用户
func GetCurrentUser(c *gin.Context) *model.User {
	user, exists := c.Get("current_user")
	if !exists {
		return nil
	}
	return user.(*model.User)
}

// FeaturePermission 功能权限中间件，检查当前用户是否有指定功能的使用权限
func FeaturePermission(featureKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := GetCurrentUser(c)
		if user == nil {
			core.WriteResponse(c, errno.ErrTokenInvalid, nil)
			c.Abort()
			return
		}

		hasPermission, err := CheckFeaturePermissionFunc(c, user.ID, featureKey)
		if err != nil {
			log.C(c).Errorw("Failed to check feature permission", "user_id", user.ID, "feature_key", featureKey, "err", err)
			core.WriteResponse(c, errno.ErrInternalServer, nil)
			c.Abort()
			return
		}

		if !hasPermission {
			core.WriteResponse(c, errno.ErrForbidden.SetMessage("未开通该功能权限，请联系管理员"), nil)
			c.Abort()
			return
		}

		c.Next()
	}
}
