package model

import (
	"time"

	"gorm.io/datatypes"
)

// ToolFactoryRegistryRow maps to the tool_factory_registry table.
// source_type: platform | mcp | cli | webhook
type ToolFactoryRegistryRow struct {
	ID               uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	FactoryID        string         `gorm:"size:64;not null;uniqueIndex" json:"factory_id"`
	SourceType       string         `gorm:"size:16;not null" json:"source_type"`
	DisplayName      string         `gorm:"size:128;not null" json:"display_name"`
	ConfigJSON       datatypes.JSON `gorm:"type:json" json:"config_json,omitempty"`
	IsEnabled        bool           `gorm:"not null;default:true" json:"is_enabled"`
	LoadedToolsCount int            `gorm:"not null;default:0" json:"loaded_tools_count"`
	LastLoadedAt     *time.Time     `gorm:"type:datetime(3)" json:"last_loaded_at,omitempty"`
	CreatedAt        time.Time      `gorm:"type:datetime(3);autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time      `gorm:"type:datetime(3);autoUpdateTime" json:"updated_at"`
}

func (ToolFactoryRegistryRow) TableName() string { return "tool_factory_registry" }
