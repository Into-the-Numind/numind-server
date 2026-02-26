package account

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
	"strconv"

	"github.com/gin-gonic/gin"
)

// AccountController 账户记录控制器
type AccountController struct {
	b biz.IBiz
}

// NewAccountController 创建账户记录控制器实例
func NewAccountController(b biz.IBiz) *AccountController {
	return &AccountController{b: b}
}

// GetUserPaymentHistory 获取用户支付历史
func (ac *AccountController) GetUserPaymentHistory(c *gin.Context) {
	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 获取查询参数
	offset := 0
	limit := 20
	if offsetStr := c.Query("offset"); offsetStr != "" {
		if offsetVal, err := strconv.Atoi(offsetStr); err == nil {
			offset = offsetVal
		}
	}
	if limitStr := c.Query("limit"); limitStr != "" {
		if limitVal, err := strconv.Atoi(limitStr); err == nil {
			limit = limitVal
		}
	}

	// 获取用户支付历史
	records, err := ac.b.AccountRecords().GetUserPaymentHistory(c, userID.ID, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, records)
}

// GetUserTotalAmount 获取用户总消费金额
func (ac *AccountController) GetUserTotalAmount(c *gin.Context) {
	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 获取记录类型，默认为payment
	recordType := c.DefaultQuery("type", "payment")

	total, err := ac.b.AccountRecords().GetUserTotalAmount(c, userID.ID, recordType)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total_amount":      total,
		"total_amount_yuan": float64(total) / 100.0,
		"record_type":       recordType,
	})
}

// GetUserAccountSummary 获取用户账户摘要信息
func (ac *AccountController) GetUserAccountSummary(c *gin.Context) {
	// 获取当前用户ID
	userID := middleware.GetCurrentUser(c)
	if userID == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户未登录"), nil)
		return
	}

	// 获取用户账户摘要信息
	summary, err := ac.b.AccountRecords().GetUserAccountSummary(c, userID.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, summary)
}
