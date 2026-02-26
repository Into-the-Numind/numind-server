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
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"gorm.io/gorm"
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
			stdlog.Printf("Error: %v", err.Error())

			// 返回统一的错误响应
			core.WriteResponse(c, errno.ErrInternalServer, nil)
		}
	}
}

// AuthMiddleware 认证中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// #region agent log
		func() {
			logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if logFile != nil {
				defer logFile.Close()
				logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"middleware.go:55","message":"AuthMiddleware entry","data":{"hypothesisId":"D","path":%q},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), c.Request.URL.Path)
				_, _ = logFile.WriteString(logEntry)
			}
		}()
		// #endregion
		token := extractToken(c)
		// #region agent log
		func() {
			logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if logFile != nil {
				defer logFile.Close()
				hasToken := token != ""
				logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"middleware.go:58","message":"Token extracted","data":{"hypothesisId":"D","hasToken":%t},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), hasToken)
				_, _ = logFile.WriteString(logEntry)
			}
		}()
		// #endregion
		if token == "" {
			core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("未提供认证令牌"), nil)
			c.Abort()
			return
		}

		user, err := validateToken(c.Request.Context(), token)
		// #region agent log
		func() {
			logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if logFile != nil {
				defer logFile.Close()
				hasErr := err != nil
				errMsg := ""
				userID := uint(0)
				if err != nil {
					errMsg = err.Error()
				} else if user != nil {
					userID = user.ID
				}
				logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"middleware.go:64","message":"Token validation result","data":{"hypothesisId":"D","error":%t,"errorMsg":%q,"userID":%d},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), hasErr, errMsg, userID)
				_, _ = logFile.WriteString(logEntry)
			}
		}()
		// #endregion
		if err != nil {
			core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("无效的认证令牌"), nil)
			c.Abort()
			return
		}

		c.Set("current_user", user)
		// #region agent log
		func() {
			logFile, _ := os.OpenFile("/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server/.cursor/debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if logFile != nil {
				defer logFile.Close()
				logEntry := fmt.Sprintf(`{"timestamp":%d,"location":"middleware.go:71","message":"AuthMiddleware success","data":{"hypothesisId":"D","userID":%d},"sessionId":"debug-session","runId":"request"}
`, time.Now().UnixMilli(), user.ID)
				_, _ = logFile.WriteString(logEntry)
			}
		}()
		// #endregion
		c.Next()
	}
}

// OptionalAuthMiddleware 可选认证中间件
func OptionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token != "" {
			if user, err := validateToken(c.Request.Context(), token); err == nil {
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

		user, err := validateToken(c.Request.Context(), token)
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
// 从数据库验证用户是否存在，并验证openid是否匹配
func validateToken(ctx context.Context, tokenString string) (*model.User, error) {
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

		// 安全地获取 openid（避免panic）
		openIDValue, exists := claims["openid"]
		if !exists {
			return nil, fmt.Errorf("openid not found in token")
		}

		openID, ok := openIDValue.(string)
		if !ok {
			return nil, fmt.Errorf("invalid openid type in token: %T", openIDValue)
		}

		// 从数据库验证用户是否存在，并验证openid是否匹配
		if store.S == nil {
			log.C(ctx).Warnw("store.S未初始化，跳过数据库验证")
			// 如果store未初始化，返回简化的用户对象（向后兼容）
			user := &model.User{}
			user.ID = userID
			user.OpenID = openID
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

		// 验证openid是否匹配（防止token被篡改）
		if user.OpenID != openID {
			log.C(ctx).Warnw("token中的openid与数据库不匹配", "user_id", userID, "token_openid", openID, "db_openid", user.OpenID)
			return nil, fmt.Errorf("token无效：openid不匹配")
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
