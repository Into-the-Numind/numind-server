// Package admin_ai provides Admin API handlers for the AI Service Manager.
//
// Endpoints (all under /v1/admin/ai/):
//
//	GET  /services                   — paginated list with optional filters
//	GET  /services/:id               — detail with routes
//	POST /services                   — create
//	PUT  /services/:id               — update
//	DELETE /services/:id             — soft-delete (deprecate)
//	POST /services/:id/restore       — restore (requires reason)
//	GET  /capability-schema          — capability field schemas per service_type
package admin_ai

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// AIServiceController handles CRUD operations for ai_service via the admin API.
type AIServiceController struct {
	biz aiservice_admin.IAIServiceAdminBiz
}

// NewAIServiceController creates a new AIServiceController.
func NewAIServiceController(biz aiservice_admin.IAIServiceAdminBiz) *AIServiceController {
	return &AIServiceController{biz: biz}
}

// actorFromContext extracts the acting admin's ID and name from the Gin context.
// The AdminAuthMiddleware sets "current_user" to the *model.User struct.
func actorFromContext(c *gin.Context) (actorID uint64, actorName string) {
	user := middleware.GetCurrentUser(c)
	if user != nil {
		actorID = uint64(user.ID)
		actorName = user.Username
	}
	return
}

// ----------------------------------------------------------------------------
// GET /v1/admin/ai/services
// ----------------------------------------------------------------------------

// ListServices returns a paginated, optionally filtered list of AI services.
// Query params:
//   - service_type       — llm | ocr | asr (optional)
//   - status             — active | deprecated | all (optional; default: active only)
//   - include_deprecated — true to include deprecated alongside active (legacy alias)
//   - page               — 1-based page number (default 1)
//   - page_size          — items per page (default 20, max 100)
func (ctrl *AIServiceController) ListServices(c *gin.Context) {
	log.C(c).Infow("Admin list AI services called")

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	status := c.Query("status")
	includeDep := c.Query("include_deprecated") == "true"

	filter := registry.ServiceFilter{
		ServiceType: c.Query("service_type"),
	}
	switch status {
	case "deprecated":
		filter.OnlyDeprecated = true
		filter.IncludeDeprecated = true
	case "all":
		filter.IncludeDeprecated = true
	default:
		// "active" or empty → active only (both flags remain false)
		if includeDep {
			filter.IncludeDeprecated = true
		}
	}

	result, err := ctrl.biz.ListServices(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		log.C(c).Errorw("Failed to list AI services", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": result.List, "total": result.Total})
}

// ----------------------------------------------------------------------------
// GET /v1/admin/ai/services/:id
// ----------------------------------------------------------------------------

// GetService returns the detail of a single AI service, including its routes.
func (ctrl *AIServiceController) GetService(c *gin.Context) {
	log.C(c).Infow("Admin get AI service called")

	id, err := parseID(c)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	detail, bizErr := ctrl.biz.GetService(c.Request.Context(), id)
	if bizErr != nil {
		if errors.Is(bizErr, errno.ErrAIServiceNotFound) {
			core.WriteResponse(c, errno.ErrAIServiceNotFound, nil)
			return
		}
		log.C(c).Errorw("Failed to get AI service", "id", id, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, detail)
}

// ----------------------------------------------------------------------------
// POST /v1/admin/ai/services
// ----------------------------------------------------------------------------

// createServiceReq is the request body for CreateService.
type createServiceReq struct {
	ModelKey         string                `json:"model_key"    binding:"required"`
	DisplayName      string                `json:"display_name" binding:"required"`
	ServiceType      string                `json:"service_type" binding:"required"`
	CapabilityJSON   model.JSONMap         `json:"capability_json"`
	LatencyTier      string                `json:"latency_tier"`
	QualityTier      string                `json:"quality_tier"`
	Tags             model.JSONStringSlice `json:"tags"`
	IsThinking       bool                  `json:"is_thinking"`
	SupportsThinking bool                  `json:"supports_thinking"`
	ThinkingOnly     bool                  `json:"thinking_only"`
	Icon             string                `json:"icon"`
	SortOrder        int                   `json:"sort_order"`
	IsActive         *bool                 `json:"is_active"`
}

// CreateService creates a new AI service record.
func (ctrl *AIServiceController) CreateService(c *gin.Context) {
	log.C(c).Infow("Admin create AI service called")

	var req createServiceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	svc := &model.AIService{
		ModelKey:         req.ModelKey,
		DisplayName:      req.DisplayName,
		ServiceType:      req.ServiceType,
		CapabilityJSON:   req.CapabilityJSON,
		LatencyTier:      req.LatencyTier,
		QualityTier:      req.QualityTier,
		Tags:             req.Tags,
		IsThinking:       req.IsThinking,
		SupportsThinking: req.SupportsThinking,
		ThinkingOnly:     req.ThinkingOnly,
		Icon:             req.Icon,
		SortOrder:        req.SortOrder,
		IsActive:         isActive,
	}

	actorID, actorName := actorFromContext(c)
	created, bizErr := ctrl.biz.CreateService(c.Request.Context(), svc, actorID, actorName)
	if bizErr != nil {
		if isErrno(bizErr) {
			core.WriteResponse(c, bizErr, nil)
			return
		}
		log.C(c).Errorw("Failed to create AI service", "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("创建失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, created)
}

// ----------------------------------------------------------------------------
// PUT /v1/admin/ai/services/:id
// ----------------------------------------------------------------------------

// updateServiceReq is the request body for UpdateService.
type updateServiceReq struct {
	ModelKey         *string               `json:"model_key"`
	DisplayName      *string               `json:"display_name"`
	ServiceType      *string               `json:"service_type"`
	CapabilityJSON   model.JSONMap         `json:"capability_json"`
	LatencyTier      *string               `json:"latency_tier"`
	QualityTier      *string               `json:"quality_tier"`
	Tags             model.JSONStringSlice `json:"tags"`
	IsThinking       *bool                 `json:"is_thinking"`
	SupportsThinking *bool                 `json:"supports_thinking"`
	ThinkingOnly     *bool                 `json:"thinking_only"`
	Icon             *string               `json:"icon"`
	SortOrder        *int                  `json:"sort_order"`
	IsActive         *bool                 `json:"is_active"`
}

// UpdateService updates an existing AI service.
func (ctrl *AIServiceController) UpdateService(c *gin.Context) {
	log.C(c).Infow("Admin update AI service called")

	id, parseErr := parseID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	var req updateServiceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// Load existing record first.
	existing, bizErr := ctrl.biz.GetService(c.Request.Context(), id)
	if bizErr != nil {
		if errors.Is(bizErr, errno.ErrAIServiceNotFound) {
			core.WriteResponse(c, errno.ErrAIServiceNotFound, nil)
			return
		}
		log.C(c).Errorw("Failed to get AI service for update", "id", id, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	// Merge fields.
	svc := existing.AIService
	if req.ModelKey != nil {
		svc.ModelKey = *req.ModelKey
	}
	if req.DisplayName != nil {
		svc.DisplayName = *req.DisplayName
	}
	if req.ServiceType != nil {
		svc.ServiceType = *req.ServiceType
	}
	if req.CapabilityJSON != nil {
		svc.CapabilityJSON = req.CapabilityJSON
	}
	if req.LatencyTier != nil {
		svc.LatencyTier = *req.LatencyTier
	}
	if req.QualityTier != nil {
		svc.QualityTier = *req.QualityTier
	}
	if req.Tags != nil {
		svc.Tags = req.Tags
	}
	if req.IsThinking != nil {
		svc.IsThinking = *req.IsThinking
	}
	if req.SupportsThinking != nil {
		svc.SupportsThinking = *req.SupportsThinking
	}
	if req.ThinkingOnly != nil {
		svc.ThinkingOnly = *req.ThinkingOnly
	}
	if req.Icon != nil {
		svc.Icon = *req.Icon
	}
	if req.SortOrder != nil {
		svc.SortOrder = *req.SortOrder
	}
	if req.IsActive != nil {
		svc.IsActive = *req.IsActive
	}

	actorID, actorName := actorFromContext(c)
	if bizErr = ctrl.biz.UpdateService(c.Request.Context(), &svc, actorID, actorName); bizErr != nil {
		if isErrno(bizErr) {
			core.WriteResponse(c, bizErr, nil)
			return
		}
		log.C(c).Errorw("Failed to update AI service", "id", id, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("更新失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// ----------------------------------------------------------------------------
// DELETE /v1/admin/ai/services/:id
// ----------------------------------------------------------------------------

// DeprecateService soft-deletes an AI service (sets deprecated_at = now).
// Body (optional): {"reason": "..."}
func (ctrl *AIServiceController) DeprecateService(c *gin.Context) {
	log.C(c).Infow("Admin deprecate AI service called")

	id, parseErr := parseID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req) // optional body — ignore bind error

	actorID, actorName := actorFromContext(c)
	if err := ctrl.biz.DeprecateService(c.Request.Context(), id, actorID, actorName, req.Reason); err != nil {
		if errors.Is(err, errno.ErrAIServiceNotFound) {
			core.WriteResponse(c, errno.ErrAIServiceNotFound, nil)
			return
		}
		log.C(c).Errorw("Failed to deprecate AI service", "id", id, "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("操作失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// ----------------------------------------------------------------------------
// POST /v1/admin/ai/services/:id/restore
// ----------------------------------------------------------------------------

// restoreServiceReq is the request body for RestoreService (reason is required).
type restoreServiceReq struct {
	Reason string `json:"reason" binding:"required"`
}

// RestoreService clears deprecated_at on a previously deprecated service.
// Body must contain a non-empty `reason` field (returns 400 otherwise).
func (ctrl *AIServiceController) RestoreService(c *gin.Context) {
	log.C(c).Infow("Admin restore AI service called")

	id, parseErr := parseID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	var req restoreServiceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("reason 字段必填"), nil)
		return
	}

	actorID, actorName := actorFromContext(c)
	if err := ctrl.biz.RestoreService(c.Request.Context(), id, actorID, actorName, req.Reason); err != nil {
		if isErrno(err) {
			core.WriteResponse(c, err, nil)
			return
		}
		log.C(c).Errorw("Failed to restore AI service", "id", id, "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("操作失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// ----------------------------------------------------------------------------
// GET /v1/admin/ai/capability-schema
// ----------------------------------------------------------------------------

// GetCapabilitySchema returns the capability field schema for each service_type.
// The response is keyed by service_type so the admin frontend can drive dynamic
// form rendering without hardcoding field names.
func (ctrl *AIServiceController) GetCapabilitySchema(c *gin.Context) {
	log.C(c).Infow("Admin get AI capability schema called")

	schemas, err := ctrl.biz.GetCapabilitySchemas(c.Request.Context())
	if err != nil {
		log.C(c).Errorw("Failed to get capability schemas", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("获取 schema 失败"), nil)
		return
	}

	core.WriteResponse(c, nil, schemas)
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// parseID parses the ":id" path parameter as a uint64.
func parseID(c *gin.Context) (uint64, error) {
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errno.ErrBind.SetMessage("无效的服务 ID")
	}
	return id, nil
}

// isErrno reports whether err is (or wraps) a typed *errno.Errno that should be
// forwarded directly to the caller rather than replaced with a generic 500.
func isErrno(err error) bool {
	var e *errno.Errno
	return errors.As(err, &e)
}
