package wecom

import (
	"numind-server/internal/numind/biz"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
	"strconv"

	"github.com/gin-gonic/gin"
)

type WecomController struct {
	b biz.IBiz
}

func NewWecomController(b biz.IBiz) *WecomController {
	return &WecomController{b: b}
}

// ListContacts 获取最近联系人列表
func (ctrl *WecomController) ListContacts(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	contacts, err := ctrl.b.Wecom().GetContacts(int64(user.ID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, contacts)
}

// ListMessages 获取与指定联系人的聊天记录
func (ctrl *WecomController) ListMessages(c *gin.Context) {
	partnerID := c.Param("partner_id")
	if partnerID == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	offset, _ := strconv.Atoi(offsetStr)
	limit, _ := strconv.Atoi(limitStr)

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// First get binding to know 'my' external ID
	binding, err := ctrl.b.Wecom().GetBindingByNumindUser(int64(user.ID))
	if err != nil {
		core.WriteResponse(c, err, nil) // User might not be bound
		return
	}

	messages, total, err := ctrl.b.Wecom().GetConversationMessages(binding.ID, partnerID, limit, offset)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]interface{}{
		"total":    total,
		"messages": messages,
	})
}

// CheckBindStatus 检查绑定状态
func (ctrl *WecomController) CheckBindStatus(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	binding, err := ctrl.b.Wecom().GetBindingByNumindUser(int64(user.ID))
	if err != nil {
		// allow 404 as "not bound"
		core.WriteResponse(c, nil, map[string]interface{}{"bound": false})
		return
	}

	core.WriteResponse(c, nil, map[string]interface{}{
		"bound":        true,
		"external_id":  binding.ID,
		"bound_at":     binding.BoundAt,
		"wecom_name":   binding.Name,
		"wecom_avatar": binding.Avatar,
	})
}

// GetBindCode 获取绑定验证码
func (ctrl *WecomController) GetBindCode(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	code, err := ctrl.b.Wecom().GenerateBindCode(int64(user.ID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]interface{}{
		"code": code,
	})
}

// ListSessions 获取自动归类的会话列表 (替代 ListInbox)
func (ctrl *WecomController) ListSessions(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	sessions, err := ctrl.b.Wecom().GetArchiveSessions(int64(user.ID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, sessions)
}

// GetSessionMessages 获取会话的时间轴消息 (替代 GetInboxDetail)
func (ctrl *WecomController) GetSessionMessages(c *gin.Context) {
	sessionKey := c.Param("session_key") // from path
	if sessionKey == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	messages, err := ctrl.b.Wecom().GetSessionMessages(int64(user.ID), sessionKey)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, messages)
}

// Deprecated: CommitInbox is no longer used in the new auto-archive system
func (ctrl *WecomController) CommitInbox(c *gin.Context) {
	core.WriteResponse(c, nil, nil)
}
