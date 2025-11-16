package adminaccount

import (
	"time"

	adminaccountbiz "numind-server/internal/numind/biz/admin_account"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

// AdminAccountController 管理员账户控制器
type AdminAccountController struct {
	b adminaccountbiz.AdminAccountBiz
}

// NewAdminAccountController 创建管理员账户控制器实例
func NewAdminAccountController(b adminaccountbiz.AdminAccountBiz) *AdminAccountController {
	return &AdminAccountController{b: b}
}

// LoginRequest 登录请求
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 管理员登录
func (ctrl *AdminAccountController) Login(c *gin.Context) {
	log.C(c).Infow("Admin login function called")

	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 调用业务层登录
	admin, err := ctrl.b.Login(c, req.Username, req.Password)
	if err != nil {
		log.C(c).Errorw("Admin login failed", "username", req.Username, "error", err)
		core.WriteResponse(c, errno.ErrUserNotFound.SetMessage("用户名或密码错误"), nil)
		return
	}

	// 生成 JWT Token
	jwtSecret := viper.GetString("jwt.secret")
	expireHours := viper.GetInt("jwt.expire-hours")
	if expireHours == 0 {
		expireHours = 24 // 默认24小时
	}

	claims := jwt.MapClaims{
		"admin_id": admin.ID,
		"username": admin.Username,
		"is_admin": true,
		"exp":      time.Now().Add(time.Duration(expireHours) * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		log.C(c).Errorw("Failed to sign token", "error", err)
		core.WriteResponse(c, errno.ErrSignToken.SetMessage("生成token失败"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"token":      tokenString,
		"token_type": "Bearer",
		"admin": gin.H{
			"id":       admin.ID,
			"username": admin.Username,
			"nickname": admin.Nickname,
			"email":    admin.Email,
		},
	})
}

// Logout 管理员登出
func (ctrl *AdminAccountController) Logout(c *gin.Context) {
	log.C(c).Infow("Admin logout function called")

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

	log.C(c).Infow("Admin logout successful", "admin_id", c.GetString("admin_id"))

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
