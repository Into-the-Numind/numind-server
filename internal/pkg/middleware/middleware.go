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

// scopeXhs 是浏览器插件 ext-token 的 scope claim 值（xhs-collector T7）。
// 携带此 scope 的 token 是最小权限令牌：只能访问 /v1/xhs/* 路由，打其它 /v1/* 路由被 403 拒绝。
// 无 scope claim 的普通 web token 不受影响（向后兼容）。
const scopeXhs = "xhs"

// scopeXhsPathPrefix 是 scope=xhs token 唯一允许访问的路径前缀。
// 注意带尾随斜杠，确保 /v1/xhs/notes 等子路由匹配；裸 /v1/xhs（无意义）不放行。
const scopeXhsPathPrefix = "/v1/xhs/"

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

		// 最小权限收敛（xhs-collector T7）：scope=xhs 的 ext-token 仅放行 /v1/xhs/* 路由。
		// 无 scope claim 的普通 web token 不受影响。enforceTokenScope 在被拒时已写响应并 Abort。
		if !enforceTokenScope(c, token) {
			return
		}

		c.Set("current_user", user)
		c.Next()
	}
}

// tokenScope 解析 JWT 的 "scope" claim（不做签名外的其它校验——token 已由 ValidateToken
// 验签 + 验黑名单通过）。无 scope claim 或解析失败 → 返回空串（视为无 scope 的全功能 token）。
func tokenScope(tokenString string) string {
	parsed, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return ""
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return ""
	}
	s, _ := claims["scope"].(string)
	return s
}

// enforceTokenScope 对受限 scope token 做路径白名单校验（xhs-collector T7）。
// scope=xhs 的 token 打非 /v1/xhs/* 路由 → 403 并 Abort，返回 false；否则放行返回 true。
// 无 scope token 一律放行（向后兼容旧 token）。
func enforceTokenScope(c *gin.Context, tokenString string) bool {
	scope := tokenScope(tokenString)
	if scope == "" {
		return true
	}
	if scope == scopeXhs {
		if !strings.HasPrefix(c.Request.URL.Path, scopeXhsPathPrefix) {
			core.WriteResponse(c, errno.ErrForbidden.SetMessage("该令牌仅可访问小红书采集接口"), nil)
			c.Abort()
			return false
		}
		return true
	}
	// 未知 scope：保守拒绝，避免未来新增 scope 时遗漏中间件分支导致越权放行。
	core.WriteResponse(c, errno.ErrForbidden.SetMessage("令牌权限范围不被支持"), nil)
	c.Abort()
	return false
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

		// nil guard: 防御 numind.go 注入未完成或 wire 顺序错乱（spec D2 Task 3 reviewer P1）。
		// 正常启动序列 NewBiz → 注入 → setupRouter 保证 nil 不出现, 此处是 fail-fast 防御.
		if CheckFeaturePermissionFunc == nil {
			log.C(c).Errorw("CheckFeaturePermissionFunc not initialized — biz layer wire missing", "feature_key", featureKey)
			core.WriteResponse(c, errno.ErrInternalServer, nil)
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
