package user

import (
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/util"
	v1 "numind-server/pkg/api/numind/v1"

	"github.com/gin-gonic/gin"
)

func (ctrl *UserController) WechatLogin(c *gin.Context) {
	var req v1.WechatLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: "+err.Error()), nil)
		return
	}

	result, err := ctrl.b.Users().WechatLogin(&req)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	// 转换头像URL用于展示（优先使用COS链接）
	if result.User != nil && result.User.AvatarURL != "" {
		result.User.AvatarURL = util.GetAvatarWithCOS(c, result.User.ID, result.User.AvatarURL)
	}

	core.WriteResponse(c, nil, result)
}
