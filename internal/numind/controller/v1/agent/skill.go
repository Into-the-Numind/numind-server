// Package agent implements HTTP handlers for the agent skill system.
// Controller is a thin layer: param binding + auth ctx extraction + biz call + core.WriteResponse.
// All business logic lives in biz/skill.
package agent

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/skill"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// SkillController handles 9 HTTP endpoints for the agent skill system.
type SkillController struct {
	svc skill.Service
}

// NewSkillController constructs a SkillController.
func NewSkillController(svc skill.Service) *SkillController {
	return &SkillController{svc: svc}
}

// ---------------------------------------------------------------------------
// Request structs (HTTP layer only — translated to biz request types)
// ---------------------------------------------------------------------------

// CreateRequest is the JSON body for POST /v1/agent/skills.
type CreateRequest struct {
	Name             string          `json:"name" binding:"required"`
	Description      string          `json:"description"`
	IconURL          string          `json:"icon_url"`
	WelcomeMessage   string          `json:"welcome_message"`
	SystemPrompt     string          `json:"system_prompt"`
	Starters         []string        `json:"starters"`
	ToolFlags        map[string]bool `json:"tool_flags"`
	DailyCreditCap   *uint           `json:"daily_credit_cap"`
	SourceTemplateID *uint64         `json:"source_template_id"`
}

// PatchRequest is the JSON body for PATCH /v1/agent/skills/:id.
// All fields are optional (nil = no change).
type PatchRequest struct {
	Name           *string          `json:"name"`
	Description    *string          `json:"description"`
	IconURL        *string          `json:"icon_url"`
	WelcomeMessage *string          `json:"welcome_message"`
	SystemPrompt   *string          `json:"system_prompt"`
	Starters       *[]string        `json:"starters"`
	ToolFlags      *map[string]bool `json:"tool_flags"`
	DailyCreditCap *uint            `json:"daily_credit_cap"`
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// mustParseID parses :id path param; on error writes 400 and returns 0, false.
func mustParseID(ctx *gin.Context) (uint64, bool) {
	raw := ctx.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid id: %s", raw), nil)
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// Create handles POST /v1/agent/skills.
func (c *SkillController) Create(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	var req CreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	ad, err := c.svc.Create(ctx.Request.Context(), user.ID, skill.CreateRequest{
		Name:             req.Name,
		Description:      req.Description,
		IconURL:          req.IconURL,
		WelcomeMessage:   req.WelcomeMessage,
		SystemPrompt:     req.SystemPrompt,
		Starters:         req.Starters,
		ToolFlags:        req.ToolFlags,
		DailyCreditCap:   req.DailyCreditCap,
		SourceTemplateID: req.SourceTemplateID,
	})
	core.WriteResponse(ctx, err, ad)
}

// List handles GET /v1/agent/skills.
func (c *SkillController) List(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	pageStr := ctx.DefaultQuery("page", "1")
	pageSizeStr := ctx.DefaultQuery("page_size", "20")
	includeInactiveStr := ctx.DefaultQuery("include_inactive", "false")

	page, _ := strconv.Atoi(pageStr)
	pageSize, _ := strconv.Atoi(pageSizeStr)
	includeInactive := includeInactiveStr == "true" || includeInactiveStr == "1"

	items, total, err := c.svc.List(ctx.Request.Context(), user.ID, includeInactive, page, pageSize)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"list": items, "total": total})
}

// Get handles GET /v1/agent/skills/:id.
func (c *SkillController) Get(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	id, ok := mustParseID(ctx)
	if !ok {
		return
	}

	ad, err := c.svc.Get(ctx.Request.Context(), user.ID, id)
	core.WriteResponse(ctx, err, ad)
}

// Patch handles PATCH /v1/agent/skills/:id.
func (c *SkillController) Patch(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	id, ok := mustParseID(ctx)
	if !ok {
		return
	}

	var req PatchRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	ad, err := c.svc.Patch(ctx.Request.Context(), user.ID, id, skill.PatchRequest{
		Name:           req.Name,
		Description:    req.Description,
		IconURL:        req.IconURL,
		WelcomeMessage: req.WelcomeMessage,
		SystemPrompt:   req.SystemPrompt,
		Starters:       req.Starters,
		ToolFlags:      req.ToolFlags,
		DailyCreditCap: req.DailyCreditCap,
	})
	core.WriteResponse(ctx, err, ad)
}

// Delete handles DELETE /v1/agent/skills/:id (soft delete).
func (c *SkillController) Delete(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	id, ok := mustParseID(ctx)
	if !ok {
		return
	}

	err := c.svc.SoftDelete(ctx.Request.Context(), user.ID, id)
	core.WriteResponse(ctx, err, nil)
}

// ListHistory handles GET /v1/agent/skills/:id/history.
func (c *SkillController) ListHistory(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	id, ok := mustParseID(ctx)
	if !ok {
		return
	}

	histories, err := c.svc.ListHistory(ctx.Request.Context(), user.ID, id)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"list": histories, "total": len(histories)})
}

// Restore handles POST /v1/agent/skills/:id/restore/:version.
func (c *SkillController) Restore(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	id, ok := mustParseID(ctx)
	if !ok {
		return
	}

	versionRaw := ctx.Param("version")
	versionParsed, err := strconv.ParseUint(versionRaw, 10, 64)
	if err != nil || versionParsed == 0 {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid version: %s", versionRaw), nil)
		return
	}

	ad, err := c.svc.Restore(ctx.Request.Context(), user.ID, id, uint(versionParsed))
	core.WriteResponse(ctx, err, ad)
}

// ListTemplates handles GET /v1/agent/skill-templates.
func (c *SkillController) ListTemplates(ctx *gin.Context) {
	templates, err := c.svc.ListTemplates(ctx.Request.Context())
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"list": templates, "total": len(templates)})
}
