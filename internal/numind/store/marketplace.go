package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// IMarketplaceStore 定义 skill_marketplace + skill_subscription 两表的数据访问接口。
// 跨租户安全在 biz/marketplace 层强制（caller 从 JWT 取 subscriber_user_id 不接受参数），
// store 层不做租户校验，假设输入已经过 biz 层鉴权。
type IMarketplaceStore interface {
	// ---- skill_marketplace 行 ----

	// Create 写入新的 marketplace 行。is_public 由 mp.IsPublic 决定（caller 通常设 true）；
	// database.md §6 default:true 陷阱：caller 若想 IsPublic=false 必须按 wantPublic pattern 处理。
	Create(ctx context.Context, mp *model.SkillMarketplace) error

	// GetByID 按 ID 查询 marketplace 行，不过滤 is_public（caller 自己决定是否过滤）。
	// 未找到返回 gorm.ErrRecordNotFound（caller wrap 成业务 errno）。
	GetByID(ctx context.Context, id uint) (*model.SkillMarketplace, error)

	// GetActiveBySourceSkillID 查询 source_skill_id 对应的活跃 marketplace 行（is_public=1）。
	// 用于 Publish 流程 §S2-D1 uniqueness check（同一原 skill 不允许多个活跃上架）。
	// 未找到返回 gorm.ErrRecordNotFound。
	GetActiveBySourceSkillID(ctx context.Context, sourceSkillID uint) (*model.SkillMarketplace, error)

	// UpdateIsPublic 切换上架/下架状态。
	UpdateIsPublic(ctx context.Context, id uint, isPublic bool) error

	// UpdateRecommended 切换平台推荐标记（admin only — 调用方鉴权）。
	UpdateRecommended(ctx context.Context, id uint, recommended bool) error

	// IncrementSubscribeCount 原子增减订阅计数。tx 非 nil 时在事务内执行；nil 用默认 DB。
	// delta 可正可负。下界保护：subscribe_count + delta < 0 时不更新（保留为 0）。
	IncrementSubscribeCount(ctx context.Context, tx *gorm.DB, id uint, delta int) error

	// List 按 ListOptions 检索 marketplace 行。
	// FULLTEXT BOOLEAN MODE 仅在 opts.Q 非空时启用（SQLite 不支持，调用方需要打 t.Skip 处理）。
	// Pagination: offset/limit 由 opts 提供。
	List(ctx context.Context, opts ListOptions) ([]*model.SkillMarketplace, int64, error)

	// ---- skill_subscription 行 ----

	// CreateSubscription 写入新的订阅关系行。UNIQUE(subscriber_user_id, marketplace_id) 防重复。
	// tx 非 nil 时在事务内执行。
	CreateSubscription(ctx context.Context, tx *gorm.DB, sub *model.SkillSubscription) error

	// DeleteSubscription 删除一条订阅关系（订阅方主动取消）。tx 非 nil 时在事务内执行。
	// 未匹配到行不报错（idempotent）。
	DeleteSubscription(ctx context.Context, tx *gorm.DB, subscriberUserID uint, marketplaceID uint) error

	// GetSubscription 查询订阅关系。未找到返回 gorm.ErrRecordNotFound。
	GetSubscription(ctx context.Context, subscriberUserID uint, marketplaceID uint) (*model.SkillSubscription, error)

	// ListMySubscriptions 列举订阅方的全部订阅 + 对应 marketplace 行。
	// 跨租户隔离：调用方传入的 subscriberUserID 直接用于 WHERE，store 层不二次校验。
	ListMySubscriptions(ctx context.Context, subscriberUserID uint, offset, limit int) ([]SubscriptionWithMarketplace, int64, error)
}

// ListOptions 是 browse 查询的过滤/排序参数集合。
type ListOptions struct {
	Q                  string // FULLTEXT 搜索关键词（boolean mode）；SQLite 测试需 Skip
	Category           string // 单一分类过滤（JSON_CONTAINS）
	Sort               string // "recommended"（默认）/"recent"/"popular"
	PublisherUserID    uint   // 非零时限定某发布方（发布方"我发布过哪些"视图）
	IncludeUnpublished bool   // true 时不过滤 is_public（仅在 PublisherUserID 非零时安全）
	Offset             int
	Limit              int
}

// SubscriptionWithMarketplace 是 ListMySubscriptions 的 JOIN 结果（应用层组装，
// 不走 SQL JOIN — SQLite 测试更稳，code 也好读）。
//
// AgentCount 由 biz 层负责填充（JOIN agent_skill_binding 数订阅方装载该 cloned_skill
// 的 agent 数量）；store 返回时此字段恒为 0。spec §3.1 ListMySubscriptions 响应类型
// 含 agent_count，biz 层 hydration 后才完整。
type SubscriptionWithMarketplace struct {
	Subscription model.SkillSubscription `json:"subscription"`
	Marketplace  model.SkillMarketplace  `json:"marketplace"`
	AgentCount   int                     `json:"agent_count"` // hydrated by biz/marketplace.ListMySubscriptions
}

type marketplaceStore struct {
	db *gorm.DB
}

// 编译期接口实现断言（与 agent_definition.go 等现有 store 模式一致）。
var _ IMarketplaceStore = (*marketplaceStore)(nil)

// NewMarketplaceStore 构造 marketplace store。
func NewMarketplaceStore(db *gorm.DB) IMarketplaceStore {
	return &marketplaceStore{db: db}
}

// dbOrTx returns tx if non-nil, else the store's default db.
func (s *marketplaceStore) dbOrTx(tx *gorm.DB) *gorm.DB {
	if tx != nil {
		return tx
	}
	return s.db
}

// ---- marketplace methods ----

func (s *marketplaceStore) Create(ctx context.Context, mp *model.SkillMarketplace) error {
	if mp == nil {
		return errors.New("marketplaceStore.Create: nil input")
	}
	// database.md §6 default:true bool gotcha — SkillMarketplace.IsPublic has default:1.
	// If caller wants IsPublic=false, the zero-value false is treated by GORM as "field not
	// set" and DDL DEFAULT TRUE wins. Save caller's intent before Create and fix up after.
	// Pattern mirrors agentDefinitionStore.CreateTx (agent_definition.go).
	wantPublic := mp.IsPublic
	if err := s.db.WithContext(ctx).Create(mp).Error; err != nil {
		return err
	}
	if !wantPublic && mp.IsPublic {
		if err := s.db.WithContext(ctx).Model(mp).UpdateColumn("is_public", false).Error; err != nil {
			return fmt.Errorf("marketplaceStore.Create: is_public fixup failed: %w", err)
		}
		mp.IsPublic = false
	}
	return nil
}

func (s *marketplaceStore) GetByID(ctx context.Context, id uint) (*model.SkillMarketplace, error) {
	var mp model.SkillMarketplace
	if err := s.db.WithContext(ctx).First(&mp, id).Error; err != nil {
		return nil, err
	}
	return &mp, nil
}

func (s *marketplaceStore) GetActiveBySourceSkillID(ctx context.Context, sourceSkillID uint) (*model.SkillMarketplace, error) {
	var mp model.SkillMarketplace
	err := s.db.WithContext(ctx).
		Where("source_skill_id = ? AND is_public = ?", sourceSkillID, true).
		First(&mp).Error
	if err != nil {
		return nil, err
	}
	return &mp, nil
}

func (s *marketplaceStore) UpdateIsPublic(ctx context.Context, id uint, isPublic bool) error {
	res := s.db.WithContext(ctx).
		Model(&model.SkillMarketplace{}).
		Where("id = ?", id).
		UpdateColumn("is_public", isPublic)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *marketplaceStore) UpdateRecommended(ctx context.Context, id uint, recommended bool) error {
	res := s.db.WithContext(ctx).
		Model(&model.SkillMarketplace{}).
		Where("id = ?", id).
		UpdateColumn("is_platform_recommended", recommended)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *marketplaceStore) IncrementSubscribeCount(ctx context.Context, tx *gorm.DB, id uint, delta int) error {
	db := s.dbOrTx(tx)
	if delta == 0 {
		return nil
	}
	if delta < 0 {
		// Underflow guard: only update if current >= |delta|.
		// MySQL INT UNSIGNED otherwise errors on subscribe_count - N underflow; SQLite would silently
		// store a negative on signed storage. Either way, the conditional WHERE is safer.
		res := db.WithContext(ctx).
			Model(&model.SkillMarketplace{}).
			Where("id = ? AND subscribe_count >= ?", id, -delta).
			UpdateColumn("subscribe_count", gorm.Expr("subscribe_count + ?", delta))
		return res.Error
	}
	return db.WithContext(ctx).
		Model(&model.SkillMarketplace{}).
		Where("id = ?", id).
		UpdateColumn("subscribe_count", gorm.Expr("subscribe_count + ?", delta)).Error
}

func (s *marketplaceStore) List(ctx context.Context, opts ListOptions) ([]*model.SkillMarketplace, int64, error) {
	q := s.db.WithContext(ctx).Model(&model.SkillMarketplace{})

	if opts.PublisherUserID != 0 {
		q = q.Where("publisher_user_id = ?", opts.PublisherUserID)
	}
	if !opts.IncludeUnpublished {
		q = q.Where("is_public = ?", true)
	}
	if opts.Category != "" {
		// JSON_CONTAINS works on both MySQL 8 and SQLite (with JSON1 extension, default in libs Go uses).
		q = q.Where("JSON_CONTAINS(category_tags, JSON_QUOTE(?))", opts.Category)
	}
	if opts.Q != "" {
		// FULLTEXT BOOLEAN MODE (MySQL only). Tests using SQLite must skip Q.
		q = q.Where("MATCH(name, description, when_to_use) AGAINST(? IN BOOLEAN MODE)", opts.Q)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("marketplaceStore.List count: %w", err)
	}

	switch opts.Sort {
	case "recent":
		q = q.Order("created_at DESC")
	case "popular":
		q = q.Order("subscribe_count DESC, created_at DESC")
	default: // "recommended" + fallback
		q = q.Order("is_platform_recommended DESC, subscribe_count DESC, created_at DESC")
	}

	if opts.Limit > 0 {
		q = q.Limit(opts.Limit).Offset(opts.Offset)
	}

	var items []*model.SkillMarketplace
	if err := q.Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("marketplaceStore.List find: %w", err)
	}
	return items, total, nil
}

// ---- subscription methods ----

func (s *marketplaceStore) CreateSubscription(ctx context.Context, tx *gorm.DB, sub *model.SkillSubscription) error {
	if sub == nil {
		return errors.New("marketplaceStore.CreateSubscription: nil input")
	}
	return s.dbOrTx(tx).WithContext(ctx).Create(sub).Error
}

func (s *marketplaceStore) DeleteSubscription(ctx context.Context, tx *gorm.DB, subscriberUserID uint, marketplaceID uint) error {
	return s.dbOrTx(tx).WithContext(ctx).
		Where("subscriber_user_id = ? AND marketplace_id = ?", subscriberUserID, marketplaceID).
		Delete(&model.SkillSubscription{}).Error
}

func (s *marketplaceStore) GetSubscription(ctx context.Context, subscriberUserID uint, marketplaceID uint) (*model.SkillSubscription, error) {
	var sub model.SkillSubscription
	err := s.db.WithContext(ctx).
		Where("subscriber_user_id = ? AND marketplace_id = ?", subscriberUserID, marketplaceID).
		First(&sub).Error
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

func (s *marketplaceStore) ListMySubscriptions(ctx context.Context, subscriberUserID uint, offset, limit int) ([]SubscriptionWithMarketplace, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).
		Model(&model.SkillSubscription{}).
		Where("subscriber_user_id = ?", subscriberUserID).
		Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("marketplaceStore.ListMySubscriptions count: %w", err)
	}

	var subs []model.SkillSubscription
	q := s.db.WithContext(ctx).
		Where("subscriber_user_id = ?", subscriberUserID).
		Order("subscribed_at DESC")
	if limit > 0 {
		q = q.Limit(limit).Offset(offset)
	}
	if err := q.Find(&subs).Error; err != nil {
		return nil, 0, fmt.Errorf("marketplaceStore.ListMySubscriptions find subs: %w", err)
	}
	if len(subs) == 0 {
		return []SubscriptionWithMarketplace{}, total, nil
	}

	// Bulk-load marketplaces to avoid N+1.
	mkIDs := make([]uint, 0, len(subs))
	for _, sub := range subs {
		mkIDs = append(mkIDs, sub.MarketplaceID)
	}
	var mks []model.SkillMarketplace
	if err := s.db.WithContext(ctx).Where("id IN ?", mkIDs).Find(&mks).Error; err != nil {
		return nil, 0, fmt.Errorf("marketplaceStore.ListMySubscriptions find marketplaces: %w", err)
	}
	mkMap := make(map[uint]model.SkillMarketplace, len(mks))
	for _, mk := range mks {
		mkMap[mk.ID] = mk
	}

	result := make([]SubscriptionWithMarketplace, 0, len(subs))
	for _, sub := range subs {
		result = append(result, SubscriptionWithMarketplace{
			Subscription: sub,
			Marketplace:  mkMap[sub.MarketplaceID], // zero value if missing (marketplace hard-deleted)
		})
	}
	return result, total, nil
}
