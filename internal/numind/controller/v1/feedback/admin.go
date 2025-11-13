package feedback

import (
	"strconv"

	"numind-server/internal/numind/biz"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"

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

// List 获取反馈列表（管理员）
func (c *AdminFeedbackController) List(ctx *gin.Context) {
	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "10"))
	userIDStr := ctx.Query("user_id")
	statusStr := ctx.Query("status")
	feedbackType := ctx.Query("type")

	offset := (page - 1) * limit

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
		core.WriteResponse(ctx, err, nil)
		return
	}

	pages := int((result.TotalCount + int64(limit) - 1) / int64(limit))

	core.WriteResponse(ctx, nil, gin.H{
		"items": result.Feedbacks,
		"total": result.TotalCount,
		"page":  page,
		"limit": limit,
		"pages": pages,
	})
}

// Get 获取单个反馈（管理员）
func (c *AdminFeedbackController) Get(ctx *gin.Context) {
	feedbackIDStr := ctx.Param("id")
	feedbackID, err := strconv.ParseUint(feedbackIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("反馈ID格式错误"), nil)
		return
	}

	feedback, err := c.b.Feedbacks().GetByID(ctx, uint(feedbackID))
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}

	core.WriteResponse(ctx, nil, feedback)
}

// Update 更新反馈（管理员）
func (c *AdminFeedbackController) Update(ctx *gin.Context) {
	feedbackIDStr := ctx.Param("id")
	feedbackID, err := strconv.ParseUint(feedbackIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("反馈ID格式错误"), nil)
		return
	}

	var req struct {
		Status *int    `json:"status"`
		Reply  *string `json:"reply"`
	}

	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("请求参数错误: "+err.Error()), nil)
		return
	}

	err = c.b.Feedbacks().Update(ctx, uint(feedbackID), req.Status, req.Reply)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}

	core.WriteResponse(ctx, nil, nil)
}

// Delete 删除反馈（管理员）
func (c *AdminFeedbackController) Delete(ctx *gin.Context) {
	feedbackIDStr := ctx.Param("id")
	feedbackID, err := strconv.ParseUint(feedbackIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("反馈ID格式错误"), nil)
		return
	}

	err = c.b.Feedbacks().DeleteByAdmin(ctx, uint(feedbackID))
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}

	core.WriteResponse(ctx, nil, nil)
}
