// Package admin_ai provides Admin API handlers for the AI Service Manager.
// This file adds route CRUD handlers.
//
// Endpoints (all under /v1/admin/ai/):
//
//	POST   /services/:id/routes   — create route for a service
//	PUT    /routes/:route_id      — partial update
//	DELETE /routes/:route_id      — delete
//	POST   /routes/:route_id/toggle — flip is_active
package admin_ai

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// RouteController handles CRUD operations for ai_service_route via the admin API.
type RouteController struct {
	biz aiservice_admin.IAIServiceAdminBiz
}

// NewRouteController creates a new RouteController backed by the given biz.
func NewRouteController(biz aiservice_admin.IAIServiceAdminBiz) *RouteController {
	return &RouteController{biz: biz}
}

// parseRouteID parses the ":route_id" path parameter as a uint64.
func parseRouteID(c *gin.Context) (uint64, error) {
	raw := c.Param("route_id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errno.ErrBind.SetMessage("无效的路由 ID")
	}
	return id, nil
}

// ----------------------------------------------------------------------------
// POST /v1/admin/ai/services/:id/routes
// ----------------------------------------------------------------------------

// Create creates a new ai_service_route for the service identified by :id.
// Body must contain provider_id and provider_model_id.
// Pricing fields (input_price_per_mtok, output_price_per_mtok, etc.) are intentionally
// excluded per architecture decision (T-arch drops those columns from the table).
func (ctrl *RouteController) Create(c *gin.Context) {
	log.C(c).Infow("Admin create route called")

	serviceID, parseErr := parseID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	var req aiservice_admin.CreateRouteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	actorID, actorName := actorFromContext(c)
	dto, warnings, bizErr := ctrl.biz.CreateRoute(c.Request.Context(), serviceID, req, actorID, actorName)
	if bizErr != nil {
		if isErrno(bizErr) {
			core.WriteResponse(c, bizErr, nil)
			return
		}
		log.C(c).Errorw("Failed to create route", "service_id", serviceID, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("创建路由失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"route": dto, "warnings": warnings})
}

// ----------------------------------------------------------------------------
// PUT /v1/admin/ai/routes/:route_id
// ----------------------------------------------------------------------------

// Update partially updates an existing ai_service_route.
// All fields are optional. provider_id is immutable (requires delete + recreate).
func (ctrl *RouteController) Update(c *gin.Context) {
	log.C(c).Infow("Admin update route called")

	routeID, parseErr := parseRouteID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	var req aiservice_admin.UpdateRouteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	actorID, actorName := actorFromContext(c)
	dto, warnings, bizErr := ctrl.biz.UpdateRoute(c.Request.Context(), routeID, req, actorID, actorName)
	if bizErr != nil {
		if isErrno(bizErr) {
			core.WriteResponse(c, bizErr, nil)
			return
		}
		log.C(c).Errorw("Failed to update route", "route_id", routeID, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("更新路由失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"route": dto, "warnings": warnings})
}

// ----------------------------------------------------------------------------
// DELETE /v1/admin/ai/routes/:route_id
// ----------------------------------------------------------------------------

// Delete removes a route. Rejects with 400 if this would leave the service
// with zero active routes (last-active guard).
func (ctrl *RouteController) Delete(c *gin.Context) {
	log.C(c).Infow("Admin delete route called")

	routeID, parseErr := parseRouteID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	actorID, actorName := actorFromContext(c)
	if bizErr := ctrl.biz.DeleteRoute(c.Request.Context(), routeID, actorID, actorName); bizErr != nil {
		if isErrno(bizErr) {
			core.WriteResponse(c, bizErr, nil)
			return
		}
		log.C(c).Errorw("Failed to delete route", "route_id", routeID, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("删除路由失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// ----------------------------------------------------------------------------
// POST /v1/admin/ai/routes/:route_id/toggle
// ----------------------------------------------------------------------------

// Toggle flips the is_active flag on a route. Rejects with 400 if toggling
// active→inactive would leave the service with zero active routes.
func (ctrl *RouteController) Toggle(c *gin.Context) {
	log.C(c).Infow("Admin toggle route called")

	routeID, parseErr := parseRouteID(c)
	if parseErr != nil {
		core.WriteResponse(c, parseErr, nil)
		return
	}

	actorID, actorName := actorFromContext(c)
	dto, bizErr := ctrl.biz.ToggleRoute(c.Request.Context(), routeID, actorID, actorName)
	if bizErr != nil {
		if isErrno(bizErr) {
			core.WriteResponse(c, bizErr, nil)
			return
		}
		log.C(c).Errorw("Failed to toggle route", "route_id", routeID, "error", bizErr)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("切换路由状态失败，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, dto)
}
