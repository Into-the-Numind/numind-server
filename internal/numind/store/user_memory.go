package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ListFactOpts controls UserMemoryFactStore.List 行为：过滤 + 排序 + 分页。
type ListFactOpts struct {
	Categories      []string // 空 = 全部 6 类
	MinConfidence   float64  // 0 = 不过滤
	IncludeArchived bool     // 默认 false（隐藏 is_archived=true）
	OrderBy         string   // whitelist: "confidence" / "recency" / "importance"; 默认 "confidence"
	Limit           int      // 默认 50
	Offset          int
}

// IUserMemoryProfileStore 定义 user_memory_profile（每用户单行画像）的存取接口。
//
// 所有查询签名只接受 userID 单参数（D7 拍板：B2B2C 父子账户 memory 完全隔离，
// 严禁加 parentUserID 参数；任何"父子聚合"或"组织共享"需求都视为破坏性改动）。
type IUserMemoryProfileStore interface {
	// Get 按 user_id 查询；不存在返回 gorm.ErrRecordNotFound（调用方判空创建）。
	Get(ctx context.Context, userID uint) (*model.UserMemoryProfile, error)
	// Upsert 按 user_id ON DUPLICATE KEY UPDATE 写入或更新画像。
	Upsert(ctx context.Context, profile *model.UserMemoryProfile) error
	// UpdateCachedInsight 仅更新 dialectic cache 三字段（cached_insight + at + fact_count）。
	UpdateCachedInsight(ctx context.Context, userID uint, insight string, factCount int) error
	// IncrTotalFacts 原子地把 total_facts 加 delta（可正可负）。
	// 应用层在 fact Create/BatchCreate/Archive/BulkArchive 内同事务调用。
	IncrTotalFacts(ctx context.Context, userID uint, delta int) error
	// IncrementExtractionCount 原子自增 extraction_count_since_rebuild 并返回新值
	// (Task 3.3 ExtractorService 用)。若 profile 行不存在则懒创建一行 (零值字段).
	IncrementExtractionCount(ctx context.Context, userID uint) (int, error)
	// ResetExtractionCount 把 extraction_count_since_rebuild 置 0
	// (Task 3.3 RebuildNarrative 完成后调用).
	ResetExtractionCount(ctx context.Context, userID uint) error
	// UpdateNarrative 单事务更新 work_context / personal_context / top_of_mind 三字段
	// (Task 3.3 RebuildNarrative 用). profile 行不存在则 Upsert 创建。
	UpdateNarrative(ctx context.Context, userID uint, work, personal, topOfMind string) error
}

// IUserMemoryFactStore 定义 user_memory_facts 的存取接口。
//
// 所有查询签名只接受 userID 单参数（D7：B2B2C 父子完全隔离，禁止 parentUserID 参数）。
//
// V1.5 = Layer A：Create / BatchCreate 在 SubjectID != nil 时返回 ErrLayerBNotSupported。
type IUserMemoryFactStore interface {
	// Create 写一条 fact + 同事务把 user_memory_profile.total_facts +1。
	// V1.5 校验：fact.SubjectID 非 nil 时返回 errno.ErrLayerBNotSupported。
	Create(ctx context.Context, fact *model.UserMemoryFact) error
	// UpdateConfidence 单条更新 confidence (Task 3.3 hash dedup 用,
	// 命中同 hash 老 fact 时把 confidence 提升为 max(old, new)).
	UpdateConfidence(ctx context.Context, factID uint64, newConf float64) error
	// BatchCreate 批量写 facts + 同事务把 total_facts += len(facts)。
	// V1.5 校验：任一 fact.SubjectID 非 nil 整批拒绝。
	BatchCreate(ctx context.Context, facts []model.UserMemoryFact) error
	// GetByUUID 按 uuid 查 fact（不过滤 user_id；调用方需自行 enforce auth）。
	GetByUUID(ctx context.Context, uuid string) (*model.UserMemoryFact, error)
	// List 按 user_id + opts 过滤排序分页返回 facts。
	List(ctx context.Context, userID uint, opts ListFactOpts) ([]model.UserMemoryFact, error)
	// GetByIDs 批量按 ID 取 facts，结果按入参 ids 顺序返回（task-04 selector cache hit 后用）。
	// 强制 user_id 过滤：跨用户 id 静默 drop（defense-in-depth，避免 cache hit 路径外的
	// 调用方直接传入未经 List(userID,...) 隔离过滤的 IDs 时跨 user 取数）。
	// 未找到或不属于该 user 的 id 在返回切片中被跳过（不报错）；空入参直接返回空切片。
	GetByIDs(ctx context.Context, userID uint, ids []uint64) ([]model.UserMemoryFact, error)
	// UpdateUsage 批量更新 last_used_at=NOW 且 use_count++（task-04 selector 用）。
	// 空 IDs 直接 no-op，不生成无效 SQL。
	UpdateUsage(ctx context.Context, factIDs []uint64) error
	// UpdateImportance 单条更新 importance（task-07 dialectic 推理后用）。
	UpdateImportance(ctx context.Context, factID uint64, importance float64) error
	// Archive 单条软删除 + 同事务把 total_facts -1（仅 was alive 时减）。
	Archive(ctx context.Context, factID uint64) error
	// BulkArchiveByConfidence 把 confidence < threshold 的 alive fact 批量软删；
	// 同事务把 total_facts -= 实际 archive 行数。返回实际 archive 行数。
	BulkArchiveByConfidence(ctx context.Context, userID uint, threshold float64) (int, error)
	// CountByUser 统计某用户 fact 数；includeArchived=false 时仅计 alive。
	CountByUser(ctx context.Context, userID uint, includeArchived bool) (int, error)
	// FindByEmbedHash 按 (user_id, embedding_hash) 查重（dedup 用）；
	// 不存在返回 gorm.ErrRecordNotFound。仅查 alive 行。
	FindByEmbedHash(ctx context.Context, userID uint, hash string) (*model.UserMemoryFact, error)
}

// allowedFactOrderBy 白名单防 SQL injection：3 个业务真实需要的 order key → ORDER BY 子句。
var allowedFactOrderBy = map[string]string{
	"confidence": "confidence DESC, id DESC",          // 高置信度优先
	"recency":    "source_extracted_at DESC, id DESC", // 最近抽取优先
	"importance": "importance DESC, id DESC",          // 重要性优先（dialectic 更新后用）
}

// ─── userMemoryProfileStore ──────────────────────────────────────────────────

type userMemoryProfileStore struct {
	db *gorm.DB
}

var _ IUserMemoryProfileStore = (*userMemoryProfileStore)(nil)

// NewUserMemoryProfileStore 构造 IUserMemoryProfileStore 实例。
func NewUserMemoryProfileStore(db *gorm.DB) IUserMemoryProfileStore {
	return &userMemoryProfileStore{db: db}
}

// Get 按 user_id 查询单条 profile；不存在返回 gorm.ErrRecordNotFound。
func (s *userMemoryProfileStore) Get(ctx context.Context, userID uint) (*model.UserMemoryProfile, error) {
	var p model.UserMemoryProfile
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		First(&p).Error; err != nil {
		return nil, fmt.Errorf("userMemoryProfileStore.Get(userID=%d): %w", userID, err)
	}
	return &p, nil
}

// Upsert 按 user_id ON DUPLICATE KEY UPDATE 写入或更新画像。
// 显式列出 DoUpdates 字段，避免覆盖 created_at。
func (s *userMemoryProfileStore) Upsert(ctx context.Context, profile *model.UserMemoryProfile) error {
	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"work_context", "personal_context", "top_of_mind",
				"cached_insight", "cached_insight_at", "cached_insight_fact_count",
				"total_facts", "last_extraction_at", "last_extraction_session_id",
				"updated_at",
			}),
		}).
		Create(profile).Error; err != nil {
		return fmt.Errorf("userMemoryProfileStore.Upsert(userID=%d): %w", profile.UserID, err)
	}
	return nil
}

// UpdateCachedInsight 仅更新 dialectic cache 三字段（不动其它列）。
// 若 profile 行不存在返回 gorm.ErrRecordNotFound。
// 调用方可用 errors.Is(err, gorm.ErrRecordNotFound) 判断后决定 Upsert 或忽略。
func (s *userMemoryProfileStore) UpdateCachedInsight(ctx context.Context, userID uint, insight string, factCount int) error {
	now := time.Now()
	res := s.db.WithContext(ctx).
		Model(&model.UserMemoryProfile{}).
		Where("user_id = ?", userID).
		Updates(map[string]interface{}{
			"cached_insight":            insight,
			"cached_insight_at":         now,
			"cached_insight_fact_count": factCount,
		})
	if res.Error != nil {
		return fmt.Errorf("userMemoryProfileStore.UpdateCachedInsight(userID=%d): %w", userID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("userMemoryProfileStore.UpdateCachedInsight(userID=%d): %w", userID, gorm.ErrRecordNotFound)
	}
	return nil
}

// IncrTotalFacts 原子地把 total_facts 加 delta（可正可负）。
// 若 profile 不存在自动 Upsert 一个空行（INSERT ON DUPLICATE KEY UPDATE 风格）后再加。
func (s *userMemoryProfileStore) IncrTotalFacts(ctx context.Context, userID uint, delta int) error {
	return incrTotalFactsOnTx(ctx, s.db, userID, delta)
}

// IncrementExtractionCount 原子自增 extraction_count_since_rebuild 并返回新值。
// 若 profile 行不存在自动 ON CONFLICT DO NOTHING 创建零值行后再加 (与 IncrTotalFacts 风格一致).
//
// Task 3.3 ExtractorService 在每次成功抽取 facts (无论新增/dedup) 后调用一次,
// 计数达 ExtractionCountRebuildThreshold (默认 5) 时触发 RebuildNarrative.
func (s *userMemoryProfileStore) IncrementExtractionCount(ctx context.Context, userID uint) (int, error) {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&model.UserMemoryProfile{UserID: userID}).Error; err != nil {
			return fmt.Errorf("ensure-profile(userID=%d): %w", userID, err)
		}
		if err := tx.WithContext(ctx).
			Model(&model.UserMemoryProfile{}).
			Where("user_id = ?", userID).
			UpdateColumn("extraction_count_since_rebuild", gorm.Expr("extraction_count_since_rebuild + ?", 1)).Error; err != nil {
			return fmt.Errorf("increment(userID=%d): %w", userID, err)
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("userMemoryProfileStore.IncrementExtractionCount: %w", err)
	}
	// SELECT-after-update: 返回当前值给调用方阈值判断.
	var p model.UserMemoryProfile
	if err := s.db.WithContext(ctx).
		Select("extraction_count_since_rebuild").
		Where("user_id = ?", userID).
		First(&p).Error; err != nil {
		return 0, fmt.Errorf("userMemoryProfileStore.IncrementExtractionCount select-after: %w", err)
	}
	return p.ExtractionCountSinceRebuild, nil
}

// ResetExtractionCount 把 extraction_count_since_rebuild 置 0
// (Task 3.3 RebuildNarrative 成功完成后调用).
// 若 profile 行不存在返回 gorm.ErrRecordNotFound (rebuild 路径理论上不会触发,
// 因为 IncrementExtractionCount 已经保证 profile 存在).
func (s *userMemoryProfileStore) ResetExtractionCount(ctx context.Context, userID uint) error {
	res := s.db.WithContext(ctx).
		Model(&model.UserMemoryProfile{}).
		Where("user_id = ?", userID).
		UpdateColumn("extraction_count_since_rebuild", 0)
	if res.Error != nil {
		return fmt.Errorf("userMemoryProfileStore.ResetExtractionCount(userID=%d): %w", userID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("userMemoryProfileStore.ResetExtractionCount(userID=%d): %w", userID, gorm.ErrRecordNotFound)
	}
	return nil
}

// UpdateNarrative 单事务更新 work_context / personal_context / top_of_mind 三字段
// (Task 3.3 RebuildNarrative 用). 若 profile 行不存在则 ON CONFLICT DO NOTHING 创建零值
// 后再写入 (rebuild 之前应当已存在,但 defensive).
func (s *userMemoryProfileStore) UpdateNarrative(ctx context.Context, userID uint, work, personal, topOfMind string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&model.UserMemoryProfile{UserID: userID}).Error; err != nil {
			return fmt.Errorf("userMemoryProfileStore.UpdateNarrative ensure-profile(userID=%d): %w", userID, err)
		}
		if err := tx.WithContext(ctx).
			Model(&model.UserMemoryProfile{}).
			Where("user_id = ?", userID).
			Updates(map[string]interface{}{
				"work_context":     work,
				"personal_context": personal,
				"top_of_mind":      topOfMind,
			}).Error; err != nil {
			return fmt.Errorf("userMemoryProfileStore.UpdateNarrative(userID=%d): %w", userID, err)
		}
		return nil
	})
}

// incrTotalFactsOnTx 是 IncrTotalFacts 的事务实现，供 fact store 在同事务里复用。
func incrTotalFactsOnTx(ctx context.Context, tx *gorm.DB, userID uint, delta int) error {
	// 1. 确保 profile 行存在（懒创建）。
	//    用 ON CONFLICT DO NOTHING + total_facts 初始 0；不会覆盖已存在行的 total_facts。
	if err := tx.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.UserMemoryProfile{UserID: userID}).Error; err != nil {
		return fmt.Errorf("incrTotalFactsOnTx ensure-profile(userID=%d): %w", userID, err)
	}
	// 2. 原子自增 total_facts。
	if err := tx.WithContext(ctx).
		Model(&model.UserMemoryProfile{}).
		Where("user_id = ?", userID).
		UpdateColumn("total_facts", gorm.Expr("total_facts + ?", delta)).Error; err != nil {
		return fmt.Errorf("incrTotalFactsOnTx(userID=%d, delta=%d): %w", userID, delta, err)
	}
	return nil
}

// ─── userMemoryFactStore ─────────────────────────────────────────────────────

type userMemoryFactStore struct {
	db *gorm.DB
}

var _ IUserMemoryFactStore = (*userMemoryFactStore)(nil)

// NewUserMemoryFactStore 构造 IUserMemoryFactStore 实例。
func NewUserMemoryFactStore(db *gorm.DB) IUserMemoryFactStore {
	return &userMemoryFactStore{db: db}
}

// Create 写一条 fact + 同事务 IncrTotalFacts(+1)。
// V1.5 校验：fact.SubjectID != nil 时返回 ErrLayerBNotSupported。
func (s *userMemoryFactStore) Create(ctx context.Context, fact *model.UserMemoryFact) error {
	if fact == nil {
		return fmt.Errorf("userMemoryFactStore.Create: nil fact")
	}
	if fact.SubjectID != nil {
		return errno.ErrLayerBNotSupported
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(fact).Error; err != nil {
			return fmt.Errorf("userMemoryFactStore.Create: %w", err)
		}
		if err := incrTotalFactsOnTx(ctx, tx, fact.UserID, 1); err != nil {
			return err
		}
		return nil
	})
}

// BatchCreate 批量写 + 同事务自增 total_facts。任一 SubjectID != nil 整批拒绝。
// 多 user 批次会按 (userID -> count) 聚合后分别调 IncrTotalFacts，保证 profile 计数正确。
func (s *userMemoryFactStore) BatchCreate(ctx context.Context, facts []model.UserMemoryFact) error {
	if len(facts) == 0 {
		return nil
	}
	// 1. V1.5 前置校验：任一 SubjectID 非 nil 立即拒绝。
	for i := range facts {
		if facts[i].SubjectID != nil {
			return errno.ErrLayerBNotSupported
		}
	}
	// 2. 按 userID 聚合 delta（支持跨 user 批次）。
	deltaByUser := make(map[uint]int, len(facts))
	for i := range facts {
		deltaByUser[facts[i].UserID]++
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&facts).Error; err != nil {
			return fmt.Errorf("userMemoryFactStore.BatchCreate: %w", err)
		}
		for userID, delta := range deltaByUser {
			if err := incrTotalFactsOnTx(ctx, tx, userID, delta); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetByUUID 按 uuid 查找 fact。
func (s *userMemoryFactStore) GetByUUID(ctx context.Context, uuid string) (*model.UserMemoryFact, error) {
	var f model.UserMemoryFact
	if err := s.db.WithContext(ctx).
		Where("uuid = ?", uuid).
		First(&f).Error; err != nil {
		return nil, fmt.Errorf("userMemoryFactStore.GetByUUID(uuid=%q): %w", uuid, err)
	}
	return &f, nil
}

// List 按 user_id + opts 过滤排序分页返回 facts。
// OrderBy 白名单校验防 SQL injection；非法值 fallback 到 "confidence"。
func (s *userMemoryFactStore) List(ctx context.Context, userID uint, opts ListFactOpts) ([]model.UserMemoryFact, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	orderClause, ok := allowedFactOrderBy[opts.OrderBy]
	if !ok {
		// 非法或空值都 fallback 到 confidence。
		orderClause = allowedFactOrderBy["confidence"]
	}

	q := s.db.WithContext(ctx).
		Model(&model.UserMemoryFact{}).
		Where("user_id = ?", userID)

	if !opts.IncludeArchived {
		q = q.Where("is_archived = ?", false)
	}
	if opts.MinConfidence > 0 {
		q = q.Where("confidence >= ?", opts.MinConfidence)
	}
	if len(opts.Categories) > 0 {
		q = q.Where("category IN ?", opts.Categories)
	}

	var items []model.UserMemoryFact
	if err := q.Order(orderClause).Offset(opts.Offset).Limit(limit).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("userMemoryFactStore.List(userID=%d): %w", userID, err)
	}
	return items, nil
}

// GetByIDs 批量按 ID 取 facts，结果按入参 ids 顺序返回。
// 用 Find 实现，不存在的 id 静默 drop（不触发 gorm.ErrRecordNotFound）。返回切片
// 按调用方传入 ids 顺序排列，未知 ids 不出现在结果中。
// 强制 user_id 过滤：SQL `WHERE user_id = ? AND id IN ?`，跨用户 ids 被静默 drop
// — defense-in-depth，与 selector cache key 含 userID 的过滤构成双保险。
// 空入参直接返回空切片。
func (s *userMemoryFactStore) GetByIDs(ctx context.Context, userID uint, ids []uint64) ([]model.UserMemoryFact, error) {
	if len(ids) == 0 {
		return []model.UserMemoryFact{}, nil
	}
	var rows []model.UserMemoryFact
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND id IN ?", userID, ids).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("userMemoryFactStore.GetByIDs(userID=%d, n=%d): %w", userID, len(ids), err)
	}
	// 保持入参 ids 顺序（caller 用 selector LLM 选出的相关度排序，顺序有语义）。
	byID := make(map[uint64]model.UserMemoryFact, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	out := make([]model.UserMemoryFact, 0, len(ids))
	for _, id := range ids {
		if f, ok := byID[id]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// UpdateUsage 批量更新 last_used_at=NOW 且 use_count++。空 IDs 直接 no-op。
func (s *userMemoryFactStore) UpdateUsage(ctx context.Context, factIDs []uint64) error {
	if len(factIDs) == 0 {
		return nil
	}
	now := time.Now()
	if err := s.db.WithContext(ctx).
		Model(&model.UserMemoryFact{}).
		Where("id IN ?", factIDs).
		Updates(map[string]interface{}{
			"last_used_at": now,
			"use_count":    gorm.Expr("use_count + ?", 1),
		}).Error; err != nil {
		return fmt.Errorf("userMemoryFactStore.UpdateUsage(n=%d): %w", len(factIDs), err)
	}
	return nil
}

// UpdateConfidence 单条更新 confidence (Task 3.3 hash dedup 用).
// 不动 importance / use_count / source_*; 仅刷 confidence + updated_at (autoUpdateTime).
// factID 不存在返回 gorm.ErrRecordNotFound.
func (s *userMemoryFactStore) UpdateConfidence(ctx context.Context, factID uint64, newConf float64) error {
	res := s.db.WithContext(ctx).
		Model(&model.UserMemoryFact{}).
		Where("id = ?", factID).
		Update("confidence", newConf)
	if res.Error != nil {
		return fmt.Errorf("userMemoryFactStore.UpdateConfidence(id=%d): %w", factID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("userMemoryFactStore.UpdateConfidence(id=%d): %w", factID, gorm.ErrRecordNotFound)
	}
	return nil
}

// UpdateImportance 单条更新 importance。
func (s *userMemoryFactStore) UpdateImportance(ctx context.Context, factID uint64, importance float64) error {
	res := s.db.WithContext(ctx).
		Model(&model.UserMemoryFact{}).
		Where("id = ?", factID).
		UpdateColumn("importance", importance)
	if res.Error != nil {
		return fmt.Errorf("userMemoryFactStore.UpdateImportance(id=%d): %w", factID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("userMemoryFactStore.UpdateImportance(id=%d): %w", factID, gorm.ErrRecordNotFound)
	}
	return nil
}

// Archive 单条软删除（is_archived=true）+ 同事务 total_facts -1（仅 was alive 时减）。
func (s *userMemoryFactStore) Archive(ctx context.Context, factID uint64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 取出 (user_id, is_archived) 状态。
		var f model.UserMemoryFact
		if err := tx.Where("id = ?", factID).First(&f).Error; err != nil {
			return fmt.Errorf("userMemoryFactStore.Archive Get(id=%d): %w", factID, err)
		}
		if f.IsArchived {
			// 已 archived，no-op（防止重复减计数）。
			return nil
		}
		// 2. 标记 archived。
		if err := tx.Model(&model.UserMemoryFact{}).
			Where("id = ?", factID).
			UpdateColumn("is_archived", true).Error; err != nil {
			return fmt.Errorf("userMemoryFactStore.Archive Update(id=%d): %w", factID, err)
		}
		// 3. 同事务减计数。
		if err := incrTotalFactsOnTx(ctx, tx, f.UserID, -1); err != nil {
			return err
		}
		return nil
	})
}

// BulkArchiveByConfidence 把 confidence < threshold 的 alive fact 批量软删，
// 同事务 total_facts -= 实际 archive 行数。返回实际 archive 行数。
//
// threshold > 1.00 时全部 alive 行被 archive（用作 "忘记我" GDPR 路径）。
func (s *userMemoryFactStore) BulkArchiveByConfidence(ctx context.Context, userID uint, threshold float64) (int, error) {
	var archived int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.UserMemoryFact{}).
			Where("user_id = ? AND is_archived = ? AND confidence < ?", userID, false, threshold).
			UpdateColumn("is_archived", true)
		if res.Error != nil {
			return fmt.Errorf("userMemoryFactStore.BulkArchiveByConfidence Update(userID=%d): %w", userID, res.Error)
		}
		archived = res.RowsAffected
		if archived > 0 {
			if err := incrTotalFactsOnTx(ctx, tx, userID, -int(archived)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(archived), nil
}

// CountByUser 统计某用户 fact 数；includeArchived=false 时仅计 alive。
func (s *userMemoryFactStore) CountByUser(ctx context.Context, userID uint, includeArchived bool) (int, error) {
	q := s.db.WithContext(ctx).
		Model(&model.UserMemoryFact{}).
		Where("user_id = ?", userID)
	if !includeArchived {
		q = q.Where("is_archived = ?", false)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("userMemoryFactStore.CountByUser(userID=%d): %w", userID, err)
	}
	return int(count), nil
}

// FindByEmbedHash 按 (user_id, embedding_hash) 查重；仅查 alive。
func (s *userMemoryFactStore) FindByEmbedHash(ctx context.Context, userID uint, hash string) (*model.UserMemoryFact, error) {
	if hash == "" {
		// 空 hash 不算重复，直接返回 NotFound 而不是误命中。
		return nil, fmt.Errorf("userMemoryFactStore.FindByEmbedHash: empty hash: %w", gorm.ErrRecordNotFound)
	}
	var f model.UserMemoryFact
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND embedding_hash = ? AND is_archived = ?", userID, hash, false).
		First(&f).Error; err != nil {
		return nil, fmt.Errorf("userMemoryFactStore.FindByEmbedHash(userID=%d, hash=%q): %w", userID, hash, err)
	}
	return &f, nil
}
