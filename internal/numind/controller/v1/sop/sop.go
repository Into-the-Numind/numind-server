package sop

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/model/dto"
	"numind-server/internal/pkg/util"
	v1 "numind-server/pkg/api/numind/v1"
)

// 常量定义
const (
	// 文件大小限制（10MB）
	MaxFileSize = 10 * 1024 * 1024
	// 单次上传文件数量限制
	MaxFilesPerUpload = 10
	// 文本内容最大长度（用于提取）
	MaxTextContentLength = 100000
	// AI调用超时时间（秒）
	AICallTimeout = 30
	// 支持的文件扩展名
	AllowedExtensions = ".txt,.md,.docx,.doc,.pdf,.rtf"
)

// SopController 用户端SOP控制器
type SopController struct {
	sopBiz    sop.ISopBiz
	aliBiz    ali.AliBiz
	volcBiz   volc.VolcBiz
	creditBiz credit.ICreditBiz
	llmRouter *llmrouter.Router
}

// NewSopController 创建用户端SOP控制器
func NewSopController(sopBiz sop.ISopBiz, aliBiz ali.AliBiz, volcBiz volc.VolcBiz, creditBiz credit.ICreditBiz, llmRouter *llmrouter.Router) *SopController {
	return &SopController{
		sopBiz:    sopBiz,
		aliBiz:    aliBiz,
		volcBiz:   volcBiz,
		creditBiz: creditBiz,
		llmRouter: llmRouter,
	}
}

// QualityCheckResult 质量检测结果
type QualityCheckResult struct {
	Status      string   `json:"status"`      // "合格" 或 "需要改进"
	Summary     string   `json:"summary"`     // 问题摘要
	Problems    []string `json:"problems"`    // 发现的问题列表
	Suggestions []string `json:"suggestions"` // AI改进建议
	Score       int      `json:"score"`       // 质量分数 0-100
}

// ... existing code ...

// CheckTemplatePermission 检查用户是否有运行指定模板的权限
// 用于前端在跳转SOP详情页前进行权限检查
func (ctrl *SopController) CheckTemplatePermission(c *gin.Context) {
	log.C(c).Infow("Check template permission called")

	templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 模板必须是 active 且已发布 才能在工作区运行；草稿/下线的模板一律无权限
	template, err := ctrl.sopBiz.GetTemplate(c, uint(templateID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("模板不存在"), nil)
		return
	}
	if template.Status != "active" || template.PublishStatus != model.SopPublishStatusPublished {
		core.WriteResponse(c, nil, gin.H{
			"has_permission": false,
		})
		return
	}

	// 如果用户是直接客户(parent_user_id为NULL)，则有权限执行所有已发布模板
	if user.ParentUserID == nil {
		core.WriteResponse(c, nil, gin.H{
			"has_permission": true,
		})
		return
	}

	// 如果是二级客户，检查白名单权限
	hasPermission, err := store.S.Customers().HasTemplatePermission(c, user.ID, uint(templateID))
	if err != nil {
		log.C(c).Errorw("Failed to check template permission", "user_id", user.ID, "template_id", templateID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("权限检查失败"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"has_permission": hasPermission,
	})
}

// DeleteRun 删除指定执行记录（物理删除）
func (ctrl *SopController) DeleteRun(c *gin.Context) {
	log.C(c).Infow("User delete SOP run called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 调用biz层执行删除（biz层会校验所有权）
	if err := ctrl.sopBiz.DeleteRun(c, uint(id), user.ID); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "删除成功"})
}

// BatchDeleteRuns 批量删除执行记录
func (ctrl *SopController) BatchDeleteRuns(c *gin.Context) {
	log.C(c).Infow("User batch delete SOP runs called")

	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 调用biz层执行批量删除
	if err := ctrl.sopBiz.DeleteRuns(c, req.IDs, user.ID); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "批量删除成功"})
}

// GetRun 获取SOP执行记录（用户端）
func (ctrl *SopController) GetRun(c *gin.Context) {
	log.C(c).Infow("User get SOP run called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	run, err := ctrl.sopBiz.GetRun(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}

	// 验证是否是用户自己的记录
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	core.WriteResponse(c, nil, run)
}

// GetRunDetail 获取SOP执行详情（用户端）
func (ctrl *SopController) GetRunDetail(c *gin.Context) {
	log.C(c).Infow("User get SOP run detail called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	run, nodeRuns, err := ctrl.sopBiz.GetRunWithNodes(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}

	// 验证是否是用户自己的记录
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"run":       run,
		"node_runs": nodeRuns,
	})
}

// ListMyRuns 获取当前用户的SOP执行记录列表
func (ctrl *SopController) ListMyRuns(c *gin.Context) {
	log.C(c).Infow("User list my SOP runs called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	// 自动清理处于运行中状态但已超时的"僵尸任务"（30分钟超时）
	// 这样可以确保用户看到的列表状态是准确的，提升鲁棒性
	_ = ctrl.sopBiz.CleanZombieRuns(c, 30*time.Minute)

	uid := user.ID
	runs, total, err := ctrl.sopBiz.ListRuns(c, offset, limit, &uid)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": total,
		"runs":  runs,
	})
}

// ListMyExecutedTemplates 获取当前用户已执行的SOP模板列表（按模板分组）
func (ctrl *SopController) ListMyExecutedTemplates(c *gin.Context) {
	log.C(c).Infow("User list my executed SOP templates called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取用户已执行的模板列表
	templates, err := ctrl.sopBiz.ListExecutedTemplatesByUser(c, user.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 转换为API响应格式
	response := v1.ListExecutedTemplatesResponse{
		Total:     int64(len(templates)),
		Templates: make([]v1.ExecutedTemplateInfo, len(templates)),
	}

	for i, t := range templates {
		response.Templates[i] = v1.ExecutedTemplateInfo{
			TemplateID:     t.TemplateID,
			TemplateName:   t.TemplateName,
			RunCount:       t.RunCount,
			ExecutedAt:     t.ExecutedAt,
			RunID:          t.RunID,
			RunStatus:      t.RunStatus,
			CompletedCount: t.CompletedCount,
			TotalNodes:     t.TotalNodes,
		}
	}

	core.WriteResponse(c, nil, response)
}

// ListTemplateRuns 获取指定模板下的所有历史运行记录（包含完整信息）
func (ctrl *SopController) ListTemplateRuns(c *gin.Context) {
	log.C(c).Infow("User list template runs with details called")

	// 从URL参数获取template_id
	templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 获取分页参数
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	// 限制最大limit，防止一次性返回过多数据
	if limit > 100 {
		limit = 100
	}

	// 获取template信息（用于获取name）
	template, err := ctrl.sopBiz.GetTemplate(c, uint(templateID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("模板不存在"), nil)
		return
	}

	// 调用biz层获取数据
	histories, total, err := ctrl.sopBiz.ListTemplateRunsWithDetails(c, user.ID, uint(templateID), offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 返回响应
	response := v1.ListTemplateRunsResponse{
		TemplateID: uint(templateID),
		Name:       template.Name,
		Total:      total,
		Runs:       histories,
	}

	core.WriteResponse(c, nil, response)
}

// ListMyNotes 获取当前用户的SOP笔记列表
func (ctrl *SopController) ListMyNotes(c *gin.Context) {
	log.C(c).Infow("User list my SOP notes called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	notes, total, err := ctrl.sopBiz.ListNotesByUser(c, user.ID, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": total,
		"notes": notes,
	})
}

// GetNote 获取SOP笔记详情（用户端）
func (ctrl *SopController) GetNote(c *gin.Context) {
	log.C(c).Infow("User get SOP note called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的笔记ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	note, err := ctrl.sopBiz.GetNote(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("笔记不存在"), nil)
		return
	}

	// 验证是否是用户自己的笔记
	if note.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此笔记"), nil)
		return
	}

	core.WriteResponse(c, nil, note)
}

// ListTemplates 获取可用的SOP模板列表（用户端，只显示已发布的 active 模板）
// 草稿/下线状态的模板不会出现在工作区，需从配置中心管理入口访问。
// 默认 limit 为 500，覆盖权限弹窗等需要一次性拉全量的场景；管理端列表走独立端点。
// 返回每项含 has_permission 供 UI 显示锁标志；安全 gate 仍由 check-permission / SOP 运行端点强制。
func (ctrl *SopController) ListTemplates(c *gin.Context) {
	log.C(c).Infow("User list SOP templates called")

	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "500"))

	templates, total, err := ctrl.sopBiz.ListVisibleTemplatesWithPermission(c, user, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total":     total,
		"templates": templates,
	})
}

// GetTemplateNodes 获取指定模板的所有节点（用户端）
func (ctrl *SopController) GetTemplateNodes(c *gin.Context) {
	log.C(c).Infow("User get SOP template nodes called")

	templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	// 验证模板是否存在且为active状态
	template, err := ctrl.sopBiz.GetTemplate(c, uint(templateID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("模板不存在"), nil)
		return
	}

	// 只允许获取 active 且已发布的模板节点
	if template.Status != "active" {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("模板未激活"), nil)
		return
	}
	if template.PublishStatus != model.SopPublishStatusPublished {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("模板未发布"), nil)
		return
	}

	// 获取模板的所有节点
	nodes, err := ctrl.sopBiz.ListNodesByTemplate(c, uint(templateID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 按Sort字段排序节点（使用标准库sort包）
	sortedNodes := make([]model.SopNode, len(nodes))
	copy(sortedNodes, nodes)
	sort.Slice(sortedNodes, func(i, j int) bool {
		return sortedNodes[i].Sort < sortedNodes[j].Sort
	})

	// P0 安全：禁止直接序列化 model.SopNode（含 api_key/base_url/model_name/
	// timeout_seconds/prompt 五个敏感字段），统一通过 dto 包转换隐藏字段。
	// 详见 docs/superpowers/specs/2026-04-11-sop-runtime-vue-rewrite-design.md §1
	core.WriteResponse(c, nil, gin.H{
		"template": dto.ToSopTemplatePublicDTO(template),
		"nodes":    dto.ToSopNodePublicDTOList(sortedNodes),
		"total":    len(sortedNodes),
	})
}

// CreateRun 创建SOP执行（不立即执行）
func (ctrl *SopController) CreateRun(c *gin.Context) {
	log.C(c).Infow("User create SOP run called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	var req v1.CreateSopRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 模板必须是 active 且已发布 才能创建运行；阻止草稿/下线模板被绕过 URL 执行
	templateToRun, err := ctrl.sopBiz.GetTemplate(c, req.TemplateID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("模板不存在"), nil)
		return
	}
	if templateToRun.Status != "active" || templateToRun.PublishStatus != model.SopPublishStatusPublished {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("模板未发布，无法运行"), nil)
		return
	}

	// 支持书签功能：创建 Run 并自动应用书签
	run, appliedBookmarkIDs, err := ctrl.sopBiz.CreateRunWithBookmarks(c.Request.Context(), req.TemplateID, user.ID, req.Text, req.AutoApplyBookmarks)
	if err != nil {
		// 透传 biz 层错误：*errno.Errno 保留正确 HTTP 状态码（如 403 权限拒绝），
		// 其他 error 由 errno.Decode 兜底为 500。避免把业务拒绝误报为 500
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"id":                 run.ID,
		"template_id":        run.TemplateID,
		"status":             run.Status,
		"conversation_id":    run.ConversationID,
		"auto_applied_count": len(appliedBookmarkIDs),
		"created_at":         run.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// GetNextNode 获取下一个待执行节点
func (ctrl *SopController) GetNextNode(c *gin.Context) {
	log.C(c).Infow("User get next node called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户
	run, err := ctrl.sopBiz.GetRun(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	node, hasNext, err := ctrl.sopBiz.GetNextNode(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	if node == nil {
		core.WriteResponse(c, nil, gin.H{
			"node":     nil,
			"has_next": false,
			"message":  "所有节点已执行完成",
		})
		return
	}

	core.WriteResponse(c, nil, v1.NextNodeResponse{
		NodeID:   node.ID,
		NodeName: node.Name,
		Sort:     node.Sort,
		IsFirst:  len(run.ConversationID) > 0, // 简化判断，实际应该检查是否有已完成的节点
		HasNext:  hasNext,
	})
}

// ExecuteNodeStream 流式执行指定节点（支持文件上传）
func (ctrl *SopController) ExecuteNodeStream(c *gin.Context) {
	log.C(c).Infow("User execute SOP node stream called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	nodeID, err := strconv.ParseUint(c.Param("node_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的节点ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户（使用轻量级权限验证，避免完整查询）
	hasAccess, err := ctrl.sopBiz.CheckRunOwnership(c, uint(runID), user.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if !hasAccess {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	// 积分预检：检查用户是否有足够积分执行 SOP 节点
	if canPerform, reason := ctrl.creditBiz.CanPerformAIOperation(c, user, "sop_run"); !canPerform {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", reason), nil)
		return
	}

	// 处理请求：支持JSON和multipart/form-data两种格式
	var inputText string
	var uploadedFileIDs []uint

	// 检查是否是multipart/form-data
	contentType := c.GetHeader("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		// 处理文件上传
		form, err := c.MultipartForm()
		if err == nil {
			// 获取text字段（可选）
			if textValues := form.Value["text"]; len(textValues) > 0 {
				inputText = textValues[0]
			}

			// 获取上传的文件
			files := form.File["files"]
			if len(files) == 0 {
				files = form.File["file"] // 兼容单数形式
			}

			// 如果有文件，上传到COS并保存记录
			if len(files) > 0 {
				var fileTexts []string
				var uploadedFileNames []string
				var uploadErrors []string

				for _, file := range files {
					// 上传文件到COS并创建记录
					sopFile, err := ctrl.uploadFileToCOS(c, file, user.ID, uint(runID), uint(nodeID))
					if err != nil {
						log.C(c).Warnw("Failed to upload file", "filename", file.Filename, "error", err)
						uploadErrors = append(uploadErrors, fmt.Sprintf("%s: %v", file.Filename, err))
						// 继续处理其他文件
						continue
					}

					uploadedFileIDs = append(uploadedFileIDs, sopFile.ID)
					uploadedFileNames = append(uploadedFileNames, file.Filename)

					// 提取文本内容
					if sopFile.Content != "" {
						fileTexts = append(fileTexts, fmt.Sprintf("=== %s ===\n%s", file.Filename, sopFile.Content))
					}
				}

				// 如果所有文件都上传失败，返回错误
				if len(uploadedFileNames) == 0 && len(uploadErrors) > 0 {
					errorMsg := fmt.Sprintf("所有文件上传失败：%s", strings.Join(uploadErrors, "; "))
					core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("%s", errorMsg), nil)
					return
				}

				// 合并文件内容和用户输入的文本
				if len(fileTexts) > 0 {
					// 有文本内容，使用文本内容
					fileContent := strings.Join(fileTexts, "\n\n")
					if inputText != "" {
						inputText = inputText + "\n\n" + fileContent
					} else {
						inputText = fileContent
					}
				} else if len(uploadedFileNames) > 0 {
					// 文件上传成功但无法提取文本内容（如PDF等格式）
					// 至少使用文件名作为输入，让AI知道用户上传了文件
					fileInfo := fmt.Sprintf("用户已上传以下文件：%s\n\n注意：这些文件无法自动提取文本内容，请根据文件名和上下文进行处理。", strings.Join(uploadedFileNames, "、"))
					if inputText != "" {
						inputText = inputText + "\n\n" + fileInfo
					} else {
						inputText = fileInfo
					}
					log.C(c).Infow("文件上传成功但无法提取文本，使用文件名作为输入", "files", uploadedFileNames)
				}
			}
		}
	} else {
		// JSON格式
		var req v1.ExecuteSopNodeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			// text是可选的，所以不强制要求
			req.Text = ""
		}
		inputText = req.Text
	}

	// 设置SSE响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用nginx缓冲

	// 获取Flusher（用于实时刷新）
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Streaming not supported"), nil)
		return
	}

	// 使用互斥锁保护并发写入 c.Writer（防止 heartbeater 和主 handler 冲突造成数据竞争）
	var mu sync.Mutex

	// 创建带心跳的 context，用于定期发送心跳保持连接
	heartbeatCtx, heartbeatCancel := context.WithCancel(c.Request.Context())
	defer heartbeatCancel()

	// 启动心跳 goroutine，每 15 秒发送一次注释行（SSE 心跳），更频繁地保持连接活跃
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()
	go func() {
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-heartbeatTicker.C:
				// 发送 SSE 注释行（以 : 开头）作为心跳
				// 检查连接是否仍然有效
				select {
				case <-c.Request.Context().Done():
					return
				default:
					// 发送心跳注释行（使用简洁格式，确保前端不会解析）
					mu.Lock()
					if _, err := c.Writer.WriteString(":\n\n"); err != nil {
						log.C(c).Warnw("Failed to send heartbeat", "error", err)
						mu.Unlock()
						return
					}
					flusher.Flush()
					mu.Unlock()
				}
			}
		}
	}()

	// 读取模型参数（可选），走三级 fallback 解析
	queryModelKey := c.Query("model_key")
	thinkingStr := c.Query("thinking")
	queryThinking := thinkingStr == "1" || thinkingStr == "true"
	var queryThinkingPtr *bool
	if thinkingStr != "" {
		queryThinkingPtr = &queryThinking
	}

	resolvedModelKey, resolvedThinking, resolveErr := ctrl.llmRouter.ResolveUserModel(
		c.Request.Context(), user.ID, "sop", queryModelKey, queryThinkingPtr)
	if resolveErr != nil {
		log.C(c).Warnw("LLMRouter.ResolveUserModel failed, using node default config", "error", resolveErr)
		// 解析失败不阻断执行，使用空 modelKey 回退到节点默认配置
		resolvedModelKey = ""
		resolvedThinking = false
	}

	// 流式执行节点
	err = ctrl.sopBiz.ExecuteNodeStream(heartbeatCtx, uint(runID), uint(nodeID), inputText, resolvedModelKey, resolvedThinking, func(event string, chunk string) error {
		// 检查客户端是否断开连接
		select {
		case <-c.Request.Context().Done():
			log.C(c).Infow("Client disconnected during stream")
			return c.Request.Context().Err()
		default:
		}

		// 发送SSE格式的数据（需要对JSON进行转义）
		chunkJSON, _ := json.Marshal(chunk)
		var data string
		if event == "thinking" {
			data = fmt.Sprintf("event: thinking\ndata: %s\n\n", string(chunkJSON))
		} else if event == "message" {
			data = fmt.Sprintf("data: %s\n\n", string(chunkJSON))
		} else if event == "done" {
			data = "event: done\ndata: {\"status\":\"completed\"}\n\n"
		} else {
			return nil
		}

		mu.Lock()
		defer mu.Unlock()
		if _, err := c.Writer.WriteString(data); err != nil {
			log.C(c).Warnw("Failed to write chunk to client", "error", err)
			return err
		}

		// 立即刷新，确保数据实时发送
		flusher.Flush()
		return nil
	})

	if err != nil {
		// 检查是否是客户端断开连接
		if c.Request.Context().Err() != nil {
			log.C(c).Infow("Client disconnected during stream", "error", err)
			return // 客户端断开，不需要发送错误
		}

		// 发送错误事件
		errorMsg, _ := json.Marshal(err.Error())
		errorData := fmt.Sprintf("event: error\ndata: %s\n\n", string(errorMsg))
		mu.Lock()
		_, _ = c.Writer.WriteString(errorData)
		flusher.Flush()
		mu.Unlock()
		return
	}

	// 完成事件已在流式回调中发送；此处仅附带上传文件ID的结束包（可选）
	fileIDsJSON, _ := json.Marshal(uploadedFileIDs)
	doneData := fmt.Sprintf("event: done\ndata: {\"status\":\"completed\",\"uploaded_file_ids\":%s}\n\n", string(fileIDsJSON))
	mu.Lock()
	_, _ = c.Writer.WriteString(doneData)
	flusher.Flush()
	mu.Unlock()
}

// uploadFileToCOS 上传文件到COS并创建数据库记录（考虑各种极端情况）
func (ctrl *SopController) uploadFileToCOS(c *gin.Context, file *multipart.FileHeader, userID, runID, nodeID uint) (*model.SopFile, error) {
	// 1. 验证文件大小
	if file.Size <= 0 {
		return nil, fmt.Errorf("文件为空")
	}
	if file.Size > MaxFileSize {
		return nil, fmt.Errorf("文件大小超过限制（最大%dMB）", MaxFileSize/(1024*1024))
	}

	// 2. 验证文件名（防止路径遍历攻击）
	fileName := sanitizeFileName(file.Filename)
	if fileName == "" {
		return nil, fmt.Errorf("无效的文件名")
	}

	// 3. 验证文件扩展名
	ext := strings.ToLower(filepath.Ext(fileName))
	if !isAllowedExtension(ext) {
		return nil, fmt.Errorf("不支持的文件格式: %s，支持格式: %s", ext, AllowedExtensions)
	}

	// 4. 打开文件（使用defer确保关闭）
	src, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	defer src.Close()

	// 5. 读取文件数据（限制读取大小，防止内存溢出）
	limitedReader := io.LimitReader(src, MaxFileSize+1) // +1 用于检测是否超过限制
	fileData, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}

	// 检查是否超过限制
	if int64(len(fileData)) > MaxFileSize {
		return nil, fmt.Errorf("文件大小超过限制")
	}

	// 6. 生成安全的文件名和对象键
	timestamp := time.Now().UnixNano()
	safeFileName := fmt.Sprintf("sop_file_%d_%d%s", userID, timestamp, ext)
	objectKey := fmt.Sprintf("sop/%d/%d/%s", userID, runID, safeFileName)

	// 7. 上传到COS（带重试和错误处理）
	var cosURL string
	if util.IsCOSEnabled() {
		cosURL, err = util.UploadBytesToCOS(c, objectKey, file.Header.Get("Content-Type"), fileData)
		if err != nil {
			log.C(c).Warnw("COS上传失败，继续处理", "error", err, "object_key", objectKey)
			// COS上传失败不影响功能，继续处理
			cosURL = "" // 设置为空，表示未上传到COS
		} else {
			log.C(c).Infow("文件上传到COS成功", "cos_url", cosURL, "object_key", objectKey)
			// 记录 COS 上传用量
			billing.RecordCOS(userID, "sop_file_upload", file.Size,
				billing.Metadata("object_key", objectKey, "filename", fileName))
		}
	}

	// 8. 提取文本内容（支持多种格式，限制长度）
	var content string
	if ext == ".txt" || ext == ".md" {
		text := string(fileData)
		// 验证UTF-8编码并清理
		if !utf8.ValidString(text) {
			// 尝试清理无效字符
			text = strings.ToValidUTF8(text, "")
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			content = text[:MaxTextContentLength] + "...(内容过长已截断)"
		} else {
			content = text
		}
	} else if ext == ".pdf" {
		// PDF文件文本提取
		text, err := extractTextFromPDF(fileData)
		if err == nil {
			// 清理和验证文本，确保可以安全存储到数据库
			text = sanitizeUTF8ForDatabase(text)
			if len(text) > MaxTextContentLength {
				content = text[:MaxTextContentLength] + "...(内容过长已截断)"
			} else {
				content = text
			}
		} else {
			log.C(c).Warnw("PDF文本提取失败", "error", err, "filename", fileName)
			// PDF提取失败不影响文件上传，只是不保存文本内容
		}
	} else if ext == ".docx" {
		// DOCX文件文本提取
		text, err := extractTextFromDOCX(fileData)
		if err == nil {
			// 清理和验证文本，确保可以安全存储到数据库
			text = sanitizeUTF8ForDatabase(text)
			if len(text) > MaxTextContentLength {
				content = text[:MaxTextContentLength] + "...(内容过长已截断)"
			} else {
				content = text
			}
		} else {
			log.C(c).Warnw("DOCX文本提取失败", "error", err, "filename", fileName)
			// DOCX提取失败不影响文件上传，只是不保存文本内容
		}
	} else if ext == ".doc" {
		// DOC文件文本提取
		text, err := extractTextFromDOC(fileData)
		if err == nil {
			// 清理和验证文本，确保可以安全存储到数据库
			text = sanitizeUTF8ForDatabase(text)
			if len(text) > MaxTextContentLength {
				content = text[:MaxTextContentLength] + "...(内容过长已截断)"
			} else {
				content = text
			}
		} else {
			log.C(c).Warnw("DOC文本提取失败", "error", err, "filename", fileName)
		}
	} else if ext == ".rtf" {
		// RTF文件文本提取
		text, err := extractTextFromRTF(fileData)
		if err == nil {
			// 清理和验证文本，确保可以安全存储到数据库
			text = sanitizeUTF8ForDatabase(text)
			if len(text) > MaxTextContentLength {
				content = text[:MaxTextContentLength] + "...(内容过长已截断)"
			} else {
				content = text
			}
		} else {
			log.C(c).Warnw("RTF文本提取失败", "error", err, "filename", fileName)
		}
	}

	// 最终验证：确保content是有效的UTF-8，防止数据库错误
	if content != "" {
		if !utf8.ValidString(content) {
			log.C(c).Warnw("Content包含无效UTF-8字符，进行清理", "filename", fileName)
			content = strings.ToValidUTF8(content, "")
			content = sanitizeUTF8ForDatabase(content)
		}
	}

	// 9. 创建数据库记录
	sopFile := &model.SopFile{
		UserID:    userID,
		RunID:     &runID,
		NodeID:    &nodeID,
		FileName:  fileName,
		FileURL:   cosURL,
		FileType:  file.Header.Get("Content-Type"),
		FileSize:  file.Size,
		FileExt:   ext,
		Content:   content,
		Status:    "uploaded",
		ObjectKey: objectKey,
	}

	// 如果COS上传失败，记录错误但不阻止保存
	if cosURL == "" && util.IsCOSEnabled() {
		sopFile.Status = "uploaded_no_cos"
		sopFile.ErrorMsg = "COS上传失败，但文件已保存"
	}

	// 10. 保存到数据库（使用事务确保数据一致性）
	ds := store.S
	if err := ds.Sop().CreateFile(sopFile); err != nil {
		return nil, fmt.Errorf("创建文件记录失败: %w", err)
	}

	log.C(c).Infow("文件上传成功",
		"file_id", sopFile.ID,
		"filename", fileName,
		"size", file.Size,
		"cos_url", cosURL,
		"has_content", content != "")

	return sopFile, nil
}

// FileTextResult 单个文件的文本提取结果
type FileTextResult struct {
	FileName  string `json:"file_name"`
	FileID    string `json:"file_id,omitempty"`
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ParseFileTextResponse 文本提取接口返回结果
type ParseFileTextResponse struct {
	Text    string           `json:"text"`               // 合并后的文本（用于直接回填）
	Files   []FileTextResult `json:"files"`              // 文件信息列表（包含file_id）
	FileIDs []string         `json:"file_ids,omitempty"` // 便于前端调试/透传
}

// ParseFileText 上传文件，让阿里百炼 qwen-long 解析纯文本并返回（不落库）
func (ctrl *SopController) ParseFileText(c *gin.Context) {
	log.C(c).Infow("Parse file text called")

	// 1. 获取multipart form
	form, err := c.MultipartForm()
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的multipart form: %s", err.Error()), nil)
		return
	}
	defer func() { _ = form.RemoveAll() }() // 清理临时文件

	// 2. 获取文件列表，兼容file / files
	files := form.File["files"]
	if len(files) == 0 {
		files = form.File["file"]
	}
	if len(files) == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请上传至少一个文件"), nil)
		return
	}

	// 3. 基础校验：数量和总大小
	if len(files) > MaxFilesPerUpload {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件数量超过限制（最多%d个）", MaxFilesPerUpload), nil)
		return
	}

	var (
		totalSize int64
		fileIDs   []string
		fileInfos []FileTextResult
	)

	for _, file := range files {
		if file == nil || file.Size <= 0 {
			continue
		}

		totalSize += file.Size
		if totalSize > MaxFileSize*MaxFilesPerUpload {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("所有文件总大小超过限制"), nil)
			return
		}

		// 阿里百炼支持的扩展名更宽，这里沿用现有校验
		ext := strings.ToLower(filepath.Ext(file.Filename))
		if !isAllowedExtension(ext) {
			log.C(c).Warnw("文件扩展名不支持，已跳过", "filename", file.Filename, "ext", ext)
			continue
		}

		fileID, err := uploadFileToDashScope(file)
		if err != nil {
			log.C(c).Errorw("上传文件到百炼失败", "filename", file.Filename, "error", err)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("上传文件到百炼失败: %v", err), nil)
			return
		}

		fileIDs = append(fileIDs, fileID)
		fileInfos = append(fileInfos, FileTextResult{
			FileName: file.Filename,
			FileID:   fileID,
		})
	}

	if len(fileIDs) == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("未能上传任何文件"), nil)
		return
	}

	// 4. 让 qwen-long 读取文件并输出原始纯文本
	parseCtx := c.Request.Context()
	if cu, ok := c.Get("current_user"); ok {
		if u, ok := cu.(*model.User); ok {
			parseCtx = billing.WithBilling(parseCtx, u.ID, "sop_parse_file_text")
		}
	}
	text, qwenUsage, err := extractPlainTextWithQwenLong(parseCtx, fileIDs)
	if err != nil {
		// 如果仍在解析中，返回file_ids方便前端轮询查询接口
		if strings.Contains(err.Error(), "文件仍在解析中") {
			log.C(c).Infow("qwen-long 文件仍在解析中", "file_ids", fileIDs, "error", err.Error())
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("qwen-long 解析中，请稍后用file_ids轮询查询"), ParseFileTextResponse{
				Text:    "",
				Files:   fileInfos,
				FileIDs: fileIDs,
			})
			return
		}
		log.C(c).Errorw("qwen-long 解析失败", "error", err, "file_ids", fileIDs)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("qwen-long 解析失败: %v", err), nil)
		return
	}

	_ = qwenUsage // usage recorded via billing context

	core.WriteResponse(c, nil, ParseFileTextResponse{
		Text:    strings.TrimSpace(text),
		Files:   fileInfos,
		FileIDs: fileIDs,
	})
}

// ParseFileTextQuery 轮询查询 qwen-long 解析结果（不重新上传）
func (ctrl *SopController) ParseFileTextQuery(c *gin.Context) {
	log.C(c).Infow("Parse file text query called")

	var req struct {
		FileIDs []string `json:"file_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.FileIDs) == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("file_ids 不能为空"), nil)
		return
	}

	// 直接调用 qwen-long 读取已有 file_ids
	queryCtx := c.Request.Context()
	if cu, ok := c.Get("current_user"); ok {
		if u, ok := cu.(*model.User); ok {
			queryCtx = billing.WithBilling(queryCtx, u.ID, "sop_parse_file_query")
		}
	}
	text, qwenUsage, err := extractPlainTextWithQwenLong(queryCtx, req.FileIDs)
	if err != nil {
		log.C(c).Errorw("qwen-long 解析查询失败", "error", err, "file_ids", req.FileIDs)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	_ = qwenUsage // usage recorded via billing context

	core.WriteResponse(c, nil, ParseFileTextResponse{
		Text:    strings.TrimSpace(text),
		Files:   []FileTextResult{},
		FileIDs: req.FileIDs,
	})
}

// ReadImageWithQwenVL 读取图片，调用qwen-vl-max进行理解
func (ctrl *SopController) ReadImageWithQwenVL(c *gin.Context) {
	log.C(c).Infow("Read image with qwen-vl called")

	// 仅支持 multipart 上传
	contentType := c.GetHeader("Content-Type")
	if !strings.Contains(contentType, "multipart/form-data") {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请使用multipart/form-data上传图片"), nil)
		return
	}

	const maxImageSize = 5 * 1024 * 1024 // 5MB
	form, err := c.MultipartForm()
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的multipart form: %s", err.Error()), nil)
		return
	}
	defer func() { _ = form.RemoveAll() }()

	files := form.File["file"]
	if len(files) == 0 {
		files = form.File["image"]
	}
	if len(files) == 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请上传图片文件（field: file 或 image）"), nil)
		return
	}

	question := strings.TrimSpace(c.DefaultPostForm("question", "请描述图片内容"))

	fh := files[0]
	if fh.Size > maxImageSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("图片大小超过5MB限制"), nil)
		return
	}

	file, err := fh.Open()
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无法读取文件: %s", err.Error()), nil)
		return
	}
	defer file.Close()

	buf := make([]byte, maxImageSize+1)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("读取文件失败: %s", err.Error()), nil)
		return
	}
	if int64(n) > maxImageSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("图片大小超过5MB限制"), nil)
		return
	}
	data := buf[:n]

	encoded := base64.StdEncoding.EncodeToString(data)
	visionCtx := c.Request.Context()
	if cu, ok := c.Get("current_user"); ok {
		if u, ok := cu.(*model.User); ok {
			visionCtx = billing.WithBilling(visionCtx, u.ID, "sop_image_read")
		}
	}
	resp, _, err := ctrl.aliBiz.QianwenVision(visionCtx, encoded, question, "")
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"question": question,
		"answer":   resp,
	})
}

// uploadFileToDashScope 使用OpenAI兼容接口上传文件到阿里百炼，返回file_id
func uploadFileToDashScope(file *multipart.FileHeader) (string, error) {
	apiKey := getAliConfig("text", "api_key")
	if apiKey == "" {
		return "", fmt.Errorf("未配置阿里百炼API Key")
	}

	// 打开文件
	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer src.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("file", filepath.Base(file.Filename))
	if err != nil {
		return "", fmt.Errorf("创建multipart文件部分失败: %w", err)
	}
	if _, err := io.Copy(part, src); err != nil {
		return "", fmt.Errorf("写入文件内容失败: %w", err)
	}
	// purpose 固定为 file-extract，见官方文档
	if err := writer.WriteField("purpose", "file-extract"); err != nil {
		return "", fmt.Errorf("写入purpose失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("关闭multipart writer失败: %w", err)
	}

	req, err := http.NewRequest("POST", "https://dashscope.aliyuncs.com/compatible-mode/v1/files", &buf)
	if err != nil {
		return "", fmt.Errorf("创建上传请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用上传接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("文件上传失败，HTTP %d: %s", resp.StatusCode, string(body))
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", fmt.Errorf("解析上传响应失败: %w，响应: %s", err, string(body))
	}
	if res.ID == "" {
		return "", fmt.Errorf("文件上传响应缺少file_id，响应: %s", string(body))
	}

	return res.ID, nil
}

// extractPlainTextWithQwenLong 通过 qwen-long 读取 file_id 列表并返回纯文本
func extractPlainTextWithQwenLong(ctx context.Context, fileIDs []string) (string, *billing.TokenUsage, error) {
	apiKey := getAliConfig("text", "api_key")
	if apiKey == "" {
		return "", nil, fmt.Errorf("未配置阿里百炼API Key")
	}

	model := getAliConfig("qwen_long", "model")
	if model == "" {
		model = "qwen-long"
	}

	url := "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"

	messages := []map[string]string{
		{
			"role":    "system",
			"content": "你是一个文档抽取助手，请输出文件的原始纯文本内容，保持顺序，不要总结，不要省略，也不要添加说明或格式标记。",
		},
	}
	for _, fid := range fileIDs {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": fmt.Sprintf("fileid://%s", fid),
		})
	}
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": "请将文件内容以纯文本原样输出，不要总结，不要省略，不要添加说明。",
	})

	bodyMap := map[string]interface{}{
		"model":       model,
		"messages":    messages,
		"max_tokens":  32000,
		"temperature": 0.1,
		"stream":      false,
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("调用qwen-long失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// 解析中的情况：400 + "File parsing in progress..."
		if resp.StatusCode == http.StatusBadRequest {
			return "", nil, fmt.Errorf("文件仍在解析中，请稍后重试。响应: %s", strings.TrimSpace(string(respBody)))
		}
		return "", nil, fmt.Errorf("qwen-long 返回错误 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	// 兼容OpenAI格式响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *billing.TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil, fmt.Errorf("解析qwen-long响应失败: %w，响应: %s", err, string(respBody))
	}
	if len(result.Choices) == 0 {
		return "", nil, fmt.Errorf("qwen-long 响应为空: %s", string(respBody))
	}

	return result.Choices[0].Message.Content, result.Usage, nil
}

// CheckFileQuality 检测上传文件的质量（不保存到数据库）
func (ctrl *SopController) CheckFileQuality(c *gin.Context) {
	log.C(c).Infow("Check file quality called")

	// 1. 获取multipart form
	form, err := c.MultipartForm()
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的multipart form: %s", err.Error()), nil)
		return
	}
	defer func() { _ = form.RemoveAll() }() // 确保清理临时文件

	// 2. 获取文本内容（优先从text字段，如果没有则从文件提取）
	var textContent string

	// 2.1 先检查是否有text字段
	if textValues := form.Value["text"]; len(textValues) > 0 && strings.TrimSpace(textValues[0]) != "" {
		textContent = strings.TrimSpace(textValues[0])
		// 限制文本长度
		if len(textContent) > MaxTextContentLength {
			textContent = textContent[:MaxTextContentLength] + "...(内容过长已截断)"
		}
	}

	// 2.2 如果没有text，则从上传的文件中提取
	if textContent == "" {
		files := form.File["files"]
		if len(files) == 0 {
			files = form.File["file"] // 兼容单数形式
		}

		if len(files) == 0 {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("未提供文本内容或文件"), nil)
			return
		}

		// 限制文件数量
		if len(files) > MaxFilesPerUpload {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件数量超过限制（最多%d个）", MaxFilesPerUpload), nil)
			return
		}

		// 提取所有文件的文本内容
		var fileTexts []string
		var totalSize int64
		for i, file := range files {
			// 验证文件大小
			if file.Size > MaxFileSize {
				core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件 %s 超过大小限制（最大%dMB）", file.Filename, MaxFileSize/(1024*1024)), nil)
				return
			}
			totalSize += file.Size

			// 限制总大小
			if totalSize > MaxFileSize*MaxFilesPerUpload {
				core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("所有文件总大小超过限制"), nil)
				return
			}

			text, err := extractTextFromFile(file)
			if err != nil {
				log.C(c).Warnw("提取文件文本失败", "filename", file.Filename, "error", err)
				// 如果所有文件都失败，返回错误
				if i == len(files)-1 && len(fileTexts) == 0 {
					core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无法从文件中提取文本内容: %s", err.Error()), nil)
					return
				}
				continue
			}
			if text != "" {
				fileTexts = append(fileTexts, text)
			}
		}

		if len(fileTexts) == 0 {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无法从文件中提取文本内容"), nil)
			return
		}

		textContent = strings.Join(fileTexts, "\n\n")
		// 限制总长度
		if len(textContent) > MaxTextContentLength {
			textContent = textContent[:MaxTextContentLength] + "...(内容过长已截断)"
		}
	}

	// 3. 验证文本内容不为空
	textContent = strings.TrimSpace(textContent)
	if textContent == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文本内容为空"), nil)
		return
	}

	// 4. 调用AI进行质量检测（带超时和重试）
	ctx, cancel := context.WithTimeout(c.Request.Context(), AICallTimeout*time.Second)
	defer cancel()

	var qualityUserID uint
	if cu, ok := c.Get("current_user"); ok {
		if u, ok := cu.(*model.User); ok {
			qualityUserID = u.ID
		}
	}
	result, err := ctrl.checkQualityWithAI(ctx, textContent, qualityUserID)
	if err != nil {
		log.C(c).Errorw("质量检测失败", "error", err)
		// 如果AI调用失败，返回基础的质量检测结果
		result = ctrl.fallbackQualityCheck(textContent)
	}

	core.WriteResponse(c, nil, result)
}

// checkQualityWithAI 使用AI检测文本质量（带重试和容错）
func (ctrl *SopController) checkQualityWithAI(ctx context.Context, text string, userID uint) (*QualityCheckResult, error) {
	// 构建质量检测的prompt
	prompt := `你是一位专业的文案质量检测专家。请对以下文案进行质量检测，并按照JSON格式返回结果。

## 检测要求：
1. 评估文案的完整性（是否包含开头、正文、结尾）
2. 评估文案的吸引力（是否有吸引人的开头）
3. 评估文案的内容丰富度（是否包含产品介绍、痛点、效果展示等）
4. 评估文案的互动性（是否有行动号召）

## 返回格式（必须是有效的JSON）：
{
  "status": "合格" 或 "需要改进",
  "summary": "问题摘要（如果status为需要改进，说明主要问题）",
  "problems": ["问题1", "问题2", ...],
  "suggestions": ["建议1", "建议2", ...],
  "score": 0-100的分数
}

## 待检测文案：
` + text + `

请直接返回JSON，不要包含其他说明文字。`

	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}

	// 注入计费上下文
	ctx = billing.WithBilling(ctx, userID, "sop_quality_check")

	// 调用AI（优先使用火山方舟，失败后降级到阿里百炼）
	var aiResponse string
	var err error

	// 先尝试火山方舟
	if ctrl.volcBiz != nil {
		aiResponse, _, err = ctrl.volcBiz.VolcTextStream(ctx, messages, 2000, 0.7)
		if err != nil {
			log.C(ctx).Warnw("火山方舟API失败，尝试阿里百炼降级", "error", err.Error())
		}
	}

	// 如果火山方舟失败或不可用，降级到阿里百炼
	if err != nil || aiResponse == "" {
		if ctrl.aliBiz != nil {
			aiResponse, _, err = ctrl.aliBiz.QianwenTextStream(ctx, messages, 2000, 0.7)
			if err != nil {
				return nil, fmt.Errorf("AI API调用失败: %w", err)
			}
		} else {
			return nil, fmt.Errorf("AI服务不可用")
		}
	}

	if aiResponse == "" {
		return nil, fmt.Errorf("AI返回空响应")
	}

	// 解析AI返回的JSON
	result, err := parseQualityCheckResponse(aiResponse)
	if err != nil {
		log.C(ctx).Warnw("解析AI响应失败，尝试提取JSON", "error", err, "response", aiResponse)
		// 尝试从响应中提取JSON
		result = extractQualityCheckFromText(aiResponse)
	}

	return result, nil
}

// parseQualityCheckResponse 解析AI返回的JSON响应（容错处理）
func parseQualityCheckResponse(response string) (*QualityCheckResult, error) {
	// 清理响应文本
	jsonStr := strings.TrimSpace(response)

	// 移除markdown代码块标记
	if strings.HasPrefix(jsonStr, "```json") {
		jsonStr = strings.TrimPrefix(jsonStr, "```json")
		jsonStr = strings.TrimSuffix(jsonStr, "```")
	} else if strings.HasPrefix(jsonStr, "```") {
		jsonStr = strings.TrimPrefix(jsonStr, "```")
		jsonStr = strings.TrimSuffix(jsonStr, "```")
	}
	jsonStr = strings.TrimSpace(jsonStr)

	// 尝试提取JSON对象（处理可能的前后文本）
	jsonRegex := regexp.MustCompile(`\{[\s\S]*\}`)
	matches := jsonRegex.FindString(jsonStr)
	if matches != "" {
		jsonStr = matches
	}

	// 解析JSON
	var result QualityCheckResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("解析JSON失败: %w", err)
	}

	// 验证和设置默认值
	if result.Status == "" {
		result.Status = "需要改进"
	}
	if result.Score < 0 || result.Score > 100 {
		// 根据问题数量估算分数
		if len(result.Problems) == 0 {
			result.Score = 90
		} else if len(result.Problems) <= 2 {
			result.Score = 70
		} else if len(result.Problems) <= 4 {
			result.Score = 50
		} else {
			result.Score = 30
		}
	}

	// 确保数组不为nil
	if result.Problems == nil {
		result.Problems = []string{}
	}
	if result.Suggestions == nil {
		result.Suggestions = []string{}
	}

	return &result, nil
}

// extractQualityCheckFromText 从文本中提取质量检测信息（备用方案）
func extractQualityCheckFromText(text string) *QualityCheckResult {
	result := &QualityCheckResult{
		Status:      "需要改进",
		Summary:     "文案信息不够完整，可能影响AI学习效果",
		Problems:    []string{},
		Suggestions: []string{},
		Score:       50,
	}

	// 简单的文本分析
	textRunes := []rune(text)
	textLen := len(textRunes)

	// 检查长度
	if textLen < 80 {
		result.Problems = append(result.Problems, fmt.Sprintf("文案内容过短 (建议至少80字，当前%d字)", textLen))
	}

	// 检查是否有吸引人的开头
	hasQuestion := strings.Contains(text, "？") || strings.Contains(text, "?")
	hasExclamation := strings.Contains(text, "！") || strings.Contains(text, "!")
	hasGreeting := strings.Contains(text, "姐妹") || strings.Contains(text, "你们") || strings.Contains(text, "大家")
	if !hasQuestion && !hasExclamation && !hasGreeting {
		result.Problems = append(result.Problems, "缺少吸引人的开头 (如问候、悬念等)")
	}

	// 检查是否包含产品相关关键词
	hasProduct := strings.Contains(text, "产品") || strings.Contains(text, "商品") || strings.Contains(text, "服务")
	if !hasProduct {
		result.Problems = append(result.Problems, "缺少产品介绍或提及")
	}

	// 检查是否包含痛点
	hasPainPoint := strings.Contains(text, "问题") || strings.Contains(text, "痛点") || strings.Contains(text, "困扰")
	if !hasPainPoint {
		result.Problems = append(result.Problems, "缺少痛点引入或问题描述")
	}

	// 检查是否包含效果展示
	hasEffect := strings.Contains(text, "效果") || strings.Contains(text, "感受") || strings.Contains(text, "体验")
	if !hasEffect {
		result.Problems = append(result.Problems, "缺少效果展示或使用感受")
	}

	// 检查是否有行动号召
	hasCTA := strings.Contains(text, "点击") || strings.Contains(text, "关注") || strings.Contains(text, "评论") || strings.Contains(text, "点赞")
	if !hasCTA {
		result.Problems = append(result.Problems, "缺少行动号召 (引导互动)")
	}

	// 如果问题少于3个，认为基本合格
	if len(result.Problems) < 3 {
		result.Status = "合格"
		result.Score = 75
		result.Summary = "文案基本完整，可以继续使用"
	} else {
		// 生成改进建议
		result.Suggestions = append(result.Suggestions, "您的文案需要更多内容，建议从以下方面完善：")
		if !hasGreeting {
			result.Suggestions = append(result.Suggestions, "吸引人的开头：用问候、提问或悬念开场（如\"姐妹们！\"、\"你们有没有遇到...\"）")
		}
		if !hasPainPoint {
			result.Suggestions = append(result.Suggestions, "痛点引入：描述目标用户遇到的问题或困扰")
		}
		if !hasProduct {
			result.Suggestions = append(result.Suggestions, "产品介绍：说明产品如何解决用户痛点")
		}
		if !hasEffect {
			result.Suggestions = append(result.Suggestions, "效果展示：分享使用后的感受和效果")
		}
		if !hasCTA {
			result.Suggestions = append(result.Suggestions, "行动号召：引导用户互动（如点赞、评论、关注）")
		}
	}

	return result
}

// fallbackQualityCheck 当AI调用失败时的备用质量检测
func (ctrl *SopController) fallbackQualityCheck(text string) *QualityCheckResult {
	return extractQualityCheckFromText(text)
}

// extractTextFromFile 从文件中提取文本内容（支持多种格式，容错处理）
func extractTextFromFile(file *multipart.FileHeader) (string, error) {
	// 1. 验证文件大小
	if file.Size <= 0 {
		return "", fmt.Errorf("文件为空")
	}
	if file.Size > MaxFileSize {
		return "", fmt.Errorf("文件大小超过限制")
	}

	// 2. 打开文件
	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer src.Close()

	// 3. 读取文件内容（限制大小）
	limitedReader := io.LimitReader(src, MaxFileSize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", fmt.Errorf("读取文件失败: %w", err)
	}

	// 检查是否超过限制
	if int64(len(data)) > MaxFileSize {
		return "", fmt.Errorf("文件大小超过限制")
	}

	// 4. 根据文件扩展名处理
	ext := strings.ToLower(filepath.Ext(file.Filename))

	switch ext {
	case ".txt", ".md":
		// 纯文本文件，直接返回
		text := string(data)
		// 验证UTF-8编码
		if !utf8.ValidString(text) {
			// 尝试清理无效字符
			text = strings.ToValidUTF8(text, "")
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	case ".pdf":
		// PDF文件文本提取
		text, err := extractTextFromPDF(data)
		if err != nil {
			return "", fmt.Errorf("PDF文本提取失败: %w（可能是扫描版PDF或加密PDF）", err)
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	case ".docx":
		// DOCX文件文本提取
		text, err := extractTextFromDOCX(data)
		if err != nil {
			return "", fmt.Errorf("DOCX文本提取失败: %w", err)
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	case ".doc":
		// 旧版Word文档（.doc格式较复杂，尝试基础提取）
		text, err := extractTextFromDOC(data)
		if err != nil {
			return "", fmt.Errorf("DOC文本提取失败: %w（建议转换为DOCX格式）", err)
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	case ".rtf":
		// RTF文件（富文本格式）
		text, err := extractTextFromRTF(data)
		if err != nil {
			return "", fmt.Errorf("RTF文本提取失败: %w", err)
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	default:
		// 尝试作为文本处理
		text := string(data)
		// 检查是否包含可打印字符
		hasPrintable := false
		for _, r := range text {
			if r >= 32 && r < 127 || r >= 0x4e00 && r <= 0x9fff {
				hasPrintable = true
				break
			}
		}
		if !hasPrintable {
			return "", fmt.Errorf("文件格式不支持或文件内容无法识别")
		}
		// 验证UTF-8编码
		if !utf8.ValidString(text) {
			text = strings.ToValidUTF8(text, "")
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	}
}

// sanitizeFileName 清理文件名，防止路径遍历攻击
func sanitizeFileName(fileName string) string {
	// 移除路径分隔符
	fileName = strings.ReplaceAll(fileName, "/", "_")
	fileName = strings.ReplaceAll(fileName, "\\", "_")
	fileName = strings.ReplaceAll(fileName, "..", "_")

	// 移除控制字符
	var result strings.Builder
	for _, r := range fileName {
		if r >= 32 && r != 127 {
			result.WriteRune(r)
		}
	}
	fileName = result.String()

	// 限制文件名长度
	if len(fileName) > 255 {
		ext := filepath.Ext(fileName)
		baseName := fileName[:255-len(ext)]
		fileName = baseName + ext
	}

	return strings.TrimSpace(fileName)
}

// isAllowedExtension 检查文件扩展名是否允许
func isAllowedExtension(ext string) bool {
	allowed := strings.Split(AllowedExtensions, ",")
	for _, a := range allowed {
		if strings.TrimSpace(a) == ext {
			return true
		}
	}
	return false
}

// GetRunStatus 获取Run执行状态
func (ctrl *SopController) GetRunStatus(c *gin.Context) {
	log.C(c).Infow("User get SOP run status called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户
	run, err := ctrl.sopBiz.GetRun(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	status, err := ctrl.sopBiz.GetRunStatus(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 获取该用户在该模板下的所有书签
	bookmarks, err := ctrl.sopBiz.ListBookmarksByTemplate(c.Request.Context(), user.ID, run.TemplateID)
	if err != nil {
		log.C(c).Warnw("Failed to get bookmarks", "error", err)
		// 不影响主流程，继续返回状态
		bookmarks = []model.SopNodeBookmark{}
	}

	// 转换为API响应格式
	response := v1.RunStatusResponse{
		Status:          status.Status,
		CurrentNodeSort: status.CurrentNodeSort,
		TotalNodes:      status.TotalNodes,
		CompletedCount:  status.CompletedCount,
	}

	// 转换已完成节点，包含书签信息
	completedNodes := make([]v1.CompletedNodeInfo, len(status.CompletedNodes))
	for i, node := range status.CompletedNodes {
		completedNodes[i] = v1.CompletedNodeInfo{
			NodeID:       node.NodeID,
			NodeName:     node.NodeName,
			Sort:         node.Sort,
			Input:        node.Input,  // 节点输入
			Output:       node.Output, // 返回完整输出
			Thinking:     node.Thinking,
			FromBookmark: node.FromBookmark,
			BookmarkID:   node.BookmarkID,
			IsAccessible: node.IsAccessible,
			ModelName:    node.ModelName,   // B5
			LatencyMs:    node.LatencyMs,   // B5
			TotalTokens:  node.TotalTokens, // B5
		}
	}
	response.CompletedNodes = completedNodes

	// 转换下一个节点
	if status.NextNode != nil {
		response.NextNode = &v1.NextNodeInfo{
			NodeID:   status.NextNode.NodeID,
			NodeName: status.NextNode.NodeName,
			Sort:     status.NextNode.Sort,
			IsFirst:  status.NextNode.IsFirst,
			HasNext:  status.NextNode.HasNext,
		}
	}

	// 添加可用书签信息（未应用的书签）
	availableBookmarks := []v1.BookmarkInfo{}
	appliedNodeIDs := make(map[uint]bool)
	for _, node := range status.CompletedNodes {
		appliedNodeIDs[node.NodeID] = true
	}

	for _, bookmark := range bookmarks {
		// 只返回尚未应用的书签
		if !appliedNodeIDs[bookmark.NodeID] {
			availableBookmarks = append(availableBookmarks, v1.BookmarkInfo{
				NodeID:     bookmark.NodeID,
				NodeSort:   bookmark.NodeSort,
				BookmarkID: bookmark.ID,
			})
		}
	}
	response.AvailableBookmarks = availableBookmarks

	// 统计自动应用的书签数量
	autoAppliedCount := 0
	for _, node := range status.CompletedNodes {
		if node.FromBookmark {
			autoAppliedCount++
		}
	}
	response.AutoAppliedCount = autoAppliedCount

	core.WriteResponse(c, nil, response)
}

// EditTextStream 文本编辑流式对话（不保存到数据库）
func (ctrl *SopController) EditTextStream(c *gin.Context) {
	log.C(c).Infow("Edit text stream called")

	// 1. 解析请求参数
	var req v1.EditTextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 2. 参数验证
	req.OriginalText = strings.TrimSpace(req.OriginalText)
	req.UserMessage = strings.TrimSpace(req.UserMessage)

	// 判断是否是第一次对话（没有历史且没有原始文本）
	isFirstConversation := len(req.ConversationHistory) == 0

	// 第一次对话必须有原始文本（text参数）
	if isFirstConversation && req.OriginalText == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文本内容不能为空"), nil)
		return
	}

	// 限制文本长度（防止token超限）
	const MaxOriginalTextLength = 100000 // 原始文本最大100KB
	const MaxUserMessageLength = 10000   // 用户消息最大10KB
	const MaxHistoryRounds = 10          // 最多10轮对话历史

	if req.OriginalText != "" && len(req.OriginalText) > MaxOriginalTextLength {
		req.OriginalText = req.OriginalText[:MaxOriginalTextLength] + "...(内容过长已截断)"
	}

	if len(req.UserMessage) > MaxUserMessageLength {
		req.UserMessage = req.UserMessage[:MaxUserMessageLength] + "...(内容过长已截断)"
	}

	// 限制对话历史长度
	if len(req.ConversationHistory) > MaxHistoryRounds*2 { // 每轮包含user和assistant两条消息
		req.ConversationHistory = req.ConversationHistory[len(req.ConversationHistory)-MaxHistoryRounds*2:]
		log.C(c).Warnw("对话历史过长，已截断", "original_length", len(req.ConversationHistory), "truncated_length", MaxHistoryRounds*2)
	}

	// 3. 构建对话消息
	messages := buildEditTextMessages(req.OriginalText, req.UserMessage, req.ConversationHistory, isFirstConversation)

	// 4. 设置SSE响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用nginx缓冲

	// 5. 获取Flusher（用于实时刷新）
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Streaming not supported"), nil)
		return
	}

	// 6. 创建带心跳的 context
	heartbeatCtx, heartbeatCancel := context.WithCancel(c.Request.Context())
	defer heartbeatCancel()

	// 使用互斥锁保护并发写入 c.Writer
	var mu sync.Mutex

	// 7. 启动心跳 goroutine，每 15 秒发送一次注释行（SSE 心跳）
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()
	go func() {
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-heartbeatTicker.C:
				select {
				case <-c.Request.Context().Done():
					return
				default:
					// 发送心跳注释行（使用简洁格式，确保前端不会解析）
					mu.Lock()
					if _, err := c.Writer.WriteString(":\n\n"); err != nil {
						log.C(c).Warnw("Failed to send heartbeat", "error", err)
						mu.Unlock()
						return
					}
					flusher.Flush()
					mu.Unlock()
				}
			}
		}
	}()

	// 8. 调用AI流式API（优先使用火山方舟，失败后降级到阿里百炼）
	var aiErr error
	var hasResponse bool

	// 处理思考模式开关，默认关闭
	deepThinking := false
	if req.DeepThinking != nil {
		deepThinking = *req.DeepThinking
	}

	// 注入计费上下文
	if cu, ok := c.Get("current_user"); ok {
		if u, ok := cu.(*model.User); ok {
			heartbeatCtx = billing.WithBilling(heartbeatCtx, u.ID, "sop_text_edit")
		}
	}

	// 先尝试火山方舟
	var editUsage *billing.TokenUsage
	if ctrl.volcBiz != nil {
		editUsage, aiErr = ctrl.callVolcEditStream(heartbeatCtx, messages, deepThinking, func(event string, chunk string) error {
			hasResponse = true
			// 检查客户端是否断开连接
			select {
			case <-c.Request.Context().Done():
				log.C(c).Infow("Client disconnected during stream")
				return c.Request.Context().Err()
			default:
			}

			// 发送SSE格式的数据（根据事件类型使用不同格式）
			chunkJSON, _ := json.Marshal(chunk)
			var data string
			if event == "thinking" {
				data = fmt.Sprintf("event: thinking\ndata: %s\n\n", string(chunkJSON))
			} else if event == "message" {
				data = fmt.Sprintf("data: %s\n\n", string(chunkJSON))
			} else if event == "done" {
				data = "event: done\ndata: {\"status\":\"completed\"}\n\n"
			} else {
				return nil
			}

			mu.Lock()
			defer mu.Unlock()
			if _, err := c.Writer.WriteString(data); err != nil {
				log.C(c).Warnw("Failed to write chunk to client", "error", err)
				return err
			}

			// 立即刷新，确保数据实时发送
			flusher.Flush()
			return nil
		})

		if aiErr == nil {
			log.C(c).Infow("火山方舟API调用成功")
		} else {
			log.C(c).Warnw("火山方舟API失败，尝试阿里百炼降级", "error", aiErr)
		}
	}

	// 如果火山方舟失败或不可用，降级到阿里百炼
	if aiErr != nil || !hasResponse {
		if ctrl.aliBiz != nil {
			editUsage, aiErr = ctrl.callAliEditStream(heartbeatCtx, messages, deepThinking, func(event string, chunk string) error {
				hasResponse = true
				// 检查客户端是否断开连接
				select {
				case <-c.Request.Context().Done():
					log.C(c).Infow("Client disconnected during stream")
					return c.Request.Context().Err()
				default:
				}

				// 发送SSE格式的数据（阿里百炼降级时只发送 message 事件）
				chunkJSON, _ := json.Marshal(chunk)
				var data string
				if event == "thinking" {
					data = fmt.Sprintf("event: thinking\ndata: %s\n\n", string(chunkJSON))
				} else if event == "message" {
					data = fmt.Sprintf("data: %s\n\n", string(chunkJSON))
				} else if event == "done" {
					data = "event: done\ndata: {\"status\":\"completed\"}\n\n"
				} else {
					return nil
				}

				mu.Lock()
				defer mu.Unlock()
				if _, err := c.Writer.WriteString(data); err != nil {
					log.C(c).Warnw("Failed to write chunk to client", "error", err)
					return err
				}

				// 立即刷新，确保数据实时发送
				flusher.Flush()
				return nil
			})

			if aiErr == nil {
				log.C(c).Infow("阿里百炼API降级成功")
			}
		} else {
			aiErr = fmt.Errorf("AI服务不可用")
		}
	}

	_ = editUsage // usage recorded via billing context

	// 9. 处理错误
	if aiErr != nil {
		// 检查是否是客户端断开连接
		if c.Request.Context().Err() != nil {
			log.C(c).Infow("Client disconnected during stream", "error", aiErr)
			return // 客户端断开，不需要发送错误
		}

		// 发送错误事件
		errorMsg, _ := json.Marshal(aiErr.Error())
		errorData := fmt.Sprintf("event: error\ndata: %s\n\n", string(errorMsg))
		_, _ = c.Writer.WriteString(errorData)
		flusher.Flush()
		return
	}

	// 10. 发送完成事件
	_, _ = c.Writer.WriteString("event: done\ndata: {\"status\":\"completed\"}\n\n")
	flusher.Flush()
}

// ChatAfterRunStream 已完成run后的对话（SSE）
func (ctrl *SopController) ChatAfterRunStream(c *gin.Context) {
	log.C(c).Infow("Chat after run stream called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 积分预检：检查用户是否有足够积分执行追问
	if canPerform, reason := ctrl.creditBiz.CanPerformAIOperation(c, user, "sop_chat"); !canPerform {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", reason), nil)
		return
	}

	// 解析请求参数
	var req struct {
		RunID           uint   `json:"run_id"`
		ConversationID  string `json:"conversation_id"`
		Question        string `json:"question"`
		ModelKey        string `json:"model_key"`         // 用户选择的模型 key（可选，走三级 fallback）
		DeepThinking    *bool  `json:"deep_thinking"`     // 思考模式开关
		RegenerateMsgID uint   `json:"regenerate_msg_id"` // 需要重新生成的AI消息ID（可选）
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.RunID == 0 || strings.TrimSpace(req.Question) == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}
	req.Question = strings.TrimSpace(req.Question)

	// 处理思考模式开关，默认关闭
	deepThinking := false
	var deepThinkingPtr *bool
	if req.DeepThinking != nil {
		deepThinking = *req.DeepThinking
		deepThinkingPtr = req.DeepThinking
	}

	// 三级 fallback 解析用户选择的模型（query → 用户偏好 → 系统默认）
	resolvedModelKey, resolvedThinking, resolveErr := ctrl.llmRouter.ResolveUserModel(
		c.Request.Context(), user.ID, "sop", req.ModelKey, deepThinkingPtr)
	if resolveErr != nil {
		log.C(c).Warnw("LLMRouter.ResolveUserModel failed, falling back to last node default", "error", resolveErr)
		// 与 ExecuteNodeStream 控制器（见本文件 :821）保持一致：解析失败时 thinking=false，避免
		// 在无法确认用户偏好的情况下意外开启深度思考（成本更高、部分模型不支持）
		resolvedModelKey = ""
		resolvedThinking = false
	}
	deepThinking = resolvedThinking

	// 设置SSE响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用nginx缓冲

	// 获取Flusher（用于实时刷新）
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Streaming not supported"), nil)
		return
	}

	// 使用互斥锁保护并发写入 c.Writer
	var mu sync.Mutex

	// 创建带心跳的 context
	heartbeatCtx, heartbeatCancel := context.WithCancel(c.Request.Context())
	defer heartbeatCancel()

	// 心跳 goroutine
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()
	go func() {
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-heartbeatTicker.C:
				select {
				case <-c.Request.Context().Done():
					return
				default:
					// 发送心跳注释行（使用简洁格式，确保前端不会解析）
					mu.Lock()
					if _, err := c.Writer.WriteString(":\n\n"); err != nil {
						log.C(c).Warnw("Failed to send heartbeat", "error", err)
						mu.Unlock()
						return
					}
					flusher.Flush()
					mu.Unlock()
				}
			}
		}
	}()

	// 执行业务流
	err := ctrl.sopBiz.ChatAfterRunStream(heartbeatCtx, req.RunID, req.ConversationID, req.Question, user.ID, resolvedModelKey, deepThinking, req.RegenerateMsgID, func(event string, chunk string) error {
		// 检查客户端连接
		select {
		case <-c.Request.Context().Done():
			return c.Request.Context().Err()
		default:
		}

		chunkJSON, _ := json.Marshal(chunk)
		var data string
		switch event {
		case "thinking":
			data = fmt.Sprintf("event: thinking\ndata: %s\n\n", string(chunkJSON))
		case "message":
			data = fmt.Sprintf("data: %s\n\n", string(chunkJSON))
		case "done":
			if chunk != "" {
				data = fmt.Sprintf("event: done\ndata: %s\n\n", chunk)
			} else {
				data = "event: done\ndata: {\"status\":\"completed\"}\n\n"
			}
		default:
			return nil
		}

		mu.Lock()
		defer mu.Unlock()
		if _, err := c.Writer.WriteString(data); err != nil {
			log.C(c).Warnw("Failed to write chunk to client", "error", err)
			return err
		}
		flusher.Flush()
		return nil
	})

	if err != nil {
		if c.Request.Context().Err() != nil {
			log.C(c).Infow("Client disconnected during stream", "error", err)
			return
		}
		// 发送错误事件
		errorMsg, _ := json.Marshal(err.Error())
		errorData := fmt.Sprintf("event: error\ndata: %s\n\n", string(errorMsg))
		mu.Lock()
		_, _ = c.Writer.WriteString(errorData)
		flusher.Flush()
		mu.Unlock()
		return
	}

	// 结尾done事件已在handler写入
}

// ListRunChatMessages 获取指定run的聊天记录（需登录且归属校验）
func (ctrl *SopController) ListRunChatMessages(c *gin.Context) {
	log.C(c).Infow("List run chat messages called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	msgs, err := ctrl.sopBiz.ListChatMessages(c, uint(runID), user.ID)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 转换为响应结构体
	responseMessages := make([]v1.RunChatMessageItem, len(msgs))
	for i, msg := range msgs {
		responseMessages[i] = v1.RunChatMessageItem{
			ID:                    msg.ID,
			Role:                  msg.Role,
			Content:               msg.Content,
			Thinking:              msg.Thinking,
			CreatedAt:             msg.CreatedAt.Format(time.RFC3339),
			PromptTokens:          msg.PromptTokens,
			CompletionTokens:      msg.CompletionTokens,
			TotalTokens:           msg.TotalTokens,
			ReasoningTokens:       msg.ReasoningTokens,
			EstimatedPromptTokens: msg.EstimatedPromptTokens,
			ModelName:             msg.ModelName,  // B5
			DurationMs:            msg.DurationMs, // B5
		}
	}

	core.WriteResponse(c, nil, v1.RunChatMessagesResponse{
		RunID:          uint(runID),
		ConversationID: msgsSafeConversationID(msgs),
		Messages:       responseMessages,
	})
}

// msgsSafeConversationID 取聊天记录里的一个conversation_id用于响应
func msgsSafeConversationID(msgs []model.SopChatMsg) string {
	for _, m := range msgs {
		if m.ConversationID != "" {
			return m.ConversationID
		}
	}
	return ""
}

// buildEditTextMessages 构建文本编辑的对话消息
func buildEditTextMessages(originalText, userMessage string, history []v1.EditTextMessage, isFirstConversation bool) []map[string]string {
	messages := []map[string]string{}

	// 只在第一次对话时添加系统提示词（包含原始文本）
	if isFirstConversation && originalText != "" {
		systemPrompt := `### 角色：内容工程质检员

#### 核心指令
以"严苛、数据驱动、反空话"的原则，对用户上传的《业务介绍文档》进行六维核验。核心使命是核验文档的"信息密度"与"证据强度"，严禁模糊、宽泛、缺乏证据的原材料通过。

#### 审计标准：
（以下纬度需要逐项点评："通过"/"存疑"/"缺失" + 具体意见）
一、业务定位
- 身份：清晰界定（如：全案陪跑 vs 代运营）
- 壁垒：必须包含排他性优势（如：全网首家、商业闭环）
- 痛点：覆盖用户在决策链条中的核心卡点（如：信息不对称、合规风险、执行门槛、决策成本等）
二、信任背书
- 背景：硬核学历（如：QS前100或大厂/名企高管经历）
- 人设：复合标签（如：老板+妈妈+留学生，缺一不可）
- 战绩：
    - 体量：如：陪跑>400位
    - 结果：如：GMV>5亿美金
    - 归因：战绩需挂钩具体方法论
三、高精度画像
- 属性：如：锁定高净值/创始人
- 地域：如：全球布局（北美/欧洲/澳洲）或二线以上城市
- 门槛：如：暗示或明确35-55岁、年入50-800万
四、深层心理
- 焦虑：如：行业内卷、自我怀疑
- 渴望：如：不做网红，只做正规军打法
- 顾虑：如：主动化解异地信任、时差、落地执行疑虑
五、交付体系
- 命名：如：专属模型名称（如：高势能IP-6力模型）
- 模式：如：矩阵式交付（分赛道+分时区）
- 颗粒度：如：极致细节（改标题、给模板、妆造指导）
六、案例证据
- 反差：如：低粉丝 vs 高变现
- 覆盖：如：涵盖房产、留学、移民等核心赛道

#### 得分等级
评价：0-70分为"有待改进"，70-85分为"微调"，85-100分为"优秀"
得分范围：0-100分

#### 输出格式
"
### 产品手册检测报告
**得分**：[具体分数，0-100分的范围]
**评价**：[从三个档中选择对应的具体评价，如：微调]

**业务定位**：
**信任背书**：
**用户画像**：
**深层心理**：
**交付体系**：
**案例证据**：
"

#### 输入
下方是产品文档的内容：
` + originalText

		messages = append(messages, map[string]string{
			"role":    "system",
			"content": systemPrompt,
		})
	}

	// 添加对话历史
	for _, msg := range history {
		if msg.Role == "user" || msg.Role == "assistant" {
			messages = append(messages, map[string]string{
				"role":    msg.Role,
				"content": msg.Content,
			})
		}
	}

	// 添加当前用户消息（如果存在）
	if userMessage != "" {
		messages = append(messages, map[string]string{
			"role":    "user",
			"content": userMessage,
		})
	}

	return messages
}

// callVolcEditStream 调用火山方舟流式API进行文本编辑
func (ctrl *SopController) callVolcEditStream(ctx context.Context, messages []map[string]string, deepThinking bool, handler func(event string, chunk string) error) (*billing.TokenUsage, error) {
	baseURL := viper.GetString("volc.base_url")
	if baseURL == "" {
		return nil, fmt.Errorf("volc base_url not configured")
	}
	url := baseURL + "/chat/completions"

	thinkingType := "disabled"
	if deepThinking {
		thinkingType = "enabled"
	}

	bodyMap := map[string]interface{}{
		"model":       "deepseek-v3-2-251201",
		"messages":    messages,
		"max_tokens":  4000,
		"temperature": 0.7,
		"stream":      true,
		"thinking": map[string]interface{}{
			"type": thinkingType,
		},
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viper.GetString("volc.api_key"))

	client := &http.Client{
		Timeout: 600 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP错误: %d, 响应: %s", resp.StatusCode, string(body))
	}

	// 流式读取响应（带3秒超时）
	reader := bufio.NewReader(resp.Body)
	readTimeout := 3 * time.Second
	maxConsecutiveTimeouts := 3
	consecutiveTimeouts := 0
	var usage *billing.TokenUsage

	for {
		// 检查context是否被取消
		select {
		case <-ctx.Done():
			return usage, ctx.Err()
		default:
		}

		// 使用context.WithTimeout实现读取超时
		readCtx, readCancel := context.WithTimeout(ctx, readTimeout)
		var lineBytes []byte
		var readErr error
		readDone := make(chan struct{})

		go func() {
			defer readCancel()
			lineBytes, readErr = reader.ReadBytes('\n')
			close(readDone)
		}()

		select {
		case <-readCtx.Done():
			// 超时
			consecutiveTimeouts++
			if consecutiveTimeouts >= maxConsecutiveTimeouts {
				log.C(ctx).Warnw("Multiple read timeouts in volc edit stream",
					"consecutive_timeouts", consecutiveTimeouts)
			}
			// 超时，继续尝试读取
			continue
		case <-readDone:
			// 读取完成
			readCancel()
		}

		if readErr != nil {
			// 检查是否是超时错误
			if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
				consecutiveTimeouts++
				if consecutiveTimeouts >= maxConsecutiveTimeouts {
					log.C(ctx).Warnw("Multiple read timeouts in volc edit stream",
						"consecutive_timeouts", consecutiveTimeouts)
				}
				// 超时，继续尝试读取
				continue
			}
			if readErr == io.EOF {
				// 流结束
				break
			}
			return usage, fmt.Errorf("read error: %w", readErr)
		}

		// 重置超时计数器
		consecutiveTimeouts = 0

		// 转换为字符串并去除换行符
		line := strings.TrimRight(string(lineBytes), "\r\n")

		// 过滤 SSE 注释行（以 : 开头）和空行，防止心跳等注释内容混入输出
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				_ = handler("done", "")
				break
			}

			// 提取 usage
			if u := billing.ExtractUsageFromSSEData(data); u != nil {
				usage = u
			}

			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				if choices, ok := m["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							// 思考模式下，优先处理 reasoning_content（思维链内容）
							if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
								if err := handler("thinking", rc); err != nil {
									return usage, err
								}
							}
							// 处理 content 字段（最终回答内容）
							if content, ok := delta["content"].(string); ok && content != "" {
								if err := handler("message", content); err != nil {
									return usage, err
								}
							}
						}
					}
				}
			}
		}
	}

	return usage, nil
}

// callAliEditStream 调用阿里百炼流式API进行文本编辑
func (ctrl *SopController) callAliEditStream(ctx context.Context, messages []map[string]string, deepThinking bool, handler func(event string, chunk string) error) (*billing.TokenUsage, error) {
	url := "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"

	bodyMap := map[string]interface{}{
		"model":       getAliConfig("text", "model"),
		"messages":    messages,
		"max_tokens":  4000,
		"temperature": 0.7,
		"stream":      true,
		"extra_body": map[string]interface{}{
			"enable_thinking": deepThinking,
		},
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+getAliConfig("text", "api_key"))

	client := &http.Client{
		Timeout: 600 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP错误: %d, 响应: %s", resp.StatusCode, string(body))
	}

	// 流式读取响应（带3秒超时）
	reader := bufio.NewReader(resp.Body)
	readTimeout := 3 * time.Second
	maxConsecutiveTimeouts := 3
	consecutiveTimeouts := 0
	var usage *billing.TokenUsage

	for {
		// 检查context是否被取消
		select {
		case <-ctx.Done():
			return usage, ctx.Err()
		default:
		}

		// 使用context.WithTimeout实现读取超时
		readCtx, readCancel := context.WithTimeout(ctx, readTimeout)
		var lineBytes []byte
		var readErr error
		readDone := make(chan struct{})

		go func() {
			defer readCancel()
			lineBytes, readErr = reader.ReadBytes('\n')
			close(readDone)
		}()

		select {
		case <-readCtx.Done():
			// 超时
			consecutiveTimeouts++
			if consecutiveTimeouts >= maxConsecutiveTimeouts {
				log.C(ctx).Warnw("Multiple read timeouts in ali edit stream",
					"consecutive_timeouts", consecutiveTimeouts)
			}
			// 超时，继续尝试读取
			continue
		case <-readDone:
			// 读取完成
			readCancel()
		}

		if readErr != nil {
			// 检查是否是超时错误
			if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
				consecutiveTimeouts++
				if consecutiveTimeouts >= maxConsecutiveTimeouts {
					log.C(ctx).Warnw("Multiple read timeouts in ali edit stream",
						"consecutive_timeouts", consecutiveTimeouts)
				}
				// 超时，继续尝试读取
				continue
			}
			if readErr == io.EOF {
				// 流结束
				break
			}
			return usage, fmt.Errorf("read error: %w", readErr)
		}

		// 重置超时计数器
		consecutiveTimeouts = 0

		// 转换为字符串并去除换行符
		line := strings.TrimRight(string(lineBytes), "\r\n")

		// 过滤 SSE 注释行（以 : 开头）和空行，防止心跳等注释内容混入输出
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				_ = handler("done", "")
				break
			}

			// 提取 usage
			if u := billing.ExtractUsageFromSSEData(data); u != nil {
				usage = u
			}

			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				if choices, ok := m["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							if content, ok := delta["content"].(string); ok && content != "" {
								if err := handler("message", content); err != nil {
									return usage, err
								}
							}
						}
					}
				}
			}
		}
	}

	return usage, nil
}

// getAliConfig 获取Ali配置（从controller中访问，因为aliBiz是私有的）
func getAliConfig(service string, key string) string {
	// 先尝试服务特定配置
	serviceKey := fmt.Sprintf("ali.%s.%s", service, key)
	if viper.IsSet(serviceKey) {
		return viper.GetString(serviceKey)
	}

	// 回退到通用配置
	commonKey := fmt.Sprintf("ali.%s", key)
	if viper.IsSet(commonKey) {
		return viper.GetString(commonKey)
	}

	// 如果都没有，返回空字符串
	return ""
}

// extractTextFromPDF 从PDF文件中提取文本
func extractTextFromPDF(data []byte) (string, error) {
	var result strings.Builder

	// 方法1: 提取PDF文本对象中的文本（BT ... ET）
	btPattern := regexp.MustCompile(`BT\s+(.*?)\s+ET`)
	matches := btPattern.FindAllStringSubmatch(string(data), -1)
	for _, match := range matches {
		if len(match) > 1 {
			// 提取文本内容（通常在Tj或TJ操作符中）
			textPattern := regexp.MustCompile(`\((.*?)\)\s*Tj|\[(.*?)\]\s*TJ`)
			textMatches := textPattern.FindAllStringSubmatch(match[1], -1)
			for _, tm := range textMatches {
				if len(tm) > 1 {
					text := tm[1]
					if len(tm) > 2 && tm[2] != "" {
						text = tm[2]
					}
					// 处理PDF转义字符
					text = strings.ReplaceAll(text, "\\n", "\n")
					text = strings.ReplaceAll(text, "\\r", "\r")
					text = strings.ReplaceAll(text, "\\t", "\t")
					text = strings.ReplaceAll(text, "\\(", "(")
					text = strings.ReplaceAll(text, "\\)", ")")
					result.WriteString(text)
					result.WriteString(" ")
				}
			}
		}
	}

	// 方法2: 如果方法1没有提取到文本，尝试提取所有可打印字符
	if result.Len() == 0 {
		text := extractPrintableTextFromPDF(data)
		if text != "" {
			result.WriteString(text)
		}
	}

	if result.Len() == 0 {
		return "", fmt.Errorf("无法从PDF中提取文本，可能是扫描版PDF或加密PDF")
	}

	return cleanExtractedText(result.String()), nil
}

// extractPrintableTextFromPDF 从PDF中提取可打印文本（备用方法）
// 注意：此方法会过滤掉PDF格式代码，只提取实际文本内容
func extractPrintableTextFromPDF(data []byte) string {
	// PDF格式代码关键词，用于识别和过滤PDF格式代码
	pdfKeywords := []string{
		"PDF-", "obj", "endobj", "stream", "endstream",
		"FilterFlateDecode", "Filter", "Length", "BT", "ET",
		"Tj", "TJ", "Tm", "Td", "TD", "T*", "q", "Q",
		"cm", "rg", "RG", "g", "G", "f", "F", "S", "s",
		"W", "n", "m", "l", "c", "v", "y", "h", "re",
		"xref", "trailer", "startxref", "/Type", "/Subtype",
		"/Pages", "/Page", "/Font", "/Resources", "/MediaBox",
		"/Contents", "/Parent", "/Kids", "/Count", "/Root",
		"/Info", "/ID", "/Size", "/Prev", "/W", "/D",
	}

	var result strings.Builder
	var currentWord strings.Builder

	for i := 0; i < len(data); i++ {
		b := data[i]

		// 检查是否是ASCII可打印字符或UTF-8字符的开始
		if (b >= 32 && b < 127) || (b >= 0xC0 && b < 0xF8) {
			// 尝试读取UTF-8字符
			if b >= 0xC0 {
				// UTF-8多字节字符
				charLen := getUTF8CharLength(b)
				if i+charLen <= len(data) {
					charBytes := data[i : i+charLen]
					if isValidUTF8(charBytes) {
						currentWord.Write(charBytes)
						i += charLen - 1
						continue
					}
				}
			} else {
				currentWord.WriteByte(b)
			}
		} else if b == '\n' || b == '\r' || b == '\t' || b == ' ' {
			// 遇到空白字符，结束当前单词
			if currentWord.Len() > 0 {
				word := currentWord.String()
				// 过滤掉PDF格式代码和太短的单词
				if len(word) >= 2 && !isPDFFormatCode(word, pdfKeywords) {
					result.WriteString(word)
					result.WriteByte(' ')
				}
				currentWord.Reset()
			}
		} else {
			// 其他字符，重置当前单词
			if currentWord.Len() > 0 {
				word := currentWord.String()
				if len(word) >= 2 && !isPDFFormatCode(word, pdfKeywords) {
					result.WriteString(word)
					result.WriteByte(' ')
				}
				currentWord.Reset()
			}
		}
	}

	// 处理最后一个单词
	if currentWord.Len() > 0 {
		word := currentWord.String()
		if len(word) >= 2 && !isPDFFormatCode(word, pdfKeywords) {
			result.WriteString(word)
		}
	}

	return result.String()
}

// isPDFFormatCode 检查单词是否是PDF格式代码
func isPDFFormatCode(word string, keywords []string) bool {
	wordUpper := strings.ToUpper(word)
	for _, keyword := range keywords {
		if strings.Contains(wordUpper, strings.ToUpper(keyword)) {
			return true
		}
	}
	// 检查是否是PDF对象引用格式（如 "141 0 obj" 中的 "141" 或 "0"）
	if matched, _ := regexp.MatchString(`^\d+\s+\d+\s+obj$`, word); matched {
		return true
	}
	// 检查是否只包含数字和PDF操作符（可能是PDF格式代码）
	if matched, _ := regexp.MatchString(`^[\d\s/()\[\]<>]+$`, word); matched && len(word) < 20 {
		return true
	}
	return false
}

// extractTextFromDOCX 从DOCX文件中提取文本
// DOCX文件实际上是ZIP格式，包含document.xml文件
func extractTextFromDOCX(data []byte) (string, error) {
	// 创建ZIP reader
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("无法读取DOCX文件（ZIP格式错误）: %w", err)
	}

	var documentXML []byte
	found := false

	// 查找document.xml文件
	for _, file := range zipReader.File {
		if file.Name == "word/document.xml" {
			rc, err := file.Open()
			if err != nil {
				return "", fmt.Errorf("无法打开document.xml: %w", err)
			}
			defer rc.Close()

			documentXML, err = io.ReadAll(rc)
			if err != nil {
				return "", fmt.Errorf("无法读取document.xml: %w", err)
			}
			found = true
			break
		}
	}

	if !found {
		return "", fmt.Errorf("DOCX文件中未找到document.xml")
	}

	// 解析XML并提取文本
	text, err := extractTextFromDOCXXML(documentXML)
	if err != nil {
		return "", fmt.Errorf("解析DOCX XML失败: %w", err)
	}

	return cleanExtractedText(text), nil
}

// extractTextFromDOCXXML 从DOCX的XML中提取文本
func extractTextFromDOCXXML(xmlData []byte) (string, error) {
	var result strings.Builder

	// DOCX使用WordprocessingML格式
	// 文本通常在<w:t>标签中
	textPattern := regexp.MustCompile(`<w:t[^>]*>([^<]*)</w:t>`)
	matches := textPattern.FindAllStringSubmatch(string(xmlData), -1)

	for _, match := range matches {
		if len(match) > 1 {
			text := match[1]
			// 解码XML实体
			text = decodeXMLEntities(text)
			result.WriteString(text)
			result.WriteString(" ")
		}
	}

	// 如果正则表达式没有匹配到，尝试使用XML解析器
	if result.Len() == 0 {
		text, err := parseDOCXXMLWithParser(xmlData)
		if err == nil && text != "" {
			return text, nil
		}
	}

	if result.Len() == 0 {
		return "", fmt.Errorf("无法从DOCX XML中提取文本")
	}

	return result.String(), nil
}

// parseDOCXXMLWithParser 使用XML解析器解析DOCX
func parseDOCXXMLWithParser(xmlData []byte) (string, error) {
	var result strings.Builder

	decoder := xml.NewDecoder(bytes.NewReader(xmlData))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		switch se := token.(type) {
		case xml.StartElement:
			// 查找w:t元素（Word文本元素）
			if se.Name.Local == "t" {
				// 读取文本内容
				var text string
				if err := decoder.DecodeElement(&text, &se); err == nil {
					result.WriteString(text)
					result.WriteString(" ")
				}
			}
		case xml.CharData:
			// 直接文本内容
			text := strings.TrimSpace(string(se))
			if text != "" && !strings.HasPrefix(text, "<") {
				result.WriteString(text)
				result.WriteString(" ")
			}
		}
	}

	return result.String(), nil
}

// extractTextFromDOC 从旧版Word文档(.doc)中提取文本
func extractTextFromDOC(data []byte) (string, error) {
	// .doc格式是OLE2格式，解析比较复杂
	// 这里使用简单的文本提取方法
	text := extractPrintableTextFromPDF(data) // 复用PDF的文本提取逻辑
	if text == "" {
		return "", fmt.Errorf("无法从DOC文件中提取文本，建议转换为DOCX格式")
	}
	return cleanExtractedText(text), nil
}

// extractTextFromRTF 从RTF文件中提取文本
func extractTextFromRTF(data []byte) (string, error) {
	var result strings.Builder
	rtfText := string(data)

	// RTF格式：文本通常在控制词之间
	// 移除RTF控制词和转义字符
	textPattern := regexp.MustCompile(`\\[a-z]+\d*\s?|\\'[0-9a-f]{2}|[{}]`)
	rtfText = textPattern.ReplaceAllString(rtfText, " ")

	// 提取可打印字符
	for _, r := range rtfText {
		if r >= 32 && r < 127 || (r >= 0x4e00 && r <= 0x9fff) {
			result.WriteRune(r)
		} else if r == '\n' || r == '\r' {
			result.WriteRune(' ')
		}
	}

	text := result.String()
	if text == "" {
		return "", fmt.Errorf("无法从RTF文件中提取文本")
	}

	return cleanExtractedText(text), nil
}

// cleanExtractedText 清理提取的文本，确保所有字符都是有效的UTF-8
func cleanExtractedText(text string) string {
	// 第一步：确保字符串是有效的UTF-8，移除无效的UTF-8序列
	if !utf8.ValidString(text) {
		// 将无效的UTF-8序列替换为空字符串
		text = strings.ToValidUTF8(text, "")
	}

	// 第二步：移除控制字符（包括NULL、换行、回车等，但保留换行符和制表符）
	// 保留有用的空白字符：换行符(\n)、回车符(\r)、制表符(\t)、空格( )
	text = regexp.MustCompile(`[\x00-\x08\x0B-\x0C\x0E-\x1F\x7F-\x9F]`).ReplaceAllString(text, "")

	// 第三步：移除PDF格式代码（在清理其他字符之前）
	text = removePDFFormatCode(text)

	// 第四步：规范化换行符（统一为\n）
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// 第五步：移除多余的空白字符（多个连续空格合并为单个空格）
	text = regexp.MustCompile(`[ \t]+`).ReplaceAllString(text, " ")

	// 第六步：移除PDF/DOCX格式残留字符（保留中文、英文、数字和常用标点）
	// Go的regexp不支持\u转义序列，使用Unicode属性类和字符类
	// \p{L} 匹配所有字母，\p{N} 匹配所有数字，\p{Han} 匹配汉字
	// \s 匹配空白字符
	// 直接列出要保留的标点符号
	keepPattern := regexp.MustCompile(`[^\p{L}\p{N}\p{Han}\s.,!?;:()\[\]{}\-—–'""…。，、；：？！（）【】《》]`)
	text = keepPattern.ReplaceAllString(text, "")

	// 第七步：再次验证UTF-8有效性，并移除任何剩余的无效字符
	text = sanitizeUTF8ForDatabase(text)

	// 第八步：清理多余的空行（3个以上连续换行符合并为2个）
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}

// removePDFFormatCode 移除PDF格式代码
func removePDFFormatCode(text string) string {
	// PDF格式代码关键词
	pdfKeywords := []string{
		"PDF-", "obj", "endobj", "stream", "endstream",
		"FilterFlateDecode", "Filter", "Length", "BT", "ET",
		"Tj", "TJ", "Tm", "Td", "TD", "T*", "q", "Q",
		"cm", "rg", "RG", "g", "G", "f", "F", "S", "s",
		"W", "n", "m", "l", "c", "v", "y", "h", "re",
		"xref", "trailer", "startxref", "/Type", "/Subtype",
		"/Pages", "/Page", "/Font", "/Resources", "/MediaBox",
		"/Contents", "/Parent", "/Kids", "/Count", "/Root",
		"/Info", "/ID", "/Size", "/Prev", "/W", "/D",
	}

	// 检查是否包含多个PDF格式关键词（可能是PDF原始内容）
	keywordCount := 0
	textLower := strings.ToLower(text)
	for _, keyword := range pdfKeywords {
		if strings.Contains(textLower, strings.ToLower(keyword)) {
			keywordCount++
		}
	}

	// 如果包含3个或以上的PDF关键词，且内容看起来像PDF格式代码，则进行清理
	if keywordCount >= 3 {
		// 检查是否包含PDF对象标记（如 "141 0 obj"）
		objPattern := regexp.MustCompile(`\d+\s+\d+\s+obj`)
		if objPattern.MatchString(text) {
			// 如果内容很长且包含大量PDF格式代码，尝试提取实际文本
			if len(text) > 500 && keywordCount >= 5 {
				// 移除PDF格式代码行
				lines := strings.Split(text, "\n")
				var cleanedLines []string
				for _, line := range lines {
					lineLower := strings.ToLower(strings.TrimSpace(line))
					isPDFLine := false
					for _, keyword := range pdfKeywords {
						if strings.Contains(lineLower, strings.ToLower(keyword)) {
							isPDFLine = true
							break
						}
					}
					// 检查是否是PDF对象引用格式
					if objPattern.MatchString(strings.TrimSpace(line)) {
						isPDFLine = true
					}
					// 如果这一行不是PDF格式代码，保留它
					if !isPDFLine && strings.TrimSpace(line) != "" {
						cleanedLines = append(cleanedLines, line)
					}
				}
				text = strings.Join(cleanedLines, "\n")
			}
		}
	}

	return text
}

// sanitizeUTF8ForDatabase 清理文本以确保可以安全存储到数据库
// 移除所有无效的UTF-8字符和可能导致数据库错误的字符
func sanitizeUTF8ForDatabase(text string) string {
	var result strings.Builder
	result.Grow(len(text))

	for _, r := range text {
		// 检查是否是有效的UTF-8字符
		if r == utf8.RuneError {
			// 跳过无效的UTF-8字符
			continue
		}

		// 检查字符是否在有效的Unicode范围内
		// Unicode范围：U+0000 到 U+10FFFF
		if r > 0x10FFFF {
			// 超出Unicode范围的字符，跳过
			continue
		}

		// 检查是否是替换字符（通常表示无效的UTF-8序列）
		if r == 0xFFFD {
			// Unicode替换字符，跳过
			continue
		}

		// 检查是否是私有使用区字符（可能导致问题）
		if (r >= 0xE000 && r <= 0xF8FF) || (r >= 0xF0000 && r <= 0xFFFFD) || (r >= 0x100000 && r <= 0x10FFFD) {
			// 私有使用区字符，跳过
			continue
		}

		// 检查是否是控制字符（除了常见的空白字符）
		if r < 0x20 && r != 0x09 && r != 0x0A && r != 0x0D {
			// 控制字符（除了Tab、LF、CR），跳过
			continue
		}

		// 检查是否是未分配的字符范围（可能导致数据库错误）
		if r >= 0xD800 && r <= 0xDFFF {
			// 代理对范围（用于UTF-16，在UTF-8中无效），跳过
			continue
		}

		// 字符通过所有检查，添加到结果中
		result.WriteRune(r)
	}

	// 最终验证：确保结果字符串是有效的UTF-8
	finalText := result.String()
	if !utf8.ValidString(finalText) {
		// 如果仍然无效，使用ToValidUTF8强制修复
		finalText = strings.ToValidUTF8(finalText, "")
	}

	return finalText
}

// decodeXMLEntities 解码XML实体
func decodeXMLEntities(text string) string {
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&apos;", "'")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&#160;", " ")
	return text
}

// getUTF8CharLength 获取UTF-8字符长度
func getUTF8CharLength(firstByte byte) int {
	if firstByte&0xE0 == 0xC0 {
		return 2
	} else if firstByte&0xF0 == 0xE0 {
		return 3
	} else if firstByte&0xF8 == 0xF0 {
		return 4
	}
	return 1
}

// isValidUTF8 检查字节序列是否是有效的UTF-8
func isValidUTF8(data []byte) bool {
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		data = data[size:]
	}
	return true
}
