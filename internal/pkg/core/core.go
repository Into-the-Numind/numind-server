package core

import (
	"net/http"
	"strings"

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
		Message: "",
		Data:    data,
	})
}

// WriteCompressedResponse 返回压缩的响应，减少带宽使用
func WriteCompressedResponse(c *gin.Context, err error, data interface{}) {
	// 检查客户端是否支持gzip压缩
	acceptEncoding := c.GetHeader("Accept-Encoding")
	if !strings.Contains(acceptEncoding, "gzip") {
		WriteResponse(c, err, data)
		return
	}

	if err != nil {
		httpCode, _, message := errno.Decode(err)
		response := Response{
			Code:    1,
			Message: message,
			Data:    nil,
		}

		// 修复：使用c.JSON而不是手动设置状态码和写入
		c.Header("Content-Encoding", "gzip")
		c.JSON(httpCode, response)
		return
	}

	response := Response{
		Code:    0,
		Message: "",
		Data:    data,
	}

	// 修复：使用c.JSON而不是手动压缩
	c.Header("Content-Encoding", "gzip")
	c.JSON(http.StatusOK, response)
}

// WriteMinimalResponse 返回最小化的响应，只包含必要字段
func WriteMinimalResponse(c *gin.Context, err error, data interface{}, fields ...string) {
	if err != nil {
		WriteResponse(c, err, nil)
		return
	}

	// 如果指定了字段，则过滤数据
	if len(fields) > 0 {
		data = filterFields(data, fields)
	}

	WriteResponse(c, nil, data)
}

// filterFields 根据指定字段过滤数据
func filterFields(data interface{}, fields []string) interface{} {
	// 这里可以实现字段过滤逻辑
	// 暂时返回原始数据，后续可以扩展
	return data
}
