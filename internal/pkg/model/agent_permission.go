// Package model — agent-mode-permission-pipeline #6 表模型。
//
// 注意：AgentPermissionConfig.IsActive 是 GORM `default:true` bool 坑场景。
// 调用方 Create 时若 IsActive=false 必须走 store CreateRule 的 UpdateColumn fixup
// (见 .claude/rules/database.md §6) 才能正确持久化 false 值。
package model

import "time"

// AgentPermissionConfig — L2 租户管理员权限规则（agent-mode-permission-pipeline #6）。
//
// rule_type 取值：tool_blacklist / tool_input_regex_deny / topic_blacklist
// action 取值：deny / ask
type AgentPermissionConfig struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ParentUserID uint      `gorm:"not null;index:idx_apc_parent_active" json:"parent_user_id"`
	RuleType     string    `gorm:"size:32;not null" json:"rule_type"`
	RuleKey      string    `gorm:"size:255;not null" json:"rule_key"`
	RuleValue    string    `gorm:"type:text" json:"rule_value"`
	Action       string    `gorm:"size:16;not null;default:'deny'" json:"action"`
	Message      string    `gorm:"size:500" json:"message"`
	IsActive     bool      `gorm:"not null;default:true;index:idx_apc_parent_active" json:"is_active"`
	CreatedAt    time.Time `gorm:"not null;autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"`
}

// TableName 返回 SQL 表名。
func (AgentPermissionConfig) TableName() string { return "agent_permission_config" }

// AgentPermissionDecisionLog — 权限决策审计日志（agent-mode-permission-pipeline #6）。
//
// 异步写入路径；不阻塞 hook 返回。每次 PermissionGate.Check 都会写一条。
type AgentPermissionDecisionLog struct {
	ID                uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	AgentRunID        uint64    `gorm:"not null;index:idx_apdl_run_tool" json:"agent_run_id"`
	UserID            uint      `gorm:"not null" json:"user_id"`
	ParentUserID      uint      `gorm:"not null;index:idx_apdl_parent_created" json:"parent_user_id"`
	AgentDefinitionID uint64    `gorm:"not null" json:"agent_definition_id"`
	ToolName          string    `gorm:"size:64;not null;index:idx_apdl_run_tool" json:"tool_name"`
	ToolInputDigest   string    `gorm:"type:char(64);not null" json:"tool_input_digest"`
	Behavior          string    `gorm:"size:16;not null" json:"behavior"`
	DecisionReason    string    `gorm:"size:32;not null" json:"decision_reason"`
	ValidatorID       string    `gorm:"size:64;not null" json:"validator_id"`
	Message           string    `gorm:"type:text" json:"message"`
	LatencyMs         int       `gorm:"not null;default:0" json:"latency_ms"`
	CreatedAt         time.Time `gorm:"not null;autoCreateTime;index:idx_apdl_parent_created" json:"created_at"`
}

// TableName 返回 SQL 表名。
func (AgentPermissionDecisionLog) TableName() string { return "agent_permission_decision_log" }
