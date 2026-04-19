// Package admin_ai — AuditLogController handles GET /v1/admin/ai/audit-logs.
package admin_ai

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// AuditLogController handles audit log listing for the AI Service Manager admin API.
type AuditLogController struct {
	biz aiservice_admin.IAIServiceAdminBiz
}

// NewAuditLogController creates a new AuditLogController.
func NewAuditLogController(biz aiservice_admin.IAIServiceAdminBiz) *AuditLogController {
	return &AuditLogController{biz: biz}
}

// ListLogs handles GET /v1/admin/ai/audit-logs.
//
// Query params:
//   - page       — 1-based page number (default 1)
//   - page_size  — items per page (default 20, max 100)
//   - actor      — partial match on actor_name (LIKE %value%)
//   - target_type — exact match: service | task_profile | provider
//   - date_from  — ISO date inclusive, e.g. "2026-01-01"
//   - date_to    — ISO date inclusive, e.g. "2026-01-31"
//
// Response: {"code":0,"message":"ok","data":{"list":[...],"total":N}}
func (ctrl *AuditLogController) ListLogs(c *gin.Context) {
	log.C(c).Infow("Admin list AI audit logs called")

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	// Clamp page and pageSize.
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	filter := aiservice_admin.AuditLogFilter{
		Actor:      c.Query("actor"),
		TargetType: c.Query("target_type"),
	}

	// Parse optional date range.
	if raw := c.Query("date_from"); raw != "" {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("date_from 格式错误，应为 YYYY-MM-DD"), nil)
			return
		}
		filter.DateFrom = &t
	}
	if raw := c.Query("date_to"); raw != "" {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("date_to 格式错误，应为 YYYY-MM-DD"), nil)
			return
		}
		filter.DateTo = &t
	}

	result, err := ctrl.biz.ListAuditLogs(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		log.C(c).Errorw("Failed to list AI audit logs", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询审计日志失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": result.Items, "total": result.Total})
}
