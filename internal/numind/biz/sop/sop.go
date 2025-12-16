package sop

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ISopBiz SOP业务逻辑接口
type ISopBiz interface {
	// Template operations
	CreateTemplate(ctx context.Context, name, description string) (*model.SopTemplate, error)
	GetTemplate(ctx context.Context, id uint) (*model.SopTemplate, error)
	ListTemplates(ctx context.Context, offset, limit int) ([]model.SopTemplate, int64, error)
	UpdateTemplate(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteTemplate(ctx context.Context, id uint) error

	// Node operations
	CreateNode(ctx context.Context, node *model.SopNode) (*model.SopNode, error)
	GetNode(ctx context.Context, id uint) (*model.SopNode, error)
	ListNodesByTemplate(ctx context.Context, templateID uint) ([]model.SopNode, error)
	UpdateNode(ctx context.Context, id uint, updates map[string]interface{}) error
	DeleteNode(ctx context.Context, id uint) error

	// Execution operations
	ExecuteTemplate(ctx context.Context, templateID, userID uint, initialInput string) (*model.SopRun, error)
	GetRun(ctx context.Context, id uint) (*model.SopRun, error)
	ListRuns(ctx context.Context, offset, limit int, userID *uint) ([]model.SopRun, int64, error)
	GetRunWithNodes(ctx context.Context, runID uint) (*model.SopRun, []model.SopNodeRun, error)

	// Note operations
	GetNote(ctx context.Context, id uint) (*model.SopNote, error)
	ListNotesByUser(ctx context.Context, userID uint, offset, limit int) ([]model.SopNote, int64, error)
}

type sopBiz struct {
	ds       store.IStore
	executor *SopExecutor
}

// NewSopBiz 创建SOP业务逻辑实例
func NewSopBiz(ds store.IStore, executor *SopExecutor) ISopBiz {
	return &sopBiz{
		ds:       ds,
		executor: executor,
	}
}

// Template operations
func (b *sopBiz) CreateTemplate(ctx context.Context, name, description string) (*model.SopTemplate, error) {
	template := &model.SopTemplate{
		Name:        name,
		Description: description,
		Status:      model.SopNodeStatusActive,
	}

	if err := b.ds.Sop().CreateTemplate(template); err != nil {
		return nil, fmt.Errorf("failed to create template: %w", err)
	}

	return template, nil
}

func (b *sopBiz) GetTemplate(ctx context.Context, id uint) (*model.SopTemplate, error) {
	return b.ds.Sop().GetTemplate(id)
}

func (b *sopBiz) ListTemplates(ctx context.Context, offset, limit int) ([]model.SopTemplate, int64, error) {
	return b.ds.Sop().ListTemplates(offset, limit)
}

func (b *sopBiz) UpdateTemplate(ctx context.Context, id uint, updates map[string]interface{}) error {
	return b.ds.Sop().UpdateTemplate(id, updates)
}

func (b *sopBiz) DeleteTemplate(ctx context.Context, id uint) error {
	return b.ds.Sop().DeleteTemplate(id)
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

	// 如果没有父节点，设置为根节点
	if node.ParentID == nil {
		node.IsRoot = true
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

// Execution operations
func (b *sopBiz) ExecuteTemplate(ctx context.Context, templateID, userID uint, initialInput string) (*model.SopRun, error) {
	// 验证模板是否存在
	_, err := b.ds.Sop().GetTemplate(templateID)
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}

	// 获取模板的所有节点
	nodes, err := b.ds.Sop().ListNodesByTemplate(templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template nodes: %w", err)
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("template has no nodes")
	}

	// 生成唯一的conversation_id
	conversationID := fmt.Sprintf("sop_%d_%d_%d", templateID, userID, time.Now().Unix())

	// 创建Run记录
	run := &model.SopRun{
		TemplateID:     templateID,
		UserID:         userID,
		Status:         model.SopStatusPending,
		ConversationID: conversationID,
	}

	if err := b.ds.Sop().CreateRun(run); err != nil {
		return nil, fmt.Errorf("failed to create run: %w", err)
	}

	// 异步执行SOP
	go func() {
		execCtx := context.Background()
		if err := b.executor.Execute(execCtx, run, nodes, initialInput); err != nil {
			log.C(execCtx).Errorw("SOP execution failed", "run_id", run.ID, "error", err)
		}
	}()

	return run, nil
}

func (b *sopBiz) GetRun(ctx context.Context, id uint) (*model.SopRun, error) {
	return b.ds.Sop().GetRun(id)
}

func (b *sopBiz) ListRuns(ctx context.Context, offset, limit int, userID *uint) ([]model.SopRun, int64, error) {
	return b.ds.Sop().ListRuns(offset, limit, userID)
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
