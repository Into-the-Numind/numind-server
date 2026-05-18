package sop

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
	v1 "numind-server/pkg/api/numind/v1"

	"gorm.io/gorm"
)

// ISopBiz SOP业务逻辑接口
type ISopBiz interface {
	// Template operations
	CreateTemplate(ctx context.Context, name, description, prompt string) (*model.SopTemplate, error)
	GetTemplate(ctx context.Context, id uint) (*model.SopTemplate, error)
	ListTemplates(ctx context.Context, offset, limit int) ([]model.SopTemplate, int64, error)
	// ListVisibleTemplates 列出 C 端用户可见的模板（status=active AND publish_status=published）。
	// 仅供用户端 /v1/sop/templates；管理端和统计保持调用 ListTemplates 以看到全部状态。
	ListVisibleTemplates(ctx context.Context, offset, limit int) ([]model.SopTemplate, int64, error)
	// ListVisibleTemplatesWithPermission 返回 C 端可见模板 + 每项的运行权限标志，供 UI 显示锁。
	// 父账号全 true；子账号按 user_template_permission 白名单 O(N) 本地匹配，不走 N+1 查询。
	// 安全 gate 仍由 CheckTemplatePermission + SOP 运行端点强制，本方法仅影响 UI 可见性。
	ListVisibleTemplatesWithPermission(ctx context.Context, user *model.User, offset, limit int) ([]SopTemplateVisibleItem, int64, error)
	UpdateTemplate(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteTemplate(ctx context.Context, id uint) error

	// Node operations
	CreateNode(ctx context.Context, node *model.SopNode) (*model.SopNode, error)
	GetNode(ctx context.Context, id uint) (*model.SopNode, error)
	ListNodesByTemplate(ctx context.Context, templateID uint) ([]model.SopNode, error)
	UpdateNode(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteNode(ctx context.Context, id uint) error

	// Execution operations
	GetRun(ctx context.Context, id uint) (*model.SopRun, error)
	CheckRunOwnership(ctx context.Context, runID, userID uint) (bool, error)
	ListRuns(ctx context.Context, offset, limit int, userID *uint) ([]model.SopRun, int64, error)
	GetRunsStats(ctx context.Context, runIDs []uint) (map[uint]RunStats, error)
	GetRunWithNodes(ctx context.Context, runID uint) (*model.SopRun, []model.SopNodeRun, error)
	ListExecutedTemplatesByUser(ctx context.Context, userID uint) ([]store.ExecutedTemplateInfo, error)
	ListTemplateRunsWithDetails(ctx context.Context, userID, templateID uint, offset, limit int) ([]v1.TemplateRunHistoryResponse, int64, error)

	// Step-by-step execution operations
	CreateRun(ctx context.Context, templateID, userID uint, text string) (*model.SopRun, error)
	GetNextNode(ctx context.Context, runID uint) (*model.SopNode, bool, error)
	ExecuteNodeStream(ctx context.Context, runID, nodeID uint, text string, modelKey string, thinkingMode bool, handler func(event string, chunk string) error) error
	GetRunStatus(ctx context.Context, runID uint) (*RunStatus, error)

	// Note operations
	GetNote(ctx context.Context, id uint) (*model.SopNote, error)
	ListNotesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.SopNote, int64, error)

	// Chat operations
	ChatAfterRunStream(ctx context.Context, runID uint, conversationID string, question string, userID uint, modelKey string, deepThinking bool, regenerateMsgID uint, handler func(event string, chunk string) error) error
	ListChatMessages(ctx context.Context, runID uint, userID uint) ([]model.SopChatMsg, error)

	// Admin operations
	DeleteRun(ctx context.Context, runID uint, userID uint) error
	DeleteRuns(ctx context.Context, runIDs []uint, userID uint) error
	CleanZombieRuns(ctx context.Context, timeout time.Duration) error

	// B-end SOP config operations
	CreateTemplateByUser(ctx context.Context, userID uint, req *CreateTemplateByUserReq) (*model.SopTemplate, error)
	ListTemplatesByCreator(ctx context.Context, creatorID uint, offset, limit int) ([]model.SopTemplate, int64, error)
	PublishTemplate(ctx context.Context, userID uint, templateID uint) error
	UnpublishTemplate(ctx context.Context, userID uint, templateID uint) error

	// WithCreditService 注入 Phase 2 Task 2.1 的 credits Reserve/Reconcile 控制流依赖。
	// 调用方：wire（biz.go）在构造 sopBiz 后立即调用。返回自身以支持链式。
	WithCreditService(svc credit.ICreditService, pc pricing.ICalculator) ISopBiz

	// Bookmark operations
	SaveNodeBookmark(ctx context.Context, userID, nodeRunID uint, bookmarkName, description string) (*model.SopNodeBookmark, error)
	SaveNodeBookmarkByRunAndNode(ctx context.Context, userID, runID, nodeID uint, bookmarkName, description string) (*model.SopNodeBookmark, error)
	GetBookmark(ctx context.Context, id, userID uint) (*model.SopNodeBookmark, error)
	ListBookmarksByTemplate(ctx context.Context, userID, templateID uint) ([]model.SopNodeBookmark, error)
	DeleteBookmark(ctx context.Context, id, userID uint) error
	ApplyBookmarkToNode(ctx context.Context, userID, runID, nodeID uint, bookmarkID *uint) (*model.SopNodeRun, error)
	CreateRunWithBookmarks(ctx context.Context, templateID, userID uint, text string, autoApplyBookmarks bool) (*model.SopRun, []uint, error)
	DeleteDraftRun(ctx context.Context, runID, userID uint) error
	CleanupDraftRuns(ctx context.Context, timeout time.Duration) error
}

type sopBiz struct {
	ds        store.IStore
	executor  *SopExecutor
	creditBiz credit.ICreditBiz
	// creditSvc 用于 Phase 2 的 Reserve → LLM → Reconcile 控制流
	// nil 时退化为旧 fire-and-forget 路径（兼容 sop_test.go 等不关心 credits 的测试）
	creditSvc credit.ICreditService
	// pricing 用于 LLM 调用完成后同步计算实际 cost（Reconcile 输入）
	// nil 时走 Refund("no_actual_cost") 兜底
	pricing pricing.ICalculator
	// runningRuns 用于存储正在执行的任务 ID，防止并发冲突（互斥锁）
	runningRuns sync.Map
}

// NewSopBiz 创建SOP业务逻辑实例（向后兼容构造函数）
//
// 对于 Phase 2 Task 2.1 的 credits Reserve/Reconcile 控制流，调用方应在构造
// 之后调用 WithCreditService(svc, pc) 注入额外依赖。保持此签名不动是为了兼容
// 已存在的 sop_test.go 和其他可能尚未迁移的测试。
func NewSopBiz(ds store.IStore, executor *SopExecutor, creditBiz credit.ICreditBiz) ISopBiz {
	return &sopBiz{
		ds:        ds,
		executor:  executor,
		creditBiz: creditBiz,
	}
}

// WithCreditService 注入 Phase 2 Task 2.1 的 credits 控制流依赖。
//
// 返回值仍是 *sopBiz（通过 ISopBiz 接口暴露）以支持链式调用。
//
// 典型调用（见 biz.go）：
//
//	b.sopService = sopbiz.NewSopBiz(ds, executor, creditBiz).
//	    WithCreditService(creditSvc, pricingCalc)
func (b *sopBiz) WithCreditService(svc credit.ICreditService, pc pricing.ICalculator) ISopBiz {
	b.creditSvc = svc
	b.pricing = pc
	return b
}

// Template operations
func (b *sopBiz) CreateTemplate(ctx context.Context, name, description, prompt string) (*model.SopTemplate, error) {
	template := &model.SopTemplate{
		Name:          name,
		Description:   description,
		Prompt:        prompt,
		Status:        model.SopNodeStatusActive,
		PublishStatus: model.SopPublishStatusPublished, // admin 创建的模板默认对用户可见（无 publish 流程）
	}

	if err := b.ds.Sop().CreateTemplate(template); err != nil {
		return nil, fmt.Errorf("failed to create template: %w", err)
	}

	// 自动将新模板授权给所有已配置权限的子用户
	if err := b.ds.Customers().GrantTemplateToConfiguredSubUsers(ctx, template.ID); err != nil {
		log.C(ctx).Warnw("Failed to auto-grant new template to sub-users", "template_id", template.ID, "err", err)
	}

	return template, nil
}

func (b *sopBiz) GetTemplate(ctx context.Context, id uint) (*model.SopTemplate, error) {
	return b.ds.Sop().GetTemplate(id)
}

func (b *sopBiz) ListTemplates(ctx context.Context, offset, limit int) ([]model.SopTemplate, int64, error) {
	return b.ds.Sop().ListTemplates(offset, limit)
}

func (b *sopBiz) ListVisibleTemplates(ctx context.Context, offset, limit int) ([]model.SopTemplate, int64, error) {
	return b.ds.Sop().ListVisibleTemplates(offset, limit)
}

// SopTemplateVisibleItem 包装 SopTemplate 并附带当前用户对该模板的运行权限。
// 用于首页 UI 显示锁标志（has_permission=false 时加锁徽章但卡片仍正常渲染）。
// 安全 gate 仍在 CheckTemplatePermission + SOP 运行端点层强制执行。
type SopTemplateVisibleItem struct {
	model.SopTemplate
	HasPermission bool `json:"has_permission"`
}

// ListVisibleTemplatesWithPermission 见接口注释。
//
// 两层 gate 串行 (sop-chatbot-visibility-scope spec §4.2.1):
//   - Layer 1: visibility 过滤 (本 feature 新增) — 物理移除受限不可见的 SOP, 子用户看不到入口
//   - Layer 2: run-permission 标志 (既有) — 列出剩余 SOP 但用 HasPermission 标记是否可运行
//
// Gate 顺序固化: visibility 先于 run-permission. 含义:
//   - visibility 受限 + 在白名单: 可见, 可能可运行 (取决于 run-permission)
//   - visibility 受限 + 不在白名单: 不可见 (移除, 不出现在列表)
//   - visibility 不受限: 全部子用户可见, 仅 HasPermission 区分能否运行
//
// 父账户 bypass: 不应用 visibility 过滤, 也不查 run-permission (全部 HasPermission=true).
func (b *sopBiz) ListVisibleTemplatesWithPermission(ctx context.Context, user *model.User, offset, limit int) ([]SopTemplateVisibleItem, int64, error) {
	templates, total, err := b.ds.Sop().ListVisibleTemplates(offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("ListVisibleTemplatesWithPermission: %w", err)
	}

	// 父账号：全部 true，不查白名单
	if user.ParentUserID == nil {
		items := make([]SopTemplateVisibleItem, 0, len(templates))
		for _, t := range templates {
			items = append(items, SopTemplateVisibleItem{SopTemplate: t, HasPermission: true})
		}
		return items, total, nil
	}

	// Layer 1: visibility 过滤 (visibility_restricted=true 且不在白名单则物理移除)
	visibilitySet, err := b.ds.SopVisibilityGrant().ListVisibleSopIDsBySubUser(ctx, user.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("ListVisibleTemplatesWithPermission visibility: %w", err)
	}
	filtered := make([]model.SopTemplate, 0, len(templates))
	for _, t := range templates {
		if t.VisibilityRestricted {
			if _, ok := visibilitySet[t.ID]; !ok {
				continue // 短路移除: 受限且不在 visibility 白名单
			}
		}
		filtered = append(filtered, t)
	}

	// Layer 2: run-permission 标志 (一次查询白名单, 本地匹配)
	perms, err := b.ds.Customers().ListUserTemplatePermissions(ctx, user.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("ListVisibleTemplatesWithPermission whitelist: %w", err)
	}
	allowed := make(map[uint]struct{}, len(perms))
	for _, p := range perms {
		allowed[p.TemplateID] = struct{}{}
	}
	items := make([]SopTemplateVisibleItem, 0, len(filtered))
	for _, t := range filtered {
		_, ok := allowed[t.ID]
		items = append(items, SopTemplateVisibleItem{SopTemplate: t, HasPermission: ok})
	}

	// 注: total 反映 visibility 过滤后的实际可见数量, 与 items 长度一致
	// (而非 store 层返回的 visibility 过滤前 total). 与列表语义对齐.
	// 局限: 当 store 层做了 offset/limit 分页时, total 仅反映当前页过滤后的数量,
	// 不是全量可见数. 当前默认 limit=500 远超实际模板数, 此局限不构成问题; 未来若
	// 需要小页大、精确分页 total, 应改为先查全部 visibility 白名单 + 在 store 层
	// 做 WHERE id IN(...) 过滤 (改造代价较大, 推迟为 tech debt).
	return items, int64(len(items)), nil
}

func (b *sopBiz) UpdateTemplate(ctx context.Context, id uint, updates map[string]interface{}) error {
	return b.ds.Sop().UpdateTemplate(id, updates)
}

// DeleteTemplate 删除 SOP 模板, 同事务清理可见范围授权.
//
// EC-6 (sop-chatbot-visibility-scope spec §9): 实体删除时清理它的所有 visibility grant
// 记录, 避免 grant 表残留指向不存在 SOP 的孤儿数据. 软删 grant 保留审计.
//
// 事务模式 b.ds.DB().WithContext(ctx).Transaction(...) 与项目既有惯例一致.
func (b *sopBiz) DeleteTemplate(ctx context.Context, id uint) error {
	return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// EC-6: 软删该 SOP 的所有 visibility grant
		if err := b.ds.SopVisibilityGrant().CleanupByEntity(ctx, tx, id); err != nil {
			return fmt.Errorf("DeleteTemplate: cleanup visibility grants: %w", err)
		}
		// 既有: 软删 sop_template (GORM 默认软删, gorm.Model.DeletedAt)
		if err := tx.Delete(&model.SopTemplate{}, id).Error; err != nil {
			return fmt.Errorf("DeleteTemplate: delete sop_template: %w", err)
		}
		return nil
	})
}

// Node operations
func (b *sopBiz) CreateNode(ctx context.Context, node *model.SopNode) (*model.SopNode, error) {
	// 验证模板是否存在
	if _, err := b.ds.Sop().GetTemplate(node.TemplateID); err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("template not found")
		}
		return nil, err
	}

	// 检查模板下节点数量是否已达上限（最多20个）
	nodeCount, err := b.ds.Sop().CountNodesByTemplate(ctx, node.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("CreateNode: count nodes: %w", err)
	}
	if nodeCount >= 20 {
		return nil, errno.ErrMaxNodesExceeded
	}

	// 只有模板下第一个节点（无其他节点且无父节点）才是根节点
	if node.ParentID == nil && nodeCount == 0 {
		node.IsRoot = true
	} else {
		node.IsRoot = false
	}

	if err := b.ds.Sop().CreateNode(node); err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	return node, nil
}

func (b *sopBiz) GetNode(ctx context.Context, id uint) (*model.SopNode, error) {
	return b.ds.Sop().GetNode(id)
}

func (b *sopBiz) ListNodesByTemplate(ctx context.Context, templateID uint) ([]model.SopNode, error) {
	return b.ds.Sop().ListNodesByTemplate(templateID)
}

func (b *sopBiz) UpdateNode(ctx context.Context, id uint, updates map[string]interface{}) error {
	return b.ds.Sop().UpdateNode(id, updates)
}

func (b *sopBiz) DeleteNode(ctx context.Context, id uint) error {
	return b.ds.Sop().DeleteNode(id)
}

// RunStatus Run执行状态
type RunStatus struct {
	Status          string              `json:"status"`
	CurrentNodeSort int                 `json:"current_node_sort"`
	CompletedNodes  []CompletedNodeInfo `json:"completed_nodes"`
	NextNode        *NextNodeInfo       `json:"next_node,omitempty"`
	TotalNodes      int                 `json:"total_nodes"`
	CompletedCount  int                 `json:"completed_count"`
}

// CompletedNodeInfo 已完成节点信息
type CompletedNodeInfo struct {
	NodeRunID    uint   `json:"node_run_id"` // 节点运行ID
	NodeID       uint   `json:"node_id"`
	NodeName     string `json:"node_name"`
	Sort         int    `json:"sort"`
	Input        string `json:"input"`  // 节点输入
	Output       string `json:"output"` // 完整输出
	Thinking     string `json:"thinking,omitempty"`
	FromBookmark bool   `json:"from_bookmark"`         // 是否从书签恢复
	BookmarkID   *uint  `json:"bookmark_id,omitempty"` // 关联的书签ID
	IsAccessible bool   `json:"is_accessible"`         // 是否可访问（前面所有节点都已完成）
	ModelName    string `json:"model_name"`            // 实际调用的模型名称（B5）
	LatencyMs    int64  `json:"latency_ms"`            // 执行耗时（毫秒）（B5）
	TotalTokens  int    `json:"total_tokens"`          // 总 tokens（B5）
}

// NextNodeInfo 下一个节点信息
type NextNodeInfo struct {
	NodeID   uint   `json:"node_id"`
	NodeName string `json:"node_name"`
	Sort     int    `json:"sort"`
	IsFirst  bool   `json:"is_first"`
	HasNext  bool   `json:"has_next"`
}

// CreateRun 创建Run（不立即执行）
func (b *sopBiz) CreateRun(ctx context.Context, templateID, userID uint, text string) (*model.SopRun, error) {
	// ===== 用户等级权限检查（控制SOP运行权限）=====
	user, err := b.ds.Users().GetByID(ctx, userID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get user for SOP permission check", "user_id", userID, "err", err)
		return nil, fmt.Errorf("获取用户信息失败: %w", err)
	}

	// credits 用户：粗粒度余额预检，避免零余额时创建 orphan pending run
	// （精确检查由 ExecuteNode 中的 creditSvc.CheckAndEstimate 负责）
	// 三池都计入：trial_grant + credit_cycle + user_booster_balance。
	if b.creditSvc != nil {
		bal, balErr := b.creditSvc.GetBalance(ctx, user)
		if balErr != nil {
			log.C(ctx).Warnw("Credits pre-check: failed to get balance, allowing run creation",
				"user_id", userID, "err", balErr)
		} else {
			totalRemain := bal.SubRemain + bal.BoosterRemain + bal.TrialRemain
			if totalRemain <= 0 {
				log.C(ctx).Warnw("Credits pre-check: zero balance",
					"user_id", userID,
					"sub_remain", bal.SubRemain,
					"booster_remain", bal.BoosterRemain,
					"trial_remain", bal.TrialRemain)
				return nil, errno.ErrInsufficientCredits.SetMessage("积分不足，请充值积分")
			}
			log.C(ctx).Infow("Credits user balance pre-check passed",
				"user_id", userID, "total_remain", totalRemain)
		}
	}

	// ===== 模板权限检查 =====
	// 权限验证:检查用户是否有权限执行此模板
	hasPermission, err := b.ds.Customers().HasTemplatePermission(ctx, userID, templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to check template permission: %w", err)
	}
	if !hasPermission {
		log.C(ctx).Warnw("User has no permission to execute template", "user_id", userID, "template_id", templateID)
		return nil, errno.ErrTemplateUnauthorized
	}
	log.C(ctx).Infow("Template permission check passed", "user_id", userID, "template_id", templateID)

	// 验证模板是否存在
	template, err := b.ds.Sop().GetTemplate(templateID)
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}

	// B端模板发布状态检查：如果模板有创建者（B端创建），则必须是已发布状态才允许运行
	if template.CreatorUserID != nil && template.PublishStatus != model.SopPublishStatusPublished {
		return nil, errno.ErrTemplateNotPublished
	}

	// 生成唯一的conversation_id（改用纳秒级时间戳，彻底解决碰撞问题）
	conversationID := fmt.Sprintf("sop_%d_%d_%d", templateID, userID, time.Now().UnixNano())

	// 创建Run记录（状态为pending）
	run := &model.SopRun{
		TemplateID:     templateID,
		UserID:         userID,
		Status:         model.SopStatusPending,
		ConversationID: conversationID,
	}

	if err := b.ds.Sop().CreateRun(run); err != nil {
		return nil, fmt.Errorf("failed to create run: %w", err)
	}

	log.C(ctx).Infow("Created SOP run", "run_id", run.ID, "template_id", templateID, "user_id", userID)
	return run, nil
}

// GetNextNode 获取下一个待执行节点
func (b *sopBiz) GetNextNode(ctx context.Context, runID uint) (*model.SopNode, bool, error) {
	// 获取Run信息
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, false, fmt.Errorf("run not found: %w", err)
	}

	// 获取模板的所有节点（按sort排序）
	nodes, err := b.ds.Sop().ListNodesByTemplate(run.TemplateID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get template nodes: %w", err)
	}

	if len(nodes) == 0 {
		return nil, false, fmt.Errorf("template has no nodes")
	}

	// 获取已执行的NodeRun（按sort排序）
	completedNodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get node runs: %w", err)
	}

	// 找出已完成的节点ID集合
	completedNodeIDs := make(map[uint]bool)
	for _, nodeRun := range completedNodeRuns {
		if nodeRun.Status == model.SopStatusSucceeded {
			completedNodeIDs[nodeRun.NodeID] = true
		}
	}

	// 找到第一个未执行的节点
	for _, node := range nodes {
		if !completedNodeIDs[node.ID] {
			// 检查是否还有下一个节点
			hasNext := false
			for i := 0; i < len(nodes); i++ {
				if nodes[i].Sort > node.Sort && !completedNodeIDs[nodes[i].ID] {
					hasNext = true
					break
				}
			}
			return &node, hasNext, nil
		}
	}

	// 所有节点都已执行完成
	return nil, false, nil
}

// ExecuteNodeStream 流式执行指定节点
// modelKey: 用户选择的模型 key（空字符串表示使用节点默认配置）
// thinkingMode: 是否开启深度思考模式
func (b *sopBiz) ExecuteNodeStream(ctx context.Context, runID, nodeID uint, text string, modelKey string, thinkingMode bool, handler func(event string, chunk string) error) (retErr error) {
	// 互斥锁检测：防止同一个 RunID 的任务在后台并发执行（解决“执行互斥锁”问题）
	if _, loaded := b.runningRuns.LoadOrStore(runID, struct{}{}); loaded {
		log.C(ctx).Warnw("Detected concurrent execution attempt", "run_id", runID)
		return fmt.Errorf("该任务正在处理中，请勿重复操作")
	}
	defer b.runningRuns.Delete(runID)

	// 使用合并查询一次性获取所有需要的数据（优化性能）
	execCtx, err := b.ds.Sop().GetExecutionContext(runID, nodeID)
	if err != nil {
		return err
	}

	run := execCtx.Run
	node := execCtx.Node
	template := execCtx.Template
	allNodes := execCtx.AllNodes
	allNodeRuns := execCtx.AllNodeRuns
	existingNodeRun := execCtx.ExistingNodeRun

	// ===== 权限检查 =====
	// 检查用户是否仍有权限执行此模板（针对子用户权限被撤销的情况）
	hasPermission, err := b.ds.Customers().HasTemplatePermission(ctx, run.UserID, run.TemplateID)
	if err != nil {
		return fmt.Errorf("failed to check template permission: %w", err)
	}
	if !hasPermission {
		log.C(ctx).Warnw("User permission revoked for template", "user_id", run.UserID, "template_id", run.TemplateID)
		return fmt.Errorf("您没有权限执行此模板（权限已被撤销）")
	}

	// ===== 前置节点完成检查 =====
	// 确保当前节点之前的所有节点都已完成，以维护业务流程的顺序性
	for _, n := range allNodes {
		if n.Sort < node.Sort {
			// 检查该前置节点是否已完成
			completed := false
			for _, nodeRun := range allNodeRuns {
				if nodeRun.NodeID == n.ID && nodeRun.Status == model.SopStatusSucceeded {
					completed = true
					break
				}
			}
			if !completed {
				log.C(ctx).Warnw("Prerequisite node not completed",
					"run_id", runID,
					"current_node_id", nodeID,
					"current_node_name", node.Name,
					"prerequisite_node_id", n.ID,
					"prerequisite_node_name", n.Name)
				return fmt.Errorf("前置节点「%s」尚未完成，无法执行当前节点「%s」", n.Name, node.Name)
			}
		}
	}

	// ===== 状态转换：draft → running =====
	// 如果当前run是draft状态，首次执行节点时转换为running状态（此时才计入配额）
	if run.Status == model.SopStatusDraft {
		run.Status = model.SopStatusRunning
		draftUpdate := map[string]interface{}{"status": model.SopStatusRunning}
		if run.StartedAt == nil {
			now := time.Now()
			run.StartedAt = &now
			draftUpdate["started_at"] = now
		}
		if err := b.ds.Sop().UpdateRun(run.ID, draftUpdate); err != nil {
			log.C(ctx).Errorw("Failed to update run status from draft to running", "run_id", run.ID, "error", err)
			// 不阻断执行，记录错误后继续
		} else {
			log.C(ctx).Infow("Run status changed from draft to running", "run_id", run.ID, "user_id", run.UserID)
		}
	}

	// 找到最大的sort值（最后一个节点）
	maxSort := -1
	for _, n := range allNodes {
		if n.Sort > maxSort {
			maxSort = n.Sort
		}
	}

	// 判断当前节点是否是最后一个节点
	isLastNode := node.Sort == maxSort

	// 构建对话历史
	conversationHistory := []LLMMessage{}

	// 添加模板的预处理prompt
	if template != nil && template.Prompt != "" {
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "system",
			Content: template.Prompt,
		})
	}

	// 构建历史：严格过滤出当前节点之前的最新成功记录（解决“幻觉注入”问题）
	// 我们按 Sort 分组，每组只取最新的一条
	latestRunsMap := make(map[int]model.SopNodeRun)
	for _, nr := range allNodeRuns {
		if nr.Sort < node.Sort && nr.Status == model.SopStatusSucceeded && nr.Output != "" {
			existing, ok := latestRunsMap[nr.Sort]
			if !ok || nr.CreatedAt.After(existing.CreatedAt) {
				latestRunsMap[nr.Sort] = nr
			}
		}
	}

	relevantNodeRuns := []model.SopNodeRun{}
	for _, nr := range latestRunsMap {
		relevantNodeRuns = append(relevantNodeRuns, nr)
	}

	// 按Sort字段排序
	sort.Slice(relevantNodeRuns, func(i, j int) bool {
		return relevantNodeRuns[i].Sort < relevantNodeRuns[j].Sort
	})

	// 添加到对话历史
	for _, nodeRun := range relevantNodeRuns {
		// 查找对应的 node 以获取 Prompt
		var nodePrompt string
		for _, n := range allNodes {
			if n.ID == nodeRun.NodeID {
				nodePrompt = n.Prompt
				break
			}
		}

		contentToAppend := nodeRun.Input
		if nodePrompt != "" {
			contentToAppend = fmt.Sprintf("%s\n\n%s", nodePrompt, nodeRun.Input)
		}

		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "user",
			Content: contentToAppend,
		})
		conversationHistory = append(conversationHistory, LLMMessage{
			Role:    "assistant",
			Content: nodeRun.Output,
		})
	}

	// 确定当前节点的输入
	var currentInput string
	if text != "" {
		// 用户提供了输入内容，直接使用
		currentInput = text
	} else {
		// 用户未提供任何输入，直接拒绝执行
		// 前端应确保用户必须输入内容后才能点击生成按钮
		return fmt.Errorf("请输入内容后再执行此步骤")
	}

	// 确全局执行记录（Run）的状态标记为正在执行（解决“状态僵死”问题）
	// 无论是第一个节点启动，还是中间节点重生成，都必须确保整体状态为 running
	if run.Status != model.SopStatusRunning {
		updateData := map[string]interface{}{
			"status":        model.SopStatusRunning,
			"error_message": "", // 重新执行时清空之前的全局错误
		}
		// 如果是第一次真正启动（没有开始时间），记录开始时间
		if run.StartedAt == nil {
			updateData["started_at"] = time.Now()
		}
		if err := b.ds.Sop().UpdateRun(runID, updateData); err != nil {
			return fmt.Errorf("failed to update run status to running: %w", err)
		}
	}

	var nodeRun *model.SopNodeRun
	var isUpdate bool

	if existingNodeRun != nil {
		// 如果已存在，更新现有记录（重新执行）
		log.C(ctx).Infow("Node run already exists, will update it for re-execution",
			"run_id", runID, "node_id", nodeID, "existing_node_run_id", existingNodeRun.ID)
		nodeRun = existingNodeRun
		isUpdate = true

		// ===== 如果该节点是从书签恢复的，删除对应的书签 =====
		// 避免书签内容与重新生成的内容不一致，导致数据过期问题
		if existingNodeRun.FromBookmark {
			// 查找该用户在该模板该节点的书签
			bookmark, err := b.ds.Sop().GetBookmarkByUserTemplateNode(run.UserID, run.TemplateID, nodeID)
			if err != nil {
				log.C(ctx).Warnw("Failed to query bookmark before regeneration",
					"node_id", nodeID,
					"user_id", run.UserID,
					"template_id", run.TemplateID,
					"error", err)
				// 查询失败不阻断执行，继续处理
			} else if bookmark != nil {
				// 删除书签
				if err := b.ds.Sop().DeleteBookmark(bookmark.ID); err != nil {
					log.C(ctx).Warnw("Failed to delete bookmark after regeneration",
						"bookmark_id", bookmark.ID,
						"node_id", nodeID,
						"user_id", run.UserID,
						"error", err)
					// 删除失败不阻断执行，只记录警告
				} else {
					log.C(ctx).Infow("Bookmark deleted due to node regeneration",
						"bookmark_id", bookmark.ID,
						"node_id", nodeID,
						"user_id", run.UserID,
						"bookmark_name", bookmark.BookmarkName)
				}
			}
		}

		// 更新节点执行状态和时间（清空之前的输出、错误信息和 token 统计）
		updateData := map[string]interface{}{
			"status":            model.SopStatusRunning,
			"input":             currentInput,
			"started_at":        time.Now(),
			"output":            "",    // 清空之前的输出
			"thinking":          "",    // 清空之前的思考内容
			"error_message":     "",    // 清空之前的错误信息
			"finished_at":       nil,   // 清空完成时间
			"latency_ms":        0,     // 重置延迟
			"prompt_tokens":     0,     // 重置 token 统计
			"completion_tokens": 0,     // 重置 token 统计
			"total_tokens":      0,     // 重置 token 统计
			"reasoning_tokens":  0,     // 重置 token 统计
			"from_bookmark":     false, // 清除书签标记
			"bookmark_id":       nil,   // 清除书签ID
		}

		if err := b.ds.Sop().UpdateNodeRun(nodeRun.ID, updateData); err != nil {
			return fmt.Errorf("failed to update node run: %w", err)
		}

		// 联动清理（解决“时空推演矛盾”）：既然中间节点重做了，后续的所有步骤、笔记和对话都已经失效，必须清除以防冲突
		log.C(ctx).Infow("Regeneration triggered, cleaning up downstream records", "run_id", runID, "after_sort", node.Sort)

		// 1. 删除后续节点的执行记录
		if err := b.ds.Sop().DeleteNodeRunsAfterSort(runID, node.Sort); err != nil {
			log.C(ctx).Warnw("Failed to cleanup downstream node runs", "error", err)
		}
		// 2. 删除该任务关联的最终笔记
		if err := b.ds.Sop().DeleteNotesByRun(runID); err != nil {
			log.C(ctx).Warnw("Failed to cleanup run notes", "error", err)
		}
		// 3. 删除该任务关联的对话消息（历史已变，追问需重来）
		if err := b.ds.Sop().DeleteChatMessagesByRun(runID); err != nil {
			log.C(ctx).Warnw("Failed to cleanup run chat messages", "error", err)
		}

		// 重新加载nodeRun以获取最新数据
		nodeRun, err = b.ds.Sop().GetNodeRun(nodeRun.ID)
		if err != nil {
			return fmt.Errorf("failed to reload node run: %w", err)
		}
	} else {
		// 如果不存在，创建新记录
		nodeRun = &model.SopNodeRun{
			RunID:          runID,
			NodeID:         nodeID,
			TemplateID:     run.TemplateID,
			UserID:         run.UserID,
			ParentNodeID:   node.ParentID,
			Status:         model.SopStatusRunning,
			Input:          currentInput,
			ConversationID: run.ConversationID,
			Sort:           node.Sort,
			StartedAt:      &[]time.Time{time.Now()}[0],
		}

		if err := b.ds.Sop().CreateNodeRun(nodeRun); err != nil {
			return fmt.Errorf("failed to create node run: %w", err)
		}
		isUpdate = false
	}

	log.C(ctx).Infow("Node run prepared for execution",
		"run_id", runID, "node_id", nodeID, "node_run_id", nodeRun.ID, "is_update", isUpdate)

	// 执行节点（流式），返回完整输出、思考内容和 token 使用统计
	startTime := time.Now()
	// 注入计费上下文
	traceID := langfuse.TraceID()
	ctx = billing.WithBillingMeta(ctx, run.UserID, "sop_node_execute",
		billing.Metadata("run_id", billing.FormatUint(runID), "node_id", billing.FormatUint(nodeID), "trace_id", traceID))
	// 创建 Langfuse trace
	langfuse.CreateTrace(traceID, "sop_execute",
		langfuse.WithUserID(run.UserID),
		langfuse.WithTraceInput(map[string]interface{}{"run_id": runID, "node_id": nodeID, "node_name": node.Name}),
		langfuse.WithTraceTags("sop"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// ===== Phase 2 Task 2.1: Reserve → LLM → Reconcile 控制流 =====
	// 在 LLM 调用前：CheckAndEstimate + Reserve（legacy_tier 自动跳过，SkipDeduction=true）
	// defer FinalizeReservation 在函数返回时对账：
	//   - actualCost 非零 → Reconcile（delta 回补/退还）
	//   - opErr 非空    → Refund（分类 user_cancelled / provider_timeout / op_failed）
	//   - 两者都空       → Refund("no_actual_cost")（pricing 失败兜底）
	// 注入点：在 langfuse trace 建立后，以便 credits span 挂到同一 trace 下。
	//
	// Task 9 双 Reserve 防护：
	//   Gateway 路径（modelKey != ""）时，ContextBudgetCredits middleware 通过
	//   ReserveBudget（新 budget path）负责预扣。直接调用 creditSvc.Reserve（旧
	//   R2 char-based path）会造成双重预扣，必须跳过。
	var (
		rsv        *credit.Reservation
		actualCost int64
		opErr      error
	)
	defer func() {
		if rsv != nil && b.creditSvc != nil {
			_ = b.creditSvc.FinalizeReservation(ctx, rsv, &actualCost, &opErr)
		}
	}()
	if b.creditSvc != nil && !shouldSkipDirectReserveForGateway(modelKey) {
		user, uerr := b.ds.Users().GetByID(ctx, run.UserID)
		if uerr != nil {
			log.C(ctx).Warnw("Failed to load user for credits pre-check; skipping Reserve",
				"user_id", run.UserID, "err", uerr)
		} else {
			promptChars := computeSopPromptChars(template, node, conversationHistory, currentInput)
			pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSopRun, credit.EstimationInput{
				PromptChars: promptChars,
				Model:       node.ModelName,
				Provider:    credit.ProviderFromModel(node.ModelName),
			})
			if err != nil {
				return wrapCreditError(err, pre)
			}
			if !pre.SkipDeduction {
				idempKey := fmt.Sprintf("sop_run:%d:%d", runID, nodeID)
				rsv, err = b.creditSvc.Reserve(ctx, user, credit.OpSopRun,
					pre.EstimatedCredits, pre.CoefficientID, &idempKey)
				if err != nil {
					return err // ErrInsufficientCredits race 等
				}
			}
		}
	}

	var output, thinking string
	var usage *TokenUsage
	if modelKey != "" {
		// Gateway 路径：用户选择了特定模型，调用 ExecuteNodeStream（内含 executeViaGateway）
		// 通过 handler 拦截 thinking 事件来积累思考内容，确保能持久化到数据库
		var thinkingBuf strings.Builder
		output, usage, err = b.executor.ExecuteNodeStream(ctx, node, currentInput, conversationHistory, modelKey, thinkingMode, func(event string, chunk string) error {
			if event == "thinking" {
				thinkingBuf.WriteString(chunk)
			}
			return handler(event, chunk)
		})
		thinking = thinkingBuf.String()
	} else {
		// 默认路径：使用节点配置的 LLM，根据用户设置决定是否开启深度思考
		output, thinking, usage, err = b.executor.ExecuteNodeStreamWithThinking(ctx, node, currentInput, conversationHistory, func(event string, chunk string) error {
			// 直接透传事件给上层 handler
			return handler(event, chunk)
		}, isLastNode, thinkingMode, run.ConversationID)
	}
	nodeEndTime := time.Now()
	latency := nodeEndTime.Sub(startTime).Milliseconds()

	// 确定实际使用的模型名：Gateway 路径从 usage.ModelName 获取，默认路径从 node.ModelName 获取
	actualModelName := node.ModelName
	if usage != nil && usage.ModelName != "" {
		actualModelName = usage.ModelName
	}

	if err != nil {
		// LLM 调用失败 → defer FinalizeReservation 走 Refund 分支
		opErr = err

		// 节点执行失败，但仍然尝试保存已生成的中间内容和 Token 消耗（Issue 3 & 4）
		updateData := map[string]interface{}{
			"status":        model.SopStatusFailed,
			"output":        output,   // 保存已生成的文字
			"thinking":      thinking, // 保存已生成的思考
			"error_message": err.Error(),
			"latency_ms":    latency,
			"finished_at":   nodeEndTime,
			"model_name":    actualModelName,
		}
		if usage != nil {
			updateData["prompt_tokens"] = usage.PromptTokens
			updateData["completion_tokens"] = usage.CompletionTokens
			updateData["total_tokens"] = usage.TotalTokens
			updateData["reasoning_tokens"] = usage.ReasoningTokens
			updateData["estimated_prompt_tokens"] = usage.EstimatedPromptTokens
		}
		_ = b.ds.Sop().UpdateNodeRun(nodeRun.ID, updateData)
		return fmt.Errorf("node execution failed: %w", err)
	}

	// 更新NodeRun为成功，同时保存思考内容和 token 使用统计
	updateData := map[string]interface{}{
		"status":      model.SopStatusSucceeded,
		"output":      output,
		"thinking":    thinking,
		"latency_ms":  latency,
		"finished_at": nodeEndTime,
		"model_name":  actualModelName,
	}

	// 保存 token 使用统计（如果存在）
	if usage != nil {
		updateData["prompt_tokens"] = usage.PromptTokens
		updateData["completion_tokens"] = usage.CompletionTokens
		updateData["total_tokens"] = usage.TotalTokens
		updateData["reasoning_tokens"] = usage.ReasoningTokens
		updateData["estimated_prompt_tokens"] = usage.EstimatedPromptTokens
		log.C(ctx).Infow("Saving token usage to node run",
			"node_run_id", nodeRun.ID,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens,
			"reasoning_tokens", usage.ReasoningTokens,
			"estimated_prompt_tokens", usage.EstimatedPromptTokens)
	}

	if err := b.ds.Sop().UpdateNodeRun(nodeRun.ID, updateData); err != nil {
		return fmt.Errorf("failed to update node run: %w", err)
	}

	// ===== Phase 2 Task 2.1: 同步计算 actualCost 供 defer Reconcile 使用 =====
	// LLM 成功返回后，从 pricing.CalculateCost 拿真实 cost。失败时：
	//   - 有 rsv：opErr 置位 → defer 触发 Refund("op_failed") 防误扣
	//   - 无 rsv（legacy_tier / creditSvc=nil）：直接跳过
	if rsv != nil && b.pricing != nil {
		if usage != nil && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
			// 优先使用 Gateway 回传的真实 provider（aihubmix / volc / ...）；
			// Gateway 未设置时回退到 ProviderFromModel 的 prefix 猜测。
			provider := usage.Provider
			if provider == "" {
				provider = credit.ProviderFromModel(actualModelName)
			}
			cost, pErr := b.pricing.CalculateCost(ctx, "llm_chat",
				provider, actualModelName,
				usage.PromptTokens, usage.CompletionTokens)
			if pErr != nil {
				// pricing 失败不阻塞业务，defer 走 Refund("op_failed")
				opErr = fmt.Errorf("sop_run pricing calc: %w", pErr)
				log.C(ctx).Warnw("Pricing calc failed; defer will Refund",
					"run_id", runID, "node_id", nodeID,
					"provider", provider, "model", actualModelName, "err", pErr)
			} else {
				actualCost = cost
				// 将真实 token 数回写到 rsv，FinalizeReservation 会把它们
				// 透传到 credit-reconcile span metadata（spec §5.1.3）。
				rsv.ActualPromptTokens = usage.PromptTokens
				rsv.ActualCompletionTokens = usage.CompletionTokens
			}
		}
		// usage 为空或 tokens 全 0：actualCost=0 → defer 走 Refund("no_actual_cost")
	}

	// 节点执行成功后，标记此run已计入（防止后续节点重复处理 Counted 翻转）
	if !run.Counted {
		log.C(ctx).Infow("First successful node execution, marking run as counted", "run_id", runID, "user_id", run.UserID)
		_ = b.ds.Sop().UpdateRun(runID, map[string]interface{}{
			"counted": true,
		})
	}

	// 检查是否所有节点都执行完成（重新获取最新的 NodeRuns 状态）
	allNodeRunsForCheck, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err == nil {
		// 统计每个节点的最新成功记录（用于处理重复执行的情况）
		nodeStatusMap := make(map[uint]bool) // nodeID -> has succeeded record
		for _, nr := range allNodeRunsForCheck {
			if nr.Status == model.SopStatusSucceeded {
				nodeStatusMap[nr.NodeID] = true
			}
		}
		completedCount := len(nodeStatusMap)
		if completedCount == len(allNodes) {
			// 所有节点执行完成，生成最终Note
			finalOutput := output
			note, err := b.executor.CreateFinalNote(ctx, run, finalOutput)
			if err == nil {
				finishTime := time.Now()
				_ = b.ds.Sop().UpdateRun(runID, map[string]interface{}{
					"status":        model.SopStatusSucceeded,
					"final_note_id": note.ID,
					"finished_at":   finishTime,
				})
				log.C(ctx).Infow("SOP execution completed", "run_id", runID, "user_id", run.UserID)
			}
		}
	}

	return nil
}

// GetRunStatus 获取Run执行状态
func (b *sopBiz) GetRunStatus(ctx context.Context, runID uint) (*RunStatus, error) {
	// 获取Run信息
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("run not found: %w", err)
	}

	// 获取模板的所有节点
	allNodes, err := b.ds.Sop().ListNodesByTemplate(run.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template nodes: %w", err)
	}

	// 获取已执行的NodeRun
	completedNodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node runs: %w", err)
	}

	// 构建已完成节点信息
	completedNodes := []CompletedNodeInfo{}
	completedNodeIDs := make(map[uint]bool)
	currentNodeSort := -1

	for _, nodeRun := range completedNodeRuns {
		if nodeRun.Status == model.SopStatusSucceeded {
			completedNodeIDs[nodeRun.NodeID] = true
			completedNodes = append(completedNodes, CompletedNodeInfo{
				NodeRunID:    nodeRun.ID, // 节点运行ID
				NodeID:       nodeRun.NodeID,
				NodeName:     nodeRun.Node.Name,
				Sort:         nodeRun.Sort,
				Input:        nodeRun.Input,  // 节点输入
				Output:       nodeRun.Output, // 返回完整输出
				Thinking:     nodeRun.Thinking,
				FromBookmark: nodeRun.FromBookmark, // 是否从书签恢复
				BookmarkID:   nodeRun.BookmarkID,   // 关联的书签ID
				ModelName:    nodeRun.ModelName,    // B5: 实际调用的模型名称
				LatencyMs:    nodeRun.LatencyMs,    // B5: 执行耗时
				TotalTokens:  nodeRun.TotalTokens,  // B5: 总 tokens（绕过 model json:"-"）
			})
			if nodeRun.Sort > currentNodeSort {
				currentNodeSort = nodeRun.Sort
			}
		}
	}

	// 计算每个已完成节点的可访问性
	// 规则：只有当一个节点之前的所有节点都已完成时，该节点才可访问
	for i := range completedNodes {
		isAccessible := true
		// 检查该节点之前的所有节点是否都已完成
		for _, node := range allNodes {
			if node.Sort < completedNodes[i].Sort {
				if !completedNodeIDs[node.ID] {
					// 存在未完成的前置节点，标记为不可访问
					isAccessible = false
					break
				}
			}
		}
		completedNodes[i].IsAccessible = isAccessible
	}

	// 找到下一个节点
	var nextNode *NextNodeInfo
	for _, node := range allNodes {
		if !completedNodeIDs[node.ID] {
			// 检查是否还有下一个节点
			hasNext := false
			for i := 0; i < len(allNodes); i++ {
				if allNodes[i].Sort > node.Sort && !completedNodeIDs[allNodes[i].ID] {
					hasNext = true
					break
				}
			}
			nextNode = &NextNodeInfo{
				NodeID:   node.ID,
				NodeName: node.Name,
				Sort:     node.Sort,
				IsFirst:  len(completedNodes) == 0,
				HasNext:  hasNext,
			}
			break
		}
	}

	return &RunStatus{
		Status:          run.Status,
		CurrentNodeSort: currentNodeSort,
		CompletedNodes:  completedNodes,
		NextNode:        nextNode,
		TotalNodes:      len(allNodes),
		CompletedCount:  len(completedNodes),
	}, nil
}

func (b *sopBiz) GetRun(ctx context.Context, id uint) (*model.SopRun, error) {
	return b.ds.Sop().GetRun(id)
}

// CheckRunOwnership 检查Run是否属于指定用户（轻量级权限验证）
func (b *sopBiz) CheckRunOwnership(ctx context.Context, runID, userID uint) (bool, error) {
	return b.ds.Sop().CheckRunOwnership(runID, userID)
}

func (b *sopBiz) ListRuns(ctx context.Context, offset, limit int, userID *uint) ([]model.SopRun, int64, error) {
	return b.ds.Sop().ListRuns(offset, limit, userID)
}

// RunStats 运行统计数据（token 和成本）
type RunStats struct {
	TotalTokens int64 `json:"total_tokens"`
	CostCents   int64 `json:"cost_cents"`
}

// GetRunsStats 批量获取运行的 token 和成本统计
func (b *sopBiz) GetRunsStats(ctx context.Context, runIDs []uint) (map[uint]RunStats, error) {
	if len(runIDs) == 0 {
		return map[uint]RunStats{}, nil
	}

	result := make(map[uint]RunStats, len(runIDs))

	// 从 sop_node_run 聚合 token
	type tokenRow struct {
		RunID       uint  `gorm:"column:run_id"`
		TotalTokens int64 `gorm:"column:total_tokens"`
	}
	var tokenRows []tokenRow
	if err := b.ds.DB().Raw(
		"SELECT run_id, COALESCE(SUM(total_tokens), 0) AS total_tokens FROM sop_node_run WHERE run_id IN ? AND deleted_at IS NULL GROUP BY run_id",
		runIDs,
	).Scan(&tokenRows).Error; err != nil {
		return nil, fmt.Errorf("query token stats: %w", err)
	}
	for _, r := range tokenRows {
		s := result[r.RunID]
		s.TotalTokens = r.TotalTokens
		result[r.RunID] = s
	}

	// 从 usage_record 聚合成本
	type costRow struct {
		BizRefID  uint  `gorm:"column:biz_ref_id"`
		CostCents int64 `gorm:"column:cost_cents"`
	}
	var costRows []costRow
	if err := b.ds.DB().Raw(
		"SELECT biz_ref_id, COALESCE(SUM(cost_cents), 0) AS cost_cents FROM usage_record WHERE biz_ref_type = 'sop_run' AND biz_ref_id IN ? GROUP BY biz_ref_id",
		runIDs,
	).Scan(&costRows).Error; err != nil {
		return nil, fmt.Errorf("query cost stats: %w", err)
	}
	for _, r := range costRows {
		s := result[r.BizRefID]
		s.CostCents = r.CostCents
		result[r.BizRefID] = s
	}

	return result, nil
}

func (b *sopBiz) GetRunWithNodes(ctx context.Context, runID uint) (*model.SopRun, []model.SopNodeRun, error) {
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, nil, err
	}

	nodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return nil, nil, err
	}

	return run, nodeRuns, nil
}

// Note operations
func (b *sopBiz) GetNote(ctx context.Context, id uint) (*model.SopNote, error) {
	return b.ds.Sop().GetNote(id)
}

func (b *sopBiz) ListNotesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.SopNote, int64, error) {
	return b.ds.Sop().ListNotesByUser(userID, offset, limit)
}

func (b *sopBiz) ListExecutedTemplatesByUser(ctx context.Context, userID uint) ([]store.ExecutedTemplateInfo, error) {
	return b.ds.Sop().ListExecutedTemplatesByUser(userID)
}

func (b *sopBiz) ListTemplateRunsWithDetails(ctx context.Context, userID, templateID uint, offset, limit int) ([]v1.TemplateRunHistoryResponse, int64, error) {
	// 验证template是否存在
	_, err := b.ds.Sop().GetTemplate(templateID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, 0, fmt.Errorf("模板不存在")
		}
		return nil, 0, err
	}

	// 获取模板的所有节点，用于计算total_nodes
	nodes, err := b.ds.Sop().ListNodesByTemplate(templateID)
	if err != nil {
		return nil, 0, fmt.Errorf("获取模板节点失败: %w", err)
	}
	totalNodes := len(nodes)

	// 获取runs列表
	runs, total, err := b.ds.Sop().ListRunsByUserAndTemplate(userID, templateID, offset, limit)
	if err != nil {
		return nil, 0, err
	}

	if len(runs) == 0 {
		return []v1.TemplateRunHistoryResponse{}, total, nil
	}

	// 提取所有run_id
	runIDs := make([]uint, len(runs))
	for i, run := range runs {
		runIDs[i] = run.ID
	}

	// 批量获取node执行记录
	nodeRunsMap, err := b.ds.Sop().ListNodeRunsByRuns(runIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("获取节点执行记录失败: %w", err)
	}

	// 批量获取对话记录
	chatMsgsMap, err := b.ds.Sop().ListChatMessagesByRuns(runIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("获取对话记录失败: %w", err)
	}

	// 提取所有node_id用于查询文件
	nodeIDs := make([]uint, 0)
	for _, nodeRuns := range nodeRunsMap {
		for _, nodeRun := range nodeRuns {
			if nodeRun.Node != nil {
				nodeIDs = append(nodeIDs, nodeRun.Node.ID)
			}
		}
	}

	// 批量获取文件列表（按node_id分组）
	filesMap := make(map[uint][]model.SopFile)
	if len(nodeIDs) > 0 {
		filesMap, err = b.ds.Sop().ListFilesByNodeRuns(nodeIDs)
		if err != nil {
			// 文件获取失败不影响主流程，记录日志即可
			log.C(ctx).Warnw("获取文件列表失败", "error", err)
		}
	}

	// 组装结果
	result := make([]v1.TemplateRunHistoryResponse, len(runs))
	for i, run := range runs {
		nodeRuns := nodeRunsMap[run.ID]
		chatMsgs := chatMsgsMap[run.ID]

		// 计算已完成节点数
		completedCount := 0
		for _, nodeRun := range nodeRuns {
			if nodeRun.Status == model.SopStatusSucceeded {
				completedCount++
			}
		}

		// 转换nodeRuns
		nodeRunInfos := make([]v1.TemplateNodeRunInfo, len(nodeRuns))
		for j, nodeRun := range nodeRuns {
			nodeName := ""
			nodeID := uint(0)
			if nodeRun.Node != nil {
				nodeName = nodeRun.Node.Name
				nodeID = nodeRun.Node.ID
			}

			// 获取该节点关联的文件（需要同时匹配node_id和run_id）
			// 因为同一个template的不同run可能使用相同的node_id，所以必须按run_id过滤
			allFiles := filesMap[nodeID]
			files := make([]model.SopFile, 0)
			for _, file := range allFiles {
				// 只返回属于当前run的文件
				if file.RunID != nil && *file.RunID == run.ID {
					files = append(files, file)
				}
			}
			fileInfos := make([]v1.TemplateFileInfo, len(files))
			for k, file := range files {
				fileInfos[k] = v1.TemplateFileInfo{
					ID:       file.ID,
					FileName: file.FileName,
					FileURL:  file.FileURL,
					FileSize: file.FileSize,
					FileType: file.FileType,
				}
			}

			// 清理input和output，过滤PDF格式代码等无效内容
			cleanedInput := cleanPDFFormatCode(nodeRun.Input)
			cleanedOutput := cleanPDFFormatCode(nodeRun.Output)
			cleanedThinking := cleanPDFFormatCode(nodeRun.Thinking)

			// 生成输出预览（截取前200字符）
			outputPreview := ""
			if len(cleanedOutput) > 200 {
				outputPreview = cleanedOutput[:200] + "..."
			} else {
				outputPreview = cleanedOutput
			}

			// 格式化时间
			finishedAtStr := (*string)(nil)
			if nodeRun.FinishedAt != nil {
				finishedAtStr = new(string)
				*finishedAtStr = nodeRun.FinishedAt.Format(time.RFC3339)
			}

			nodeRunInfos[j] = v1.TemplateNodeRunInfo{
				ID:            nodeRun.ID,
				NodeID:        nodeID,
				NodeName:      nodeName,
				Sort:          nodeRun.Sort,
				Status:        nodeRun.Status,
				FinishedAt:    finishedAtStr,
				Input:         cleanedInput,
				Output:        cleanedOutput,
				Thinking:      cleanedThinking,
				OutputPreview: outputPreview,
				Files:         fileInfos,
			}
		}

		// 转换chatMessages
		chatMsgInfos := make([]v1.TemplateChatMessageInfo, len(chatMsgs))
		for j, msg := range chatMsgs {
			chatMsgInfos[j] = v1.TemplateChatMessageInfo{
				ID:                    msg.ID,
				Role:                  msg.Role,
				Content:               msg.Content,
				CreatedAt:             msg.CreatedAt.Format(time.RFC3339),
				PromptTokens:          msg.PromptTokens,
				CompletionTokens:      msg.CompletionTokens,
				TotalTokens:           msg.TotalTokens,
				ReasoningTokens:       msg.ReasoningTokens,
				EstimatedPromptTokens: msg.EstimatedPromptTokens,
			}
		}

		result[i] = v1.TemplateRunHistoryResponse{
			ID:             run.ID,
			TemplateID:     run.TemplateID,
			Status:         run.Status,
			CreatedAt:      run.CreatedAt.Format(time.RFC3339),
			UpdatedAt:      run.UpdatedAt.Format(time.RFC3339),
			CompletedCount: completedCount,
			TotalNodes:     totalNodes,
			NodeRuns:       nodeRunInfos,
			ChatMessages:   chatMsgInfos,
		}
	}

	return result, total, nil
}

// ChatAfterRunStream Run完成后的对话流式接口
// modelKey: 用户选择的模型 key（空字符串表示使用最后一个节点的默认配置）
func (b *sopBiz) ChatAfterRunStream(ctx context.Context, runID uint, conversationID string, question string, userID uint, modelKey string, deepThinking bool, regenerateMsgID uint, handler func(event string, chunk string) error) (retErr error) {
	// 互斥锁检测（解决“执行互斥锁”问题）
	if _, loaded := b.runningRuns.LoadOrStore(runID, struct{}{}); loaded {
		log.C(ctx).Warnw("Detected concurrent chat attempt", "run_id", runID)
		return fmt.Errorf("该任务正在处理中，请勿重复操作")
	}
	defer b.runningRuns.Delete(runID)

	// 验证Run

	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return fmt.Errorf("run not found: %w", err)
	}
	if run.UserID != userID {
		return fmt.Errorf("no permission to access this run")
	}
	if conversationID != "" && run.ConversationID != conversationID {
		return fmt.Errorf("conversation_id mismatch with run")
	}
	// 使用 run 中的会话ID
	conversationID = run.ConversationID

	// ===== 权限检查 =====
	// 检查用户是否仍有权限执行此模板（针对子用户权限被撤销的情况）
	hasPermission, err := b.ds.Customers().HasTemplatePermission(ctx, run.UserID, run.TemplateID)
	if err != nil {
		return fmt.Errorf("failed to check template permission: %w", err)
	}
	if !hasPermission {
		log.C(ctx).Warnw("User permission revoked for template", "user_id", run.UserID, "template_id", run.TemplateID)
		return fmt.Errorf("您没有权限执行此模板（权限已被撤销）")
	}

	// 获取模板、节点、节点执行记录
	template, err := b.ds.Sop().GetTemplate(run.TemplateID)
	if err != nil {
		return fmt.Errorf("template not found: %w", err)
	}
	// 防御性检查：若模板关闭了末尾 AI 聊天开关，直接拒绝对话请求（防 API 直连绕过）
	if !template.TrailingChatEnabled {
		log.C(ctx).Warnw("ChatAfterRunStream rejected: trailing chat disabled", "run_id", runID, "template_id", run.TemplateID)
		return fmt.Errorf("该 SOP 未启用末尾 AI 聊天")
	}
	nodes, err := b.ds.Sop().ListNodesByTemplate(run.TemplateID)
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("template has no nodes")
	}
	// 找到最后一个节点，作为对话模型配置载体
	lastNode := nodes[0]
	for _, n := range nodes {
		if n.Sort > lastNode.Sort {
			lastNode = n
		}
	}

	nodeRuns, err := b.ds.Sop().ListNodeRunsByRun(runID)
	if err != nil {
		return fmt.Errorf("failed to list node runs: %w", err)
	}

	// 3. 获取已有的聊天消息（通过 /v1/sop/chat/stream API 产生的历史对话）
	// ListChatMessagesByRun 会获取同一个 run_id 下的所有聊天记录，按 seq 排序
	// 这些记录都在同一个 conversation_id 下，确保大模型有完整的对话记忆
	chatMessages, err := b.ds.Sop().ListChatMessagesByRun(runID)
	if err != nil {
		return fmt.Errorf("failed to list chat messages: %w", err)
	}

	// 处理重新生成逻辑
	if regenerateMsgID > 0 {
		log.C(ctx).Infow("Processing regeneration request", "regenerate_msg_id", regenerateMsgID)

		targetIndex := -1
		for i, msg := range chatMessages {
			if msg.ID == regenerateMsgID {
				targetIndex = i
				break
			}
		}

		if targetIndex == -1 {
			return fmt.Errorf("message to regenerate not found: %d", regenerateMsgID)
		}

		targetMsg := chatMessages[targetIndex]
		if targetMsg.Role != "assistant" {
			return fmt.Errorf("can only regenerate assistant message")
		}

		// 确定要排除的消息索引列表（目标消息 + 可能存在的前置用户消息）
		excludeIndices := make(map[int]bool)
		excludeIndices[targetIndex] = true

		if targetIndex > 0 {
			prevMsg := chatMessages[targetIndex-1]
			if prevMsg.Role == "user" {
				excludeIndices[targetIndex-1] = true
			}
		}

		// 删除旧记录并构建新切片（解决数据库记录重复问题）
		newChatMessages := make([]model.SopChatMsg, 0, len(chatMessages))
		for i, msg := range chatMessages {
			if excludeIndices[i] {
				// 物理删除旧记录，确保数据库只保留最新生成的对话
				if err := b.ds.Sop().DeleteChatMessage(msg.ID); err != nil {
					log.C(ctx).Warnw("Failed to delete chat message during regeneration", "msg_id", msg.ID, "error", err)
				}
			} else {
				newChatMessages = append(newChatMessages, msg)
			}
		}
		chatMessages = newChatMessages

		log.C(ctx).Infow("Regeneration process: old records deleted", "target_msg_id", targetMsg.ID)
	}

	// 构建对话历史：仅包含节点输入/输出 -> 已有聊天消息
	// 注意：不包含模板或节点的 Prompt，使 AI 回归纯净助手状态
	history := []LLMMessage{}
	// 1. 添加所有成功执行的节点输入/输出对（前序步骤的内容）
	for _, nr := range nodeRuns {
		if nr.Status != model.SopStatusSucceeded {
			continue
		}
		history = append(history, LLMMessage{Role: "user", Content: nr.Input})
		history = append(history, LLMMessage{Role: "assistant", Content: nr.Output})
	}

	maxSeq := 0
	for _, msg := range chatMessages {
		if msg.Seq > maxSeq {
			maxSeq = msg.Seq
		}
		history = append(history, LLMMessage{Role: msg.Role, Content: msg.Content})
	}

	// 保存当前用户提问
	userMsg := &model.SopChatMsg{
		RunID:          runID,
		ConversationID: conversationID,
		UserID:         userID,
		Role:           "user",
		Content:        question,
		Seq:            maxSeq + 1,
	}
	if err := b.ds.Sop().CreateChatMessage(userMsg); err != nil {
		return fmt.Errorf("failed to save chat message: %w", err)
	}
	// 将当前问题加入 history
	history = append(history, LLMMessage{Role: "user", Content: question})

	// 调用模型流式生成回答
	// 注入 Langfuse trace（追问路径可观测性，与节点执行路径对齐）
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "sop_chat",
		langfuse.WithUserID(userID),
		langfuse.WithTraceInput(map[string]interface{}{
			"run_id":          runID,
			"conversation_id": conversationID,
			"question":        question,
			"history_len":     len(history),
			"model_key":       modelKey,
			"deep_thinking":   deepThinking,
		}),
		langfuse.WithTraceTags("sop", "chat"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)
	// 注入计费上下文
	ctx = billing.WithBillingMeta(ctx, userID, "sop_chat_stream",
		billing.Metadata("run_id", billing.FormatUint(runID), "conversation_id", conversationID, "trace_id", traceID))

	// ===== Phase 2 Task 2.1: Reserve → LLM → Reconcile 控制流 =====
	// 与 ExecuteNodeStream 同构（operation=OpSopChat）。idempKey 基于 run + 新 user message seq
	// （maxSeq+1 就是上面 userMsg.Seq），保证同一追问重试不会二次扣减。
	//
	// Task 9 双 Reserve 防护（chat 路径同节点执行路径）：
	//   Gateway 路径（modelKey != ""）跳过 creditSvc.Reserve，由 middleware 的
	//   ReserveBudget 负责预扣，避免双重扣减。
	var (
		chatRsv        *credit.Reservation
		chatActualCost int64
		chatOpErr      error
	)
	defer func() {
		if chatRsv != nil && b.creditSvc != nil {
			_ = b.creditSvc.FinalizeReservation(ctx, chatRsv, &chatActualCost, &chatOpErr)
		}
	}()
	if b.creditSvc != nil && !shouldSkipDirectReserveForGateway(modelKey) {
		user, uerr := b.ds.Users().GetByID(ctx, userID)
		if uerr != nil {
			log.C(ctx).Warnw("Failed to load user for credits pre-check; skipping Reserve",
				"user_id", userID, "err", uerr)
		} else {
			chatPromptChars := computeSopChatPromptChars(history)
			chatModel := lastNode.ModelName
			pre, err := b.creditSvc.CheckAndEstimate(ctx, user, credit.OpSopChat, credit.EstimationInput{
				PromptChars: chatPromptChars,
				Model:       chatModel,
				Provider:    credit.ProviderFromModel(chatModel),
			})
			if err != nil {
				return wrapCreditError(err, pre)
			}
			if !pre.SkipDeduction {
				idempKey := fmt.Sprintf("sop_chat:%d:%d", runID, userMsg.Seq)
				chatRsv, err = b.creditSvc.Reserve(ctx, user, credit.OpSopChat,
					pre.EstimatedCredits, pre.CoefficientID, &idempKey)
				if err != nil {
					return err
				}
			}
		}
	}

	// 传入空字符串作为 input，这样执行器不会拼装节点的 Prompt，AI 保持普通助手身份
	var answerBuf, thinkingBuf strings.Builder
	chatStart := time.Now() // B4: 记录 chat LLM 调用起始时间，用于 duration_ms 持久化
	streamHandler := func(event string, chunk string) error {
		if event == "message" {
			answerBuf.WriteString(chunk)
		}
		if event == "thinking" {
			thinkingBuf.WriteString(chunk)
		}
		if event == "done" {
			return nil // 拦截 done 事件，稍后随着 ID 一起发送
		}
		return handler(event, chunk)
	}

	var thinking string
	var usage *TokenUsage
	if modelKey != "" {
		// Gateway 路径：用户选择了特定模型，调用 ExecuteNodeStream（内含 executeViaGateway）
		_, usage, err = b.executor.ExecuteNodeStream(ctx, &lastNode, "", history, modelKey, deepThinking, streamHandler)
		thinking = thinkingBuf.String()
	} else {
		// 默认路径：使用最后一个节点配置的 LLM
		_, thinking, usage, err = b.executor.ExecuteNodeStreamWithThinking(
			ctx,
			&lastNode,
			"", // 传入空字符串，避免触发 node.Prompt 拼接
			history,
			streamHandler,
			true,         // isLastNode
			deepThinking, // deepThinking
			conversationID,
		)
	}
	if err != nil {
		// LLM 调用失败或客户端断连 → defer FinalizeReservation 走 Refund 分支
		// classifyReason 会把 context.Canceled 归类为 user_cancelled。
		chatOpErr = err
		return err
	}

	// 保存助手回复
	assistantMsg := &model.SopChatMsg{
		RunID:          runID,
		ConversationID: conversationID,
		UserID:         userID,
		Role:           "assistant",
		Content:        answerBuf.String(),
		Thinking:       thinking, // 保存思考过程
		Seq:            maxSeq + 2,
		ModelName:      lastNode.ModelName,                   // B4: 持久化实际调用的模型名（router 路径下若 usage.ModelName 非空会在下方覆盖）
		DurationMs:     time.Since(chatStart).Milliseconds(), // B4: LLM 流式总耗时
	}

	// 保存 token 使用统计（如果存在）
	if usage != nil {
		if usage.ModelName != "" {
			assistantMsg.ModelName = usage.ModelName
		}
		assistantMsg.PromptTokens = usage.PromptTokens
		assistantMsg.CompletionTokens = usage.CompletionTokens
		assistantMsg.TotalTokens = usage.TotalTokens
		assistantMsg.ReasoningTokens = usage.ReasoningTokens
		assistantMsg.EstimatedPromptTokens = usage.EstimatedPromptTokens
		log.C(ctx).Infow("Saving token usage to chat message",
			"run_id", runID,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens,
			"reasoning_tokens", usage.ReasoningTokens)
	}

	if err := b.ds.Sop().CreateChatMessage(assistantMsg); err != nil {
		return fmt.Errorf("failed to save assistant message: %w", err)
	}

	// 更新 trace output（token 用量与响应长度）
	traceOutput := map[string]interface{}{
		"response_length": answerBuf.Len(),
		"model_name":      assistantMsg.ModelName,
		"duration_ms":     assistantMsg.DurationMs,
	}
	if usage != nil {
		traceOutput["prompt_tokens"] = usage.PromptTokens
		traceOutput["completion_tokens"] = usage.CompletionTokens
		traceOutput["total_tokens"] = usage.TotalTokens
	}
	langfuse.CreateTrace(traceID, "sop_chat", langfuse.WithTraceOutput(traceOutput))

	// ===== Phase 2 Task 2.1: 同步计算 actualCost 供 defer Reconcile 使用 =====
	if chatRsv != nil && b.pricing != nil {
		if usage != nil && (usage.PromptTokens > 0 || usage.CompletionTokens > 0) {
			effectiveModel := lastNode.ModelName
			if usage.ModelName != "" {
				effectiveModel = usage.ModelName
			}
			// 优先使用 Gateway 回传的真实 provider；回退到 prefix 猜测。
			provider := usage.Provider
			if provider == "" {
				provider = credit.ProviderFromModel(effectiveModel)
			}
			cost, pErr := b.pricing.CalculateCost(ctx, "llm_chat",
				provider, effectiveModel,
				usage.PromptTokens, usage.CompletionTokens)
			if pErr != nil {
				chatOpErr = fmt.Errorf("sop_chat pricing calc: %w", pErr)
				log.C(ctx).Warnw("Pricing calc failed; defer will Refund",
					"run_id", runID, "provider", provider, "model", effectiveModel, "err", pErr)
			} else {
				chatActualCost = cost
				// 把真实 token 数透传到 credit-reconcile span metadata（spec §5.1.3）。
				chatRsv.ActualPromptTokens = usage.PromptTokens
				chatRsv.ActualCompletionTokens = usage.CompletionTokens
			}
		}
	}

	// 发送包含 message_id 的完成事件
	donePayload := fmt.Sprintf(`{"status":"completed","message_id":%d}`, assistantMsg.ID)
	return handler("done", donePayload)
}

// ListChatMessages 获取指定run的聊天记录（需校验归属）
func (b *sopBiz) ListChatMessages(ctx context.Context, runID uint, userID uint) ([]model.SopChatMsg, error) {
	// 校验 run 归属
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("run not found: %w", err)
	}
	if run.UserID != userID {
		return nil, fmt.Errorf("no permission to access this run")
	}

	msgs, err := b.ds.Sop().ListChatMessagesByRun(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to list chat messages: %w", err)
	}
	return msgs, nil
}

// DeleteRun 删除指定用户的 SOP 记录（含级联数据）
func (b *sopBiz) DeleteRun(ctx context.Context, runID uint, userID uint) error {
	// 1. 校验所有权
	isOwner, err := b.ds.Sop().CheckRunOwnership(runID, userID)
	if err != nil {
		return err
	}
	if !isOwner {
		return errors.New("无权删除此运行记录")
	}

	// 2. 调用 store 层执行物理删除
	return b.ds.Sop().DeleteRun(runID)
}

// DeleteRuns 批量删除 SOP 记录
func (b *sopBiz) DeleteRuns(ctx context.Context, runIDs []uint, userID uint) error {
	if len(runIDs) == 0 {
		return nil
	}

	// 1. 逐个校验所有权，确保安全性
	for _, runID := range runIDs {
		isOwner, err := b.ds.Sop().CheckRunOwnership(runID, userID)
		if err != nil {
			return err
		}
		if !isOwner {
			return fmt.Errorf("无权删除运行记录 #%d", runID)
		}
	}

	// 2. 调用 store 层执行批量物理删除
	return b.ds.Sop().DeleteRuns(runIDs)
}

// CleanZombieRuns 清理僵尸任务（将长时间运行中的任务标记为失败）
func (b *sopBiz) CleanZombieRuns(ctx context.Context, timeout time.Duration) error {
	affected, err := b.ds.Sop().ResetZombieRuns(timeout)
	if err != nil {
		return fmt.Errorf("failed to reset zombie runs: %w", err)
	}
	if affected > 0 {
		log.C(ctx).Infow("Zombies cleaned successfully", "count", affected, "timeout", timeout)
	}
	return nil
}

// Bookmark operations

// SaveNodeBookmarkByRunAndNode 通过 runID 和 nodeID 保存书签
func (b *sopBiz) SaveNodeBookmarkByRunAndNode(ctx context.Context, userID, runID, nodeID uint, bookmarkName, description string) (*model.SopNodeBookmark, error) {
	// 1. 根据 runID 和 nodeID 查询 NodeRun
	nodeRun, err := b.ds.Sop().GetNodeRunByRunAndNode(runID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node run: %w", err)
	}
	if nodeRun == nil {
		return nil, fmt.Errorf("未找到节点执行记录")
	}

	// 2. 调用原有的 SaveNodeBookmark 方法
	return b.SaveNodeBookmark(ctx, userID, nodeRun.ID, bookmarkName, description)
}

// SaveNodeBookmark 保存节点为书签
func (b *sopBiz) SaveNodeBookmark(ctx context.Context, userID, nodeRunID uint, bookmarkName, description string) (*model.SopNodeBookmark, error) {
	// 1. 获取NodeRun记录
	nodeRun, err := b.ds.Sop().GetNodeRun(nodeRunID)
	if err != nil {
		return nil, fmt.Errorf("node run not found: %w", err)
	}

	// 2. 验证权限（NodeRun必须属于当前用户）
	if nodeRun.UserID != userID {
		return nil, fmt.Errorf("无权限操作该节点运行记录")
	}

	// 3. 验证节点运行状态（只能保存成功的节点）
	if nodeRun.Status != model.SopStatusSucceeded {
		return nil, fmt.Errorf("只能保存执行成功的节点")
	}

	// 4. 获取Run信息（用于获取TemplateID）
	run, err := b.ds.Sop().GetRun(nodeRun.RunID)
	if err != nil {
		return nil, fmt.Errorf("run not found: %w", err)
	}

	// 5. 获取Node信息（用于获取Sort）
	node, err := b.ds.Sop().GetNode(nodeRun.NodeID)
	if err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}

	// 6. 检查是否已有书签（用户、模板、节点的唯一性）
	existingBookmark, err := b.ds.Sop().GetBookmarkByUserTemplateNode(userID, run.TemplateID, node.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing bookmark: %w", err)
	}

	// 7. 创建或更新书签
	bookmark := &model.SopNodeBookmark{
		UserID:           userID,
		TemplateID:       run.TemplateID,
		NodeID:           node.ID,
		NodeSort:         node.Sort,
		Input:            nodeRun.Input,
		Output:           nodeRun.Output,
		Thinking:         nodeRun.Thinking,
		SourceRunID:      &run.ID,
		SourceNodeRunID:  &nodeRun.ID,
		PromptTokens:     nodeRun.PromptTokens,
		CompletionTokens: nodeRun.CompletionTokens,
		TotalTokens:      nodeRun.TotalTokens,
		BookmarkName:     bookmarkName,
		Description:      description,
	}

	if existingBookmark != nil {
		// 更新现有书签
		updates := map[string]interface{}{
			"input":              bookmark.Input,
			"output":             bookmark.Output,
			"thinking":           bookmark.Thinking,
			"source_run_id":      bookmark.SourceRunID,
			"source_node_run_id": bookmark.SourceNodeRunID,
			"prompt_tokens":      bookmark.PromptTokens,
			"completion_tokens":  bookmark.CompletionTokens,
			"total_tokens":       bookmark.TotalTokens,
			"bookmark_name":      bookmark.BookmarkName,
			"description":        bookmark.Description,
		}
		if err := b.ds.Sop().UpdateBookmark(existingBookmark.ID, updates); err != nil {
			return nil, fmt.Errorf("failed to update bookmark: %w", err)
		}
		bookmark.ID = existingBookmark.ID
		log.C(ctx).Infow("Updated bookmark", "bookmark_id", bookmark.ID, "node_id", node.ID, "user_id", userID)
	} else {
		// 创建新书签
		if err := b.ds.Sop().CreateBookmark(bookmark); err != nil {
			return nil, fmt.Errorf("failed to create bookmark: %w", err)
		}
		log.C(ctx).Infow("Created bookmark", "bookmark_id", bookmark.ID, "node_id", node.ID, "user_id", userID)
	}

	return bookmark, nil
}

// GetBookmark 获取书签详情
func (b *sopBiz) GetBookmark(ctx context.Context, id, userID uint) (*model.SopNodeBookmark, error) {
	bookmark, err := b.ds.Sop().GetBookmark(id)
	if err != nil {
		return nil, fmt.Errorf("bookmark not found: %w", err)
	}

	// 验证权限
	if bookmark.UserID != userID {
		return nil, fmt.Errorf("无权限访问该书签")
	}

	return bookmark, nil
}

// ListBookmarksByTemplate 获取用户在指定模板下的所有书签
func (b *sopBiz) ListBookmarksByTemplate(ctx context.Context, userID, templateID uint) ([]model.SopNodeBookmark, error) {
	return b.ds.Sop().ListBookmarksByUserAndTemplate(userID, templateID)
}

// DeleteBookmark 删除书签
func (b *sopBiz) DeleteBookmark(ctx context.Context, id, userID uint) error {
	// 验证权限
	bookmark, err := b.ds.Sop().GetBookmark(id)
	if err != nil {
		return fmt.Errorf("bookmark not found: %w", err)
	}

	if bookmark.UserID != userID {
		return fmt.Errorf("无权限删除该书签")
	}

	return b.ds.Sop().DeleteBookmark(id)
}

// ApplyBookmarkToNode 应用书签到节点
func (b *sopBiz) ApplyBookmarkToNode(ctx context.Context, userID, runID, nodeID uint, bookmarkID *uint) (*model.SopNodeRun, error) {
	// 1. 验证Run权限
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("run not found: %w", err)
	}
	if run.UserID != userID {
		return nil, fmt.Errorf("无权限操作该运行记录")
	}

	// 2. 获取Node信息
	node, err := b.ds.Sop().GetNode(nodeID)
	if err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}

	// 验证节点属于该模板
	if node.TemplateID != run.TemplateID {
		return nil, fmt.Errorf("节点不属于该模板")
	}

	// 3. 获取书签
	var bookmark *model.SopNodeBookmark
	if bookmarkID != nil {
		bookmark, err = b.ds.Sop().GetBookmark(*bookmarkID)
		if err != nil {
			return nil, fmt.Errorf("bookmark not found: %w", err)
		}
		if bookmark.UserID != userID {
			return nil, fmt.Errorf("无权限访问该书签")
		}
	} else {
		// 自动查找该节点的书签
		bookmark, err = b.ds.Sop().GetBookmarkByUserTemplateNode(userID, run.TemplateID, nodeID)
		if err != nil {
			return nil, fmt.Errorf("failed to get bookmark: %w", err)
		}
		if bookmark == nil {
			return nil, fmt.Errorf("该节点没有保存的书签")
		}
	}

	// 4. 检查是否已有NodeRun记录
	existingNodeRun, err := b.ds.Sop().GetNodeRunByRunAndNode(runID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing node run: %w", err)
	}

	// 5. 如果该节点已经执行过，需要清理下游节点（与重新执行逻辑一致）
	if existingNodeRun != nil {
		if err := b.ds.Sop().DeleteNodeRunsAfterSort(runID, node.Sort); err != nil {
			return nil, fmt.Errorf("failed to clean downstream nodes: %w", err)
		}
		// 同时清理笔记和聊天消息
		_ = b.ds.Sop().DeleteNotesByRun(runID)
		_ = b.ds.Sop().DeleteChatMessagesByRun(runID)
	}

	// 6. 创建或更新NodeRun
	now := time.Now()
	nodeRun := &model.SopNodeRun{
		RunID:            runID,
		NodeID:           nodeID,
		TemplateID:       run.TemplateID,
		UserID:           userID,
		Sort:             node.Sort,
		Status:           model.SopStatusSucceeded,
		FromBookmark:     true,
		BookmarkID:       &bookmark.ID,
		Input:            bookmark.Input,
		Output:           bookmark.Output,
		Thinking:         bookmark.Thinking,
		PromptTokens:     bookmark.PromptTokens,
		CompletionTokens: bookmark.CompletionTokens,
		TotalTokens:      bookmark.TotalTokens,
		ConversationID:   run.ConversationID,
		StartedAt:        &now,
		FinishedAt:       &now,
	}

	if existingNodeRun != nil {
		nodeRun.ID = existingNodeRun.ID
		updates := map[string]interface{}{
			"status":            nodeRun.Status,
			"from_bookmark":     nodeRun.FromBookmark,
			"bookmark_id":       nodeRun.BookmarkID,
			"input":             nodeRun.Input,
			"output":            nodeRun.Output,
			"thinking":          nodeRun.Thinking,
			"prompt_tokens":     nodeRun.PromptTokens,
			"completion_tokens": nodeRun.CompletionTokens,
			"total_tokens":      nodeRun.TotalTokens,
			"started_at":        nodeRun.StartedAt,
			"finished_at":       nodeRun.FinishedAt,
		}
		if err := b.ds.Sop().UpdateNodeRun(existingNodeRun.ID, updates); err != nil {
			return nil, fmt.Errorf("failed to update node run: %w", err)
		}
	} else {
		if err := b.ds.Sop().CreateNodeRun(nodeRun); err != nil {
			return nil, fmt.Errorf("failed to create node run: %w", err)
		}
	}

	log.C(ctx).Infow("Applied bookmark to node", "bookmark_id", bookmark.ID, "node_id", nodeID, "run_id", runID)
	return nodeRun, nil
}

// CreateRunWithBookmarks 创建Run并自动应用书签
func (b *sopBiz) CreateRunWithBookmarks(ctx context.Context, templateID, userID uint, text string, autoApplyBookmarks bool) (*model.SopRun, []uint, error) {
	// 1. 先创建普通的Run（包含权限检查）
	run, err := b.CreateRun(ctx, templateID, userID, text)
	if err != nil {
		return nil, nil, err
	}

	// 2. 将状态改为 draft（不计入配额和历史记录）
	run.Status = model.SopStatusDraft
	if err := b.ds.Sop().UpdateRun(run.ID, map[string]interface{}{"status": model.SopStatusDraft}); err != nil {
		log.C(ctx).Errorw("Failed to update run status to draft", "run_id", run.ID, "error", err)
		// 不阻断流程，继续执行
	}
	log.C(ctx).Infow("Created draft run", "run_id", run.ID, "template_id", templateID, "user_id", userID)

	appliedBookmarkIDs := []uint{}

	// 2. 如果启用自动应用书签
	if autoApplyBookmarks {
		// 获取该用户在该模板下的所有书签
		bookmarks, err := b.ds.Sop().ListBookmarksByUserAndTemplate(userID, templateID)
		if err != nil {
			log.C(ctx).Errorw("Failed to get bookmarks", "error", err)
			// 不影响Run创建，继续返回
			return run, appliedBookmarkIDs, nil
		}

		// 为每个有书签的节点应用书签
		for _, bookmark := range bookmarks {
			nodeRun, err := b.ApplyBookmarkToNode(ctx, userID, run.ID, bookmark.NodeID, &bookmark.ID)
			if err != nil {
				log.C(ctx).Errorw("Failed to apply bookmark", "bookmark_id", bookmark.ID, "error", err)
				// 继续处理其他书签
				continue
			}
			if nodeRun != nil {
				appliedBookmarkIDs = append(appliedBookmarkIDs, bookmark.ID)
			}
		}

		log.C(ctx).Infow("Auto-applied bookmarks", "run_id", run.ID, "count", len(appliedBookmarkIDs))
	}

	return run, appliedBookmarkIDs, nil
}

// DeleteDraftRun 删除草稿状态的 run（用户离开页面时调用）
// 只能删除 status="draft" 的 run，防止误删除正在运行的记录
func (b *sopBiz) DeleteDraftRun(ctx context.Context, runID, userID uint) error {
	// 1. 获取 run 信息
	run, err := b.ds.Sop().GetRun(runID)
	if err != nil {
		return fmt.Errorf("failed to get run: %w", err)
	}

	// 2. 权限检查
	if run.UserID != userID {
		return errors.New("无权限删除该记录")
	}

	// 3. 状态检查：只能删除 draft 状态的 run
	if run.Status != model.SopStatusDraft {
		return fmt.Errorf("只能删除草稿状态的记录，当前状态: %s", run.Status)
	}

	// 4. 删除关联的 node_run 记录（如果有书签被应用）
	if err := b.ds.Sop().DeleteNodeRunsByRunID(runID); err != nil {
		log.C(ctx).Warnw("Failed to delete node runs", "run_id", runID, "error", err)
		// 不阻断删除流程
	}

	// 5. 删除 run 记录
	if err := b.ds.Sop().DeleteRun(runID); err != nil {
		return fmt.Errorf("failed to delete run: %w", err)
	}

	log.C(ctx).Infow("Draft run deleted", "run_id", runID, "user_id", userID)
	return nil
}

// ----------------------------------------------------------------------------
// Phase 2 Task 2.1 helpers — credits Reserve/Reconcile 控制流
// ----------------------------------------------------------------------------

// computeSopPromptChars 统计 SOP 节点执行时送入 LLM 的 prompt 大致字符数。
// 用途：喂给 credit.CheckAndEstimate 的 EstimationInput.PromptChars 做 R2 预估。
// 口径：模板系统提示 + 节点 prompt + 全部 history message + 当前输入。
// 不追求逐 token 精确（safety_buffer_pct 覆盖），只要稳定即可。
func computeSopPromptChars(template *model.SopTemplate, node *model.SopNode,
	history []LLMMessage, currentInput string,
) int {
	chars := 0
	if template != nil {
		chars += utf8.RuneCountInString(template.Prompt)
	}
	if node != nil {
		chars += utf8.RuneCountInString(node.Prompt)
	}
	for _, m := range history {
		chars += utf8.RuneCountInString(m.Content)
	}
	chars += utf8.RuneCountInString(currentInput)
	return chars
}

// computeSopChatPromptChars 统计 sop_chat 追问送入 LLM 的 prompt 字符数。
// history 此时已包含系统/用户消息拼接 + 当前 question，直接求和即可。
func computeSopChatPromptChars(history []LLMMessage) int {
	chars := 0
	for _, m := range history {
		chars += utf8.RuneCountInString(m.Content)
	}
	return chars
}

// wrapCreditError 将 credit 层返回的业务错误翻译为 errno 可承载的 HTTP 响应。
// 若 pre.Reason 非空（credit 层拒绝原因），则将其作为消息回传给前端（spec §3.6）。
func wrapCreditError(err error, pre *credit.PreCheckResult) error {
	if err == nil {
		return nil
	}
	if pre != nil && pre.Reason != "" && errors.Is(err, credit.ErrInsufficientCredits) {
		return errno.ErrInsufficientCredits.SetMessage("%s", pre.Reason)
	}
	if errors.Is(err, credit.ErrInsufficientCredits) {
		return errno.ErrInsufficientCredits
	}
	return err
}

// deductCreditsForSop was the Phase 1 fire-and-forget credit deduction helper.
// It has been removed in Phase 2 Task 2.1 — runNode (ExecuteNodeStream) and
// runChat (ChatAfterRunStream) now drive deduction synchronously via
// ICreditService.Reserve + FinalizeReservation (Reconcile or Refund) using
// actual pricing.CalculateCost output. See spec §3.2 / §3.3 for the new flow.
//
// Keeping this comment intentionally (no code) as a grep-breadcrumb so anyone
// searching for the old helper lands on the replacement rationale.

// cleanPDFFormatCode 清理PDF格式代码等无效内容
// 如果检测到内容是PDF格式代码（如FilterFlateDecode、stream、endstream等），返回空字符串
func cleanPDFFormatCode(content string) string {
	if content == "" {
		return ""
	}

	// 检测PDF格式代码的关键词
	pdfKeywords := []string{
		"FilterFlateDecode",
		"stream",
		"endstream",
		"endobj",
		"obj",
		"Length",
		"PDF-",
		"BT",
		"ET",
		"Tj",
		"TJ",
	}

	// 检查内容中是否包含多个PDF格式关键词
	keywordCount := 0
	contentLower := strings.ToLower(content)
	for _, keyword := range pdfKeywords {
		if strings.Contains(contentLower, strings.ToLower(keyword)) {
			keywordCount++
		}
	}

	// 如果包含3个或以上的PDF关键词，且内容看起来像PDF格式代码，则认为是无效内容
	if keywordCount >= 3 {
		// 进一步检查：如果内容中PDF格式代码的比例很高，则认为是无效内容
		// 检查是否包含大量PDF对象标记（如数字+空格+数字+空格+obj）
		objPattern := `\d+\s+\d+\s+obj`
		if matched, _ := regexp.MatchString(objPattern, content); matched {
			// 检查内容长度，如果很长且包含大量PDF格式代码，则认为是PDF原始内容
			if len(content) > 500 && keywordCount >= 5 {
				log.C(context.Background()).Warnw("检测到PDF格式代码，已过滤", "content_length", len(content), "keyword_count", keywordCount)
				return "" // 返回空字符串，表示内容无效
			}
		}
	}

	// 如果内容看起来正常，返回原内容
	return content
}

// ==================== B-end SOP Config Methods ====================

// CreateTemplateByUserReq B端用户创建SOP模板的请求参数
type CreateTemplateByUserReq struct {
	Name                string `json:"name" binding:"required"`
	Description         string `json:"description"`
	TrailingChatEnabled *bool  `json:"trailing_chat_enabled"` // 可选；不传则默认 true 保持向后兼容
}

// CreateTemplateByUser B端用户创建SOP模板
func (b *sopBiz) CreateTemplateByUser(ctx context.Context, userID uint, req *CreateTemplateByUserReq) (*model.SopTemplate, error) {
	// 未显式传入时默认开启，保持与历史行为一致
	trailingChat := true
	if req.TrailingChatEnabled != nil {
		trailingChat = *req.TrailingChatEnabled
	}

	template := &model.SopTemplate{
		Name:                req.Name,
		Description:         req.Description,
		CreatorUserID:       &userID,
		PublishStatus:       model.SopPublishStatusDraft,
		Status:              "active",
		TrailingChatEnabled: trailingChat,
	}

	if err := b.ds.Sop().CreateTemplate(template); err != nil {
		return nil, fmt.Errorf("CreateTemplateByUser: %w", err)
	}
	// GORM v2 skips bool zero value (false) when the field has a `default:true` tag
	// (model.SopTemplate.TrailingChatEnabled). If the user explicitly asked for
	// trailing_chat_enabled=false, GORM silently falls back to the DB default of true.
	// A follow-up UpdateColumn restores the requested value.
	if !trailingChat && template.TrailingChatEnabled {
		if err := b.ds.DB().WithContext(ctx).Model(template).UpdateColumn("trailing_chat_enabled", false).Error; err != nil {
			return nil, fmt.Errorf("CreateTemplateByUser: fixup trailing_chat_enabled: %w", err)
		}
		template.TrailingChatEnabled = false
	}

	log.C(ctx).Infow("B-end user created SOP template", "user_id", userID, "template_id", template.ID, "name", req.Name)
	return template, nil
}

// ListTemplatesByCreator 列出B端用户创建的SOP模板
func (b *sopBiz) ListTemplatesByCreator(ctx context.Context, creatorID uint, offset, limit int) ([]model.SopTemplate, int64, error) {
	return b.ds.Sop().ListTemplatesByCreator(ctx, creatorID, offset, limit)
}

// PublishTemplate 发布B端用户创建的SOP模板，并自动授权给所有子用户
func (b *sopBiz) PublishTemplate(ctx context.Context, userID uint, templateID uint) error {
	template, err := b.ds.Sop().GetTemplate(templateID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("template not found")
		}
		return fmt.Errorf("PublishTemplate: get template: %w", err)
	}

	// 验证模板归属：必须是B端创建且属于当前用户
	if template.CreatorUserID == nil || *template.CreatorUserID != userID {
		return fmt.Errorf("您没有权限操作此模板")
	}

	// 更新发布状态
	if err := b.ds.Sop().UpdateTemplate(templateID, map[string]interface{}{
		"publish_status": model.SopPublishStatusPublished,
	}); err != nil {
		return fmt.Errorf("PublishTemplate: update status: %w", err)
	}

	// 自动授权给所有子用户
	if err := b.ds.Customers().GrantTemplateToAllSubUsers(ctx, userID, templateID); err != nil {
		log.C(ctx).Errorw("PublishTemplate: grant to sub users failed (non-blocking)", "user_id", userID, "template_id", templateID, "err", err)
		// 授权失败不阻塞发布，但记录错误
	}

	log.C(ctx).Infow("B-end template published", "user_id", userID, "template_id", templateID)
	return nil
}

// UnpublishTemplate 下线B端用户创建的SOP模板（不撤销已有授权）
func (b *sopBiz) UnpublishTemplate(ctx context.Context, userID uint, templateID uint) error {
	template, err := b.ds.Sop().GetTemplate(templateID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("template not found")
		}
		return fmt.Errorf("UnpublishTemplate: get template: %w", err)
	}

	// 验证模板归属
	if template.CreatorUserID == nil || *template.CreatorUserID != userID {
		return fmt.Errorf("您没有权限操作此模板")
	}

	// 回退到 draft 状态，不撤销已有授权（grants 表保留不动）
	if err := b.ds.Sop().UpdateTemplate(templateID, map[string]interface{}{
		"publish_status": model.SopPublishStatusDraft,
	}); err != nil {
		return fmt.Errorf("UnpublishTemplate: update status: %w", err)
	}

	log.C(ctx).Infow("B-end template unpublished", "user_id", userID, "template_id", templateID)
	return nil
}
