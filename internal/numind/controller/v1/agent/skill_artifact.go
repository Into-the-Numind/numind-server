// Package agent — skill_artifact.go 提供 v2 Skill artifact 系统的 HTTP handlers。
//
// 与同包 skill.go（v1 内嵌式 skill on agent_definition）共存：
//   - v1 路径：/v1/agent/skills/* （内嵌式，未来 v2 #2 接管后将下线）
//   - v2 路径：/v1/skills/*（独立资产）+ /v1/agents/:id/skills/*（装载关系）
//
// Controller 职责：参数绑定 + 父账户校验 + 调 biz + core.WriteResponse。
// 业务逻辑（含跨租户校验、复装、级联软删）全在 internal/numind/biz/skill/artifact/。
package agent

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/skill/artifact"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// SkillArtifactController 持有 Skill 资产 CRUD 与 Agent-Skill 装载关系的两个 biz Service。
//
// Wire 一次，11 个 handler 共享同一对依赖（spec §4）：
//   - skillSvc：CRUD / history / restore / bound agents
//   - bindingSvc：attach / detach / reorder
type SkillArtifactController struct {
	skillSvc   *artifact.Service
	bindingSvc *artifact.BindingService
}

// NewSkillArtifactController 构造 controller（router init 时调一次）。
func NewSkillArtifactController(skillSvc *artifact.Service, bindingSvc *artifact.BindingService) *SkillArtifactController {
	return &SkillArtifactController{skillSvc: skillSvc, bindingSvc: bindingSvc}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// resolveCaller 返回当前请求的 caller 身份（T4 三级可见性版）。
//
// 行为：
//  1. 未登录 → 401 ErrTokenInvalid，返回 (0, 0, false, false)
//  2. 任意账户（父或子）均放行——子账户不再被 403 拦截（contract D）
//
// 返回 (callerUserID, instID, isParent, ok)：
//   - callerUserID = user.ID（真正创建者）
//   - instID       = user.ParentUserID==nil ? user.ID : *user.ParentUserID（机构 id）
//   - isParent     = user.ParentUserID == nil
func (c *SkillArtifactController) resolveCaller(ctx *gin.Context) (callerUserID, instID uint, isParent bool, ok bool) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return 0, 0, false, false
	}
	if user.ParentUserID == nil {
		return user.ID, user.ID, true, true
	}
	return user.ID, *user.ParentUserID, false, true
}

// parseUintParam 解析路径参数（:id, :skill_id, :version），失败写 400 + 返回 (0,false)。
func parseUintParam(ctx *gin.Context, name string) (uint, bool) {
	raw := ctx.Param(name)
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("invalid %s: %s", name, raw), nil)
		return 0, false
	}
	return uint(id), true
}

// ---------------------------------------------------------------------------
// /v1/skills/* — Skill CRUD + 历史 + 装载关系查询（8 endpoints）
// ---------------------------------------------------------------------------

// CreateSkill handles POST /v1/skills.
//
// Body：artifact.CreateRequest（name 必填，body_md 必填，max=200KB）
// Success：200 + 完整 Skill 对象（含 id / version=1）
// Errors：400 binding / 403 子账户 / 413 body 过大
func (c *SkillArtifactController) CreateSkill(ctx *gin.Context) {
	callerUserID, instID, isParent, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}

	var req artifact.CreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	sk, err := c.skillSvc.Create(ctx.Request.Context(), callerUserID, instID, isParent, req)
	core.WriteResponse(ctx, err, sk)
}

// ImportTemplateRequest 从官方模板一键克隆的 JSON Body。
type ImportTemplateRequest struct {
	TemplateID uint64 `json:"template_id" binding:"required"`
}

// ImportTemplate handles POST /v1/skills/import-template.
func (c *SkillArtifactController) ImportTemplate(ctx *gin.Context) {
	callerUserID, instID, isParent, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	// import-template 盖 official 可见性（机构级），仅父账户可执行。
	if !isParent {
		core.WriteResponse(ctx, errno.ErrChildAccountForbidden, nil)
		return
	}

	var req ImportTemplateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	sk, err := c.skillSvc.ImportTemplate(ctx.Request.Context(), instID, callerUserID, req.TemplateID)
	core.WriteResponse(ctx, err, sk)
}

// listSkillsQuery 是 GET /v1/skills 的 query 参数。
type listSkillsQuery struct {
	Page     int    `form:"page"`
	PageSize int    `form:"page_size"`
	Search   string `form:"search"`
}

// ListSkills handles GET /v1/skills?page=&page_size=&search=
//
// Success：200 + {list, total}
func (c *SkillArtifactController) ListSkills(ctx *gin.Context) {
	callerUserID, _, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}

	var q listSkillsQuery
	if err := ctx.ShouldBindQuery(&q); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	// T4：用户端列表走三级可见性（official + 本机构 institution + 本人 sub_user）。
	items, total, err := c.skillSvc.ListVisibleSkills(ctx.Request.Context(), callerUserID, q.Page, q.PageSize, q.Search)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"list": items, "total": total})
}

// GetSkill handles GET /v1/skills/:id.
//
// 详情接口；含 is_active=0 的 skill（前端可展示软删 skill 的历史）。
// 同时返回该 Skill 装载的 Agent 列表（bound_agents）便于前端一次性渲染详情页。
// 跨租户或不存在 → 404。
func (c *SkillArtifactController) GetSkill(ctx *gin.Context) {
	callerUserID, instID, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	skillID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}

	// T4：详情走三级可见性（含 can_edit）。
	sk, err := c.skillSvc.GetForCaller(ctx.Request.Context(), callerUserID, skillID)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}

	// bound agents 走机构 id（agent 属机构；binding 表是机构内共享）。
	agents, err := c.skillSvc.ListBoundAgents(ctx.Request.Context(), instID, skillID)
	if err != nil {
		// ListBoundAgents 失败不致命：详情还能拿到，bound_agents 退化为空数组
		core.WriteResponse(ctx, nil, gin.H{"skill": sk, "bound_agents": []model.AgentDefinition{}})
		return
	}
	// nil 切片 marshal 为 null；显式归一为 [] 让前端不用判 null
	if agents == nil {
		agents = []model.AgentDefinition{}
	}
	core.WriteResponse(ctx, nil, gin.H{"skill": sk, "bound_agents": agents})
}

// UpdateSkill handles PUT /v1/skills/:id（全量更新）。
//
// 与 Create 相同 schema；每次更新 version+1 + 写 history 快照。
// Errors：404 不存在/跨租户 / 413 body 过大 / 422 frontmatter（前端解析阶段处理，本期不到 controller）。
func (c *SkillArtifactController) UpdateSkill(ctx *gin.Context) {
	callerUserID, _, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	skillID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}

	var req artifact.CreateRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	// T4：can_edit 门控在 biz 层（caller 解析机构/父子身份）。
	sk, err := c.skillSvc.Update(ctx.Request.Context(), callerUserID, skillID, req)
	core.WriteResponse(ctx, err, sk)
}

// DeleteSkill handles DELETE /v1/skills/:id（软删 + 级联）。
//
// 返回 {affected_bindings} 告诉前端有几个 Agent 受影响。
func (c *SkillArtifactController) DeleteSkill(ctx *gin.Context) {
	callerUserID, _, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	skillID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}

	// T4：can_edit 门控在 biz 层。
	affected, err := c.skillSvc.Delete(ctx.Request.Context(), callerUserID, skillID)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"affected_bindings": affected})
}

// ListSkillHistory handles GET /v1/skills/:id/history.
//
// 返回 [{version, created_by, created_at, ...}]（snapshot 字段已 unmarshal 到 HistoryItem）。
// T4：history 暴露 created_by + diff（含 body 行级增删数），按 can_edit 门控（在 biz 层），
// 传 callerUserID（非 instID）——否则同机构另一子账户可读他人私有 skill 历史元数据。
func (c *SkillArtifactController) ListSkillHistory(ctx *gin.Context) {
	callerUserID, _, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	skillID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}

	histories, err := c.skillSvc.ListHistory(ctx.Request.Context(), callerUserID, skillID)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"list": histories, "total": len(histories)})
}

// RestoreSkill handles POST /v1/skills/:id/restore/:version.
//
// 回滚把指定 history version 的 snapshot 作为新 skill 状态 + version+1 + 写 history。
// 旧版本不删（保留审计链）。
// T4：Restore 是写路径（改写 body + 复活软删），按 can_edit 门控（在 biz 层），传 callerUserID
// （非 instID）——否则同机构另一子账户可劫持/复活他人私有 skill，绕过 computeCanEdit 守卫。
func (c *SkillArtifactController) RestoreSkill(ctx *gin.Context) {
	callerUserID, _, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	skillID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}
	version, ok := parseUintParam(ctx, "version")
	if !ok {
		return
	}

	sk, err := c.skillSvc.Restore(ctx.Request.Context(), callerUserID, skillID, version)
	core.WriteResponse(ctx, err, sk)
}

// ListSkillBoundAgents handles GET /v1/skills/:id/agents.
//
// 返回装载该 Skill 的活跃 Agent 列表（按 binding.bound_at DESC，spec §4）。
func (c *SkillArtifactController) ListSkillBoundAgents(ctx *gin.Context) {
	_, instID, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	skillID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}

	agents, err := c.skillSvc.ListBoundAgents(ctx.Request.Context(), instID, skillID)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"list": agents, "total": len(agents)})
}

// ---------------------------------------------------------------------------
// /v1/agents/:id/skills/* — Agent-Skill 装载关系（4 endpoints）
// ---------------------------------------------------------------------------

// ListAgentSkills handles GET /v1/agents/:id/skills.
//
// 列出指定 Agent 装载的所有活跃 Skill，按 sort_order ASC 排序。
func (c *SkillArtifactController) ListAgentSkills(ctx *gin.Context) {
	_, instID, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	agentID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}

	skills, err := c.bindingSvc.ListByAgent(ctx.Request.Context(), instID, agentID)
	if err != nil {
		core.WriteResponse(ctx, err, nil)
		return
	}
	if skills == nil {
		skills = []model.Skill{}
	}
	core.WriteResponse(ctx, nil, gin.H{"list": skills, "total": len(skills)})
}

// attachSkillRequest 是 POST /v1/agents/:id/skills 的 body。
type attachSkillRequest struct {
	SkillID   uint `json:"skill_id" binding:"required"`
	SortOrder int  `json:"sort_order"` // 默认 0；前端可指定显示顺序
}

// AttachSkill handles POST /v1/agents/:id/skills.
//
// 装载 Skill 到 Agent。已活跃 binding 时返回 409 ErrSkillArtifactBindingExists；
// 非活跃（曾卸载）binding 时复活 + 更新 sort_order（uk_agent_skill 唯一行）。
// 跨租户 → 404。
func (c *SkillArtifactController) AttachSkill(ctx *gin.Context) {
	_, instID, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	agentID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}

	var req attachSkillRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	err := c.bindingSvc.Attach(ctx.Request.Context(), instID, agentID, req.SkillID, req.SortOrder)
	core.WriteResponse(ctx, err, nil)
}

// DetachSkill handles DELETE /v1/agents/:id/skills/:skill_id.
//
// 软删（is_active=0 + unbound_at=NOW()）。已卸载或不存在 → 404 ErrSkillArtifactNotFound。
func (c *SkillArtifactController) DetachSkill(ctx *gin.Context) {
	_, instID, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	agentID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}
	skillID, ok := parseUintParam(ctx, "skill_id")
	if !ok {
		return
	}

	err := c.bindingSvc.Detach(ctx.Request.Context(), instID, agentID, skillID)
	core.WriteResponse(ctx, err, nil)
}

// reorderSkillsRequest 是 PUT /v1/agents/:id/skills/reorder 的 body。
type reorderSkillsRequest struct {
	SkillIDs []uint `json:"skill_ids" binding:"required"`
}

// ReorderSkills handles PUT /v1/agents/:id/skills/reorder.
//
// 按 skill_ids 数组顺序批量更新 sort_order = 0, 1, 2, ...
// 非活跃或不存在的 skill_id 静默跳过（biz 层语义）。
func (c *SkillArtifactController) ReorderSkills(ctx *gin.Context) {
	_, instID, _, ok := c.resolveCaller(ctx)
	if !ok {
		return
	}
	agentID, ok := parseUintParam(ctx, "id")
	if !ok {
		return
	}

	var req reorderSkillsRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	err := c.bindingSvc.Reorder(ctx.Request.Context(), instID, agentID, req.SkillIDs)
	core.WriteResponse(ctx, err, nil)
}
