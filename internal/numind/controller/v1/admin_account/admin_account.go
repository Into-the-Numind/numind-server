package adminaccount

import (
	"time"

	adminaccountbiz "numind-server/internal/numind/biz/admin_account"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"

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
