package middleware

import (
	"fmt"
	"log"
	"net/http"
	"numind-server/internal/pkg/model"
	"strings"
	"time"

	"numind-server/internal/services"

	"github.com/gin-gonic/gin"
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
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    1,
				"message": "服务器内部错误",
				"data":    nil,
			})
		}
	}
}

// AuthMiddleware 认证中间件
func AuthMiddleware(authService *services.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    1,
				"message": "未提供认证令牌",
				"data":    nil,
			})
			c.Abort()
			return
		}

		user, err := authService.ValidateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    1,
				"message": "无效的认证令牌",
				"data":    nil,
			})
			c.Abort()
			return
		}

		c.Set("user", user)
		c.Next()
	}
}

// OptionalAuthMiddleware 可选认证中间件
func OptionalAuthMiddleware(authService *services.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token != "" {
			if user, err := authService.ValidateToken(token); err == nil {
				c.Set("user", user)
			}
		}
		c.Next()
	}
}

// AdminAuthMiddleware 管理员认证中间件
func AdminAuthMiddleware(authService *services.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    1,
				"message": "未提供认证令牌",
				"data":    nil,
			})
			c.Abort()
			return
		}

		user, err := authService.ValidateToken(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    1,
				"message": "无效的认证令牌",
				"data":    nil,
			})
			c.Abort()
			return
		}

		if !user.IsAdmin {
			c.JSON(http.StatusForbidden, gin.H{
				"code":    1,
				"message": "需要管理员权限",
				"data":    nil,
			})
			c.Abort()
			return
		}

		c.Set("user", user)
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

// GetCurrentUser 获取当前用户
func GetCurrentUser(c *gin.Context) *model.User {
	user, exists := c.Get("user")
	if !exists {
		return nil
	}
	return user.(*model.User)
}
