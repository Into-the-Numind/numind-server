package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// IAgentDefinitionStore 定义 agent_definition 表及其版本历史的存取接口。
type IAgentDefinitionStore interface {
	// Create 创建新的 agent 定义。含 is_active=false 的 UpdateColumn fixup（database.md §6）。
	Create(ctx context.Context, m *model.AgentDefinition) error
	// CreateTx 在指定事务中创建 agent 定义。
	CreateTx(ctx context.Context, tx *gorm.DB, m *model.AgentDefinition) error
	// GetByID 按 ID 查询激活的 agent 定义（is_active=1）。
	GetByID(ctx context.Context, id uint64) (*model.AgentDefinition, error)
	// GetByIDIncludeInactive 按 ID 查询 agent 定义，不过滤 is_active（详情接口用）。
	GetByIDIncludeInactive(ctx context.Context, id uint64) (*model.AgentDefinition, error)
	// ListByParent 按 parent_user_id 列举 agent 定义，支持是否包含非激活项。
	ListByParent(ctx context.Context, parentUserID uint, includeInactive bool, offset, limit int) ([]model.AgentDefinition, int64, error)
	// Update 更新 agent 定义（用 db.Save 安全，database.md §6b）。
	Update(ctx context.Context, m *model.AgentDefinition) error
	// UpdateTx 在指定事务中更新 agent 定义。
	UpdateTx(ctx context.Context, tx *gorm.DB, m *model.AgentDefinition) error
	// SoftDelete 软删除（is_active=0）。
	SoftDelete(ctx context.Context, id uint64) error
	// SoftDeleteTx 在指定事务中软删除。
	SoftDeleteTx(ctx context.Context, tx *gorm.DB, id uint64) error
	// WriteHistory append-only 写入版本快照。
	WriteHistory(ctx context.Context, h *model.AgentDefinitionHistory) error
	// WriteHistoryTx 在指定事务中写入版本快照。
	WriteHistoryTx(ctx context.Context, tx *gorm.DB, h *model.AgentDefinitionHistory) error
	// ListHistory 列举指定 agent 的所有历史版本（按 version DESC），包含已软删除的 agent。
	ListHistory(ctx context.Context, agentID uint64) ([]model.AgentDefinitionHistory, error)
	// GetHistoryByVersion 按 (agent_id, version) 获取版本快照。
	GetHistoryByVersion(ctx context.Context, agentID uint64, version uint) (*model.AgentDefinitionHistory, error)
	// MaxVersion 返回指定 agent 当前最大版本号，若无历史记录返回 0。
	MaxVersion(ctx context.Context, agentID uint64) (uint, error)
}

type agentDefinitionStore struct {
	db *gorm.DB
}

var _ IAgentDefinitionStore = (*agentDefinitionStore)(nil)

func newAgentDefinitionStore(db *gorm.DB) IAgentDefinitionStore {
	return &agentDefinitionStore{db: db}
}

// Create 创建新的 agent 定义，含 is_active=false UpdateColumn fixup（database.md §6）。
func (s *agentDefinitionStore) Create(ctx context.Context, m *model.AgentDefinition) error {
	return s.CreateTx(ctx, s.db, m)
}

// CreateTx 在指定事务中创建 agent 定义。
func (s *agentDefinitionStore) CreateTx(ctx context.Context, tx *gorm.DB, m *model.AgentDefinition) error {
	wantActive := m.IsActive
	if err := tx.WithContext(ctx).Create(m).Error; err != nil {
		return fmt.Errorf("agentDefinitionStore.CreateTx: %w", err)
	}
	// database.md §6：is_active 带 default:1，GORM Create 会忽略显式 false。
	// 用 UpdateColumn 修正（不触发 hooks/updated_at）。
	if !wantActive && m.IsActive {
		if err := tx.WithContext(ctx).Model(m).UpdateColumn("is_active", false).Error; err != nil {
			return fmt.Errorf("agentDefinitionStore.CreateTx is_active fixup: %w", err)
		}
		m.IsActive = false
	}
	return nil
}

// GetByID 按 ID 查询激活的 agent 定义（is_active=1）。
func (s *agentDefinitionStore) GetByID(ctx context.Context, id uint64) (*model.AgentDefinition, error) {
	var m model.AgentDefinition
	if err := s.db.WithContext(ctx).
		Where("id = ? AND is_active = 1", id).
		First(&m).Error; err != nil {
		return nil, fmt.Errorf("agentDefinitionStore.GetByID(id=%d): %w", id, err)
	}
	return &m, nil
}

// GetByIDIncludeInactive 按 ID 查询，不过滤 is_active（详情/恢复场景用）。
func (s *agentDefinitionStore) GetByIDIncludeInactive(ctx context.Context, id uint64) (*model.AgentDefinition, error) {
	var m model.AgentDefinition
	if err := s.db.WithContext(ctx).First(&m, id).Error; err != nil {
		return nil, fmt.Errorf("agentDefinitionStore.GetByIDIncludeInactive(id=%d): %w", id, err)
	}
	return &m, nil
}

// ListByParent 按 parent_user_id 列举，支持 includeInactive 控制是否过滤 is_active。
func (s *agentDefinitionStore) ListByParent(ctx context.Context, parentUserID uint, includeInactive bool, offset, limit int) ([]model.AgentDefinition, int64, error) {
	var (
		items []model.AgentDefinition
		total int64
	)
	base := s.db.WithContext(ctx).Model(&model.AgentDefinition{}).Where("parent_user_id = ?", parentUserID)
	if !includeInactive {
		base = base.Where("is_active = 1")
	}
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("agentDefinitionStore.ListByParent.Count: %w", err)
	}
	if err := base.Offset(offset).Limit(limit).Order("created_at DESC").Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("agentDefinitionStore.ListByParent.Find: %w", err)
	}
	return items, total, nil
}

// Update 更新 agent 定义，用 db.Save（database.md §6b：Save 安全地写入 is_active=false）。
func (s *agentDefinitionStore) Update(ctx context.Context, m *model.AgentDefinition) error {
	return s.UpdateTx(ctx, s.db, m)
}

// UpdateTx 在指定事务中更新 agent 定义。
func (s *agentDefinitionStore) UpdateTx(ctx context.Context, tx *gorm.DB, m *model.AgentDefinition) error {
	if err := tx.WithContext(ctx).Save(m).Error; err != nil {
		return fmt.Errorf("agentDefinitionStore.UpdateTx: %w", err)
	}
	return nil
}

// SoftDelete 软删除（is_active=0）。
func (s *agentDefinitionStore) SoftDelete(ctx context.Context, id uint64) error {
	return s.SoftDeleteTx(ctx, s.db, id)
}

// SoftDeleteTx 在指定事务中软删除。
func (s *agentDefinitionStore) SoftDeleteTx(ctx context.Context, tx *gorm.DB, id uint64) error {
	result := tx.WithContext(ctx).Model(&model.AgentDefinition{}).
		Where("id = ?", id).
		UpdateColumn("is_active", false)
	if result.Error != nil {
		return fmt.Errorf("agentDefinitionStore.SoftDeleteTx(id=%d): %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return errno.ErrSkillNotFound.SetMessage("SoftDeleteTx: no row matched id=%d", id)
	}
	return nil
}

// WriteHistory append-only 写入版本快照。
func (s *agentDefinitionStore) WriteHistory(ctx context.Context, h *model.AgentDefinitionHistory) error {
	return s.WriteHistoryTx(ctx, s.db, h)
}

// WriteHistoryTx 在指定事务中写入版本快照。
func (s *agentDefinitionStore) WriteHistoryTx(ctx context.Context, tx *gorm.DB, h *model.AgentDefinitionHistory) error {
	if err := tx.WithContext(ctx).Create(h).Error; err != nil {
		return fmt.Errorf("agentDefinitionStore.WriteHistoryTx: %w", err)
	}
	return nil
}

// ListHistory 列举指定 agent 的所有历史版本（version DESC），不 JOIN agent_definition（含已软删除）。
func (s *agentDefinitionStore) ListHistory(ctx context.Context, agentID uint64) ([]model.AgentDefinitionHistory, error) {
	var histories []model.AgentDefinitionHistory
	if err := s.db.WithContext(ctx).
		Where("agent_id = ?", agentID).
		Order("version DESC").
		Find(&histories).Error; err != nil {
		return nil, fmt.Errorf("agentDefinitionStore.ListHistory(agentID=%d): %w", agentID, err)
	}
	return histories, nil
}

// GetHistoryByVersion 按 (agent_id, version) 获取特定版本快照。
func (s *agentDefinitionStore) GetHistoryByVersion(ctx context.Context, agentID uint64, version uint) (*model.AgentDefinitionHistory, error) {
	var h model.AgentDefinitionHistory
	if err := s.db.WithContext(ctx).
		Where("agent_id = ? AND version = ?", agentID, version).
		First(&h).Error; err != nil {
		return nil, fmt.Errorf("agentDefinitionStore.GetHistoryByVersion(agentID=%d, version=%d): %w", agentID, version, err)
	}
	return &h, nil
}

// MaxVersion 返回指定 agent 当前最大版本号；若无历史记录返回 0。
func (s *agentDefinitionStore) MaxVersion(ctx context.Context, agentID uint64) (uint, error) {
	var maxVer *uint
	if err := s.db.WithContext(ctx).
		Model(&model.AgentDefinitionHistory{}).
		Where("agent_id = ?", agentID).
		Select("MAX(version)").
		Scan(&maxVer).Error; err != nil {
		return 0, fmt.Errorf("agentDefinitionStore.MaxVersion(agentID=%d): %w", agentID, err)
	}
	if maxVer == nil {
		return 0, nil
	}
	return *maxVer, nil
}
