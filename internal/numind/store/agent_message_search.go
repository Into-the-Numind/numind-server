package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// SearchOpts 控制 agent_message_search 表 FULLTEXT 查询的过滤、分页参数。
//
// UserID 强制必填 — Search 方法会硬过滤 WHERE user_id = ?，跨 user 严格隔离。
// Query 走 MySQL 8 ngram FULLTEXT MATCH ... AGAINST 子句。
type SearchOpts struct {
	UserID    uint
	Query     string
	SessionID string
	DateFrom  *time.Time
	DateTo    *time.Time
	Limit     int // default 20, max 100
	Offset    int
}

// SearchHit 是 search store 层的查询命中行（biz 层会包装成对外 SearchResult）。
type SearchHit struct {
	ID          uint64    `gorm:"column:id"`
	MessageUUID string    `gorm:"column:message_uuid"`
	AgentRunID  uint64    `gorm:"column:agent_run_id"`
	SessionID   string    `gorm:"column:session_id"`
	Role        string    `gorm:"column:role"`
	Content     string    `gorm:"column:content"`
	Score       float64   `gorm:"column:score"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// IAgentMessageSearchStore 定义 agent_message_search 表的存取接口。
type IAgentMessageSearchStore interface {
	// BulkInsert 批量写入 search 行；写入失败由 biz 层 log warn 不阻塞主流程。
	BulkInsert(ctx context.Context, rows []model.AgentMessageSearch) error
	// Search 执行 MySQL 8 FULLTEXT MATCH AGAINST 查询；强制 WHERE user_id = ?。
	// 返回 hits, total, error。
	Search(ctx context.Context, opts SearchOpts) ([]SearchHit, int64, error)
	// DeleteByRunID 删除某 agent_run 的全部 search 行（agent_run 物理删除时调用）。
	DeleteByRunID(ctx context.Context, runID uint64) error
	// CountByUser 统计某 user 的 search 行数；监控 / 健康检查用。
	CountByUser(ctx context.Context, userID uint) (int64, error)
	// GetMessageUUIDsByRun 返回某 agent_run 已索引的 message_uuid 集合（用于 diff）。
	GetMessageUUIDsByRun(ctx context.Context, runID uint64) ([]string, error)
}

type agentMessageSearchStore struct {
	db *gorm.DB
}

var _ IAgentMessageSearchStore = (*agentMessageSearchStore)(nil)

// NewAgentMessageSearchStore 构造 IAgentMessageSearchStore 实例。
func NewAgentMessageSearchStore(db *gorm.DB) IAgentMessageSearchStore {
	return &agentMessageSearchStore{db: db}
}

// BulkInsert 批量写入 search 行；空 rows no-op。
// 使用 ON DUPLICATE KEY UPDATE 行为不必要 — message_uuid 全局唯一 + diff-by-uuid 已在 biz
// 层保证不会重插同一条 message。但为防御性写入用 OnConflict.DoNothing。
func (s *agentMessageSearchStore) BulkInsert(ctx context.Context, rows []model.AgentMessageSearch) error {
	if len(rows) == 0 {
		return nil
	}
	// 防御性：若 biz 层 diff 漏掉，DB UNIQUE 约束兜底；OnConflict DoNothing 避免 fail。
	// GORM v2 不支持 SQLite OnConflict + INSERT IGNORE 跨方言完全一致；这里直接
	// CreateInBatches(rows, 100) 走标准 INSERT，让 DB UNIQUE 约束错误浮上来；
	// biz 层 BulkInsert 会捕获 unique constraint err 后 log warn 不阻塞。
	if err := s.db.WithContext(ctx).CreateInBatches(rows, 100).Error; err != nil {
		return fmt.Errorf("agentMessageSearchStore.BulkInsert: %w", err)
	}
	return nil
}

// Search 执行 FULLTEXT MATCH AGAINST 查询。
//
// SQL：
//
//	SELECT id, message_uuid, agent_run_id, session_id, role, content,
//	       MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) AS score,
//	       created_at
//	FROM agent_message_search
//	WHERE user_id = ?
//	  AND MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE)
//	  [AND session_id = ?]
//	  [AND created_at BETWEEN ? AND ?]
//	ORDER BY score DESC, created_at DESC
//	LIMIT ? OFFSET ?;
//
// MySQL 8 ngram parser (n=2) 双字符 token，对中文短词检索友好。
// 同条件 COUNT(*) 查 total。
//
// SQLite 后端：不支持 MATCH AGAINST 语法，会返回 error。测试中相关 case 用 mock 或 skip。
func (s *agentMessageSearchStore) Search(ctx context.Context, opts SearchOpts) ([]SearchHit, int64, error) {
	if opts.UserID == 0 {
		return nil, 0, errors.New("agentMessageSearchStore.Search: UserID required (cross-user isolation enforced)")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	// SQLite (test) fallback path: emulate FULLTEXT via LIKE so tests without
	// MATCH AGAINST aren't completely blocked. Production MySQL takes the FULLTEXT
	// path. We detect dialect by db.Dialector.Name().
	isMySQL := strings.EqualFold(s.db.Dialector.Name(), "mysql")

	// Build base SELECT.
	tx := s.db.WithContext(ctx).Table("agent_message_search").Where("user_id = ?", opts.UserID)

	hasQuery := strings.TrimSpace(opts.Query) != ""
	if hasQuery {
		if isMySQL {
			tx = tx.Where("MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE)", opts.Query)
		} else {
			// SQLite fallback — LIKE %query% (does not preserve FULLTEXT semantics
			// but keeps non-search test paths runnable).
			tx = tx.Where("content LIKE ?", "%"+opts.Query+"%")
		}
	}
	if opts.SessionID != "" {
		tx = tx.Where("session_id = ?", opts.SessionID)
	}
	if opts.DateFrom != nil {
		tx = tx.Where("created_at >= ?", *opts.DateFrom)
	}
	if opts.DateTo != nil {
		tx = tx.Where("created_at <= ?", *opts.DateTo)
	}

	// COUNT total (separate query — clone the WHERE state).
	countTx := s.db.WithContext(ctx).Table("agent_message_search").Where("user_id = ?", opts.UserID)
	if hasQuery {
		if isMySQL {
			countTx = countTx.Where("MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE)", opts.Query)
		} else {
			countTx = countTx.Where("content LIKE ?", "%"+opts.Query+"%")
		}
	}
	if opts.SessionID != "" {
		countTx = countTx.Where("session_id = ?", opts.SessionID)
	}
	if opts.DateFrom != nil {
		countTx = countTx.Where("created_at >= ?", *opts.DateFrom)
	}
	if opts.DateTo != nil {
		countTx = countTx.Where("created_at <= ?", *opts.DateTo)
	}

	var total int64
	if err := countTx.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("agentMessageSearchStore.Search count: %w", err)
	}

	// Build SELECT projection. MySQL: include MATCH ... AS score; SQLite: synthesise 0.
	var selectExpr string
	var selectArgs []interface{}
	if hasQuery && isMySQL {
		selectExpr = "id, message_uuid, agent_run_id, session_id, role, content, " +
			"MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) AS score, created_at"
		selectArgs = []interface{}{opts.Query}
	} else {
		selectExpr = "id, message_uuid, agent_run_id, session_id, role, content, " +
			"0.0 AS score, created_at"
	}

	var hits []SearchHit
	if err := tx.
		Select(selectExpr, selectArgs...).
		Order("score DESC, created_at DESC").
		Offset(opts.Offset).
		Limit(limit).
		Find(&hits).Error; err != nil {
		return nil, 0, fmt.Errorf("agentMessageSearchStore.Search find: %w", err)
	}

	return hits, total, nil
}

// DeleteByRunID 删除某 agent_run 的全部 search 行。
func (s *agentMessageSearchStore) DeleteByRunID(ctx context.Context, runID uint64) error {
	if err := s.db.WithContext(ctx).
		Where("agent_run_id = ?", runID).
		Delete(&model.AgentMessageSearch{}).Error; err != nil {
		return fmt.Errorf("agentMessageSearchStore.DeleteByRunID(runID=%d): %w", runID, err)
	}
	return nil
}

// CountByUser 统计某 user 的 search 行数；监控 / 健康检查用。
func (s *agentMessageSearchStore) CountByUser(ctx context.Context, userID uint) (int64, error) {
	var n int64
	if err := s.db.WithContext(ctx).
		Model(&model.AgentMessageSearch{}).
		Where("user_id = ?", userID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("agentMessageSearchStore.CountByUser(userID=%d): %w", userID, err)
	}
	return n, nil
}

// GetMessageUUIDsByRun 返回某 agent_run 已索引的 message_uuid 集合（用于 diff）。
func (s *agentMessageSearchStore) GetMessageUUIDsByRun(ctx context.Context, runID uint64) ([]string, error) {
	var uuids []string
	if err := s.db.WithContext(ctx).
		Model(&model.AgentMessageSearch{}).
		Where("agent_run_id = ?", runID).
		Pluck("message_uuid", &uuids).Error; err != nil {
		return nil, fmt.Errorf("agentMessageSearchStore.GetMessageUUIDsByRun(runID=%d): %w", runID, err)
	}
	return uuids, nil
}
