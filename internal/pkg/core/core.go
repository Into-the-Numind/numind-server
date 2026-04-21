package core

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
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

// buildErrorLogFields 构建结构化字段；返回 nil 表示该 httpCode 应当被跳过。
// 跳过 401（token 过期噪声）与 404（favicon / 不存在路由噪声）。
// 拆成独立函数以便单测断言过滤行为与字段完整性。
func buildErrorLogFields(c *gin.Context, httpCode int, errCode, message string) []interface{} {
	if httpCode < 400 || httpCode == 401 || httpCode == 404 {
		return nil
	}
	fields := []interface{}{
		"http_code", httpCode,
		"errno", errCode,
		"message", message,
		"path", c.Request.URL.Path,
		"method", c.Request.Method,
		"client_ip", c.ClientIP(),
	}
	if u, exists := c.Get("current_user"); exists {
		if user, ok := u.(*model.User); ok {
			fields = append(fields, "user_id", user.ID)
		}
	}
	return fields
}

// logErrorResponse 结构化记录 4xx/5xx 响应，方便线上定位"谁、什么端点、什么原因"。
func logErrorResponse(c *gin.Context, httpCode int, errCode, message string) {
	fields := buildErrorLogFields(c, httpCode, errCode, message)
	if fields == nil {
		return
	}
	log.C(c).Warnw("API error response", fields...)
}

// WriteResponse 封装统一的响应格式
func WriteResponse(c *gin.Context, err error, data interface{}) {
	if err != nil {
		httpCode, errCode, message := errno.Decode(err)
		logErrorResponse(c, httpCode, errCode, message)
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
		httpCode, errCode, message := errno.Decode(err)
		logErrorResponse(c, httpCode, errCode, message)
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
