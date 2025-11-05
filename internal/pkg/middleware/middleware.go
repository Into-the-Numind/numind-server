package middleware

import (
	"fmt"
	"log"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

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
			log.Printf("Error: %v", err.Error())

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

		user, err := validateToken(token)
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
			if user, err := validateToken(token); err == nil {
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

		user, err := validateToken(token)
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

// validateToken 验证JWT token并返回用户信息
// 注意：这个方法目前返回的是简化的用户对象，如果需要完整用户信息，应该从数据库查询
func validateToken(tokenString string) (*model.User, error) {
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
		userID := uint(claims["user_id"].(float64))

		// 优先使用unionid，如果没有则使用openid（兼容旧token）
		var unionID, openID string
		if uid, ok := claims["unionid"].(string); ok && uid != "" {
			unionID = uid
		}
		if oid, ok := claims["openid"].(string); ok && oid != "" {
			openID = oid
		}

		// 这里应该从数据库获取用户信息，暂时返回模拟数据
		// 在实际使用中，应该通过依赖注入的方式获取数据库连接
		user := &model.User{}
		user.ID = userID
		user.UnionID = unionID
		user.OpenID = openID

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
