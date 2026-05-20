package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// IAgentSandboxSessionStore defines the persistence interface for the
// agent_sandbox_session audit table. PreToolCall writes a row with
// status='running'; PostToolCall updates status/exit_code/error_msg/ended_at.
type IAgentSandboxSessionStore interface {
	Create(ctx context.Context, sess *model.AgentSandboxSession) error
	UpdateState(ctx context.Context, id uint64, status string, exitCode *int, errMsg string, endedAt *time.Time) error
	GetByContainerID(ctx context.Context, containerID string) (*model.AgentSandboxSession, error)
	ListByUser(ctx context.Context, userID uint, limit int) ([]model.AgentSandboxSession, error)
}

type agentSandboxSessionStore struct {
	db *gorm.DB
}

func newAgentSandboxSessionStore(db *gorm.DB) IAgentSandboxSessionStore {
	return &agentSandboxSessionStore{db: db}
}

var _ IAgentSandboxSessionStore = (*agentSandboxSessionStore)(nil)

// Create inserts a new sandbox session row. Caller fills the
// (UserID, ContainerID, ImageTag, MemLimitMB, CPUQuota, StartedAt) fields
// — the rest default at the SQL layer.
func (s *agentSandboxSessionStore) Create(ctx context.Context, sess *model.AgentSandboxSession) error {
	if err := s.db.WithContext(ctx).Create(sess).Error; err != nil {
		return fmt.Errorf("agentSandboxSessionStore.Create: %w", err)
	}
	return nil
}

// UpdateState transitions the row to a terminal state (terminated|failed)
// and stamps exit_code / error_msg / ended_at. Uses map-form Updates to
// avoid GORM zero-value-skipping for empty errMsg.
func (s *agentSandboxSessionStore) UpdateState(
	ctx context.Context,
	id uint64,
	status string,
	exitCode *int,
	errMsg string,
	endedAt *time.Time,
) error {
	updates := map[string]interface{}{
		"status":    status,
		"error_msg": errMsg,
	}
	if exitCode != nil {
		updates["exit_code"] = *exitCode
	}
	if endedAt != nil {
		updates["ended_at"] = *endedAt
	}
	res := s.db.WithContext(ctx).
		Model(&model.AgentSandboxSession{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("agentSandboxSessionStore.UpdateState(id=%d): %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("agentSandboxSessionStore.UpdateState(id=%d): row not found", id)
	}
	return nil
}

// GetByContainerID returns the most recent session row for the given
// Docker container ID, or gorm.ErrRecordNotFound wrapped in fmt.Errorf.
func (s *agentSandboxSessionStore) GetByContainerID(ctx context.Context, containerID string) (*model.AgentSandboxSession, error) {
	var sess model.AgentSandboxSession
	err := s.db.WithContext(ctx).
		Where("container_id = ?", containerID).
		Order("started_at DESC").
		First(&sess).Error
	if err != nil {
		return nil, fmt.Errorf("agentSandboxSessionStore.GetByContainerID(%s): %w", containerID, err)
	}
	return &sess, nil
}

// ListByUser returns the N most recent sandbox sessions for the user
// (limit defaults to 100 if non-positive).
func (s *agentSandboxSessionStore) ListByUser(ctx context.Context, userID uint, limit int) ([]model.AgentSandboxSession, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []model.AgentSandboxSession
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("started_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("agentSandboxSessionStore.ListByUser(uid=%d): %w", userID, err)
	}
	return rows, nil
}
