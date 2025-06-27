package user

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"

	"numind-server/internal/pkg/core"
)

// 登录 miniblog 并返回一个 JWT Token.
func (ctrl *UserController) Login(c *gin.Context) {
	log.C(c).Infow("Login function called")

	var r v1.LoginRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)

		return
	}

	user, err := ctrl.b.Users().Login(c, &r)
	if err != nil {
		core.WriteResponse(c, err, nil)

		return
	}

	core.WriteResponse(c, nil, user)
}
