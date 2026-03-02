package admin_login

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
	"numind-server/pkg/auth"
)

// AdminLoginController 管理员登录控制器
type AdminLoginController struct {
	ds store.IStore
}

// New 创建管理员登录控制器
func New(ds store.IStore) *AdminLoginController {
	return &AdminLoginController{ds: ds}
}

// Login 管理员登录
func (ctrl *AdminLoginController) Login(c *gin.Context) {
	log.C(c).Infow("Admin login function called")

	var req v1.AdminLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	// 查询用户
	var user model.User
	if err := ctrl.ds.DB().Where("username = ?", req.Username).First(&user).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户名或密码错误"), nil)
			return
		}
		log.C(c).Errorw("Failed to query user for admin login", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("登录失败，请稍后重试"), nil)
		return
	}

	// 验证密码（使用 bcrypt 安全比对）
	// TODO: 添加登录频率限制（需要 Redis 支持）
	if err := auth.Compare(user.Password, req.Password); err != nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户名或密码错误"), nil)
		return
	}

	// 检查是否为管理员
	if !user.IsAdmin {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("需要管理员权限"), nil)
		return
	}

	// 更新最后登录时间
	now := time.Now()
	user.LastLogin = &now
	ctrl.ds.DB().Model(&user).Update("last_login", now)

	// 生成JWT token
	token, err := generateAdminToken(&user)
	if err != nil {
		core.WriteResponse(c, errno.ErrSignToken.SetMessage("生成token失败"), nil)
		return
	}

	log.C(c).Infow("Admin login success", "admin_id", user.ID, "username", user.Username)

	core.WriteResponse(c, nil, v1.AdminLoginResponse{
		Token: token,
		User: v1.AdminLoginUser{
			ID:       user.ID,
			Username: user.Username,
			Nickname: user.Nickname,
		},
	})
}

// generateAdminToken 生成管理员JWT token
// 有效期 7 天：管理后台使用频率低，过短会影响体验；安全性通过 AdminAuthMiddleware 的 is_admin 检查保障。
func generateAdminToken(user *model.User) (string, error) {
	if !user.IsAdmin {
		return "", fmt.Errorf("用户不是管理员")
	}

	claims := jwt.MapClaims{
		"user_id":  user.ID,
		"is_admin": true,
		"exp":      time.Now().Add(7 * 24 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(viper.GetString("jwt.secret")))
}

// Logout 管理员登出（客户端清除 token，预留后续 token 黑名单）
func (ctrl *AdminLoginController) Logout(c *gin.Context) {
	adminUser := middleware.GetCurrentUser(c)
	log.C(c).Infow("Admin logout", "admin_id", adminUser.ID)

	// TODO: 后续可加入 token 黑名单机制（需要 Redis 支持）
	core.WriteResponse(c, nil, gin.H{"message": "已登出"})
}
