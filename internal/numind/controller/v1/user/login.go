package user

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"

	"numind-server/internal/pkg/core"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

// 登录 miniblog 并返回一个 JWT Token.
func (ctrl *UserController) Login(c *gin.Context) {
	log.C(c).Infow("Admin login function called")

	var r v1.LoginRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if r.Username != "admin" || r.Password != "admin123456" {
		core.WriteResponse(c, errno.ErrUserNotFound.SetMessage("用户名或密码错误"), nil)
		return
	}

	jwtSecret := viper.GetString("jwt.secret")
	expireHours := viper.GetInt("jwt.expire-hours")
	if expireHours == 0 {
		expireHours = 24 // 默认24小时
	}

	claims := jwt.MapClaims{
		"username": r.Username,
		"is_admin": true,
		"exp":      time.Now().Add(time.Duration(expireHours) * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		core.WriteResponse(c, errno.ErrSignToken.SetMessage("生成token失败"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"token":      tokenString,
		"token_type": "Bearer",
		"username":   r.Username,
	})
}
