package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

func newToolFactoryTestStore(t *testing.T) IToolFactoryRegistryStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// Use raw DDL: datetime(3) is MySQL-specific; plain DATETIME works with go-sqlite3.
	ddl := `CREATE TABLE IF NOT EXISTS tool_factory_registry (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		factory_id          TEXT NOT NULL UNIQUE,
		source_type         TEXT NOT NULL,
		display_name        TEXT NOT NULL,
		config_json         TEXT,
		is_enabled          INTEGER NOT NULL DEFAULT 1,
		loaded_tools_count  INTEGER NOT NULL DEFAULT 0,
		last_loaded_at      DATETIME,
		created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at          DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	require.NoError(t, db.Exec(ddl).Error)
	return newToolFactoryRegistryStore(db)
}

func TestToolFactoryRegistryStore_Upsert_Insert(t *testing.T) {
	s := newToolFactoryTestStore(t)
	row := &model.ToolFactoryRegistryRow{
		FactoryID:   "platform-builtin",
		SourceType:  "platform",
		DisplayName: "Platform Built-in Tools",
		IsEnabled:   true,
	}
	require.NoError(t, s.Upsert(context.Background(), row))

	rows, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "platform-builtin", rows[0].FactoryID)
	assert.Equal(t, "Platform Built-in Tools", rows[0].DisplayName)
}

func TestToolFactoryRegistryStore_Upsert_UpdateExisting(t *testing.T) {
	s := newToolFactoryTestStore(t)
	// Initial insert
	require.NoError(t, s.Upsert(context.Background(), &model.ToolFactoryRegistryRow{
		FactoryID:   "mcp-server-1",
		SourceType:  "mcp",
		DisplayName: "MCP Server v1",
		IsEnabled:   true,
	}))

	// Upsert with updated display name
	require.NoError(t, s.Upsert(context.Background(), &model.ToolFactoryRegistryRow{
		FactoryID:   "mcp-server-1",
		SourceType:  "mcp",
		DisplayName: "MCP Server v2",
		IsEnabled:   true,
	}))

	rows, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "MCP Server v2", rows[0].DisplayName)
}

func TestToolFactoryRegistryStore_List_MultipleRows(t *testing.T) {
	s := newToolFactoryTestStore(t)
	for _, fid := range []string{"factory-b", "factory-a", "factory-c"} {
		require.NoError(t, s.Upsert(context.Background(), &model.ToolFactoryRegistryRow{
			FactoryID:   fid,
			SourceType:  "platform",
			DisplayName: fid,
			IsEnabled:   true,
		}))
	}

	rows, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 3)
	// Must be ordered by factory_id ASC
	assert.Equal(t, "factory-a", rows[0].FactoryID)
	assert.Equal(t, "factory-b", rows[1].FactoryID)
	assert.Equal(t, "factory-c", rows[2].FactoryID)
}

func TestToolFactoryRegistryStore_UpdateLoadStats(t *testing.T) {
	s := newToolFactoryTestStore(t)
	require.NoError(t, s.Upsert(context.Background(), &model.ToolFactoryRegistryRow{
		FactoryID:   "factory-x",
		SourceType:  "platform",
		DisplayName: "X",
		IsEnabled:   true,
	}))

	loadedAt := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.UpdateLoadStats(context.Background(), "factory-x", 42, loadedAt))

	rows, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 42, rows[0].LoadedToolsCount)
	require.NotNil(t, rows[0].LastLoadedAt)
	assert.Equal(t, loadedAt.UTC(), rows[0].LastLoadedAt.UTC())
}

func TestToolFactoryRegistryStore_UpdateLoadStats_NoOp_MissingFactory(t *testing.T) {
	s := newToolFactoryTestStore(t)
	// UpdateLoadStats on non-existent factory should not error (RowsAffected=0 is allowed)
	err := s.UpdateLoadStats(context.Background(), "nonexistent", 0, time.Now())
	require.NoError(t, err)
}

func TestToolFactoryRegistryStore_ConfigJSON(t *testing.T) {
	s := newToolFactoryTestStore(t)
	require.NoError(t, s.Upsert(context.Background(), &model.ToolFactoryRegistryRow{
		FactoryID:   "cfg-factory",
		SourceType:  "mcp",
		DisplayName: "cfg",
		IsEnabled:   true,
		ConfigJSON:  datatypes.JSON(`{"endpoint":"http://localhost:8080"}`),
	}))

	rows, err := s.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"endpoint":"http://localhost:8080"}`, string(rows[0].ConfigJSON))
}
