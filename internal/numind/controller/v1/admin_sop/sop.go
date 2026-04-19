package admin_sop

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

func toAdminRunItem(r *model.SopRun, totalTokens, costCents int64) v1.AdminRunItem {
	item := v1.AdminRunItem{
		ID:           r.ID,
		TemplateID:   r.TemplateID,
		UserID:       r.UserID,
		Status:       r.Status,
		StartedAt:    r.StartedAt,
		FinishedAt:   r.FinishedAt,
		ErrorMessage: r.ErrorMessage,
		CreatedAt:    r.CreatedAt,
		TotalTokens:  totalTokens,
		CostCents:    costCents,
	}
	if r.Template != nil {
		item.Template = &v1.AdminRunTemplateRef{ID: r.Template.ID, Name: r.Template.Name}
	}
	if r.User != nil {
		item.User = &v1.AdminRunUserRef{ID: r.User.ID, Nickname: r.User.Nickname}
	}
	return item
}

func toAdminNodeRunItem(nr *model.SopNodeRun) v1.AdminNodeRunItem {
	return v1.AdminNodeRunItem{
		ID:        nr.ID,
		NodeID:    nr.NodeID,
		Status:    nr.Status,
		Input:     nr.Input,
		Output:    nr.Output,
		Thinking:  nr.Thinking,
		LatencyMs: nr.LatencyMs,
		Sort:      nr.Sort,
	}
}

// SopController SOP控制器
type SopController struct {
	sopBiz sop.ISopBiz
}

// NewSopController 创建SOP控制器
func NewSopController(sopBiz sop.ISopBiz) *SopController {
	return &SopController{
		sopBiz: sopBiz,
	}
}

// CreateTemplate 创建SOP模板
func (ctrl *SopController) CreateTemplate(c *gin.Context) {
	log.C(c).Infow("Create SOP template called")

	var req v1.CreateSopTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	template, err := ctrl.sopBiz.CreateTemplate(c, req.Name, req.Description, req.Prompt)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, template)
}

// GetTemplate 获取SOP模板
func (ctrl *SopController) GetTemplate(c *gin.Context) {
	log.C(c).Infow("Get SOP template called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	template, err := ctrl.sopBiz.GetTemplate(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("模板不存在"), nil)
		return
	}

	core.WriteResponse(c, nil, template)
}

// ListTemplates 获取SOP模板列表
func (ctrl *SopController) ListTemplates(c *gin.Context) {
	log.C(c).Infow("List SOP templates called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	templates, total, err := ctrl.sopBiz.ListTemplates(c, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total":     total,
		"templates": templates,
	})
}

// UpdateTemplate 更新SOP模板
func (ctrl *SopController) UpdateTemplate(c *gin.Context) {
	log.C(c).Infow("Update SOP template called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	var req v1.UpdateSopTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.Prompt != nil {
		updates["prompt"] = *req.Prompt
	}

	if err := ctrl.sopBiz.UpdateTemplate(c, uint(id), updates); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// DeleteTemplate 删除SOP模板
func (ctrl *SopController) DeleteTemplate(c *gin.Context) {
	log.C(c).Infow("Delete SOP template called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	if err := ctrl.sopBiz.DeleteTemplate(c, uint(id)); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// CreateNode 创建SOP节点
func (ctrl *SopController) CreateNode(c *gin.Context) {
	log.C(c).Infow("Create SOP node called")

	var req v1.CreateSopNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 设置默认超时时间
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 60
	}

	node := &model.SopNode{
		TemplateID:     req.TemplateID,
		ParentID:       req.ParentID,
		Name:           req.Name,
		BaseURL:        req.BaseURL,
		ModelName:      req.ModelName,
		APIKey:         req.APIKey, // 设置API密钥
		TimeoutSeconds: req.TimeoutSeconds,
		Sort:           req.Sort,
		Status:         model.SopNodeStatusActive,
		Prompt:         req.Prompt,
	}

	createdNode, err := ctrl.sopBiz.CreateNode(c, node)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, createdNode)
}

// GetNode 获取SOP节点
func (ctrl *SopController) GetNode(c *gin.Context) {
	log.C(c).Infow("Get SOP node called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的节点ID"), nil)
		return
	}

	node, err := ctrl.sopBiz.GetNode(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("节点不存在"), nil)
		return
	}

	core.WriteResponse(c, nil, node)
}

// ListNodesByTemplate 获取模板的所有节点
func (ctrl *SopController) ListNodesByTemplate(c *gin.Context) {
	log.C(c).Infow("List SOP nodes by template called")

	templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	nodes, err := ctrl.sopBiz.ListNodesByTemplate(c, uint(templateID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": len(nodes),
		"nodes": nodes,
	})
}

// UpdateNode 更新SOP节点
func (ctrl *SopController) UpdateNode(c *gin.Context) {
	log.C(c).Infow("Update SOP node called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的节点ID"), nil)
		return
	}

	var req v1.UpdateSopNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.BaseURL != nil {
		updates["base_url"] = *req.BaseURL
	}
	if req.ModelName != nil {
		updates["model_name"] = *req.ModelName
	}
	if req.APIKey != nil {
		updates["api_key"] = *req.APIKey
	}
	if req.TimeoutSeconds != nil {
		updates["timeout_seconds"] = *req.TimeoutSeconds
	}
	if req.Sort != nil {
		updates["sort"] = *req.Sort
	}
	if req.Prompt != nil {
		updates["prompt"] = *req.Prompt
	}

	if err := ctrl.sopBiz.UpdateNode(c, uint(id), updates); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// DeleteNode 删除SOP节点
func (ctrl *SopController) DeleteNode(c *gin.Context) {
	log.C(c).Infow("Delete SOP node called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的节点ID"), nil)
		return
	}

	if err := ctrl.sopBiz.DeleteNode(c, uint(id)); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// GetRun 获取SOP执行记录
func (ctrl *SopController) GetRun(c *gin.Context) {
	log.C(c).Infow("Get SOP run called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	run, err := ctrl.sopBiz.GetRun(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}

	totalTokens, costCents := fetchRunStats(c, ctrl.sopBiz, run.ID)

	core.WriteResponse(c, nil, toAdminRunItem(run, totalTokens, costCents))
}

// GetRunDetail 获取SOP执行详情（包含节点执行记录）
func (ctrl *SopController) GetRunDetail(c *gin.Context) {
	log.C(c).Infow("Get SOP run detail called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	run, nodeRuns, err := ctrl.sopBiz.GetRunWithNodes(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}

	totalTokens, costCents := fetchRunStats(c, ctrl.sopBiz, run.ID)

	nodeItems := make([]v1.AdminNodeRunItem, len(nodeRuns))
	for i := range nodeRuns {
		nodeItems[i] = toAdminNodeRunItem(&nodeRuns[i])
	}

	core.WriteResponse(c, nil, gin.H{
		"run":       toAdminRunItem(run, totalTokens, costCents),
		"node_runs": nodeItems,
	})
}

// fetchRunStats 单 run 维度获取 token/cost 统计。失败时记录 warn 并返回零值，
// 不阻断响应 — 统计缺失的可观测性价值低于整个详情接口 200 返回的可用性。
func fetchRunStats(c *gin.Context, sopBiz sop.ISopBiz, runID uint) (int64, int64) {
	stats, err := sopBiz.GetRunsStats(c, []uint{runID})
	if err != nil {
		log.C(c).Warnw("Failed to get run stats", "run_id", runID, "error", err)
		return 0, 0
	}
	s, ok := stats[runID]
	if !ok {
		return 0, 0
	}
	return s.TotalTokens, s.CostCents
}

// ListRuns 获取SOP执行记录列表
func (ctrl *SopController) ListRuns(c *gin.Context) {
	log.C(c).Infow("List SOP runs called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	var userID *uint
	if userIDStr := c.Query("user_id"); userIDStr != "" {
		uid, err := strconv.ParseUint(userIDStr, 10, 32)
		if err == nil {
			uidUint := uint(uid)
			userID = &uidUint
		}
	}

	runs, total, err := ctrl.sopBiz.ListRuns(c, offset, limit, userID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 批量获取 token 和成本统计
	runIDs := make([]uint, len(runs))
	for i, r := range runs {
		runIDs[i] = r.ID
	}

	stats, err := ctrl.sopBiz.GetRunsStats(c, runIDs)
	if err != nil {
		log.C(c).Warnw("Failed to get run stats", "error", err)
		// 不阻断，stats 为空 map 即可
		stats = map[uint]sop.RunStats{}
	}

	items := make([]v1.AdminRunItem, len(runs))
	for i := range runs {
		s := stats[runs[i].ID]
		items[i] = toAdminRunItem(&runs[i], s.TotalTokens, s.CostCents)
	}

	core.WriteResponse(c, nil, gin.H{
		"total": total,
		"runs":  items,
	})
}

// GetNote 获取SOP笔记
func (ctrl *SopController) GetNote(c *gin.Context) {
	log.C(c).Infow("Get SOP note called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的笔记ID"), nil)
		return
	}

	note, err := ctrl.sopBiz.GetNote(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("笔记不存在"), nil)
		return
	}

	core.WriteResponse(c, nil, note)
}

// ListNotesByUser 获取用户的SOP笔记列表
func (ctrl *SopController) ListNotesByUser(c *gin.Context) {
	log.C(c).Infow("List SOP notes by user called")

	userID, err := strconv.ParseUint(c.Param("user_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID"), nil)
		return
	}

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	notes, total, err := ctrl.sopBiz.ListNotesByUser(c, uint(userID), offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": total,
		"notes": notes,
	})
}
