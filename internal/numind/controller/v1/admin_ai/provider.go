// Package admin_ai provides Admin API handlers for the AI Service Manager.
// This file contains the ProviderController for llm_provider CRUD.
//
// Endpoints (all under /v1/admin/ai/providers):
//
//	GET    /providers          — list all providers (api_key masked)
//	GET    /providers/:id      — get single provider (api_key masked)
//	POST   /providers          — create provider
//	PUT    /providers/:id      — partial update provider
//	DELETE /providers/:id      — delete provider (guard: no active routes)
//	POST   /providers/:id/test-connection — probe OpenAI-compatible endpoint
package admin_ai

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// ProviderController handles CRUD operations for llm_provider via the admin API.
type ProviderController struct {
	biz aiservice_admin.IAIServiceAdminBiz
}

// NewProviderController creates a new ProviderController.
func NewProviderController(biz aiservice_admin.IAIServiceAdminBiz) *ProviderController {
	return &ProviderController{biz: biz}
}

// ----------------------------------------------------------------------------
// GET /v1/admin/ai/providers
// ----------------------------------------------------------------------------

// ListProviders returns all llm_provider rows with api_key masked.
func (ctrl *ProviderController) ListProviders(c *gin.Context) {
	log.C(c).Infow("Admin list AI providers called")

	providers, err := ctrl.biz.ListProviders(c.Request.Context())
	if err != nil {
		log.C(c).Errorw("Failed to list AI providers", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": providers, "total": len(providers)})
}

// ----------------------------------------------------------------------------
// GET /v1/admin/ai/providers/:id
// ----------------------------------------------------------------------------

// GetProvider returns a single llm_provider by ID with api_key masked.
func (ctrl *ProviderController) GetProvider(c *gin.Context) {
	log.C(c).Infow("Admin get AI provider called")

	id, err := parseProviderID(c)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	dto, bizErr := ctrl.biz.GetProvider(c.Request.Context(), id)
	if bizErr != nil {
		if errors.Is(bizErr, errno.ErrAIProviderNotFound) {
			core.WriteResponse(c, errno.ErrAIProviderNotFound, nil)
			return
		}
		log.C(c).Errorw("Failed to get AI provider", "id", id, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// ----------------------------------------------------------------------------
// POST /v1/admin/ai/providers
// ----------------------------------------------------------------------------

// CreateProvider creates a new llm_provider record.
func (ctrl *ProviderController) CreateProvider(c *gin.Context) {
	log.C(c).Infow("Admin create AI provider called")

	var req aiservice_admin.CreateProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	actorID, actorName := actorFromContext(c)
	dto, bizErr := ctrl.biz.CreateProvider(c.Request.Context(), req, actorID, actorName)
	if bizErr != nil {
		if isErrno(bizErr) {
			core.WriteResponse(c, bizErr, nil)
			return
		}
		log.C(c).Errorw("Failed to create AI provider", "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("创建失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// ----------------------------------------------------------------------------
// PUT /v1/admin/ai/providers/:id
// ----------------------------------------------------------------------------

// UpdateProvider applies a partial update to an llm_provider.
// Empty or nil api_key in the request body preserves the existing key.
func (ctrl *ProviderController) UpdateProvider(c *gin.Context) {
	log.C(c).Infow("Admin update AI provider called")

	id, parseErr := parseProviderID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	var req aiservice_admin.UpdateProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	actorID, actorName := actorFromContext(c)
	dto, bizErr := ctrl.biz.UpdateProvider(c.Request.Context(), id, req, actorID, actorName)
	if bizErr != nil {
		if errors.Is(bizErr, errno.ErrAIProviderNotFound) {
			core.WriteResponse(c, errno.ErrAIProviderNotFound, nil)
			return
		}
		if isErrno(bizErr) {
			core.WriteResponse(c, bizErr, nil)
			return
		}
		log.C(c).Errorw("Failed to update AI provider", "id", id, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("更新失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}

// ----------------------------------------------------------------------------
// DELETE /v1/admin/ai/providers/:id
// ----------------------------------------------------------------------------

// DeleteProvider hard-deletes an llm_provider.
// Returns 409 Conflict if any ai_service_route rows reference the provider.
func (ctrl *ProviderController) DeleteProvider(c *gin.Context) {
	log.C(c).Infow("Admin delete AI provider called")

	id, parseErr := parseProviderID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	actorID, actorName := actorFromContext(c)
	if err := ctrl.biz.DeleteProvider(c.Request.Context(), id, actorID, actorName); err != nil {
		if errors.Is(err, errno.ErrAIProviderNotFound) {
			core.WriteResponse(c, errno.ErrAIProviderNotFound, nil)
			return
		}
		if errors.Is(err, errno.ErrAIProviderInUse) {
			core.WriteResponse(c, err, nil)
			return
		}
		if isErrno(err) {
			core.WriteResponse(c, err, nil)
			return
		}
		log.C(c).Errorw("Failed to delete AI provider", "id", id, "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("删除失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// ----------------------------------------------------------------------------
// POST /v1/admin/ai/providers/:id/test-connection
// ----------------------------------------------------------------------------

// TestProviderConnection probes an OpenAI-compatible provider with a 1-token request.
// For non-OpenAI-compatible providers, returns success=false with a descriptive message.
// This endpoint never records usage or touches billing infrastructure.
func (ctrl *ProviderController) TestProviderConnection(c *gin.Context) {
	log.C(c).Infow("Admin test AI provider connection called")

	id, parseErr := parseProviderID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	result, bizErr := ctrl.biz.TestProviderConnection(c.Request.Context(), id)
	if bizErr != nil {
		if errors.Is(bizErr, errno.ErrAIProviderNotFound) {
			core.WriteResponse(c, errno.ErrAIProviderNotFound, nil)
			return
		}
		log.C(c).Errorw("Failed to test AI provider connection", "id", id, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("测试连接失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, result)
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// parseProviderID parses the ":id" path parameter as a uint64.
func parseProviderID(c *gin.Context) (uint64, error) {
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errno.ErrBind.SetMessage("无效的供应商 ID")
	}
	return id, nil
}
