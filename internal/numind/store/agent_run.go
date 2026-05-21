package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// IAgentRunStore 定义 agent_run 表的存取接口。
type IAgentRunStore interface {
	Create(ctx context.Context, run *model.AgentRun) error
	Get(ctx context.Context, id uint64) (*model.AgentRun, error)
	UpdateState(ctx context.Context, id uint64, status, stateReason string, endedAt *time.Time) error
	WriteTurn(ctx context.Context, id uint64, messages json.RawMessage) error // turn 级整体覆写
	ListBySession(ctx context.Context, sessionID string, offset, limit int) ([]model.AgentRun, int64, error)
	// UpdateTerminalMetadata 写入 terminal_metadata JSON 字段。
	// 用途：#12 BudgetGate.writeTerminalMetadata 写 budget_dimension 等元数据，
	// #13 compliance 后续追加 compliance_block_reason 等。
	// RowsAffected==0 时报错（认为 id 不存在）。
	UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error
	// SetCancellationRequested marks agent_run.cancellation_requested_at = NOW()
	// and writes terminal_metadata (merged with existing). Used by admin force-cancel (M-C3b).
	// RowsAffected==0 means the run was not found.
	SetCancellationRequested(ctx context.Context, id uint64, metadata datatypes.JSON) error
	// ListByParentUserIDAndStatus returns runs whose agent_definition.parent_user_id = parentUserID
	// and agent_run.status = status (M-C4a admin listing).
	// parentUserID=0 skips the parent filter (global cross-tenant view).
	ListByParentUserIDAndStatus(ctx context.Context, parentUserID uint, status string, offset, limit int) ([]model.AgentRun, int64, error)
}

type agentRunStore struct {
	db *gorm.DB
}

func newAgentRunStore(db *gorm.DB) IAgentRunStore {
	return &agentRunStore{db: db}
}

var _ IAgentRunStore = (*agentRunStore)(nil)

func (s *agentRunStore) Create(ctx context.Context, run *model.AgentRun) error {
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return fmt.Errorf("agentRunStore.Create: %w", err)
	}
	return nil
}

func (s *agentRunStore) Get(ctx context.Context, id uint64) (*model.AgentRun, error) {
	var run model.AgentRun
	if err := s.db.WithContext(ctx).First(&run, id).Error; err != nil {
		return nil, fmt.Errorf("agentRunStore.Get(id=%d): %w", id, err)
	}
	return &run, nil
}

func (s *agentRunStore) UpdateState(ctx context.Context, id uint64, status, stateReason string, endedAt *time.Time) error {
	updates := map[string]interface{}{
		"status":       status,
		"state_reason": stateReason,
	}
	if endedAt != nil {
		updates["ended_at"] = *endedAt
	}
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdateState(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.UpdateState: no row matched id=%d", id)
	}
	return nil
}

func (s *agentRunStore) WriteTurn(ctx context.Context, id uint64, messages json.RawMessage) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Update("messages", datatypes.JSON(messages))
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.WriteTurn(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.WriteTurn: no row matched id=%d", id)
	}
	return nil
}

// UpdateTerminalMetadata 写入 terminal_metadata JSON 字段。
// 用途：#12 BudgetGate.writeTerminalMetadata 写 budget_dimension 等元数据，
// #13 compliance 后续追加 compliance_block_reason 等。
// RowsAffected==0 时报错（认为 id 不存在）。
func (s *agentRunStore) UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", id).
		Update("terminal_metadata", metadata)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.UpdateTerminalMetadata(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.UpdateTerminalMetadata: no row matched id=%d", id)
	}
	return nil
}

// SetCancellationRequested sets cancellation_requested_at = NOW() and merges terminal_metadata.
func (s *agentRunStore) SetCancellationRequested(ctx context.Context, id uint64, metadata datatypes.JSON) error {
	updates := map[string]interface{}{
		"cancellation_requested_at": time.Now(),
	}
	if metadata != nil {
		updates["terminal_metadata"] = metadata
	}
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("agentRunStore.SetCancellationRequested(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentRunStore.SetCancellationRequested: no row matched id=%d", id)
	}
	return nil
}

// ListByParentUserIDAndStatus joins agent_run ⋈ agent_definition on agent_definition_id
// to filter by parent_user_id, then filters agent_run.status.
// parentUserID=0 skips the parent filter (cross-tenant view for super-admin).
func (s *agentRunStore) ListByParentUserIDAndStatus(ctx context.Context, parentUserID uint, status string, offset, limit int) ([]model.AgentRun, int64, error) {
	base := s.db.WithContext(ctx).Model(&model.AgentRun{})
	if parentUserID != 0 {
		// LEFT JOIN: historical runs with agent_definition_id=0 have no join row.
		// Only return runs with a matching agent_definition row for the given parent.
		base = base.
			Joins("JOIN agent_definition ON agent_definition.id = agent_run.agent_definition_id").
			Where("agent_definition.parent_user_id = ?", parentUserID)
	}
	if status != "" {
		base = base.Where("agent_run.status = ?", status)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("agentRunStore.ListByParentUserIDAndStatus.Count: %w", err)
	}

	var runs []model.AgentRun
	if limit <= 0 {
		limit = 20
	}
	if err := base.Offset(offset).Limit(limit).Order("agent_run.started_at DESC").Find(&runs).Error; err != nil {
		return nil, 0, fmt.Errorf("agentRunStore.ListByParentUserIDAndStatus.Find: %w", err)
	}
	return runs, total, nil
}

func (s *agentRunStore) ListBySession(ctx context.Context, sessionID string, offset, limit int) ([]model.AgentRun, int64, error) {
	var (
		runs  []model.AgentRun
		total int64
	)
	base := s.db.WithContext(ctx).Model(&model.AgentRun{}).Where("session_id = ?", sessionID)
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("agentRunStore.ListBySession.Count: %w", err)
	}
	if err := base.Offset(offset).Limit(limit).Order("started_at DESC").Find(&runs).Error; err != nil {
		return nil, 0, fmt.Errorf("agentRunStore.ListBySession.Find: %w", err)
	}
	return runs, total, nil
}
