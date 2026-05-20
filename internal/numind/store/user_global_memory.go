package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

// IUserGlobalMemoryStore 定义 user_global_memory（L2 长期记忆 Notepad）的存取接口。
type IUserGlobalMemoryStore interface {
	// Upsert 按 (user_id, key_name) ON DUPLICATE KEY UPDATE 写入或更新记忆条目。
	// Select("*") 强制 confidence=0.0 不被 GORM default:1.0 跳过（spec §3.5 P2-2 修复）。
	Upsert(ctx context.Context, m *model.UserGlobalMemory) error
	// GetByUserKey 按 (user_id, key_name) 精确查找；未找到返回 gorm.ErrRecordNotFound。
	GetByUserKey(ctx context.Context, userID uint, key string) (*model.UserGlobalMemory, error)
	// ListByUserKind 按 (user_id, kind) 列举；limit<=0 默认 50。
	ListByUserKind(ctx context.Context, userID uint, kind string, limit int) ([]model.UserGlobalMemory, error)
	// DeleteByUserKey 删除指定 (user_id, key_name) 的记忆条目。
	DeleteByUserKey(ctx context.Context, userID uint, key string) error
	// DeleteByUser 删除某 user 的全部 L2 记忆（Clear 用）。
	DeleteByUser(ctx context.Context, userID uint) error
}

type userGlobalMemoryStore struct {
	db *gorm.DB
}

var _ IUserGlobalMemoryStore = (*userGlobalMemoryStore)(nil)

// NewUserGlobalMemoryStore 构造 IUserGlobalMemoryStore 实例。
func NewUserGlobalMemoryStore(db *gorm.DB) IUserGlobalMemoryStore {
	return &userGlobalMemoryStore{db: db}
}

// Upsert 按 (user_id, key_name) ON CONFLICT UPDATE 写入或更新记忆条目。
// Select("*") 强制所有列（含 confidence=0.0）入 INSERT，绕过 GORM default:1.0 zero-value gotcha（spec §3.5）。
// 额外：Create 后检测 GORM 是否将 confidence 恢复为 DEFAULT 1.0（SQLite 路径），
// 若被覆盖则用 UpdateColumn fixup（database.md §6 同款模式）。
func (s *userGlobalMemoryStore) Upsert(ctx context.Context, m *model.UserGlobalMemory) error {
	wantConfidence := m.Confidence
	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "key_name"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "kind", "confidence", "source_type", "source_agent_definition_id", "updated_at"}),
		}).
		Select("*"). // 强制 confidence=0 不被 GORM 跳过（spec §3.5 P2-2）
		Create(m).Error; err != nil {
		return fmt.Errorf("userGlobalMemoryStore.Upsert: %w", err)
	}
	// SQLite gotcha: GORM default:1.0 may override confidence=0.0 even with Select("*").
	// Apply UpdateColumn fixup (same pattern as database.md §6 bool default:true).
	if wantConfidence == 0.0 && m.Confidence != 0.0 {
		if err := s.db.WithContext(ctx).Model(m).UpdateColumn("confidence", 0.0).Error; err != nil {
			return fmt.Errorf("userGlobalMemoryStore.Upsert confidence fixup: %w", err)
		}
		m.Confidence = 0.0
	}
	return nil
}

// GetByUserKey 按 (user_id, key_name) 精确查找。
func (s *userGlobalMemoryStore) GetByUserKey(ctx context.Context, userID uint, key string) (*model.UserGlobalMemory, error) {
	var m model.UserGlobalMemory
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND key_name = ?", userID, key).
		First(&m).Error; err != nil {
		return nil, fmt.Errorf("userGlobalMemoryStore.GetByUserKey(userID=%d, key=%q): %w", userID, key, err)
	}
	return &m, nil
}

// ListByUserKind 按 (user_id, kind) 列举；默认 limit 50，按 updated_at desc 排序。
func (s *userGlobalMemoryStore) ListByUserKind(ctx context.Context, userID uint, kind string, limit int) ([]model.UserGlobalMemory, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []model.UserGlobalMemory
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND kind = ?", userID, kind).
		Order("updated_at desc").
		Limit(limit).
		Find(&items).Error; err != nil {
		return nil, fmt.Errorf("userGlobalMemoryStore.ListByUserKind: %w", err)
	}
	return items, nil
}

// DeleteByUserKey 删除指定 (user_id, key_name) 的记忆条目。
func (s *userGlobalMemoryStore) DeleteByUserKey(ctx context.Context, userID uint, key string) error {
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND key_name = ?", userID, key).
		Delete(&model.UserGlobalMemory{}).Error; err != nil {
		return fmt.Errorf("userGlobalMemoryStore.DeleteByUserKey(userID=%d, key=%q): %w", userID, key, err)
	}
	return nil
}

// DeleteByUser 删除某 user 的全部 L2 记忆。
func (s *userGlobalMemoryStore) DeleteByUser(ctx context.Context, userID uint) error {
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Delete(&model.UserGlobalMemory{}).Error; err != nil {
		return fmt.Errorf("userGlobalMemoryStore.DeleteByUser(userID=%d): %w", userID, err)
	}
	return nil
}
