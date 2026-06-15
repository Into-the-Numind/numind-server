// Package announcement 是通知中心（公告/问卷）的 HTTP 控制层（notification-center T4a）。
//
// 职责边界（controller 硬规则）：本层只做参数绑定 + 鉴权上下文提取 + 调用 biz +
// core.WriteResponse(c, nil, dto)。RESPONSE DTO 由 biz 层拥有（json tag 严格匹配
// spec §3.1），controller 不二次包装、不含任何业务逻辑。所有 domain error
// （ErrAnnouncementNotFound / ErrAnnouncementNotSurvey / ErrSurveyAlreadySubmitted /
// ErrSurveyValidation）由 biz 返回，直接透传给 core.WriteResponse。
package announcement

import (
	"strconv"

	"github.com/gin-gonic/gin"

	announcementbiz "numind-server/internal/numind/biz/announcement"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// 用户端分页默认值（api-design.md §4：page 1-based 默认 1，page_size 默认 20、上限 100）。
const (
	defaultPage     = 1
	defaultPageSize = 20
	maxPageSize     = 100
)

// AnswerItem 是问卷提交里单题答案的请求形状（spec §3.1 submit body.answers[]）。
// controller 绑定后映射进 announcementbiz.AnswerInput（biz 层不做 gin binding）。
type AnswerItem struct {
	QuestionID uint64   `json:"question_id" binding:"required"`
	Options    []string `json:"options"`
	Rating     *int     `json:"rating"`
	Text       *string  `json:"text"`
}

// SubmitSurveyRequest 是问卷提交请求 body（spec §3.1）。
type SubmitSurveyRequest struct {
	Answers []AnswerItem `json:"answers" binding:"required"`
}

// UserController 是通知中心用户端控制器。
type UserController struct {
	biz announcementbiz.IAnnouncementBiz
}

// NewUserController 创建通知中心用户端控制器（沿用本项目 NewXxxController(biz) 模式）。
func NewUserController(biz announcementbiz.IAnnouncementBiz) *UserController {
	return &UserController{biz: biz}
}

// currentUserID 从 gin 上下文提取当前用户（AuthMiddleware 注入 current_user）。
// 缺失 → ok=false，调用方应回 ErrUnauthorized。
func currentUserID(c *gin.Context) (uint, bool) {
	currentUser, exists := c.Get("current_user")
	if !exists {
		return 0, false
	}
	user, ok := currentUser.(*model.User)
	if !ok {
		return 0, false
	}
	return user.ID, true
}

// parsePagination 解析 page/page_size 查询参数（默认 page=1、page_size=20，上限 100）。
func parsePagination(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", strconv.Itoa(defaultPage)))
	if page < 1 {
		page = defaultPage
	}
	pageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(defaultPageSize)))
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// ListAnnouncements GET /v1/announcements?page=&page_size=
// 返回当前用户可见的公告列表 + total + unread_count（spec §3.1）。
func (ctrl *UserController) ListAnnouncements(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}

	page, pageSize := parsePagination(c)
	dto, err := ctrl.biz.ListForUser(c, userID, page, pageSize)
	if err != nil {
		log.C(c).Errorw("Failed to list announcements", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// UnreadCount GET /v1/announcements/unread-count
// 返回未读公告数（铃铛轮询用，spec §3.1）。
func (ctrl *UserController) UnreadCount(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}

	dto, err := ctrl.biz.UnreadCount(c, userID)
	if err != nil {
		log.C(c).Errorw("Failed to get unread count", "user_id", userID, "err", err)
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// GetDetail GET /v1/announcements/:id
// 返回对用户可见的公告详情（含问卷题目，spec §3.1）。不可见 → ErrAnnouncementNotFound（biz 透传）。
func (ctrl *UserController) GetDetail(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	dto, err := ctrl.biz.DetailForUser(c, userID, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// MarkRead POST /v1/announcements/:id/read
// 幂等标记已读，返回最新 unread_count（spec §3.1）。
func (ctrl *UserController) MarkRead(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	dto, err := ctrl.biz.MarkRead(c, userID, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// SubmitSurvey POST /v1/announcements/:id/survey/submit
// 提交问卷答卷，返回 {submitted:true}（spec §3.1）。
// biz 透传 ErrAnnouncementNotFound / ErrAnnouncementNotSurvey /
// ErrSurveyAlreadySubmitted / ErrSurveyValidation。
func (ctrl *UserController) SubmitSurvey(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的公告ID"), nil)
		return
	}

	var req SubmitSurveyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 映射 request → biz 入参（biz 层负责按题型校验答案形状）。
	answers := make([]announcementbiz.AnswerInput, 0, len(req.Answers))
	for _, a := range req.Answers {
		answers = append(answers, announcementbiz.AnswerInput{
			QuestionID: a.QuestionID,
			Options:    a.Options,
			Rating:     a.Rating,
			Text:       a.Text,
		})
	}

	dto, err := ctrl.biz.SubmitSurvey(c, userID, id, answers)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}
