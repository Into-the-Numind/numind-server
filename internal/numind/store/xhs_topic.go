package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// XhsNoteFilter 选题库列表的可选过滤条件。零值表示不过滤。
type XhsNoteFilter struct {
	NoteType     string // normal/video，空 = 不过滤
	EnrichStatus string // pending/enriching/done/partial/failed/insufficient_credits，空 = 不过滤
	Keyword      string // 标题/正文模糊匹配，空 = 不过滤
}

// XhsSnapshotProjection controls which XHS columns are loaded for an Agent scan.
type XhsSnapshotProjection string

const (
	// XhsSnapshotProjectionIndex returns only the stable business key and collection time.
	XhsSnapshotProjectionIndex XhsSnapshotProjection = "index"
	// XhsSnapshotProjectionFull returns the raw fields required by the tagging Agent.
	XhsSnapshotProjectionFull XhsSnapshotProjection = "full"
)

// XhsSnapshotFilter is the validated AND filter used by the Agent keyset scan.
// Nil time bounds are open; CollectedFrom is inclusive and CollectedTo exclusive.
type XhsSnapshotFilter struct {
	XhsNoteIDs    []string
	Keyword       string
	CollectedFrom *time.Time
	CollectedTo   *time.Time
}

// XhsSnapshotQuery describes one page in a stable, ascending keyset scan.
// SnapshotMaxID zero starts a new snapshot; subsequent pages must reuse the
// returned SnapshotMaxID and advance AfterID to NextAfterID.
type XhsSnapshotQuery struct {
	Filter        XhsSnapshotFilter
	Projection    XhsSnapshotProjection
	AfterID       uint64
	SnapshotMaxID uint64
	Limit         int
}

// XhsSnapshotPage is one stable page of current-user XHS notes.
type XhsSnapshotPage struct {
	Notes         []model.XhsTopicNote
	SnapshotMaxID uint64
	SnapshotTotal int64
	NextAfterID   uint64
	HasMore       bool
}

// IXhsTopicStore 定义小红书选题库（xhs_topic_note）的数据库操作接口。
// 多数方法均带 user 隔离，确保多租户私有累积选题库互不可见。
//
// 例外（内部富化流水线专用，无 user_id 谓词，按主键定位）：
//   - UpdateEnrichStatus / UpdateEnrichResult 是富化流水线内部 helper，
//     调用方须在调用前通过 GetByIDs（已强制 user_id）确认所有权。
type IXhsTopicStore interface {
	// UpsertByUserNote 按 uk_xtn_user_note(user_id, xhs_note_id) 唯一键 upsert 一条笔记。
	// 新建返回 hashChanged=true；命中已存在记录时比对 ContentHash：变化返回 true 并更新，
	// 未变化返回 false 且不覆盖已有富化结果。
	//
	// 注意：内容变化时底层用 Save 覆盖全部字段（含 6 个 AI 富化字段与 video_transcript），
	// 调用方须传入 enrich_status=pending 且 AI 字段置零，以在内容变化时重置富化状态、
	// 触发重新富化（避免旧内容的陈旧富化结果残留在新内容上）。
	UpsertByUserNote(ctx context.Context, n *model.XhsTopicNote) (hashChanged bool, err error)
	// ListNotes 分页查询某用户的选题库，支持 note_type/enrich_status/keyword 过滤。
	ListNotes(ctx context.Context, userID uint, filter XhsNoteFilter, offset, limit int) ([]model.XhsTopicNote, int64, error)
	// ListSnapshot 按 id ASC 读取当前用户的稳定快照页，供 Agent 工具使用。
	ListSnapshot(ctx context.Context, userID uint, query XhsSnapshotQuery) (*XhsSnapshotPage, error)
	// ListPendingEnrich 扫描 enrich_status='pending' 的待富化队列（跨用户，富化流水线用）。
	// 不分页、不返回 total，按 crawled_at 升序（先来先富化）返回至多 limit 条。
	ListPendingEnrich(ctx context.Context, limit int) ([]model.XhsTopicNote, error)
	// GetNote 按 (user_id, id) 获取单条笔记；不存在返回 errno.ErrXhsNoteNotFound。
	GetNote(ctx context.Context, userID uint, id uint64) (*model.XhsTopicNote, error)
	// DeleteNote 按 (user_id, id) 删除单条笔记。
	DeleteNote(ctx context.Context, userID uint, id uint64) error
	// UpdateEnrichStatus 仅更新某条笔记的富化状态（富化状态机流转用）。
	// 内部流水线 helper：按主键定位，无 user_id 隔离，调用方须先经 GetByIDs 确认所有权。
	UpdateEnrichStatus(ctx context.Context, id uint64, status string) error
	// ClaimForEnrich 原子地把笔记从 pending 抢占为 enriching（CAS）。
	// 只有 enrich_status 当前仍为 pending 的笔记会被更新并返回 claimed=true；
	// 已被其它 worker 抢占（非 pending）返回 claimed=false。用于富化流水线的
	// 并发二次保护：同一笔记被重复投递时只有一个 worker 抢到、只富化一次，
	// 避免 read-then-update 的 TOCTOU 窗口导致重复富化重复扣分。
	ClaimForEnrich(ctx context.Context, id uint64) (claimed bool, err error)
	// UpdateEnrichResult 写回 LLM 富化结果（6 分析字段 + 转写 + enrich_status）。
	// 内部流水线 helper：按主键定位，无 user_id 隔离，调用方须先经 GetByIDs 确认所有权。
	UpdateEnrichResult(ctx context.Context, n *model.XhsTopicNote) error
	// GetByIDs 按 (user_id, ids) 批量获取笔记（所有权校验 / 批量富化用）。
	GetByIDs(ctx context.Context, userID uint, ids []uint64) ([]model.XhsTopicNote, error)
}

type xhsStore struct {
	db *gorm.DB
}

var _ IXhsTopicStore = (*xhsStore)(nil)

// NewXhsStore 创建一个 IXhsTopicStore 实例。
func NewXhsStore(db *gorm.DB) IXhsTopicStore {
	return &xhsStore{db: db}
}

// UpsertByUserNote 按 uk_xtn_user_note 唯一键 upsert，并返回内容是否变化。
//
// 内容变化路径使用 Save 覆盖全部字段：调用方须传入 enrich_status=pending 且
// AI 字段置零，以重置富化状态，触发对新内容的重新富化。
//
// 并发安全：事务内 SELECT 加 FOR UPDATE 行锁（clause.Locking），避免两个并发请求
// 对同一 (user_id, xhs_note_id) 同时读到 not-found 后都 Create。即便加锁后仍因隔离
// 级别/无现存行而竞争到唯一索引冲突（MySQL 1062），Create 分支兜底降级为重新读取
// 已存在行并按 hash 比对处理（幂等回退，镜像 store/monitor.go 的 1062 处理模式），
// 不向调用方抛 500。
func (s *xhsStore) UpsertByUserNote(ctx context.Context, n *model.XhsTopicNote) (bool, error) {
	hashChanged := true
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.XhsTopicNote
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND xhs_note_id = ?", n.UserID, n.XhsNoteID).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 新建：内容视为变化。
			if cErr := tx.Create(n).Error; cErr != nil {
				// TOCTOU 兜底：并发新建撞唯一键（1062）→ 降级为读已存在行按 hash 处理。
				if isDuplicateKeyErr(cErr) {
					changed, reErr := s.applyExistingInTx(tx, n)
					if reErr != nil {
						return reErr
					}
					hashChanged = changed
					return nil
				}
				return fmt.Errorf("create: %w", cErr)
			}
			hashChanged = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("lookup: %w", err)
		}

		// 命中已存在记录，比对 content_hash 判定内容是否变化。
		changed, applyErr := applyAgainstExisting(tx, n, &existing)
		if applyErr != nil {
			return applyErr
		}
		hashChanged = changed
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("UpsertByUserNote: %w", err)
	}
	return hashChanged, nil
}

// applyExistingInTx 在 Create 撞 1062 后重新读取已存在行并按 hash 处理（TOCTOU 兜底）。
func (s *xhsStore) applyExistingInTx(tx *gorm.DB, n *model.XhsTopicNote) (bool, error) {
	var existing model.XhsTopicNote
	if err := tx.Where("user_id = ? AND xhs_note_id = ?", n.UserID, n.XhsNoteID).
		First(&existing).Error; err != nil {
		return false, fmt.Errorf("reload after duplicate: %w", err)
	}
	return applyAgainstExisting(tx, n, &existing)
}

// applyAgainstExisting 对已存在行比对 content_hash：未变化保留已有记录（含富化结果）
// 并回填主键返回 false；变化则 Save 覆盖全字段返回 true。
func applyAgainstExisting(tx *gorm.DB, n, existing *model.XhsTopicNote) (bool, error) {
	if existing.ContentHash == n.ContentHash {
		// 内容未变化：保留已有记录（含富化结果），把主键回填到入参便于调用方使用。
		n.ID = existing.ID
		return false, nil
	}
	// 内容变化：更新采集字段，复用已存在记录主键。
	n.ID = existing.ID
	if uErr := tx.Save(n).Error; uErr != nil {
		return false, fmt.Errorf("update: %w", uErr)
	}
	return true, nil
}

// isDuplicateKeyErr 判定 err 是否为 MySQL 唯一索引冲突（1062）。
func isDuplicateKeyErr(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// ListPendingEnrich 扫描待富化队列（enrich_status='pending'），按 crawled_at 升序返回至多 limit 条。
func (s *xhsStore) ListPendingEnrich(ctx context.Context, limit int) ([]model.XhsTopicNote, error) {
	var list []model.XhsTopicNote
	query := s.db.WithContext(ctx).
		Where("enrich_status = ?", model.XhsEnrichPending).
		Order("crawled_at ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&list).Error; err != nil {
		return nil, fmt.Errorf("ListPendingEnrich: %w", err)
	}
	return list, nil
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

// ListSnapshot 按稳定的 id 上界和 keyset 游标读取当前用户的 XHS 笔记。
func (s *xhsStore) ListSnapshot(ctx context.Context, userID uint, query XhsSnapshotQuery) (*XhsSnapshotPage, error) {
	if query.Limit < 1 || query.Limit > 100 {
		return nil, fmt.Errorf("ListSnapshot: limit must be between 1 and 100")
	}
	if query.Projection != XhsSnapshotProjectionIndex && query.Projection != XhsSnapshotProjectionFull {
		return nil, fmt.Errorf("ListSnapshot: unsupported projection %q", query.Projection)
	}
	if query.Filter.CollectedFrom != nil && query.Filter.CollectedTo != nil && !query.Filter.CollectedFrom.Before(*query.Filter.CollectedTo) {
		return nil, fmt.Errorf("ListSnapshot: collected_from must be before collected_to")
	}
	if query.SnapshotMaxID > 0 && query.AfterID > query.SnapshotMaxID {
		return nil, fmt.Errorf("ListSnapshot: after_id exceeds snapshot_max_id")
	}

	snapshotMaxID := query.SnapshotMaxID
	if snapshotMaxID == 0 {
		var maxRow struct {
			MaxID uint64 `gorm:"column:max_id"`
		}
		if err := xhsSnapshotBaseQuery(s.db.WithContext(ctx), userID, query.Filter).
			Select("COALESCE(MAX(id), 0) AS max_id").
			Scan(&maxRow).Error; err != nil {
			return nil, fmt.Errorf("ListSnapshot max: %w", err)
		}
		snapshotMaxID = maxRow.MaxID
	}

	page := &XhsSnapshotPage{
		Notes:         []model.XhsTopicNote{},
		SnapshotMaxID: snapshotMaxID,
		NextAfterID:   query.AfterID,
	}
	if snapshotMaxID == 0 {
		return page, nil
	}

	bounded := func() *gorm.DB {
		return xhsSnapshotBaseQuery(s.db.WithContext(ctx), userID, query.Filter).
			Where("id <= ?", snapshotMaxID)
	}
	if err := bounded().Count(&page.SnapshotTotal).Error; err != nil {
		return nil, fmt.Errorf("ListSnapshot count: %w", err)
	}

	columns := []string{"id", "xhs_note_id", "collected_at"}
	if query.Projection == XhsSnapshotProjectionFull {
		columns = []string{
			"id", "xhs_note_id", "note_type", "title", "content", "video_transcript",
			"like_count", "collect_count", "comment_count", "comments", "note_url", "collected_at",
		}
	}

	var notes []model.XhsTopicNote
	if err := bounded().
		Select(columns).
		Where("id > ?", query.AfterID).
		Order("id ASC").
		Limit(query.Limit + 1).
		Find(&notes).Error; err != nil {
		return nil, fmt.Errorf("ListSnapshot find: %w", err)
	}

	if len(notes) > query.Limit {
		page.HasMore = true
		notes = notes[:query.Limit]
	}
	page.Notes = notes
	if len(notes) > 0 {
		page.NextAfterID = notes[len(notes)-1].ID
	}
	return page, nil
}

func xhsSnapshotBaseQuery(db *gorm.DB, userID uint, filter XhsSnapshotFilter) *gorm.DB {
	query := db.Model(&model.XhsTopicNote{}).Where("user_id = ?", userID)
	if len(filter.XhsNoteIDs) > 0 {
		query = query.Where("xhs_note_id IN ?", filter.XhsNoteIDs)
	}
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		like := "%" + escapeXhsLikeLiteral(keyword) + "%"
		query = query.Where("(title LIKE ? ESCAPE '!' OR content LIKE ? ESCAPE '!')", like, like)
	}
	if filter.CollectedFrom != nil {
		query = query.Where("collected_at >= ?", *filter.CollectedFrom)
	}
	if filter.CollectedTo != nil {
		query = query.Where("collected_at < ?", *filter.CollectedTo)
	}
	return query
}

func escapeXhsLikeLiteral(value string) string {
	replacer := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return replacer.Replace(value)
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

// ClaimForEnrich 原子 CAS：仅当 enrich_status='pending' 时置为 'enriching'。
//
// 用 UPDATE ... WHERE enrich_status='pending' 的 RowsAffected 判定抢占成功，
// 把 read-then-check-then-update 收敛为单条原子语句，杜绝两个 worker 同时读到
// pending 后都富化的 TOCTOU 竞态（防重复富化重复扣分的并发正确性基石）。
func (s *xhsStore) ClaimForEnrich(ctx context.Context, id uint64) (bool, error) {
	res := s.db.WithContext(ctx).
		Model(&model.XhsTopicNote{}).
		Where("id = ? AND enrich_status = ?", id, model.XhsEnrichPending).
		Updates(map[string]interface{}{
			"enrich_status": model.XhsEnrichEnriching,
			"updated_at":    time.Now(),
		})
	if res.Error != nil {
		return false, fmt.Errorf("ClaimForEnrich: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
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
			"images":             n.Images,
			"cover_url":          n.CoverURL,
			"video_url":          n.VideoURL,
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
