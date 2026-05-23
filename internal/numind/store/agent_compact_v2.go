package store

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/compactv2"
	"numind-server/internal/pkg/model"
)

// IAgentCompactV2Store 是 Agent Mode V1.5 板块 2「上下文管理 V2」的独立存储接口。
//
// 平行重做策略（D3）：与 V1 `IAgentRunStore` 物理隔离，**不修改** V1 接口。
// 所有方法只读写 `*_v2` 列（compact_state_v2 / total_tokens_used_v2 / use_compact_v2 /
// context_window_limit_v2 / messages），不动 V1 字段（compact_state / compact_summary）。
type IAgentCompactV2Store interface {
	// GetCompactStateV2 读 V2 状态。
	// 返回 (nil, nil) 表示该 run 的 compact_state_v2 列为 NULL（V2 路径尚未写入）。
	// 调用方应当结合 use_compact_v2 flag 决定是否走 V2 路径。
	GetCompactStateV2(ctx context.Context, runID uint64) (*compactv2.CompactStateV2, error)

	// UpdateCompactStateV2 写 V2 状态（read-merge-write）。
	// MySQL JSON 字段 GORM Update 会整列覆写，所以即使传入 state 的子字段为零值，
	// 也按原样持久化。调用方应当先读后写（外部 merge），不依赖此方法做字段级 patch。
	// （这一点与 V1 `MergeTerminalMetadata` 风格保持一致。）
	UpdateCompactStateV2(ctx context.Context, runID uint64, state *compactv2.CompactStateV2) error

	// IncrementTokensUsedV2 原子累加 token usage。
	// 必须用 `UPDATE col=col+?` GORM 表达式实现，避免 read-then-write 竞态。
	IncrementTokensUsedV2(ctx context.Context, runID uint64, deltaActual int64) error

	// SetContextWindowLimitV2 在 run 启动时冻结 model context_window 上限。
	SetContextWindowLimitV2(ctx context.Context, runID uint64, limit int) error

	// SetUseCompactV2 在 run 创建时设置 V2 flag。
	// 仅创建路径调用一次，run 进行中禁止切换（由 runner 层保证，本 store 不强制）。
	// 多次调用记 warn log 由调用方负责，本方法不报错。
	SetUseCompactV2(ctx context.Context, runID uint64, enabled bool) error

	// UpdateMessagesV2 V2 专用：写 messages JSON（task 2.3/2.4 用）。
	// V2 写入的 entry 含 uuid + meta 字段；V1 read 时遇到这些字段直接忽略（向后兼容）。
	// agent_run.messages 列由 V1/V2 共用，但 schema 不变。
	UpdateMessagesV2(ctx context.Context, runID uint64, messages []compactv2.MessageV2) error
}

type agentCompactV2Store struct {
	db *gorm.DB
}

func newAgentCompactV2Store(db *gorm.DB) IAgentCompactV2Store {
	return &agentCompactV2Store{db: db}
}

var _ IAgentCompactV2Store = (*agentCompactV2Store)(nil)

// GetCompactStateV2 读 compact_state_v2 列；空 / NULL 返回 (nil, nil)。
// agent_run 行不存在时返回 wrapped gorm.ErrRecordNotFound（调用方按需 errors.Is 判断）。
func (s *agentCompactV2Store) GetCompactStateV2(ctx context.Context, runID uint64) (*compactv2.CompactStateV2, error) {
	var run model.AgentRun
	if err := s.db.WithContext(ctx).Select("id", "compact_state_v2").First(&run, runID).Error; err != nil {
		return nil, fmt.Errorf("agentCompactV2Store.GetCompactStateV2(id=%d): %w", runID, err)
	}
	// 空 / NULL / 字面 "null" 都视为未写入
	if len(run.CompactStateV2) == 0 || string(run.CompactStateV2) == "null" {
		return nil, nil
	}
	var state compactv2.CompactStateV2
	if err := json.Unmarshal(run.CompactStateV2, &state); err != nil {
		return nil, fmt.Errorf("agentCompactV2Store.GetCompactStateV2 unmarshal(id=%d): %w", runID, err)
	}
	return &state, nil
}

// UpdateCompactStateV2 整列覆写 compact_state_v2（read-merge-write 由调用方负责）。
// 实现层不强制 read-merge，因为 V2 state 字段总数有限（6 个），调用方先 Get → 修改 → Update
// 的成本可接受。
func (s *agentCompactV2Store) UpdateCompactStateV2(ctx context.Context, runID uint64, state *compactv2.CompactStateV2) error {
	if state == nil {
		return fmt.Errorf("agentCompactV2Store.UpdateCompactStateV2: state must not be nil")
	}
	b, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("agentCompactV2Store.UpdateCompactStateV2 marshal(id=%d): %w", runID, err)
	}
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", runID).
		Update("compact_state_v2", datatypes.JSON(b))
	if result.Error != nil {
		return fmt.Errorf("agentCompactV2Store.UpdateCompactStateV2(id=%d): %w", runID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentCompactV2Store.UpdateCompactStateV2: no row matched id=%d", runID)
	}
	return nil
}

// IncrementTokensUsedV2 用 `UPDATE col=col+?` 原子累加。
// 并发安全：MySQL / SQLite 单语句更新原子。
func (s *agentCompactV2Store) IncrementTokensUsedV2(ctx context.Context, runID uint64, deltaActual int64) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", runID).
		UpdateColumn("total_tokens_used_v2", gorm.Expr("total_tokens_used_v2 + ?", deltaActual))
	if result.Error != nil {
		return fmt.Errorf("agentCompactV2Store.IncrementTokensUsedV2(id=%d): %w", runID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentCompactV2Store.IncrementTokensUsedV2: no row matched id=%d", runID)
	}
	return nil
}

// SetContextWindowLimitV2 写 context_window_limit_v2（指针字段，传入 int 即可）。
func (s *agentCompactV2Store) SetContextWindowLimitV2(ctx context.Context, runID uint64, limit int) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", runID).
		Update("context_window_limit_v2", limit)
	if result.Error != nil {
		return fmt.Errorf("agentCompactV2Store.SetContextWindowLimitV2(id=%d): %w", runID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentCompactV2Store.SetContextWindowLimitV2: no row matched id=%d", runID)
	}
	return nil
}

// SetUseCompactV2 显式写 use_compact_v2 列。
// 用 `Update("col", val)` 而非 struct Updates 避免 default:false 零值跳过坑
// （database.md §6 — 本字段 default:false 不踩 default:true 坑，但用单列 Update
// 是更明确的写入）。
func (s *agentCompactV2Store) SetUseCompactV2(ctx context.Context, runID uint64, enabled bool) error {
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", runID).
		Update("use_compact_v2", enabled)
	if result.Error != nil {
		return fmt.Errorf("agentCompactV2Store.SetUseCompactV2(id=%d): %w", runID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentCompactV2Store.SetUseCompactV2: no row matched id=%d", runID)
	}
	return nil
}

// UpdateMessagesV2 把 V2 风格的 messages（含 uuid + meta）整列覆写到 agent_run.messages。
// V1 read 时遇到 V2 新字段直接忽略（向后兼容）。
func (s *agentCompactV2Store) UpdateMessagesV2(ctx context.Context, runID uint64, messages []compactv2.MessageV2) error {
	if messages == nil {
		messages = []compactv2.MessageV2{}
	}
	b, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("agentCompactV2Store.UpdateMessagesV2 marshal(id=%d): %w", runID, err)
	}
	result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
		Where("id = ?", runID).
		Update("messages", datatypes.JSON(b))
	if result.Error != nil {
		return fmt.Errorf("agentCompactV2Store.UpdateMessagesV2(id=%d): %w", runID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentCompactV2Store.UpdateMessagesV2: no row matched id=%d", runID)
	}
	return nil
}
