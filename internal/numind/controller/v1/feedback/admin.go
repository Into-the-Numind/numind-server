package feedback

import (
	"strconv"

	"numind-server/internal/numind/biz"
	v1 "numind-server/pkg/api/numind/v1"

	"github.com/gin-gonic/gin"
)

// AdminFeedbackController 管理员反馈控制器
type AdminFeedbackController struct {
	b biz.IBiz
}

// NewAdminFeedbackController 创建管理员反馈控制器实例
func NewAdminFeedbackController(b biz.IBiz) *AdminFeedbackController {
	return &AdminFeedbackController{b: b}
}

// Create 创建反馈（管理员）
func (c *AdminFeedbackController) Create(ctx *gin.Context) {
	var req struct {
		UserID  uint   `json:"user_id" binding:"required"`
		Content string `json:"content" binding:"required"`
		Type    string `json:"type" binding:"required"`
	}

	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(400, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	// 使用 biz 层的 Create 方法创建反馈
	createReq := &v1.CreateFeedbackRequest{
		Content: req.Content,
		Type:    req.Type,
	}

	err := c.b.Feedbacks().Create(ctx, req.UserID, createReq)
	if err != nil {
		ctx.JSON(500, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(200, gin.H{
		"code":    0,
		"message": "创建反馈成功",
		"data":    nil,
	})
}

// List 获取反馈列表（管理员，返回所有字段）
func (c *AdminFeedbackController) List(ctx *gin.Context) {
	offsetStr := ctx.DefaultQuery("offset", "0")
	limitStr := ctx.DefaultQuery("limit", "10")
	userIDStr := ctx.Query("user_id")
	statusStr := ctx.Query("status")
	feedbackType := ctx.Query("type")

	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		ctx.JSON(400, gin.H{
			"code":    1,
			"message": "offset参数格式错误",
			"data":    nil,
		})
		return
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		ctx.JSON(400, gin.H{
			"code":    1,
			"message": "limit参数格式错误",
			"data":    nil,
		})
		return
	}

	var userID *uint
	if userIDStr != "" {
		if id, err := strconv.ParseUint(userIDStr, 10, 32); err == nil {
			uid := uint(id)
			userID = &uid
		}
	}

	var status *int
	if statusStr != "" {
		if s, err := strconv.Atoi(statusStr); err == nil {
			status = &s
		}
	}

	var feedbackTypePtr *string
	if feedbackType != "" {
		feedbackTypePtr = &feedbackType
	}

	result, err := c.b.Feedbacks().ListAll(ctx, offset, limit, userID, status, feedbackTypePtr)
	if err != nil {
		ctx.JSON(500, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(200, gin.H{
		"code":    0,
		"message": "获取反馈列表成功",
		"data": gin.H{
			"items":  result.Feedbacks,
			"total":  result.TotalCount,
			"offset": offset,
			"limit":  limit,
		},
	})
}

// Get 获取单个反馈（管理员，返回所有字段）
func (c *AdminFeedbackController) Get(ctx *gin.Context) {
	feedbackIDStr := ctx.Param("id")
	feedbackID, err := strconv.ParseUint(feedbackIDStr, 10, 32)
	if err != nil {
		ctx.JSON(400, gin.H{
			"code":    1,
			"message": "反馈ID格式错误",
			"data":    nil,
		})
		return
	}

	feedback, err := c.b.Feedbacks().GetByID(ctx, uint(feedbackID))
	if err != nil {
		ctx.JSON(500, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(200, gin.H{
		"code":    0,
		"message": "获取反馈成功",
		"data":    feedback,
	})
}

// Update 更新反馈（管理员）
func (c *AdminFeedbackController) Update(ctx *gin.Context) {
	feedbackIDStr := ctx.Param("id")
	feedbackID, err := strconv.ParseUint(feedbackIDStr, 10, 32)
	if err != nil {
		ctx.JSON(400, gin.H{
			"code":    1,
			"message": "反馈ID格式错误",
			"data":    nil,
		})
		return
	}

	var req struct {
		Status *int    `json:"status"`
		Reply  *string `json:"reply"`
	}

	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(400, gin.H{
			"code":    1,
			"message": "请求参数错误: " + err.Error(),
			"data":    nil,
		})
		return
	}

	err = c.b.Feedbacks().Update(ctx, uint(feedbackID), req.Status, req.Reply)
	if err != nil {
		ctx.JSON(500, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(200, gin.H{
		"code":    0,
		"message": "更新反馈成功",
		"data":    nil,
	})
}

// Delete 删除反馈（管理员）
func (c *AdminFeedbackController) Delete(ctx *gin.Context) {
	feedbackIDStr := ctx.Param("id")
	feedbackID, err := strconv.ParseUint(feedbackIDStr, 10, 32)
	if err != nil {
		ctx.JSON(400, gin.H{
			"code":    1,
			"message": "反馈ID格式错误",
			"data":    nil,
		})
		return
	}

	err = c.b.Feedbacks().DeleteByAdmin(ctx, uint(feedbackID))
	if err != nil {
		ctx.JSON(500, gin.H{
			"code":    1,
			"message": err.Error(),
			"data":    nil,
		})
		return
	}

	ctx.JSON(200, gin.H{
		"code":    0,
		"message": "删除反馈成功",
		"data":    nil,
	})
}
