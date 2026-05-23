package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// IAgentAttachmentStore defines persistence operations for agent_attachment rows.
type IAgentAttachmentStore interface {
	// Create inserts a new attachment record and populates att.ID.
	Create(ctx context.Context, att *model.AgentAttachment) error

	// GetByID returns the attachment with the given primary key.
	// Returns gorm.ErrRecordNotFound if the row does not exist.
	GetByID(ctx context.Context, id uint64) (*model.AgentAttachment, error)

	// GetByIDAndUser returns the attachment only if it belongs to userID.
	// Returns gorm.ErrRecordNotFound if not found or if the owner does not match.
	GetByIDAndUser(ctx context.Context, id uint64, userID uint) (*model.AgentAttachment, error)

	// UpdateFallback writes fallback-related fields atomically.
	// Uses a map to avoid the GORM bool zero-value gotcha (database.md §6).
	UpdateFallback(ctx context.Context, id uint64, fields map[string]interface{}) error

	// ListPendingFallback returns attachment rows that need fallback generation:
	//   fallback_ready = false
	//   AND (fallback_started_at IS NULL OR fallback_started_at < staleThreshold)
	// Used by RecoverPending on startup to re-enqueue interrupted jobs.
	ListPendingFallback(ctx context.Context, staleThreshold time.Time, limit int) ([]model.AgentAttachment, error)
}

// agentAttachmentStore is the concrete GORM implementation.
type agentAttachmentStore struct {
	db *gorm.DB
}

// newAgentAttachmentStore constructs an IAgentAttachmentStore backed by db.
func newAgentAttachmentStore(db *gorm.DB) IAgentAttachmentStore {
	return &agentAttachmentStore{db: db}
}

// NewAgentAttachmentStoreForTest constructs an IAgentAttachmentStore for use
// in external test packages (bypasses the package-private constructor).
func NewAgentAttachmentStoreForTest(db *gorm.DB) IAgentAttachmentStore {
	return newAgentAttachmentStore(db)
}

var _ IAgentAttachmentStore = (*agentAttachmentStore)(nil)

// Create inserts att into agent_attachment and fills att.ID on success.
func (s *agentAttachmentStore) Create(ctx context.Context, att *model.AgentAttachment) error {
	if err := s.db.WithContext(ctx).Create(att).Error; err != nil {
		return fmt.Errorf("agentAttachmentStore.Create: %w", err)
	}
	return nil
}

// GetByID retrieves a single attachment by primary key.
func (s *agentAttachmentStore) GetByID(ctx context.Context, id uint64) (*model.AgentAttachment, error) {
	var att model.AgentAttachment
	if err := s.db.WithContext(ctx).First(&att, id).Error; err != nil {
		return nil, fmt.Errorf("agentAttachmentStore.GetByID id=%d: %w", id, err)
	}
	return &att, nil
}

// GetByIDAndUser retrieves an attachment and enforces ownership.
func (s *agentAttachmentStore) GetByIDAndUser(ctx context.Context, id uint64, userID uint) (*model.AgentAttachment, error) {
	var att model.AgentAttachment
	if err := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", id, userID).
		First(&att).Error; err != nil {
		return nil, fmt.Errorf("agentAttachmentStore.GetByIDAndUser id=%d user=%d: %w", id, userID, err)
	}
	return &att, nil
}

// UpdateFallback writes the provided fields to the attachment row.
// Callers must use the map form (not a struct) to avoid the GORM bool
// zero-value gotcha documented in .claude/rules/database.md §6.
func (s *agentAttachmentStore) UpdateFallback(ctx context.Context, id uint64, fields map[string]interface{}) error {
	result := s.db.WithContext(ctx).
		Model(&model.AgentAttachment{}).
		Where("id = ?", id).
		Updates(fields)
	if result.Error != nil {
		return fmt.Errorf("agentAttachmentStore.UpdateFallback id=%d: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentAttachmentStore.UpdateFallback id=%d: no rows updated", id)
	}
	return nil
}

// ListPendingFallback returns rows with fallback_ready=false and either no
// fallback_started_at or a stale one (older than staleThreshold).
func (s *agentAttachmentStore) ListPendingFallback(ctx context.Context, staleThreshold time.Time, limit int) ([]model.AgentAttachment, error) {
	var rows []model.AgentAttachment
	err := s.db.WithContext(ctx).
		Where("fallback_ready = ? AND (fallback_started_at IS NULL OR fallback_started_at < ?)",
			false, staleThreshold).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("agentAttachmentStore.ListPendingFallback: %w", err)
	}
	return rows, nil
}
