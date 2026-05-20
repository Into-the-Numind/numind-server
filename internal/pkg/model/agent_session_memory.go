package model

import "time"

// AgentSessionMemory 是 L1 短期记忆（per-agent 会话历史）。
// score 带 gorm:"default:1.0"（float zero-value gotcha）；
// Create 路径必须用 Select("*").Create(&m) 强制所有列入 INSERT。
type AgentSessionMemory struct {
	ID                      uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID                  uint       `gorm:"not null;index:idx_asm_recency,priority:1" json:"user_id"`
	AgentDefinitionID       uint64     `gorm:"not null;index:idx_asm_recency,priority:2" json:"agent_definition_id"`
	Kind                    string     `gorm:"size:20;not null" json:"kind"`
	Content                 string     `gorm:"type:text;not null" json:"content"`
	Embedding               []byte     `gorm:"type:longblob" json:"-"`
	Score                   float64    `gorm:"not null;default:1.0" json:"score"`
	SourceType              string     `gorm:"size:20;not null;default:agent" json:"source_type"`
	SourceAgentDefinitionID *uint64    `gorm:"column:source_agent_definition_id" json:"source_agent_definition_id,omitempty"`
	RecencyAt               time.Time  `gorm:"not null;index:idx_asm_recency,priority:3" json:"recency_at"`
	ExpiresAt               *time.Time `gorm:"column:expires_at" json:"expires_at,omitempty"`
	CreatedAt               time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt               time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

func (AgentSessionMemory) TableName() string { return "agent_session_memory" }
