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
