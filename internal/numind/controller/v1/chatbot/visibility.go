package chatbot

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// VisibilityResponse GET /v1/chatbot/:id/visibility 响应体.
type VisibilityResponse struct {
	Restricted bool   `json:"restricted"`
	SubUserIDs []uint `json:"sub_user_ids"`
}

// UpdateVisibilityRequest PUT /v1/chatbot/:id/visibility 请求体.
// 字段语义对称 SOP 端点 (见 controller/v1/sop/visibility.go).
type UpdateVisibilityRequest struct {
	Restricted bool   `json:"restricted"`
	SubUserIDs []uint `json:"sub_user_ids"`
}

// GetVisibility GET /v1/chatbot/:id/visibility — 读取 chatbot 可见范围配置.
//
// Thin controller: 仅做参数绑定 + 调用 biz; owner/身份校验在 biz 层
// GetChatbotVisibility 完成 (api-design.md §6 + spec §3.4).
func (ctrl *ChatbotController) GetVisibility(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	chatbotID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	restricted, ids, err := chatbot.GetChatbotVisibility(c, store.S, user.ID, chatbotID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	if ids == nil {
		ids = []uint{}
	}
	core.WriteResponse(c, nil, VisibilityResponse{Restricted: restricted, SubUserIDs: ids})
}

// UpdateVisibility PUT /v1/chatbot/:id/visibility — 更新 chatbot 可见范围配置.
//
// Thin controller: 仅做参数绑定 + 调用 biz; owner/身份/子用户归属校验在 biz 层
// UpdateChatbotVisibility 完成.
func (ctrl *ChatbotController) UpdateVisibility(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	chatbotID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req UpdateVisibilityRequest
	if bindErr := c.ShouldBindJSON(&req); bindErr != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid request body: %s", bindErr.Error()), nil)
		return
	}

	if err := chatbot.UpdateChatbotVisibility(c, store.S, user.ID, chatbotID, req.Restricted, req.SubUserIDs); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}
