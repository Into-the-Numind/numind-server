package model

import "time"

// ComplianceRule represents an L1 (parent-user-level) compliance rule.
// Configurable by tenant operators; admin CRUD UI lands in #14.
// See agent-mode-compliance-3layer feature #13/14.
type ComplianceRule struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ParentUserID uint      `gorm:"not null;index:idx_parent_active_priority,priority:1" json:"parent_user_id"`
	RuleType     string    `gorm:"size:32;not null" json:"rule_type"`
	RuleText     string    `gorm:"type:text;not null" json:"rule_text"`
	Priority     int       `gorm:"not null;default:100;index:idx_parent_active_priority,priority:3" json:"priority"`
	IsActive     bool      `gorm:"not null;default:true;index:idx_parent_active_priority,priority:2" json:"is_active"`
	CreatedAt    time.Time `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"created_at"`
	UpdatedAt    time.Time `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"updated_at"`
}

func (ComplianceRule) TableName() string { return "compliance_rule" }

const (
	ComplianceRuleTypeForbidTopic  = "forbid_topic"
	ComplianceRuleTypeForbidBrand  = "forbid_brand"
	ComplianceRuleTypeForbidPhrase = "forbid_phrase"
	ComplianceRuleTypeCustom       = "custom"
)
