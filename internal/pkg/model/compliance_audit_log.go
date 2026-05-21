package model

import "time"

type ComplianceAuditLog struct {
	ID                uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	AgentRunID        *uint64   `gorm:"index:idx_run" json:"agent_run_id,omitempty"`
	ParentUserID      uint      `gorm:"not null;index:idx_parent_created,priority:1" json:"parent_user_id"`
	AgentDefinitionID *uint64   `gorm:"" json:"agent_definition_id,omitempty"`
	RuleLayer         string    `gorm:"size:8;not null;index:idx_layer_decision,priority:1" json:"rule_layer"`
	RuleID            *uint64   `gorm:"" json:"rule_id,omitempty"`
	Decision          string    `gorm:"size:16;not null;index:idx_layer_decision,priority:2" json:"decision"`
	TriggeredText     string    `gorm:"type:text" json:"triggered_text,omitempty"`
	Reason            string    `gorm:"size:255" json:"reason,omitempty"`
	CreatedAt         time.Time `gorm:"not null;default:CURRENT_TIMESTAMP;index:idx_parent_created,priority:2" json:"created_at"`
}

func (ComplianceAuditLog) TableName() string { return "compliance_audit_log" }

const (
	RuleLayerL0        = "L0"
	RuleLayerL1        = "L1"
	RuleLayerL2        = "L2"
	RuleLayerInjection = "injection"
	RuleLayerFence     = "fence"
	RuleLayerScope     = "scope"

	DecisionAllow       = "allow"
	DecisionDeny        = "deny"
	DecisionSanitize    = "sanitize"
	DecisionPassthrough = "passthrough"
)
