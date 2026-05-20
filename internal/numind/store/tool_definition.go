package store

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

// IToolDefinitionStore defines CRUD operations for the tool_definition table.
type IToolDefinitionStore interface {
	Upsert(ctx context.Context, def *model.ToolDefinition) error
	Get(ctx context.Context, toolName string) (*model.ToolDefinition, error)
	ListEnabled(ctx context.Context) ([]model.ToolDefinition, error)
	ListBySource(ctx context.Context, source string) ([]model.ToolDefinition, error)
	SetEnabled(ctx context.Context, toolName string, enabled bool) error
}

type toolDefinitionStore struct {
	db *gorm.DB
}

func newToolDefinitionStore(db *gorm.DB) IToolDefinitionStore {
	return &toolDefinitionStore{db: db}
}

// Compile-time interface check.
var _ IToolDefinitionStore = (*toolDefinitionStore)(nil)

// Upsert inserts or updates a ToolDefinition by tool_name.
// On conflict it updates metadata columns but intentionally skips is_enabled / is_beta
// to preserve operator-managed flags set via the admin UI.
func (s *toolDefinitionStore) Upsert(ctx context.Context, def *model.ToolDefinition) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tool_name"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"display_name", "description", "input_schema", "output_schema",
				"risk_level", "requires_sandbox", "requires_tenant_whitelist",
				"category", "config_json", "updated_at",
			}),
		}).
		Create(def).Error
}

// Get returns a ToolDefinition by tool_name.
func (s *toolDefinitionStore) Get(ctx context.Context, toolName string) (*model.ToolDefinition, error) {
	var def model.ToolDefinition
	if err := s.db.WithContext(ctx).Where("tool_name = ?", toolName).First(&def).Error; err != nil {
		return nil, fmt.Errorf("toolDefinitionStore.Get(%s): %w", toolName, err)
	}
	return &def, nil
}

// ListEnabled returns all enabled tool definitions ordered by tool_name.
func (s *toolDefinitionStore) ListEnabled(ctx context.Context) ([]model.ToolDefinition, error) {
	var defs []model.ToolDefinition
	if err := s.db.WithContext(ctx).Where("is_enabled = ?", true).Order("tool_name ASC").Find(&defs).Error; err != nil {
		return nil, fmt.Errorf("toolDefinitionStore.ListEnabled: %w", err)
	}
	return defs, nil
}

// ListBySource returns all tool definitions with the given tool_source, ordered by tool_name.
func (s *toolDefinitionStore) ListBySource(ctx context.Context, source string) ([]model.ToolDefinition, error) {
	var defs []model.ToolDefinition
	if err := s.db.WithContext(ctx).Where("tool_source = ?", source).Order("tool_name ASC").Find(&defs).Error; err != nil {
		return nil, fmt.Errorf("toolDefinitionStore.ListBySource(%s): %w", source, err)
	}
	return defs, nil
}

// SetEnabled updates the is_enabled flag for a single tool_name.
func (s *toolDefinitionStore) SetEnabled(ctx context.Context, toolName string, enabled bool) error {
	result := s.db.WithContext(ctx).Model(&model.ToolDefinition{}).
		Where("tool_name = ?", toolName).
		Update("is_enabled", enabled)
	if result.Error != nil {
		return fmt.Errorf("toolDefinitionStore.SetEnabled(%s): %w", toolName, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("toolDefinitionStore.SetEnabled: no row matched tool_name=%s", toolName)
	}
	return nil
}
