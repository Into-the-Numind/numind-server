// Package marketplace — HTTP handlers for /v1/marketplace/* (user) and
// /v1/admin/marketplace/* (admin) endpoints.
//
// spec §5 controllers / §6 router registration.
//
// Controller 职责：参数绑定 + auth 上下文提取 + 调 biz + 业务 sentinel → errno 映射。
// 业务逻辑（含跨租户校验、两阶段 commit、Langfuse trace）全在
// internal/numind/biz/marketplace/。
package marketplace

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	bizmarketplace "numind-server/internal/numind/biz/marketplace"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// Controller 持有 marketplace Service 单例。Wire 一次，9 个 handler 共享。
type Controller struct {
	svc bizmarketplace.Service
}

// NewController 构造 controller（router init 时调一次）。
func NewController(svc bizmarketplace.Service) *Controller {
	return &Controller{svc: svc}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// resolveCallerUserID returns the authenticated user's ID (parent or child).
// Parent-account-only enforcement is done at the biz layer (verifyParent),
// so this helper does NOT block child accounts — it just extracts user ID.
// Unauthenticated → 401 + (0, false).
func (c *Controller) resolveCallerUserID(ctx *gin.Context) (uint, bool) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return 0, false
	}
	return user.ID, true
}

// parseUintParam parses path parameter (:id) into uint, writes 400 on failure.
func parseUintParam(ctx *gin.Context, name string) (uint, bool) {
	raw := ctx.Param(name)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid %s: %s", name, raw), nil)
		return 0, false
	}
	return uint(id), true
}

// mapBizError maps marketplace sentinel errors to project errno types so
// core.WriteResponse emits the correct HTTP status + business code.
// T7 (errno + DB seed) will replace this helper by moving sentinels to
// internal/pkg/errno/skill_marketplace.go as &Errno{...} literals.
func mapBizError(err error) error {
	switch {
	case errors.Is(err, bizmarketplace.ErrChildAccountCannotAccessMarketplace):
		return errno.ErrChildAccountForbidden
	case errors.Is(err, bizmarketplace.ErrSkillNotOwned):
		return errno.ErrForbidden
	case errors.Is(err, bizmarketplace.ErrSkillAlreadyPublished):
		return errno.ErrBind.SetMessage("该技能已上架，请先下架再重新发布")
	case errors.Is(err, bizmarketplace.ErrSelfSubscribeForbidden):
		return errno.ErrBind.SetMessage("不能订阅自己发布的技能")
	case errors.Is(err, bizmarketplace.ErrAlreadySubscribed):
		return errno.ErrBind.SetMessage("已订阅该技能")
	case errors.Is(err, bizmarketplace.ErrMarketplaceNotFound):
		return errno.ErrPageNotFound.SetMessage("市场项目不存在或已下架")
	case errors.Is(err, bizmarketplace.ErrSubscriptionNotFound):
		return errno.ErrPageNotFound.SetMessage("订阅记录不存在")
	case errors.Is(err, bizmarketplace.ErrSanitizeUnavailable):
		return errno.ErrInternalServer.SetMessage("脱敏服务暂不可用，请稍后重试")
	case errors.Is(err, bizmarketplace.ErrSanitizeConfirmationMismatch):
		return errno.ErrBind.SetMessage("脱敏内容与确认不符，请重新预览")
	case errors.Is(err, bizmarketplace.ErrSkillBodyEmpty):
		return errno.ErrBind.SetMessage("技能正文为空，无法发布")
	}
	return err // unknown → core.WriteResponse defaults to 500
}

// ---------------------------------------------------------------------------
// User endpoints — /v1/marketplace/*
// ---------------------------------------------------------------------------

// SanitizePreview handles POST /v1/marketplace/sanitize-preview.
//
// Request: {"skill_id": <uint>}
// Response: {"sanitized_body_md": "<markdown>"} on 200.
func (c *Controller) SanitizePreview(ctx *gin.Context) {
	userID, ok := c.resolveCallerUserID(ctx)
	if !ok {
		return
	}
	var req struct {
		SkillID uint `json:"skill_id" binding:"required"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	out, err := c.svc.SanitizePreview(ctx.Request.Context(), userID, req.SkillID)
	if err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"sanitized_body_md": out})
}

// Publish handles POST /v1/marketplace/publish.
func (c *Controller) Publish(ctx *gin.Context) {
	userID, ok := c.resolveCallerUserID(ctx)
	if !ok {
		return
	}
	var req bizmarketplace.PublishRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	mp, err := c.svc.Publish(ctx.Request.Context(), userID, req)
	if err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, mp)
}

// Unpublish handles POST /v1/marketplace/:id/unpublish.
func (c *Controller) Unpublish(ctx *gin.Context) {
	userID, ok := c.resolveCallerUserID(ctx)
	if !ok {
		return
	}
	id, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}
	if err := c.svc.Unpublish(ctx.Request.Context(), userID, id); err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, nil)
}

// List handles GET /v1/marketplace/list. Public browse — auth required (per spec
// "only parent accounts can use marketplace UI"), but biz layer doesn't gate
// because List doesn't fall under cross-tenant rule 2 (read-only).
func (c *Controller) List(ctx *gin.Context) {
	if _, ok := c.resolveCallerUserID(ctx); !ok {
		return
	}
	var q bizmarketplace.BrowseQuery
	if err := ctx.ShouldBindQuery(&q); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	items, total, err := c.svc.List(ctx.Request.Context(), q)
	if err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{
		"list":      items,
		"total":     total,
		"page":      q.Page,
		"page_size": q.PageSize,
	})
}

// ListMySubscriptions handles GET /v1/marketplace/my-subscriptions.
//
// Gin path order constraint: this route MUST be registered BEFORE /:id (which
// would otherwise capture "my-subscriptions" as the :id param). See router.go.
func (c *Controller) ListMySubscriptions(ctx *gin.Context) {
	userID, ok := c.resolveCallerUserID(ctx)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(ctx.DefaultQuery("page_size", "20"))
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	items, total, err := c.svc.ListMySubscriptions(ctx.Request.Context(), userID, offset, pageSize)
	if err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{
		"list":      items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// Get handles GET /v1/marketplace/:id.
func (c *Controller) Get(ctx *gin.Context) {
	userID, ok := c.resolveCallerUserID(ctx)
	if !ok {
		return
	}
	id, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}
	mp, err := c.svc.Get(ctx.Request.Context(), id, userID)
	if err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, mp)
}

// Subscribe handles POST /v1/marketplace/:id/subscribe.
func (c *Controller) Subscribe(ctx *gin.Context) {
	userID, ok := c.resolveCallerUserID(ctx)
	if !ok {
		return
	}
	id, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}
	clonedID, err := c.svc.Subscribe(ctx.Request.Context(), userID, id)
	if err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{
		"cloned_skill_id": clonedID,
		"marketplace_id":  id,
	})
}

// Unsubscribe handles DELETE /v1/marketplace/:id/unsubscribe.
func (c *Controller) Unsubscribe(ctx *gin.Context) {
	userID, ok := c.resolveCallerUserID(ctx)
	if !ok {
		return
	}
	id, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}
	if err := c.svc.Unsubscribe(ctx.Request.Context(), userID, id); err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, nil)
}

// ---------------------------------------------------------------------------
// Admin endpoint — /v1/admin/marketplace/:id/recommend
// ---------------------------------------------------------------------------

// SetRecommended handles POST /v1/admin/marketplace/:id/recommend.
// Admin path: admin_token middleware enforces auth in admin_router.go; this
// handler does not check user identity (admin operations apply platform-wide).
func (c *Controller) SetRecommended(ctx *gin.Context) {
	id, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}
	var req struct {
		Recommended bool `json:"recommended"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	if err := c.svc.SetRecommended(ctx.Request.Context(), id, req.Recommended); err != nil {
		core.WriteResponse(ctx, mapBizError(err), nil)
		return
	}
	core.WriteResponse(ctx, nil, nil)
}
