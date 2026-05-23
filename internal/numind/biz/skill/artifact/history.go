package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// IHistoryStore 是 skill_history 表的数据访问接口（mock 友好）。
//
// 仅 append-only 写 + 按 skill_id / version 查；不提供 Delete（保留完整审计链）。
//
// 注意：本接口在 T03（service）阶段已落地，但 ListBySkill / GetByVersion 主要由
// T04（versioning.ListHistory / Restore）使用——T03 只用到 WriteSnapshot。
type IHistoryStore interface {
	// WriteSnapshot 在指定 tx 内写入一行 skill_history（caller 控制事务）。
	WriteSnapshot(ctx context.Context, tx *gorm.DB, h *model.SkillHistory) error

	// ListBySkill 按 skill_id 列举所有历史快照，按 version DESC。
	ListBySkill(ctx context.Context, skillID uint) ([]model.SkillHistory, error)

	// GetByVersion 按 (skill_id, version) 取单行快照；不存在返回 ErrSkillArtifactVersionNotFound。
	GetByVersion(ctx context.Context, skillID, version uint) (*model.SkillHistory, error)
}

// gormHistoryStore 是 IHistoryStore 的 GORM 实现。
type gormHistoryStore struct {
	db *gorm.DB
}

var _ IHistoryStore = (*gormHistoryStore)(nil)

// NewHistoryStore 构造默认 GORM 实现。
func NewHistoryStore(db *gorm.DB) IHistoryStore {
	return &gormHistoryStore{db: db}
}

// WriteSnapshot append-only 写入快照行。caller 必须传入活跃事务（Service.Create/Update 内已开）。
func (s *gormHistoryStore) WriteSnapshot(ctx context.Context, tx *gorm.DB, h *model.SkillHistory) error {
	if err := tx.WithContext(ctx).Create(h).Error; err != nil {
		return fmt.Errorf("SkillHistory store.WriteSnapshot: %w", err)
	}
	return nil
}

// ListBySkill 列举该 skill 所有历史，按 version DESC（最新版本在前）。
func (s *gormHistoryStore) ListBySkill(ctx context.Context, skillID uint) ([]model.SkillHistory, error) {
	var items []model.SkillHistory
	err := s.db.WithContext(ctx).
		Where("skill_id = ?", skillID).
		Order("version DESC").
		Find(&items).Error
	if err != nil {
		return nil, fmt.Errorf("SkillHistory store.ListBySkill: %w", err)
	}
	return items, nil
}

// GetByVersion 按 (skill_id, version) 取单行快照。不存在 → ErrSkillArtifactVersionNotFound。
func (s *gormHistoryStore) GetByVersion(ctx context.Context, skillID, version uint) (*model.SkillHistory, error) {
	var h model.SkillHistory
	err := s.db.WithContext(ctx).
		Where("skill_id = ? AND version = ?", skillID, version).
		First(&h).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSkillArtifactVersionNotFound
		}
		return nil, fmt.Errorf("SkillHistory store.GetByVersion: %w", err)
	}
	return &h, nil
}

// Versioning 包装版本管理逻辑（写快照 + 列历史 + 回滚）。
//
// T03 阶段引入 struct 和 writeSnapshotTx；ListHistory / Restore 等高级方法由 T04 添加到
// versioning.go。
type Versioning struct {
	skillStore   IStore
	historyStore IHistoryStore
	db           *gorm.DB
}

// NewVersioning 构造 Versioning。
func NewVersioning(skillStore IStore, historyStore IHistoryStore, db *gorm.DB) *Versioning {
	return &Versioning{
		skillStore:   skillStore,
		historyStore: historyStore,
		db:           db,
	}
}

// HistoryItem 是 ListHistory 返回的元素，含可读 diff_summary。
// T04 versioning.ListHistory 返回 []HistoryItem。
type HistoryItem struct {
	Version     uint      `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   uint      `json:"created_by"`
	DiffSummary string    `json:"diff_summary"`
}

// writeSnapshotTx 在指定事务里写入一份完整 skill 行快照（marshal 为 JSON）。
// 由 Service.Create / Service.Update / Versioning.Restore 调用。
// 包内可见但不导出（外部调用必须经 Service 入口以走完整事务）。
func (v *Versioning) writeSnapshotTx(ctx context.Context, tx *gorm.DB, sk *model.Skill, createdBy uint) error {
	snapshot, err := json.Marshal(sk)
	if err != nil {
		return fmt.Errorf("writeSnapshotTx marshal: %w", err)
	}
	h := &model.SkillHistory{
		SkillID:   sk.ID,
		Version:   sk.Version,
		Snapshot:  datatypes.JSON(snapshot),
		CreatedBy: createdBy,
	}
	return v.historyStore.WriteSnapshot(ctx, tx, h)
}
