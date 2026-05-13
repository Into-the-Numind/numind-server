package sop

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// VisibilityResponse GET /v1/sop/templates/:id/visibility 响应体.
type VisibilityResponse struct {
	Restricted bool   `json:"restricted"`
	SubUserIDs []uint `json:"sub_user_ids"`
}

// UpdateVisibilityRequest PUT /v1/sop/templates/:id/visibility 请求体.
// 当 restricted=true 时, sub_user_ids 必填 (可为空数组表示白名单严格全拒);
// 当 restricted=false 时, sub_user_ids 被忽略 (D3 保留语义).
type UpdateVisibilityRequest struct {
	Restricted bool   `json:"restricted"`
	SubUserIDs []uint `json:"sub_user_ids"`
}

// GetVisibility GET /v1/sop/templates/:id/visibility — 读取 SOP 可见范围配置.
//
// Thin controller: 仅做参数绑定 + 调用 biz; owner/身份校验在 biz 层 GetSopVisibility 完成
// (api-design.md §6 + sop-chatbot-visibility-scope spec §3.2).
func (ctrl *SopController) GetVisibility(c *gin.Context) {
	sopID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid sop id"), nil)
		return
	}
	callerID, ok := extractCallerID(c)
	if !ok {
		return
	}

	restricted, ids, err := sop.GetSopVisibility(c, store.S, callerID, uint(sopID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	// 确保 ids 为非 nil slice (JSON 序列化为 [] 而非 null)
	if ids == nil {
		ids = []uint{}
	}
	core.WriteResponse(c, nil, VisibilityResponse{Restricted: restricted, SubUserIDs: ids})
}

// UpdateVisibility PUT /v1/sop/templates/:id/visibility — 更新 SOP 可见范围配置.
//
// Thin controller: 仅做参数绑定 + 调用 biz; owner/身份/子用户归属校验在 biz 层
// UpdateSopVisibility + ValidateSubUsersBelongToCaller 完成.
func (ctrl *SopController) UpdateVisibility(c *gin.Context) {
	sopID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid sop id"), nil)
		return
	}
	callerID, ok := extractCallerID(c)
	if !ok {
		return
	}

	var req UpdateVisibilityRequest
	if bindErr := c.ShouldBindJSON(&req); bindErr != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid request body: %s", bindErr.Error()), nil)
		return
	}

	if err := sop.UpdateSopVisibility(c, store.S, callerID, uint(sopID), req.Restricted, req.SubUserIDs); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// extractCallerID 从 gin context 提取当前 caller 的 user ID.
// 用 current_user (*model.User) 而非 GetUint("userID") 以与本 package 既有 controller 风格一致.
func extractCallerID(c *gin.Context) (uint, bool) {
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return 0, false
	}
	user, ok := currentUser.(*model.User)
	if !ok || user == nil {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("用户信息格式错误"), nil)
		return 0, false
	}
	return user.ID, true
}
