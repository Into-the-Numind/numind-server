package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

// IToolFactoryRegistryStore defines CRUD operations for the tool_factory_registry table.
type IToolFactoryRegistryStore interface {
	Upsert(ctx context.Context, row *model.ToolFactoryRegistryRow) error
	List(ctx context.Context) ([]model.ToolFactoryRegistryRow, error)
	UpdateLoadStats(ctx context.Context, factoryID string, count int, loadedAt time.Time) error
}

type toolFactoryRegistryStore struct {
	db *gorm.DB
}

func newToolFactoryRegistryStore(db *gorm.DB) IToolFactoryRegistryStore {
	return &toolFactoryRegistryStore{db: db}
}

// Compile-time interface check.
var _ IToolFactoryRegistryStore = (*toolFactoryRegistryStore)(nil)

// Upsert inserts or updates a ToolFactoryRegistryRow by factory_id.
func (s *toolFactoryRegistryStore) Upsert(ctx context.Context, row *model.ToolFactoryRegistryRow) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "factory_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"source_type", "display_name", "config_json", "is_enabled",
				"loaded_tools_count", "last_loaded_at", "updated_at",
			}),
		}).
		Create(row).Error
}

// List returns all factory registry rows ordered by factory_id.
func (s *toolFactoryRegistryStore) List(ctx context.Context) ([]model.ToolFactoryRegistryRow, error) {
	var rows []model.ToolFactoryRegistryRow
	if err := s.db.WithContext(ctx).Order("factory_id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("toolFactoryRegistryStore.List: %w", err)
	}
	return rows, nil
}

// UpdateLoadStats records how many tools a factory loaded and when.
func (s *toolFactoryRegistryStore) UpdateLoadStats(ctx context.Context, factoryID string, count int, loadedAt time.Time) error {
	result := s.db.WithContext(ctx).Model(&model.ToolFactoryRegistryRow{}).
		Where("factory_id = ?", factoryID).
		Updates(map[string]interface{}{
			"loaded_tools_count": count,
			"last_loaded_at":     loadedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("toolFactoryRegistryStore.UpdateLoadStats(%s): %w", factoryID, result.Error)
	}
	return nil
}
