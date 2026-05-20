package model

import (
	"time"

	"gorm.io/datatypes"
)

// ToolDefinition maps to the tool_definition table.
// tool_source: platform | mcp | cli | webhook
// risk_level:  safe | moderate | dangerous
type ToolDefinition struct {
	ID                      uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	ToolName                string         `gorm:"size:128;not null;uniqueIndex" json:"tool_name"`
	DisplayName             string         `gorm:"size:128;not null" json:"display_name"`
	Description             string         `gorm:"type:text;not null" json:"description"`
	ToolSource              string         `gorm:"size:16;not null;index:idx_source_enabled,priority:1" json:"tool_source"`
	RiskLevel               string         `gorm:"size:16;not null;default:'safe'" json:"risk_level"`
	RequiresSandbox         bool           `gorm:"not null;default:false" json:"requires_sandbox"`
	RequiresTenantWhitelist bool           `gorm:"not null;default:false" json:"requires_tenant_whitelist"`
	InputSchema             datatypes.JSON `gorm:"type:json" json:"input_schema,omitempty"`
	OutputSchema            datatypes.JSON `gorm:"type:json" json:"output_schema,omitempty"`
	IsEnabled               bool           `gorm:"not null;default:true;index:idx_source_enabled,priority:2" json:"is_enabled"`
	IsBeta                  bool           `gorm:"not null;default:false" json:"is_beta"`
	Category                string         `gorm:"size:64" json:"category,omitempty"`
	ConfigJSON              datatypes.JSON `gorm:"type:json" json:"config_json,omitempty"`
	CreatedAt               time.Time      `gorm:"type:datetime(3);autoCreateTime" json:"created_at"`
	UpdatedAt               time.Time      `gorm:"type:datetime(3);autoUpdateTime" json:"updated_at"`
}

func (ToolDefinition) TableName() string { return "tool_definition" }
