package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// ListOpts 控制 ListByUserAgent 的过滤、排序和分页行为。
type ListOpts struct {
	Kind      string // "" = 不过滤
	AliveOnly bool   // true: 过滤 expires_at IS NULL OR > NOW()
	Limit     int    // 0 = 50（默认上限）
	OrderBy   string // "recency_at desc" 默认
}

// IAgentSessionMemoryStore 定义 agent_session_memory（L1 短期记忆）的存取接口。
type IAgentSessionMemoryStore interface {
	// Create 写入一条 L1 记忆；Select("*") 强制 score=0 不被 GORM default:1.0 跳过。
	Create(ctx context.Context, m *model.AgentSessionMemory) error
	// ListByUserAgent 按 (user_id, agent_definition_id) 列举记忆，支持 AliveOnly / Kind 过滤。
	ListByUserAgent(ctx context.Context, userID uint, agentDefID uint64, opts ListOpts) ([]model.AgentSessionMemory, error)
	// UpdateRecency 批量更新指定 ID 集合的 recency_at 时刻。
	UpdateRecency(ctx context.Context, ids []uint64, at time.Time) error
	// DeleteByUser 删除某 user 的全部 L1 记忆（Clear 用）。
	DeleteByUser(ctx context.Context, userID uint) error
	// Count 统计某 (user, agent) 的记忆条数；aliveOnly=true 时仅计 alive 行。
	Count(ctx context.Context, userID uint, agentDefID uint64, aliveOnly bool) (int64, error)
}

type agentSessionMemoryStore struct {
	db *gorm.DB
}

var _ IAgentSessionMemoryStore = (*agentSessionMemoryStore)(nil)

// NewAgentSessionMemoryStore 构造 IAgentSessionMemoryStore 实例。
func NewAgentSessionMemoryStore(db *gorm.DB) IAgentSessionMemoryStore {
	return &agentSessionMemoryStore{db: db}
}

// Create 写入一条 L1 记忆。
// Select("*") 强制所有列（含 score=0.0）入 INSERT，绕过 GORM default:1.0 zero-value gotcha（spec §3.6）。
func (s *agentSessionMemoryStore) Create(ctx context.Context, m *model.AgentSessionMemory) error {
	if err := s.db.WithContext(ctx).Select("*").Create(m).Error; err != nil {
		return fmt.Errorf("agentSessionMemoryStore.Create: %w", err)
	}
	return nil
}

// ListByUserAgent 按 (user_id, agent_definition_id) 列举记忆。
func (s *agentSessionMemoryStore) ListByUserAgent(ctx context.Context, userID uint, agentDefID uint64, opts ListOpts) ([]model.AgentSessionMemory, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	orderBy := opts.OrderBy
	if orderBy == "" {
		orderBy = "recency_at desc"
	}

	q := s.db.WithContext(ctx).
		Where("user_id = ? AND agent_definition_id = ?", userID, agentDefID)

	if opts.Kind != "" {
		q = q.Where("kind = ?", opts.Kind)
	}
	if opts.AliveOnly {
		q = q.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}

	var items []model.AgentSessionMemory
	if err := q.Order(orderBy).Limit(limit).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("agentSessionMemoryStore.ListByUserAgent: %w", err)
	}
	return items, nil
}

// UpdateRecency 批量更新指定 ID 集合的 recency_at。
// 空 ids 直接返回（no-op），避免生成 WHERE IN () 无效 SQL。
func (s *agentSessionMemoryStore) UpdateRecency(ctx context.Context, ids []uint64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).
		Model(&model.AgentSessionMemory{}).
		Where("id IN ?", ids).
		UpdateColumn("recency_at", at).Error; err != nil {
		return fmt.Errorf("agentSessionMemoryStore.UpdateRecency: %w", err)
	}
	return nil
}

// DeleteByUser 删除某 user 的全部 L1 记忆。
func (s *agentSessionMemoryStore) DeleteByUser(ctx context.Context, userID uint) error {
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Delete(&model.AgentSessionMemory{}).Error; err != nil {
		return fmt.Errorf("agentSessionMemoryStore.DeleteByUser(userID=%d): %w", userID, err)
	}
	return nil
}

// Count 统计 (user, agent) 的记忆条数；aliveOnly=true 时仅计 alive 行。
func (s *agentSessionMemoryStore) Count(ctx context.Context, userID uint, agentDefID uint64, aliveOnly bool) (int64, error) {
	q := s.db.WithContext(ctx).
		Model(&model.AgentSessionMemory{}).
		Where("user_id = ? AND agent_definition_id = ?", userID, agentDefID)
	if aliveOnly {
		q = q.Where("expires_at IS NULL OR expires_at > ?", time.Now())
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("agentSessionMemoryStore.Count: %w", err)
	}
	return count, nil
}
