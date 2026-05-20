package model

import "time"

// AgentSandboxSession 沙箱会话生命周期审计行（#4 sandbox-integration）。
// PreToolCall 写入 status=running，PostToolCall 更新 status=terminated/failed + exit_code + ended_at。
// agent_run_id 在 #4 阶段允许 NULL（hook 内 ctx 取不到 runID 时降级），#11/#12 必填。
type AgentSandboxSession struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      uint       `gorm:"not null;index:idx_ass_user_started" json:"user_id"`
	AgentRunID  *uint64    `gorm:"index:idx_ass_run" json:"agent_run_id,omitempty"`
	ContainerID string     `gorm:"size:128;not null" json:"container_id"`
	ImageTag    string     `gorm:"size:128;not null;default:python:3.11-slim" json:"image_tag"`
	Status      string     `gorm:"size:20;not null;default:running;index:idx_ass_status" json:"status"`
	MemLimitMB  int        `gorm:"not null;default:512" json:"mem_limit_mb"`
	CPUQuota    float64    `gorm:"type:decimal(3,1);not null;default:1.0" json:"cpu_quota"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	ErrorMsg    string     `gorm:"type:text" json:"error_msg,omitempty"`
	StartedAt   time.Time  `gorm:"not null;index:idx_ass_user_started;default:CURRENT_TIMESTAMP(3)" json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	CreatedAt   time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"updated_at"`
}

// TableName 指定表名为 agent_sandbox_session。
func (AgentSandboxSession) TableName() string { return "agent_sandbox_session" }
