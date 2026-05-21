// Package admin provides Admin API handlers for compliance rule CRUD and
// agent_run admin operations.
//
// Endpoints (M-C1a compliance rules — all under /v1/admin/):
//
//	GET    /compliance-rules?page=&page_size=&parent_user_id=&rule_type=&is_active=
//	POST   /compliance-rules
//	GET    /compliance-rules/:id
//	PATCH  /compliance-rules/:id
//	DELETE /compliance-rules/:id
package admin

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/compliance"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// ComplianceRuleController handles admin CRUD for compliance_rule.
type ComplianceRuleController struct {
	svc *compliance.AdminService
}

// NewComplianceRuleController creates a new ComplianceRuleController.
func NewComplianceRuleController(svc *compliance.AdminService) *ComplianceRuleController {
	return &ComplianceRuleController{svc: svc}
}

// ----------------------------------------------------------------------------
// Response types
// ----------------------------------------------------------------------------

// ruleResponse is the canonical JSON shape for a single rule.
type ruleResponse struct {
	ID           uint64    `json:"id"`
	ParentUserID uint      `json:"parent_user_id"`
	RuleType     string    `json:"rule_type"`
	RuleText     string    `json:"rule_text"`
	Priority     int       `json:"priority"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// listRulesResponse wraps paginated rule results.
type listRulesResponse struct {
	List  []ruleResponse `json:"list"`
	Total int64          `json:"total"`
}

// ----------------------------------------------------------------------------
// GET /v1/admin/compliance-rules
// ----------------------------------------------------------------------------

// listRulesQuery holds query params for list.
type listRulesQuery struct {
	ParentUserID uint   `form:"parent_user_id"`
	RuleType     string `form:"rule_type"`
	IsActive     string `form:"is_active"` // "true" | "false" | ""
	Page         int    `form:"page,default=1"`
	PageSize     int    `form:"page_size,default=20"`
}

// List handles GET /v1/admin/compliance-rules.
func (ctrl *ComplianceRuleController) List(c *gin.Context) {
	log.C(c).Infow("Admin list compliance rules called")

	var q listRulesQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	var isActive *bool
	switch q.IsActive {
	case "true":
		v := true
		isActive = &v
	case "false":
		v := false
		isActive = &v
	}

	opts := compliance.ListOpts{
		ParentUserID: q.ParentUserID,
		RuleType:     q.RuleType,
		IsActive:     isActive,
		Page:         q.Page,
		PageSize:     q.PageSize,
	}

	result, err := ctrl.svc.List(c.Request.Context(), opts)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败: %s", err.Error()), nil)
		return
	}

	resp := listRulesResponse{
		List:  make([]ruleResponse, len(result.Rules)),
		Total: result.Total,
	}
	for i, r := range result.Rules {
		resp.List[i] = ruleResponse{
			ID:           r.ID,
			ParentUserID: r.ParentUserID,
			RuleType:     r.RuleType,
			RuleText:     r.RuleText,
			Priority:     r.Priority,
			IsActive:     r.IsActive,
			CreatedAt:    r.CreatedAt,
			UpdatedAt:    r.UpdatedAt,
		}
	}
	core.WriteResponse(c, nil, resp)
}

// ----------------------------------------------------------------------------
// POST /v1/admin/compliance-rules
// ----------------------------------------------------------------------------

type createRuleRequest struct {
	ParentUserID uint   `json:"parent_user_id" binding:"required"`
	RuleType     string `json:"rule_type" binding:"required"`
	RuleText     string `json:"rule_text" binding:"required,max=1000"`
	Priority     int    `json:"priority"`
	IsActive     *bool  `json:"is_active"` // *bool for default:true gotcha (database.md §6)
}

// Create handles POST /v1/admin/compliance-rules.
func (ctrl *ComplianceRuleController) Create(c *gin.Context) {
	log.C(c).Infow("Admin create compliance rule called")

	var req createRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	rule, err := ctrl.svc.Create(c.Request.Context(), compliance.CreateRequest{
		ParentUserID: req.ParentUserID,
		RuleType:     req.RuleType,
		RuleText:     req.RuleText,
		Priority:     req.Priority,
		IsActive:     req.IsActive,
	})
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, ruleResponse{
		ID:           rule.ID,
		ParentUserID: rule.ParentUserID,
		RuleType:     rule.RuleType,
		RuleText:     rule.RuleText,
		Priority:     rule.Priority,
		IsActive:     rule.IsActive,
		CreatedAt:    rule.CreatedAt,
		UpdatedAt:    rule.UpdatedAt,
	})
}

// ----------------------------------------------------------------------------
// GET /v1/admin/compliance-rules/:id
// ----------------------------------------------------------------------------

// Get handles GET /v1/admin/compliance-rules/:id.
func (ctrl *ComplianceRuleController) Get(c *gin.Context) {
	log.C(c).Infow("Admin get compliance rule called")

	id, err := parseID(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("id 参数无效"), nil)
		return
	}

	rule, err := ctrl.svc.Get(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, errno.ErrComplianceRuleNotFound) {
			core.WriteResponse(c, errno.ErrComplianceRuleNotFound, nil)
			return
		}
		core.WriteResponse(c, errno.InternalServerError.SetMessage("查询失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, ruleResponse{
		ID:           rule.ID,
		ParentUserID: rule.ParentUserID,
		RuleType:     rule.RuleType,
		RuleText:     rule.RuleText,
		Priority:     rule.Priority,
		IsActive:     rule.IsActive,
		CreatedAt:    rule.CreatedAt,
		UpdatedAt:    rule.UpdatedAt,
	})
}

// ----------------------------------------------------------------------------
// PATCH /v1/admin/compliance-rules/:id
// ----------------------------------------------------------------------------

type patchRuleRequest struct {
	RuleText *string `json:"rule_text"`
	RuleType *string `json:"rule_type"`
	Priority *int    `json:"priority"`
	IsActive *bool   `json:"is_active"`
}

// Patch handles PATCH /v1/admin/compliance-rules/:id.
func (ctrl *ComplianceRuleController) Patch(c *gin.Context) {
	log.C(c).Infow("Admin patch compliance rule called")

	id, err := parseID(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("id 参数无效"), nil)
		return
	}

	var req patchRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	rule, err := ctrl.svc.Patch(c.Request.Context(), id, compliance.PatchRequest{
		RuleText: req.RuleText,
		RuleType: req.RuleType,
		Priority: req.Priority,
		IsActive: req.IsActive,
	})
	if err != nil {
		if errors.Is(err, errno.ErrComplianceRuleNotFound) {
			core.WriteResponse(c, errno.ErrComplianceRuleNotFound, nil)
			return
		}
		core.WriteResponse(c, errno.InternalServerError.SetMessage("更新失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, ruleResponse{
		ID:           rule.ID,
		ParentUserID: rule.ParentUserID,
		RuleType:     rule.RuleType,
		RuleText:     rule.RuleText,
		Priority:     rule.Priority,
		IsActive:     rule.IsActive,
		CreatedAt:    rule.CreatedAt,
		UpdatedAt:    rule.UpdatedAt,
	})
}

// ----------------------------------------------------------------------------
// DELETE /v1/admin/compliance-rules/:id
// ----------------------------------------------------------------------------

// Delete handles DELETE /v1/admin/compliance-rules/:id.
func (ctrl *ComplianceRuleController) Delete(c *gin.Context) {
	log.C(c).Infow("Admin delete compliance rule called")

	id, err := parseID(c)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("id 参数无效"), nil)
		return
	}

	if err := ctrl.svc.Delete(c.Request.Context(), id); err != nil {
		if errors.Is(err, errno.ErrComplianceRuleNotFound) {
			core.WriteResponse(c, errno.ErrComplianceRuleNotFound, nil)
			return
		}
		core.WriteResponse(c, errno.InternalServerError.SetMessage("删除失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// parseID extracts and validates the :id path parameter.
func parseID(c *gin.Context) (uint64, error) {
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, err
	}
	return id, nil
}
