package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// IAgentPermissionStore defines persistence for agent-mode-permission-pipeline #6:
//   - agent_permission_config (L2 租户管理员规则；TenantAdminRuleValidator 每次工具调用查)
//   - agent_permission_decision_log (审计日志；异步写入)
type IAgentPermissionStore interface {
	// ListActiveByParent 返回某父账户下所有 is_active=true 的规则，按 id 升序。
	ListActiveByParent(ctx context.Context, parentUserID uint) ([]model.AgentPermissionConfig, error)

	// CreateRule 用于测试 fixture 或 #10 admin 端 CRUD。
	// 内置 GORM `default:true` bool fixup：若 wantActive=false 而 Create 后被 default 写回 true，
	// 自动用 UpdateColumn 修正到 false（database.md §6 模式，与 agent_definition_store 对齐）。
	CreateRule(ctx context.Context, rule *model.AgentPermissionConfig) error

	// CreateDecisionLog 写一条审计日志（同步 INSERT，audit goroutine 内调用）。
	CreateDecisionLog(ctx context.Context, log *model.AgentPermissionDecisionLog) error
}

type agentPermissionStore struct {
	db *gorm.DB
}

var _ IAgentPermissionStore = (*agentPermissionStore)(nil)

func newAgentPermissionStore(db *gorm.DB) IAgentPermissionStore {
	return &agentPermissionStore{db: db}
}

// ListActiveByParent — TenantAdminRuleValidator 每次工具调用查；
// 父账户范围 + is_active=true 双过滤；按 id 升序确保规则评估顺序确定。
func (s *agentPermissionStore) ListActiveByParent(ctx context.Context, parentUserID uint) ([]model.AgentPermissionConfig, error) {
	var rules []model.AgentPermissionConfig
	err := s.db.WithContext(ctx).
		Where("parent_user_id = ? AND is_active = ?", parentUserID, true).
		Order("id ASC").
		Find(&rules).Error
	if err != nil {
		return nil, fmt.Errorf("ListActiveByParent: %w", err)
	}
	return rules, nil
}

// CreateRule — GORM default:true bool Create 坑修复（database.md §6）。
// 调用方意图 IsActive=false 时，捕获在 Create 前 wantActive 局部变量；
// Create 后如 GORM 把 struct.IsActive 写回 true，立即用 UpdateColumn 修正 DB + struct。
func (s *agentPermissionStore) CreateRule(ctx context.Context, rule *model.AgentPermissionConfig) error {
	wantActive := rule.IsActive // 在 Create 前捕获意图
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		return fmt.Errorf("CreateRule: %w", err)
	}
	// GORM 可能把 struct.IsActive 写回 DB default（true）
	if !wantActive && rule.IsActive {
		if err := s.db.WithContext(ctx).Model(rule).UpdateColumn("is_active", false).Error; err != nil {
			return fmt.Errorf("CreateRule UpdateColumn fixup: %w", err)
		}
		rule.IsActive = false
	}
	return nil
}

// CreateDecisionLog — 异步 audit goroutine 调用；同步 INSERT，错误由调用方 zap.Warn 处理。
func (s *agentPermissionStore) CreateDecisionLog(ctx context.Context, log *model.AgentPermissionDecisionLog) error {
	return s.db.WithContext(ctx).Create(log).Error
}
