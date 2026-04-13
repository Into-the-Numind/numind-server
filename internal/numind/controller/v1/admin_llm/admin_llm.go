package admin_llm

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// AdminLLMController 管理员 LLM 配置控制器
type AdminLLMController struct {
	router *llmrouter.Router
}

// NewAdminLLMController 创建管理员 LLM 配置控制器
func NewAdminLLMController(router *llmrouter.Router) *AdminLLMController {
	return &AdminLLMController{router: router}
}

// ---- Provider ----

// providerListItem 供应商列表响应（APIKey 脱敏）
type providerListItem struct {
	ID           uint64 `json:"id"`
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	BaseURL      string `json:"base_url"`
	APIKeyMasked string `json:"api_key_masked"`
	IsActive     bool   `json:"is_active"`
}

// ListProviders GET /admin/llm/providers
func (ctrl *AdminLLMController) ListProviders(c *gin.Context) {
	log.C(c).Infow("Admin list LLM providers called")

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	providers, total, err := ctrl.router.ListProviders(c, offset, pageSize)
	if err != nil {
		log.C(c).Errorw("Failed to list LLM providers", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	items := make([]providerListItem, 0, len(providers))
	for _, p := range providers {
		items = append(items, providerListItem{
			ID:           p.ID,
			Name:         p.Name,
			DisplayName:  p.DisplayName,
			BaseURL:      p.BaseURL,
			APIKeyMasked: p.MaskedAPIKey(),
			IsActive:     p.IsActive,
		})
	}

	core.WriteResponse(c, nil, gin.H{"list": items, "total": total})
}

// createProviderReq CreateProvider 请求体
type createProviderReq struct {
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"display_name" binding:"required"`
	BaseURL     string `json:"base_url" binding:"required"`
	APIKey      string `json:"api_key" binding:"required"`
}

// CreateProvider POST /admin/llm/providers
func (ctrl *AdminLLMController) CreateProvider(c *gin.Context) {
	log.C(c).Infow("Admin create LLM provider called")

	var req createProviderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	p := &model.LLMProvider{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		BaseURL:     req.BaseURL,
		APIKey:      req.APIKey,
		IsActive:    true,
	}

	if err := ctrl.router.CreateProvider(c, p); err != nil {
		log.C(c).Errorw("Failed to create LLM provider", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("创建失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, gin.H{"id": p.ID})
}

// updateProviderReq UpdateProvider 请求体
type updateProviderReq struct {
	DisplayName string `json:"display_name"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	IsActive    *bool  `json:"is_active"`
}

// UpdateProvider PUT /admin/llm/providers/:id
func (ctrl *AdminLLMController) UpdateProvider(c *gin.Context) {
	log.C(c).Infow("Admin update LLM provider called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的供应商 ID"), nil)
		return
	}

	var req updateProviderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	if err := ctrl.router.UpdateProvider(c, id, req.DisplayName, req.BaseURL, req.APIKey, req.IsActive); err != nil {
		log.C(c).Errorw("Failed to update LLM provider", "error", err, "id", id)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("更新失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, nil)
}

// DeleteProvider DELETE /admin/llm/providers/:id
func (ctrl *AdminLLMController) DeleteProvider(c *gin.Context) {
	log.C(c).Infow("Admin delete LLM provider called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的供应商 ID"), nil)
		return
	}

	if err := ctrl.router.DeleteProvider(c, id); err != nil {
		log.C(c).Errorw("Failed to delete LLM provider", "error", err, "id", id)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("删除失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, nil)
}

// ---- Model ----

// ListModels GET /admin/llm/models
func (ctrl *AdminLLMController) ListModels(c *gin.Context) {
	log.C(c).Infow("Admin list LLM models called")

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	models, total, err := ctrl.router.ListModels(c, offset, pageSize)
	if err != nil {
		log.C(c).Errorw("Failed to list LLM models", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": models, "total": total})
}

// createModelReq CreateModel 请求体
type createModelReq struct {
	ModelKey         string  `json:"model_key" binding:"required"`
	DisplayName      string  `json:"display_name" binding:"required"`
	IsThinking       bool    `json:"is_thinking"`
	BaseModelID      *uint64 `json:"base_model_id"`
	SupportsThinking bool    `json:"supports_thinking"`
	ThinkingOnly     bool    `json:"thinking_only"`
	Icon             string  `json:"icon"`
	SortOrder        int     `json:"sort_order"`
}

// CreateModel POST /admin/llm/models
func (ctrl *AdminLLMController) CreateModel(c *gin.Context) {
	log.C(c).Infow("Admin create LLM model called")

	var req createModelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	m := &model.LLMModel{
		ModelKey:         req.ModelKey,
		DisplayName:      req.DisplayName,
		IsThinking:       req.IsThinking,
		BaseModelID:      req.BaseModelID,
		SupportsThinking: req.SupportsThinking,
		ThinkingOnly:     req.ThinkingOnly,
		Icon:             req.Icon,
		SortOrder:        req.SortOrder,
		IsActive:         true,
	}

	if err := ctrl.router.CreateModel(c, m); err != nil {
		log.C(c).Errorw("Failed to create LLM model", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("创建失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, gin.H{"id": m.ID})
}

// updateModelReq UpdateModel 请求体
type updateModelReq struct {
	DisplayName      string  `json:"display_name"`
	Icon             string  `json:"icon"`
	SortOrder        *int    `json:"sort_order"`
	IsActive         *bool   `json:"is_active"`
	SupportsThinking *bool   `json:"supports_thinking"`
	ThinkingOnly     *bool   `json:"thinking_only"`
	BaseModelID      *uint64 `json:"base_model_id"`
	IsThinking       *bool   `json:"is_thinking"`
}

// UpdateModel PUT /admin/llm/models/:id
func (ctrl *AdminLLMController) UpdateModel(c *gin.Context) {
	log.C(c).Infow("Admin update LLM model called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的模型 ID"), nil)
		return
	}

	var req updateModelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	updates := map[string]interface{}{
		"display_name":      req.DisplayName,
		"icon":              req.Icon,
		"is_active":         req.IsActive,
		"supports_thinking": req.SupportsThinking,
		"thinking_only":     req.ThinkingOnly,
		"base_model_id":     req.BaseModelID,
		"is_thinking":       req.IsThinking,
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}

	if err := ctrl.router.UpdateModel(c, id, updates); err != nil {
		log.C(c).Errorw("Failed to update LLM model", "error", err, "id", id)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("更新失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, nil)
}

// DeleteModel DELETE /admin/llm/models/:id
func (ctrl *AdminLLMController) DeleteModel(c *gin.Context) {
	log.C(c).Infow("Admin delete LLM model called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的模型 ID"), nil)
		return
	}

	if err := ctrl.router.DeleteModel(c, id); err != nil {
		log.C(c).Errorw("Failed to delete LLM model", "error", err, "id", id)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("删除失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, nil)
}

// ---- Route ----

// ListRoutes GET /admin/llm/models/:modelId/routes
func (ctrl *AdminLLMController) ListRoutes(c *gin.Context) {
	log.C(c).Infow("Admin list LLM routes called")

	modelID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的模型 ID"), nil)
		return
	}

	routes, err := ctrl.router.ListRoutes(c, modelID)
	if err != nil {
		log.C(c).Errorw("Failed to list LLM routes", "error", err, "model_id", modelID)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("查询失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": routes, "total": len(routes)})
}

// createRouteReq CreateRoute 请求体
type createRouteReq struct {
	ProviderID         uint64  `json:"provider_id" binding:"required"`
	ProviderModelID    string  `json:"provider_model_id" binding:"required"`
	Priority           int     `json:"priority"`
	InputPricePerMTok  float64 `json:"input_price_per_mtok"`
	OutputPricePerMTok float64 `json:"output_price_per_mtok"`
}

// CreateRoute POST /admin/llm/models/:modelId/routes
func (ctrl *AdminLLMController) CreateRoute(c *gin.Context) {
	log.C(c).Infow("Admin create LLM route called")

	modelID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的模型 ID"), nil)
		return
	}

	var req createRouteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	mp := &model.LLMModelProvider{
		ModelID:            modelID,
		ProviderID:         req.ProviderID,
		ProviderModelID:    req.ProviderModelID,
		Priority:           req.Priority,
		InputPricePerMTok:  req.InputPricePerMTok,
		OutputPricePerMTok: req.OutputPricePerMTok,
		IsActive:           true,
	}

	if err := ctrl.router.CreateRoute(c, mp); err != nil {
		log.C(c).Errorw("Failed to create LLM route", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("创建失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, gin.H{"id": mp.ID})
}

// updateRouteReq UpdateRoute 请求体
type updateRouteReq struct {
	ProviderModelID    string  `json:"provider_model_id"`
	Priority           int     `json:"priority"`
	InputPricePerMTok  float64 `json:"input_price_per_mtok"`
	OutputPricePerMTok float64 `json:"output_price_per_mtok"`
	IsActive           *bool   `json:"is_active"`
}

// UpdateRoute PUT /admin/llm/models/:modelId/routes/:routeId
func (ctrl *AdminLLMController) UpdateRoute(c *gin.Context) {
	log.C(c).Infow("Admin update LLM route called")

	routeID, err := strconv.ParseUint(c.Param("routeId"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的路由 ID"), nil)
		return
	}

	var req updateRouteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	if err := ctrl.router.UpdateRoute(c, routeID, req.ProviderModelID, req.Priority, req.InputPricePerMTok, req.OutputPricePerMTok, req.IsActive); err != nil {
		log.C(c).Errorw("Failed to update LLM route", "error", err, "route_id", routeID)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("更新失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, nil)
}

// DeleteRoute DELETE /admin/llm/models/:modelId/routes/:routeId
func (ctrl *AdminLLMController) DeleteRoute(c *gin.Context) {
	log.C(c).Infow("Admin delete LLM route called")

	routeID, err := strconv.ParseUint(c.Param("routeId"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的路由 ID"), nil)
		return
	}

	if err := ctrl.router.DeleteRoute(c, routeID); err != nil {
		log.C(c).Errorw("Failed to delete LLM route", "error", err, "route_id", routeID)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("删除失败，请稍后重试"), nil)
		return
	}

	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, nil)
}
