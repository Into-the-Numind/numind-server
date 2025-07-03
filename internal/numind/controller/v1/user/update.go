package user

import (
	"github.com/asaskevich/govalidator"
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	v1 "numind-server/pkg/api/numind/v1"
)

// Update 更新用户信息.
func (ctrl *UserController) Update(c *gin.Context) {
	log.C(c).Infow("Update user function called")

	var r v1.UpdateUserRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)

		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)

		return
	}

	if err := ctrl.b.Users().Update(c, c.Param("name"), &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// updateWechatUser 更新微信用户信息
func (ctrl *UserController) UpdateWechatUser(c *gin.Context) {
	log.C(c).Infow("Update wechat user function called")

	var r v1.UpdateUserRequest
	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	if _, err := govalidator.ValidateStruct(r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(err.Error()), nil)
		return
	}

	openid := c.Param("openid")
	if err := ctrl.b.Users().UpdateWechatUser(c, openid, &r); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
