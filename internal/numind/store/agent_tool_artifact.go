package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// IAgentToolArtifactStore 是 Agent Mode V1.5 板块 2 task 2.1 新增的 V2 专用存储接口，
// 用于 L0 tool result 写盘后的元数据管理（path / size / preview / expiry）。
//
// 文件内容写到 <data_dir>/agent_artifacts/<run_id>/<artifact_uuid>，本接口仅管理 DB 元数据。
// 真正的 file 落盘 + read_tool_artifact 工具由 task 2.2 实现。
type IAgentToolArtifactStore interface {
	// Create 插入一条 artifact 元数据。
	// art.UUID 必须由调用方填充（建议 ULID，时间有序，cleanup 扫表友好）。
	Create(ctx context.Context, art *model.AgentToolArtifact) error

	// Get 按 uuid 取一条。未找到返回 gorm.ErrRecordNotFound 链。
	Get(ctx context.Context, uuid string) (*model.AgentToolArtifact, error)

	// GetByToolCallID 按 (agent_run_id, tool_call_id) 取一条。
	// 命中 idx_ata_run_tool_call 索引。
	// 同一对 (run_id, tool_call_id) 理论上唯一，多条时返回最早一条（id 升序）。
	GetByToolCallID(ctx context.Context, runID uint64, toolCallID string) (*model.AgentToolArtifact, error)

	// MarkExpired 按 uuid 标记 is_expired=true。
	// 不删文件、不删 row；由 cleanup cron（task 2.2）DeleteBatch 物理清理。
	MarkExpired(ctx context.Context, uuid string) error

	// ListExpiredBefore 列出 expires_at < cutoff 且 is_expired=false 的 artifact，
	// limit<=0 默认 100。cleanup cron 按此结果先 MarkExpired，再下一轮 DeleteBatch。
	ListExpiredBefore(ctx context.Context, cutoff time.Time, limit int) ([]model.AgentToolArtifact, error)

	// DeleteBatch 按 uuid 列表批量物理删除 DB row。
	// 调用方在 DeleteBatch 之前应当先确保对应 file 已从 disk 删除（task 2.2 cleanup cron 职责）。
	// 空列表返回 nil。
	DeleteBatch(ctx context.Context, uuids []string) error
}

type agentToolArtifactStore struct {
	db *gorm.DB
}

func newAgentToolArtifactStore(db *gorm.DB) IAgentToolArtifactStore {
	return &agentToolArtifactStore{db: db}
}

var _ IAgentToolArtifactStore = (*agentToolArtifactStore)(nil)

// Create 插入一条 artifact 元数据。
func (s *agentToolArtifactStore) Create(ctx context.Context, art *model.AgentToolArtifact) error {
	if art == nil {
		return fmt.Errorf("agentToolArtifactStore.Create: art must not be nil")
	}
	if art.UUID == "" {
		return fmt.Errorf("agentToolArtifactStore.Create: art.UUID must not be empty")
	}
	if err := s.db.WithContext(ctx).Create(art).Error; err != nil {
		return fmt.Errorf("agentToolArtifactStore.Create: %w", err)
	}
	return nil
}

// Get 按 uuid 取一条。
func (s *agentToolArtifactStore) Get(ctx context.Context, uuid string) (*model.AgentToolArtifact, error) {
	var art model.AgentToolArtifact
	if err := s.db.WithContext(ctx).Where("uuid = ?", uuid).First(&art).Error; err != nil {
		return nil, fmt.Errorf("agentToolArtifactStore.Get(uuid=%s): %w", uuid, err)
	}
	return &art, nil
}

// GetByToolCallID 按 (run_id, tool_call_id) 取一条。
// 同一对理论上唯一；若多条，取 id 最小（早写入的）。
func (s *agentToolArtifactStore) GetByToolCallID(ctx context.Context, runID uint64, toolCallID string) (*model.AgentToolArtifact, error) {
	var art model.AgentToolArtifact
	err := s.db.WithContext(ctx).
		Where("agent_run_id = ? AND tool_call_id = ?", runID, toolCallID).
		Order("id ASC").
		First(&art).Error
	if err != nil {
		return nil, fmt.Errorf("agentToolArtifactStore.GetByToolCallID(run=%d, tcid=%s): %w", runID, toolCallID, err)
	}
	return &art, nil
}

// MarkExpired 按 uuid 标记 is_expired=true。
func (s *agentToolArtifactStore) MarkExpired(ctx context.Context, uuid string) error {
	result := s.db.WithContext(ctx).Model(&model.AgentToolArtifact{}).
		Where("uuid = ?", uuid).
		Update("is_expired", true)
	if result.Error != nil {
		return fmt.Errorf("agentToolArtifactStore.MarkExpired(uuid=%s): %w", uuid, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("agentToolArtifactStore.MarkExpired: no row matched uuid=%s", uuid)
	}
	return nil
}

// ListExpiredBefore 列出 expires_at < cutoff 且 is_expired=false 的 artifact。
func (s *agentToolArtifactStore) ListExpiredBefore(ctx context.Context, cutoff time.Time, limit int) ([]model.AgentToolArtifact, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []model.AgentToolArtifact
	// `Find` 不返回 ErrRecordNotFound，所以无需对 ErrRecordNotFound 特判。
	err := s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at < ? AND is_expired = ?", cutoff, false).
		Order("expires_at ASC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("agentToolArtifactStore.ListExpiredBefore: %w", err)
	}
	return out, nil
}

// DeleteBatch 按 uuid 列表批量物理删除。
func (s *agentToolArtifactStore) DeleteBatch(ctx context.Context, uuids []string) error {
	if len(uuids) == 0 {
		return nil
	}
	result := s.db.WithContext(ctx).
		Where("uuid IN ?", uuids).
		Delete(&model.AgentToolArtifact{})
	if result.Error != nil {
		return fmt.Errorf("agentToolArtifactStore.DeleteBatch: %w", result.Error)
	}
	return nil
}
