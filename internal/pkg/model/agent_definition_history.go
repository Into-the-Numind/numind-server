package model

import (
	"time"

	"gorm.io/datatypes"
)

// AgentDefinitionHistory 记录 agent_definition 的每次版本快照（append-only）。
// UNIQUE(agent_id, version) 防止重复版本写入。
type AgentDefinitionHistory struct {
	ID             uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	AgentID        uint64         `gorm:"type:bigint unsigned;not null;uniqueIndex:uniq_adh_agent_version,priority:1;index:idx_adh_agent_created,priority:1" json:"agent_id"`
	Version        uint           `gorm:"type:int unsigned;not null;uniqueIndex:uniq_adh_agent_version,priority:2" json:"version"`
	Snapshot       datatypes.JSON `gorm:"not null" json:"snapshot"`
	ChangesSummary string         `gorm:"size:200" json:"changes_summary"`
	CreatedBy      uint           `gorm:"type:int unsigned;not null" json:"created_by"`
	CreatedAt      time.Time      `gorm:"type:datetime;not null;default:CURRENT_TIMESTAMP(3);autoCreateTime;index:idx_adh_agent_created,priority:2" json:"created_at"`
}

func (AgentDefinitionHistory) TableName() string { return "agent_definition_history" }
