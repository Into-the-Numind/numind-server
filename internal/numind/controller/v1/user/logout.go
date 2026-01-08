package user

import (
	"time"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

// Logout 用户登出
func (ctrl *UserController) Logout(c *gin.Context) {
	log.C(c).Infow("User logout function called")

	// 从请求头中获取token
	token := extractToken(c)
	if token == "" {
		core.WriteResponse(c, errno.ErrTokenInvalid.SetMessage("未提供认证令牌"), nil)
		return
	}

	// 解析token获取过期时间
	jwtSecret := viper.GetString("jwt.secret")
	parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errno.ErrTokenInvalid
		}
		return []byte(jwtSecret), nil
	})

	var expireTime time.Time
	if err == nil {
		if claims, ok := parsedToken.Claims.(jwt.MapClaims); ok && parsedToken.Valid {
			// 从token中获取过期时间
			if exp, exists := claims["exp"]; exists {
				if expFloat, ok := exp.(float64); ok {
					expireTime = time.Unix(int64(expFloat), 0)
				} else if expInt, ok := exp.(int64); ok {
					expireTime = time.Unix(expInt, 0)
				}
			}
		}
	}

	// 如果无法获取过期时间，使用默认的24小时后过期
	if expireTime.IsZero() {
		expireHours := viper.GetInt("jwt.expire-hours")
		if expireHours == 0 {
			expireHours = 24
		}
		expireTime = time.Now().Add(time.Duration(expireHours) * time.Hour)
	}

	// 将token加入黑名单
	blacklist := middleware.GetTokenBlacklist()
	blacklist.AddToken(token, expireTime)

	// 获取当前用户ID用于日志
	currentUser := middleware.GetCurrentUser(c)
	userID := uint(0)
	if currentUser != nil {
		userID = currentUser.ID
	}

	log.C(c).Infow("User logout successful", "user_id", userID)

	core.WriteResponse(c, nil, gin.H{
		"message": "登出成功",
	})
}

// extractToken 从请求头中提取token
func extractToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return ""
	}

	// 支持 "Bearer token" 格式
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		return authHeader[7:]
	}

	return authHeader
}
