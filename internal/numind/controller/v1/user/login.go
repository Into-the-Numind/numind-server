package user

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
)

// WebLogin Web端用户名密码登录
func (ctrl *UserController) WebLogin(c *gin.Context) {
	log.C(c).Infow("Web login function called")

	var req v1.WebLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	resp, err := ctrl.b.Users().WebLogin(&req)
	if err != nil {
		core.WriteResponse(c, errno.ErrUserNotFound.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, resp)
}
