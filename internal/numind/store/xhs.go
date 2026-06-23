package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// XhsNoteFilter 选题库列表的可选过滤条件。零值表示不过滤。
type XhsNoteFilter struct {
	NoteType     string // normal/video，空 = 不过滤
	EnrichStatus string // pending/enriching/done/partial/failed/insufficient_credits，空 = 不过滤
	Keyword      string // 标题/正文模糊匹配，空 = 不过滤
}

// IXhsStore 定义小红书选题库（xhs_topic_note）的数据库操作接口。
// 所有方法均带 user 隔离，确保多租户私有累积选题库互不可见。
type IXhsStore interface {
	// UpsertNote 按 uk_xtn_user_note(user_id, xhs_note_id) 唯一键 upsert 一条笔记。
	// 新建返回 hashChanged=true；命中已存在记录时比对 ContentHash：变化返回 true 并更新，
	// 未变化返回 false 且不覆盖已有富化结果。
	UpsertNote(ctx context.Context, n *model.XhsTopicNote) (hashChanged bool, err error)
	// ListNotes 分页查询某用户的选题库，支持 note_type/enrich_status/keyword 过滤。
	ListNotes(ctx context.Context, userID uint, filter XhsNoteFilter, offset, limit int) ([]model.XhsTopicNote, int64, error)
	// GetNote 按 (user_id, id) 获取单条笔记；不存在返回 errno.ErrXhsNoteNotFound。
	GetNote(ctx context.Context, userID uint, id uint64) (*model.XhsTopicNote, error)
	// DeleteNote 按 (user_id, id) 删除单条笔记。
	DeleteNote(ctx context.Context, userID uint, id uint64) error
	// UpdateEnrichStatus 仅更新某条笔记的富化状态（富化状态机流转用）。
	UpdateEnrichStatus(ctx context.Context, id uint64, status string) error
	// UpdateEnrichResult 写回 LLM 富化结果（6 分析字段 + 转写 + enrich_status）。
	UpdateEnrichResult(ctx context.Context, n *model.XhsTopicNote) error
	// GetByIDs 按 (user_id, ids) 批量获取笔记（所有权校验 / 批量富化用）。
	GetByIDs(ctx context.Context, userID uint, ids []uint64) ([]model.XhsTopicNote, error)
}

type xhsStore struct {
	db *gorm.DB
}

var _ IXhsStore = (*xhsStore)(nil)

// NewXhsStore 创建一个 IXhsStore 实例。
func NewXhsStore(db *gorm.DB) IXhsStore {
	return &xhsStore{db: db}
}

// UpsertNote 按 uk_xtn_user_note 唯一键 upsert，并返回内容是否变化。
func (s *xhsStore) UpsertNote(ctx context.Context, n *model.XhsTopicNote) (bool, error) {
	hashChanged := true
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.XhsTopicNote
		err := tx.Where("user_id = ? AND xhs_note_id = ?", n.UserID, n.XhsNoteID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 新建：内容视为变化。
			if cErr := tx.Create(n).Error; cErr != nil {
				return fmt.Errorf("create: %w", cErr)
			}
			hashChanged = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("lookup: %w", err)
		}

		// 命中已存在记录，比对 content_hash 判定内容是否变化。
		if existing.ContentHash == n.ContentHash {
			hashChanged = false
			// 内容未变化：保留已有记录（含富化结果），把主键回填到入参便于调用方使用。
			n.ID = existing.ID
			return nil
		}

		// 内容变化：更新采集字段，复用已存在记录主键。
		hashChanged = true
		n.ID = existing.ID
		if uErr := tx.Save(n).Error; uErr != nil {
			return fmt.Errorf("update: %w", uErr)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("UpsertNote: %w", err)
	}
	return hashChanged, nil
}

// ListNotes 分页查询某用户的选题库列表。
func (s *xhsStore) ListNotes(ctx context.Context, userID uint, filter XhsNoteFilter, offset, limit int) ([]model.XhsTopicNote, int64, error) {
	var list []model.XhsTopicNote
	var total int64

	query := s.db.WithContext(ctx).Model(&model.XhsTopicNote{}).Where("user_id = ?", userID)

	if filter.NoteType != "" {
		query = query.Where("note_type = ?", filter.NoteType)
	}
	if filter.EnrichStatus != "" {
		query = query.Where("enrich_status = ?", filter.EnrichStatus)
	}
	if filter.Keyword != "" {
		like := "%" + strings.TrimSpace(filter.Keyword) + "%"
		query = query.Where("title LIKE ? OR content LIKE ?", like, like)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("ListNotes count: %w", err)
	}

	if err := query.Order("crawled_at DESC").Offset(offset).Limit(limit).Find(&list).Error; err != nil {
		return nil, 0, fmt.Errorf("ListNotes find: %w", err)
	}

	return list, total, nil
}

// GetNote 按 (user_id, id) 获取单条笔记。
func (s *xhsStore) GetNote(ctx context.Context, userID uint, id uint64) (*model.XhsTopicNote, error) {
	var note model.XhsTopicNote
	if err := s.db.WithContext(ctx).Where("user_id = ? AND id = ?", userID, id).First(&note).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrXhsNoteNotFound
		}
		return nil, fmt.Errorf("GetNote: %w", err)
	}
	return &note, nil
}

// DeleteNote 按 (user_id, id) 删除单条笔记。
func (s *xhsStore) DeleteNote(ctx context.Context, userID uint, id uint64) error {
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND id = ?", userID, id).
		Delete(&model.XhsTopicNote{}).Error; err != nil {
		return fmt.Errorf("DeleteNote: %w", err)
	}
	return nil
}

// UpdateEnrichStatus 仅更新某条笔记的富化状态。
func (s *xhsStore) UpdateEnrichStatus(ctx context.Context, id uint64, status string) error {
	if err := s.db.WithContext(ctx).
		Model(&model.XhsTopicNote{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"enrich_status": status,
			"updated_at":    time.Now(),
		}).Error; err != nil {
		return fmt.Errorf("UpdateEnrichStatus: %w", err)
	}
	return nil
}

// UpdateEnrichResult 写回 LLM 富化结果（6 分析字段 + 转写 + enrich_status）。
func (s *xhsStore) UpdateEnrichResult(ctx context.Context, n *model.XhsTopicNote) error {
	if err := s.db.WithContext(ctx).
		Model(&model.XhsTopicNote{}).
		Where("id = ?", n.ID).
		Updates(map[string]interface{}{
			"ai_topic_angle":     n.AITopicAngle,
			"ai_viral_reason":    n.AIViralReason,
			"ai_borrowable":      n.AIBorrowable,
			"ai_target_audience": n.AITargetAudience,
			"ai_title_formula":   n.AITitleFormula,
			"ai_one_line":        n.AIOneLine,
			"video_transcript":   n.VideoTranscript,
			"enrich_status":      n.EnrichStatus,
			"updated_at":         time.Now(),
		}).Error; err != nil {
		return fmt.Errorf("UpdateEnrichResult: %w", err)
	}
	return nil
}

// GetByIDs 按 (user_id, ids) 批量获取笔记。
func (s *xhsStore) GetByIDs(ctx context.Context, userID uint, ids []uint64) ([]model.XhsTopicNote, error) {
	var list []model.XhsTopicNote
	if len(ids) == 0 {
		return list, nil
	}
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND id IN ?", userID, ids).
		Find(&list).Error; err != nil {
		return nil, fmt.Errorf("GetByIDs: %w", err)
	}
	return list, nil
}
