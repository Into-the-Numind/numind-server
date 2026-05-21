// Package admin (continued) — agent_run admin endpoints.
//
// Endpoints (M-C3b + M-C4a — all under /v1/admin/):
//
//	GET  /agent-runs?status=running&page=&page_size=&parent_user_id=
//	POST /agent-runs/:id/cancel
package admin

import (
	"errors"

	"github.com/gin-gonic/gin"

	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// AgentRunController handles admin operations on agent_run.
type AgentRunController struct {
	svc agentbiz.IAgentAdminService
}

// NewAgentRunController creates a new AgentRunController.
func NewAgentRunController(svc agentbiz.IAgentAdminService) *AgentRunController {
	return &AgentRunController{svc: svc}
}

// ----------------------------------------------------------------------------
// GET /v1/admin/agent-runs
// ----------------------------------------------------------------------------

type listAgentRunsQuery struct {
	Status       string `form:"status"`
	ParentUserID uint   `form:"parent_user_id"`
	Page         int    `form:"page,default=1"`
	PageSize     int    `form:"page_size,default=20"`
}

// listAgentRunsResponse wraps paginated agent_run DTOs.
type listAgentRunsResponse struct {
	List  []agentbiz.RunDTO `json:"list"`
	Total int64             `json:"total"`
}

// List handles GET /v1/admin/agent-runs.
func (ctrl *AgentRunController) List(c *gin.Context) {
	log.C(c).Infow("Admin list agent runs called")

	var q listAgentRunsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	dtos, total, err := ctrl.svc.ListByStatus(c.Request.Context(), q.ParentUserID, q.Status, q.Page, q.PageSize)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, listAgentRunsResponse{
		List:  dtos,
		Total: total,
	})
}

// ----------------------------------------------------------------------------
// POST /v1/admin/agent-runs/:id/cancel
// ----------------------------------------------------------------------------

// Cancel handles POST /v1/admin/agent-runs/:id/cancel.
func (ctrl *AgentRunController) Cancel(c *gin.Context) {
	log.C(c).Infow("Admin cancel agent run called")

	id, err := parseID(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("id 参数无效"), nil)
		return
	}

	// Extract acting admin's user ID from context (set by AdminAuthMiddleware).
	var adminUserID uint
	if u := middleware.GetCurrentUser(c); u != nil {
		adminUserID = u.ID
	}

	if err := ctrl.svc.CancelByAdmin(c.Request.Context(), id, adminUserID); err != nil {
		if errors.Is(err, errno.ErrAgentRunNotFound) {
			core.WriteResponse(c, errno.ErrAgentRunNotFound, nil)
			return
		}
		if errors.Is(err, errno.ErrAgentRunNotCancellable) {
			core.WriteResponse(c, errno.ErrAgentRunNotCancellable, nil)
			return
		}
		core.WriteResponse(c, errno.InternalServerError.SetMessage("取消失败: %s", err.Error()), nil)
		return
	}

	// 204 No Content — no body.
	c.Status(204)
}
