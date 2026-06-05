package store

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ISopStore SOP数据访问接口
type ISopStore interface {
	// Template CRUD
	CreateTemplate(template *model.SopTemplate) error
	GetTemplate(id uint) (*model.SopTemplate, error)
	ListTemplates(offset, limit int) ([]model.SopTemplate, int64, error)
	// ListVisibleTemplates 列出指定父账户租户下 C 端用户可见的模板.
	// 多租户隔离 (spec D1): WHERE creator_user_id = ownerParentUserID.
	// 防御性 (spec D7): 列表永不返回 creator_user_id IS NULL 行.
	ListVisibleTemplates(ctx context.Context, ownerParentUserID uint, offset, limit int) ([]model.SopTemplate, int64, error)
	// CountVisibleTemplates 返回指定父账户租户下 C 端可见模板数（与 ListVisibleTemplates
	// 同 WHERE，仅 COUNT，不拉行）。供需要"数量"而非"列表"的调用方（如客户列表父账户行计数）。
	CountVisibleTemplates(ctx context.Context, ownerParentUserID uint) (int64, error)
	UpdateTemplate(id uint, updates map[string]interface{}) error
	DeleteTemplate(id uint) error

	// Node CRUD
	CreateNode(node *model.SopNode) error
	GetNode(id uint) (*model.SopNode, error)
	ListNodesByTemplate(templateID uint) ([]model.SopNode, error)
	UpdateNode(id uint, updates map[string]interface{}) error
	DeleteNode(id uint) error

	// Run operations
	CreateRun(run *model.SopRun) error
	GetRun(id uint) (*model.SopRun, error)
	UpdateRun(id uint, updates map[string]interface{}) error
	ListRuns(offset, limit int, userID *uint) ([]model.SopRun, int64, error)
	ListRunsByUserAndTemplate(userID, templateID uint, offset, limit int) ([]model.SopRun, int64, error)
	ListExecutedTemplatesByUser(userID uint) ([]ExecutedTemplateInfo, error)

	// NodeRun operations
	CreateNodeRun(nodeRun *model.SopNodeRun) error
	GetNodeRun(id uint) (*model.SopNodeRun, error)
	GetNodeRunByRunAndNode(runID, nodeID uint) (*model.SopNodeRun, error) // 根据runID和nodeID获取最新的NodeRun（用于检查是否已存在，支持重复执行）
	ListNodeRunsByRun(runID uint) ([]model.SopNodeRun, error)
	ListNodeRunsByRuns(runIDs []uint) (map[uint][]model.SopNodeRun, error)
	UpdateNodeRun(id uint, updates map[string]interface{}) error
	DeleteNodeRunsByRunID(runID uint) error

	// Note operations
	CreateNote(note *model.SopNote) error
	GetNote(id uint) (*model.SopNote, error)
	ListNotesByUser(userID uint, offset, limit int) ([]model.SopNote, int64, error)

	// File operations
	CreateFile(file *model.SopFile) error
	GetFile(id uint) (*model.SopFile, error)
	ListFilesByRun(runID uint) ([]model.SopFile, error)
	ListFilesByUser(userID uint, offset, limit int) ([]model.SopFile, int64, error)
	ListFilesByNodeRuns(nodeIDs []uint) (map[uint][]model.SopFile, error)
	UpdateFile(id uint, updates map[string]interface{}) error
	DeleteFile(id uint) error

	// Chat operations
	CreateChatMessage(msg *model.SopChatMsg) error
	GetChatMessage(id uint) (*model.SopChatMsg, error)
	DeleteChatMessage(id uint) error
	ListChatMessagesByRun(runID uint) ([]model.SopChatMsg, error)
	ListChatMessagesByRuns(runIDs []uint) (map[uint][]model.SopChatMsg, error)

	// Execution context (optimized batch query)
	GetExecutionContext(runID, nodeID uint) (*ExecutionContext, error)

	// CheckRunOwnership 检查Run是否属于指定用户（轻量级权限验证）
	CheckRunOwnership(runID, userID uint) (bool, error)

	// ResetZombieRuns 重置长时间处于运行中状态的"僵尸任务"
	ResetZombieRuns(timeout time.Duration) (int64, error)

	// Bookmark operations
	CreateBookmark(bookmark *model.SopNodeBookmark) error
	GetBookmark(id uint) (*model.SopNodeBookmark, error)
	GetBookmarkByUserTemplateNode(userID, templateID, nodeID uint) (*model.SopNodeBookmark, error)
	ListBookmarksByUserAndTemplate(userID, templateID uint) ([]model.SopNodeBookmark, error)
	UpdateBookmark(id uint, updates map[string]interface{}) error
	DeleteBookmark(id uint) error

	// B-end template operations
	ListTemplatesByCreator(ctx context.Context, creatorID uint, offset, limit int) ([]model.SopTemplate, int64, error)
	CountNodesByTemplate(ctx context.Context, templateID uint) (int64, error)

	// Cleanup operations
	DeleteRun(runID uint) error
	DeleteRuns(runIDs []uint) error
	DeleteNodeRunsByRun(runID uint) error
	DeleteFilesByRun(runID uint) error
	// CleanupDownstreamForRegeneration atomically removes stale downstream data
	// when a mid-flow node is regenerated (see method doc).
	CleanupDownstreamForRegeneration(runID uint, afterSort int) error
}

type sopStore struct {
	db *gorm.DB
}

// NewSopStore 创建SOP Store实例
func NewSopStore(db *gorm.DB) ISopStore {
	return &sopStore{db: db}
}

// Template operations
func (s *sopStore) CreateTemplate(template *model.SopTemplate) error {
	return s.db.Create(template).Error
}

func (s *sopStore) GetTemplate(id uint) (*model.SopTemplate, error) {
	var template model.SopTemplate
	err := s.db.First(&template, id).Error
	if err != nil {
		return nil, err
	}
	return &template, nil
}

func (s *sopStore) ListTemplates(offset, limit int) ([]model.SopTemplate, int64, error) {
	var templates []model.SopTemplate
	var total int64

	if err := s.db.Model(&model.SopTemplate{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := s.db.Offset(offset).Limit(limit).Order("created_at DESC").Find(&templates).Error; err != nil {
		return nil, 0, err
	}

	return templates, total, nil
}

// ListVisibleTemplates 列出指定父账户租户下 C 端用户可见的模板.
// 多租户隔离 (spec D1): WHERE creator_user_id = ownerParentUserID.
// 防御性 (spec D7): 同时过滤 creator_user_id IS NOT NULL，避免历史 NULL 行泄露给任何租户.
// GORM 软删除语义（deleted_at IS NULL）由 Model(&SopTemplate{}) 自动附加.
func (s *sopStore) ListVisibleTemplates(ctx context.Context, ownerParentUserID uint, offset, limit int) ([]model.SopTemplate, int64, error) {
	var templates []model.SopTemplate
	var total int64

	query := s.db.WithContext(ctx).Model(&model.SopTemplate{}).
		Where("creator_user_id = ?", ownerParentUserID).
		Where("creator_user_id IS NOT NULL"). // 防御性 (spec D7)
		Where("status = ?", "active").
		Where("publish_status = ?", model.SopPublishStatusPublished)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&templates).Error; err != nil {
		return nil, 0, err
	}

	return templates, total, nil
}

// CountVisibleTemplates 见接口注释：与 ListVisibleTemplates 同 WHERE，仅 COUNT。
func (s *sopStore) CountVisibleTemplates(ctx context.Context, ownerParentUserID uint) (int64, error) {
	var total int64
	err := s.db.WithContext(ctx).Model(&model.SopTemplate{}).
		Where("creator_user_id = ?", ownerParentUserID).
		Where("creator_user_id IS NOT NULL"). // 防御性 (spec D7)
		Where("status = ?", "active").
		Where("publish_status = ?", model.SopPublishStatusPublished).
		Count(&total).Error
	return total, err
}

func (s *sopStore) UpdateTemplate(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopTemplate{}).Where("id = ?", id).Updates(updates).Error
}

func (s *sopStore) DeleteTemplate(id uint) error {
	return s.db.Delete(&model.SopTemplate{}, id).Error
}

// Node operations
func (s *sopStore) CreateNode(node *model.SopNode) error {
	return s.db.Create(node).Error
}

func (s *sopStore) GetNode(id uint) (*model.SopNode, error) {
	var node model.SopNode
	err := s.db.First(&node, id).Error
	if err != nil {
		return nil, err
	}
	return &node, nil
}

func (s *sopStore) ListNodesByTemplate(templateID uint) ([]model.SopNode, error) {
	var nodes []model.SopNode
	err := s.db.Where("template_id = ?", templateID).Order("sort ASC").Find(&nodes).Error
	return nodes, err
}

func (s *sopStore) UpdateNode(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopNode{}).Where("id = ?", id).Updates(updates).Error
}

func (s *sopStore) DeleteNode(id uint) error {
	return s.db.Delete(&model.SopNode{}, id).Error
}

// Run operations
func (s *sopStore) CreateRun(run *model.SopRun) error {
	return s.db.Create(run).Error
}

func (s *sopStore) GetRun(id uint) (*model.SopRun, error) {
	var run model.SopRun
	// 不预加载FinalNote，因为没有外键关联（避免循环依赖）
	err := s.db.Preload("Template").Preload("User").First(&run, id).Error
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *sopStore) UpdateRun(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopRun{}).Where("id = ?", id).Updates(updates).Error
}

func (s *sopStore) ListRuns(offset, limit int, userID *uint) ([]model.SopRun, int64, error) {
	var runs []model.SopRun
	var total int64

	query := s.db.Model(&model.SopRun{}).Where("status != ?", "draft")
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Preload("Template").Preload("User").
		Offset(offset).Limit(limit).Order("created_at DESC").Find(&runs).Error; err != nil {
		return nil, 0, err
	}

	return runs, total, nil
}

func (s *sopStore) ListRunsByUserAndTemplate(userID, templateID uint, offset, limit int) ([]model.SopRun, int64, error) {
	var runs []model.SopRun
	var total int64

	query := s.db.Model(&model.SopRun{}).
		Where("user_id = ? AND template_id = ? AND status != ?", userID, templateID, "draft")

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Preload("Template").Preload("User").
		Offset(offset).Limit(defaultLimit(limit)).Order("created_at DESC").
		Find(&runs).Error; err != nil {
		return nil, 0, err
	}

	return runs, total, nil
}

// NodeRun operations
func (s *sopStore) CreateNodeRun(nodeRun *model.SopNodeRun) error {
	return s.db.Create(nodeRun).Error
}

func (s *sopStore) GetNodeRun(id uint) (*model.SopNodeRun, error) {
	var nodeRun model.SopNodeRun
	err := s.db.Preload("Node").Preload("Template").First(&nodeRun, id).Error
	if err != nil {
		return nil, err
	}
	return &nodeRun, nil
}

func (s *sopStore) GetNodeRunByRunAndNode(runID, nodeID uint) (*model.SopNodeRun, error) {
	var nodeRun model.SopNodeRun
	// 获取最新的记录（按创建时间倒序取第一条）
	err := s.db.Where("run_id = ? AND node_id = ?", runID, nodeID).
		Preload("Node").
		Order("created_at DESC").
		First(&nodeRun).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // 返回nil表示不存在
		}
		return nil, err
	}
	return &nodeRun, nil
}

func (s *sopStore) ListNodeRunsByRun(runID uint) ([]model.SopNodeRun, error) {
	var nodeRuns []model.SopNodeRun
	err := s.db.Where("run_id = ?", runID).Preload("Node").Order("sort ASC").Find(&nodeRuns).Error
	return nodeRuns, err
}

func (s *sopStore) ListNodeRunsByRuns(runIDs []uint) (map[uint][]model.SopNodeRun, error) {
	if len(runIDs) == 0 {
		return make(map[uint][]model.SopNodeRun), nil
	}

	var nodeRuns []model.SopNodeRun
	err := s.db.Where("run_id IN ?", runIDs).
		Preload("Node").
		Order("run_id ASC, sort ASC").
		Find(&nodeRuns).Error
	if err != nil {
		return nil, err
	}

	// 按run_id分组
	result := make(map[uint][]model.SopNodeRun)
	for _, nodeRun := range nodeRuns {
		result[nodeRun.RunID] = append(result[nodeRun.RunID], nodeRun)
	}

	// 确保所有run_id都有对应的空数组
	for _, runID := range runIDs {
		if _, exists := result[runID]; !exists {
			result[runID] = []model.SopNodeRun{}
		}
	}

	return result, nil
}

func (s *sopStore) UpdateNodeRun(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopNodeRun{}).Where("id = ?", id).Updates(updates).Error
}

// Note operations
func (s *sopStore) CreateNote(note *model.SopNote) error {
	return s.db.Create(note).Error
}

func (s *sopStore) GetNote(id uint) (*model.SopNote, error) {
	var note model.SopNote
	err := s.db.Preload("Template").Preload("User").Preload("Run").First(&note, id).Error
	if err != nil {
		return nil, err
	}
	return &note, nil
}

func (s *sopStore) ListNotesByUser(userID uint, offset, limit int) ([]model.SopNote, int64, error) {
	var notes []model.SopNote
	var total int64

	query := s.db.Model(&model.SopNote{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Preload("Template").Offset(offset).Limit(limit).
		Order("created_at DESC").Find(&notes).Error; err != nil {
		return nil, 0, err
	}

	return notes, total, nil
}

// File operations
func (s *sopStore) CreateFile(file *model.SopFile) error {
	return s.db.Create(file).Error
}

func (s *sopStore) GetFile(id uint) (*model.SopFile, error) {
	var file model.SopFile
	err := s.db.Preload("User").Preload("Run").Preload("Node").First(&file, id).Error
	if err != nil {
		return nil, err
	}
	return &file, nil
}

func (s *sopStore) ListFilesByRun(runID uint) ([]model.SopFile, error) {
	var files []model.SopFile
	err := s.db.Where("run_id = ?", runID).Order("created_at DESC").Find(&files).Error
	return files, err
}

func (s *sopStore) ListFilesByUser(userID uint, offset, limit int) ([]model.SopFile, int64, error) {
	var files []model.SopFile
	var total int64

	query := s.db.Model(&model.SopFile{}).Where("user_id = ?", userID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Preload("Run").Preload("Node").
		Offset(offset).Limit(limit).Order("created_at DESC").Find(&files).Error; err != nil {
		return nil, 0, err
	}

	return files, total, nil
}

func (s *sopStore) ListFilesByNodeRuns(nodeIDs []uint) (map[uint][]model.SopFile, error) {
	if len(nodeIDs) == 0 {
		return make(map[uint][]model.SopFile), nil
	}

	var files []model.SopFile
	err := s.db.Where("node_id IN ?", nodeIDs).
		Order("node_id ASC, created_at ASC").
		Find(&files).Error
	if err != nil {
		return nil, err
	}

	// 按node_id分组（注意：这里node_id对应的是SopNode的ID，不是SopNodeRun的ID）
	// 但SopFile的node_id字段存储的是SopNode的ID
	result := make(map[uint][]model.SopFile)
	for _, file := range files {
		if file.NodeID != nil {
			result[*file.NodeID] = append(result[*file.NodeID], file)
		}
	}

	return result, nil
}

func (s *sopStore) UpdateFile(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopFile{}).Where("id = ?", id).Updates(updates).Error
}

func (s *sopStore) DeleteFile(id uint) error {
	return s.db.Delete(&model.SopFile{}, id).Error
}

// Chat operations
func (s *sopStore) CreateChatMessage(msg *model.SopChatMsg) error {
	return s.db.Create(msg).Error
}

func (s *sopStore) GetChatMessage(id uint) (*model.SopChatMsg, error) {
	var msg model.SopChatMsg
	err := s.db.First(&msg, id).Error
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

func (s *sopStore) DeleteChatMessage(id uint) error {
	return s.db.Delete(&model.SopChatMsg{}, id).Error
}

func (s *sopStore) ListChatMessagesByRun(runID uint) ([]model.SopChatMsg, error) {
	var msgs []model.SopChatMsg
	err := s.db.Where("run_id = ?", runID).Order("seq ASC, created_at ASC").Find(&msgs).Error
	return msgs, err
}

func (s *sopStore) ListChatMessagesByRuns(runIDs []uint) (map[uint][]model.SopChatMsg, error) {
	if len(runIDs) == 0 {
		return make(map[uint][]model.SopChatMsg), nil
	}

	var chatMsgs []model.SopChatMsg
	err := s.db.Where("run_id IN ?", runIDs).
		Order("run_id ASC, seq ASC, created_at ASC").
		Find(&chatMsgs).Error
	if err != nil {
		return nil, err
	}

	// 按run_id分组
	result := make(map[uint][]model.SopChatMsg)
	for _, msg := range chatMsgs {
		result[msg.RunID] = append(result[msg.RunID], msg)
	}

	// 确保所有run_id都有对应的空数组
	for _, runID := range runIDs {
		if _, exists := result[runID]; !exists {
			result[runID] = []model.SopChatMsg{}
		}
	}

	return result, nil
}

// ExecutedTemplateInfo 用户已执行的模板信息
type ExecutedTemplateInfo struct {
	TemplateID     uint   `json:"template_id"`
	TemplateName   string `json:"template_name"`
	RunCount       int64  `json:"run_count"`       // 执行次数
	ExecutedAt     string `json:"executed_at"`     // 执行时间
	RunID          uint   `json:"run_id"`          // Run ID
	RunStatus      string `json:"run_status"`      // 执行状态
	CompletedCount int    `json:"completed_count"` // 已完成节点数
	TotalNodes     int    `json:"total_nodes"`     // 总节点数
}

// ListExecutedTemplatesByUser 获取用户已执行的模板列表（按模板分组）
// 只返回状态为running、succeeded、failed的记录（排除pending和draft）
func (s *sopStore) ListExecutedTemplatesByUser(userID uint) ([]ExecutedTemplateInfo, error) {
	var results []ExecutedTemplateInfo

	// 简化查询：先查出当前用户的非 pending 和非 draft 的 sop_run 列表，再在内存中做聚合
	var runs []model.SopRun
	if err := s.db.
		Preload("Template").
		Where("user_id = ? AND status != ? AND status != ?", userID, "pending", "draft").
		Order("created_at DESC").
		Find(&runs).Error; err != nil {
		return nil, err
	}

	// 获取所有相关的 run IDs 和 template IDs
	runIDs := make([]uint, 0, len(runs))
	templateIDs := make([]uint, 0)
	templateMap := make(map[uint]bool)
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
		if !templateMap[run.TemplateID] {
			templateMap[run.TemplateID] = true
			templateIDs = append(templateIDs, run.TemplateID)
		}
	}

	// 批量获取所有模板的节点数量
	templateNodeCounts := make(map[uint]int)
	if len(templateIDs) > 0 {
		type tplCountResult struct {
			TemplateID uint
			Count      int
		}
		var tplCounts []tplCountResult
		s.db.Table("sop_node").
			Select("template_id, COUNT(*) as count").
			Where("template_id IN (?) AND deleted_at IS NULL", templateIDs).
			Group("template_id").
			Scan(&tplCounts)
		for _, c := range tplCounts {
			templateNodeCounts[c.TemplateID] = c.Count
		}
	}

	// 批量获取所有 run 的已完成节点数量
	runCompletedCounts := make(map[uint]int)
	if len(runIDs) > 0 {
		type runCountResult struct {
			RunID uint
			Count int
		}
		var runCounts []runCountResult
		s.db.Table("sop_node_run").
			Select("run_id, COUNT(*) as count").
			Where("run_id IN (?) AND status = ? AND deleted_at IS NULL", runIDs, "succeeded").
			Group("run_id").
			Scan(&runCounts)
		for _, c := range runCounts {
			runCompletedCounts[c.RunID] = c.Count
		}
	}

	// 不聚合，逐条返回（前端自行聚合）
	for _, run := range runs {
		info := ExecutedTemplateInfo{
			TemplateID:     run.TemplateID,
			TemplateName:   "",
			RunCount:       1,
			ExecutedAt:     run.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			RunID:          run.ID,
			RunStatus:      run.Status,
			CompletedCount: runCompletedCounts[run.ID],
			TotalNodes:     templateNodeCounts[run.TemplateID],
		}
		if run.Template != nil {
			info.TemplateName = run.Template.Name
		}
		// 如果 total_nodes 为 0，尝试使用默认值 4
		if info.TotalNodes == 0 {
			info.TotalNodes = 4
		}
		results = append(results, info)
	}

	return results, nil
}

// ExecutionContext 执行上下文，包含执行节点所需的所有数据
type ExecutionContext struct {
	Run             *model.SopRun
	Node            *model.SopNode
	Template        *model.SopTemplate
	AllNodes        []model.SopNode
	AllNodeRuns     []model.SopNodeRun
	ExistingNodeRun *model.SopNodeRun
}

// GetExecutionContext 一次性获取执行节点所需的所有数据（优化性能）
func (s *sopStore) GetExecutionContext(runID, nodeID uint) (*ExecutionContext, error) {
	ctx := &ExecutionContext{}

	// 1. 获取 Run（不预加载，后续单独查询 Template）
	var run model.SopRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return nil, fmt.Errorf("run not found: %w", err)
	}
	ctx.Run = &run

	// 2. 获取 Node（不预加载 Template）
	var node model.SopNode
	if err := s.db.First(&node, nodeID).Error; err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}
	ctx.Node = &node

	// 3. 验证节点属于该模板
	if node.TemplateID != run.TemplateID {
		return nil, fmt.Errorf("node does not belong to this template")
	}

	// 4. 获取 Template
	var template model.SopTemplate
	if err := s.db.First(&template, run.TemplateID).Error; err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}
	ctx.Template = &template

	// 5. 获取模板的所有节点
	var allNodes []model.SopNode
	if err := s.db.Where("template_id = ?", run.TemplateID).Order("sort ASC").Find(&allNodes).Error; err != nil {
		return nil, fmt.Errorf("failed to get template nodes: %w", err)
	}
	ctx.AllNodes = allNodes

	// 6. 获取所有 NodeRuns（不预加载 Node，因为 ExecuteNodeStream 不需要 Node 信息）
	var allNodeRuns []model.SopNodeRun
	if err := s.db.Where("run_id = ?", runID).Order("sort ASC").Find(&allNodeRuns).Error; err != nil {
		return nil, fmt.Errorf("failed to get node runs: %w", err)
	}
	ctx.AllNodeRuns = allNodeRuns

	// 7. 获取已存在的 NodeRun（如果存在，不预加载 Node）
	var existingNodeRun model.SopNodeRun
	err := s.db.Where("run_id = ? AND node_id = ?", runID, nodeID).
		Order("created_at DESC").
		First(&existingNodeRun).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			ctx.ExistingNodeRun = nil
		} else {
			return nil, fmt.Errorf("failed to check existing node run: %w", err)
		}
	} else {
		ctx.ExistingNodeRun = &existingNodeRun
	}

	return ctx, nil
}

// CheckRunOwnership 检查Run是否属于指定用户（轻量级权限验证，只查询user_id）
func (s *sopStore) CheckRunOwnership(runID, userID uint) (bool, error) {
	var count int64
	err := s.db.Model(&model.SopRun{}).
		Where("id = ? AND user_id = ?", runID, userID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ResetZombieRuns 重置长时间处于运行中状态的“僵尸任务”
func (s *sopStore) ResetZombieRuns(timeout time.Duration) (int64, error) {
	threshold := time.Now().Add(-timeout)
	result := s.db.Model(&model.SopRun{}).
		Where("status = ? AND updated_at < ?", model.SopStatusRunning, threshold).
		Updates(map[string]interface{}{
			"status":        model.SopStatusFailed,
			"error_message": "任务执行超时，已由系统自动重置",
			"finished_at":   time.Now(),
		})

	return result.RowsAffected, result.Error
}

// DeleteNodeRunsByRunID 删除指定 run 的所有 node_run 记录
func (s *sopStore) DeleteNodeRunsByRunID(runID uint) error {
	return s.db.Where("run_id = ?", runID).Delete(&model.SopNodeRun{}).Error
}

// DeleteRun 物理删除指定任务及其所有关联数据
func (s *sopStore) DeleteRun(runID uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 1. 删除节点运行记录
		if err := tx.Where("run_id = ?", runID).Delete(&model.SopNodeRun{}).Error; err != nil {
			return err
		}

		// 2. 删除笔记
		if err := tx.Where("run_id = ?", runID).Delete(&model.SopNote{}).Error; err != nil {
			return err
		}

		// 3. 删除文件关联（注意：这里只删除数据库记录，物理文件由COS清理策略处理）
		if err := tx.Where("run_id = ?", runID).Delete(&model.SopFile{}).Error; err != nil {
			return err
		}

		// 4. 删除对话消息
		if err := tx.Where("run_id = ?", runID).Delete(&model.SopChatMsg{}).Error; err != nil {
			return err
		}

		// 5. 最后删除 Run 主记录
		if err := tx.Delete(&model.SopRun{}, runID).Error; err != nil {
			return err
		}

		return nil
	})
}

// DeleteRuns 批量物理删除任务
func (s *sopStore) DeleteRuns(runIDs []uint) error {
	if len(runIDs) == 0 {
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 1. 批量删除节点运行记录
		if err := tx.Where("run_id IN ?", runIDs).Delete(&model.SopNodeRun{}).Error; err != nil {
			return err
		}
		// 2. 批量删除笔记
		if err := tx.Where("run_id IN ?", runIDs).Delete(&model.SopNote{}).Error; err != nil {
			return err
		}
		// 3. 批量删除文件关联
		if err := tx.Where("run_id IN ?", runIDs).Delete(&model.SopFile{}).Error; err != nil {
			return err
		}
		// 4. 批量删除对话消息
		if err := tx.Where("run_id IN ?", runIDs).Delete(&model.SopChatMsg{}).Error; err != nil {
			return err
		}
		// 5. 最后批量删除 Run 主记录
		if err := tx.Where("id IN ?", runIDs).Delete(&model.SopRun{}).Error; err != nil {
			return err
		}
		return nil
	})
}

// DeleteNodeRunsByRun 删除指定任务的所有节点运行记录
func (s *sopStore) DeleteNodeRunsByRun(runID uint) error {
	return s.db.Where("run_id = ?", runID).Delete(&model.SopNodeRun{}).Error
}

// DeleteFilesByRun 删除指定任务关联的所有文件记录
func (s *sopStore) DeleteFilesByRun(runID uint) error {
	return s.db.Where("run_id = ?", runID).Delete(&model.SopFile{}).Error
}

// CleanupDownstreamForRegeneration 在单个事务内删除：下游节点运行（sort > afterSort）、
// 该 run 的最终笔记、该 run 的对话消息。任一删除失败整体回滚（要么全成、要么全不动），
// 避免再生中间节点时三次独立删除有部分失败而残留过时下游数据（解决“时空推演矛盾”）。
func (s *sopStore) CleanupDownstreamForRegeneration(runID uint, afterSort int) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// 1. 删除后续节点的执行记录
		if err := tx.Where("run_id = ? AND sort > ?", runID, afterSort).Delete(&model.SopNodeRun{}).Error; err != nil {
			return fmt.Errorf("delete downstream node runs: %w", err)
		}
		// 2. 删除该任务关联的最终笔记
		if err := tx.Where("run_id = ?", runID).Delete(&model.SopNote{}).Error; err != nil {
			return fmt.Errorf("delete run notes: %w", err)
		}
		// 3. 删除该任务关联的对话消息（历史已变，追问需重来）
		if err := tx.Where("run_id = ?", runID).Delete(&model.SopChatMsg{}).Error; err != nil {
			return fmt.Errorf("delete run chat messages: %w", err)
		}
		return nil
	})
}

// Bookmark operations

// CreateBookmark 创建书签
func (s *sopStore) CreateBookmark(bookmark *model.SopNodeBookmark) error {
	return s.db.Create(bookmark).Error
}

// GetBookmark 根据ID获取书签
func (s *sopStore) GetBookmark(id uint) (*model.SopNodeBookmark, error) {
	var bookmark model.SopNodeBookmark
	err := s.db.Preload("Node").Preload("Template").First(&bookmark, id).Error
	if err != nil {
		return nil, err
	}
	return &bookmark, nil
}

// GetBookmarkByUserTemplateNode 根据用户ID、模板ID、节点ID获取书签
func (s *sopStore) GetBookmarkByUserTemplateNode(userID, templateID, nodeID uint) (*model.SopNodeBookmark, error) {
	var bookmark model.SopNodeBookmark
	err := s.db.Where("user_id = ? AND template_id = ? AND node_id = ?", userID, templateID, nodeID).
		Preload("Node").
		Preload("Template").
		First(&bookmark).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // 返回nil表示不存在
		}
		return nil, err
	}
	return &bookmark, nil
}

// ListBookmarksByUserAndTemplate 获取用户在指定模板下的所有书签
func (s *sopStore) ListBookmarksByUserAndTemplate(userID, templateID uint) ([]model.SopNodeBookmark, error) {
	var bookmarks []model.SopNodeBookmark
	err := s.db.Where("user_id = ? AND template_id = ?", userID, templateID).
		Preload("Node").
		Order("node_sort ASC").
		Find(&bookmarks).Error
	return bookmarks, err
}

// UpdateBookmark 更新书签
func (s *sopStore) UpdateBookmark(id uint, updates map[string]interface{}) error {
	return s.db.Model(&model.SopNodeBookmark{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteBookmark 删除书签
func (s *sopStore) DeleteBookmark(id uint) error {
	return s.db.Delete(&model.SopNodeBookmark{}, id).Error
}

// ListTemplatesByCreator 按创建者查询模板列表（B端SOP配置）
func (s *sopStore) ListTemplatesByCreator(_ context.Context, creatorID uint, offset, limit int) ([]model.SopTemplate, int64, error) {
	var templates []model.SopTemplate
	var total int64

	query := s.db.Model(&model.SopTemplate{}).Where("creator_user_id = ? AND deleted_at IS NULL", creatorID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(defaultLimit(limit)).Order("created_at DESC").Find(&templates).Error; err != nil {
		return nil, 0, err
	}

	return templates, total, nil
}

// CountNodesByTemplate 统计模板下的节点数量
func (s *sopStore) CountNodesByTemplate(_ context.Context, templateID uint) (int64, error) {
	var count int64
	err := s.db.Model(&model.SopNode{}).Where("template_id = ? AND deleted_at IS NULL", templateID).Count(&count).Error
	return count, err
}
