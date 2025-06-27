package user

import (
	"net/http"
	v1 "numind-server/pkg/api/numind/v1"

	"github.com/gin-gonic/gin"
)

func (ctrl *UserController) WechatLogin(c *gin.Context) {
	var req v1.WechatLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	result, err := ctrl.b.Users().WechatLogin(&req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    0,
		"message": "登录成功",
		"data":    result,
	})
}
