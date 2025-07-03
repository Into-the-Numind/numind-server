package core

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/errno"
)

// Response 定义统一的响应格式
// Code: 0=成功，1=错误
// Message: 提示信息
// Data: 具体数据
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

// WriteResponse 封装统一的响应格式
func WriteResponse(c *gin.Context, err error, data interface{}) {
	if err != nil {
		httpCode, _, message := errno.Decode(err)
		c.JSON(httpCode, Response{
			Code:    1,
			Message: message,
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, Response{
		Code:    0,
		Message: "操作成功",
		Data:    data,
	})
}
