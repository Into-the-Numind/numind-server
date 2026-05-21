package model

import (
	"time"

	"gorm.io/datatypes"
)

// AgentRun 记录一次 agent 运行生命周期，messages 列 turn 级整体覆写。
// Phase 0 agent-mode #2 runtime skeleton — 唯一持久化表。
type AgentRun struct {
	ID               uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID           uint           `gorm:"not null;index:idx_ar_user_started" json:"user_id"`
	SessionID        string         `gorm:"size:64;index:idx_ar_session" json:"session_id"`
	Status           string         `gorm:"size:20;not null;default:'running';index:idx_ar_status_started" json:"status"`
	StateReason      string         `gorm:"size:50" json:"state_reason,omitempty"`
	TerminalMetadata datatypes.JSON `gorm:"type:json" json:"terminal_metadata,omitempty"`
	Messages         datatypes.JSON `gorm:"type:json;not null" json:"messages"`
	ReservationID    *uint64        `json:"reservation_id,omitempty"`
	StartedAt        time.Time      `gorm:"type:datetime(3);not null;index:idx_ar_user_started;index:idx_ar_status_started" json:"started_at"`
	EndedAt          *time.Time     `gorm:"type:datetime(3)" json:"ended_at,omitempty"`
	// #9 compact: tracks compaction state per run and stores latest summary for resume.
	CompactState   datatypes.JSON `gorm:"type:json" json:"compact_state,omitempty"`
	CompactSummary string         `gorm:"type:longtext" json:"compact_summary,omitempty"`
	// CancellationRequestedAt is set when POST /v1/admin/agent-runs/:id/cancel
	// fires. Nullable: nil means run was not admin-cancelled (#14 Phase C C3).
	CancellationRequestedAt *time.Time `gorm:"column:cancellation_requested_at" json:"cancellation_requested_at,omitempty"`
	// AgentDefinitionID is the join key to agent_definition.id; non-zero for runs
	// created after #14 deploys. Nullable for historical rows (#14 Phase C C4).
	AgentDefinitionID uint64    `gorm:"column:agent_definition_id;index:idx_ar_agent_def_id" json:"agent_definition_id,omitempty"`
	CreatedAt         time.Time `gorm:"type:datetime(3);autoCreateTime" json:"created_at"`
	UpdatedAt         time.Time `gorm:"type:datetime(3);autoUpdateTime" json:"updated_at"`
}

func (AgentRun) TableName() string { return "agent_run" }
