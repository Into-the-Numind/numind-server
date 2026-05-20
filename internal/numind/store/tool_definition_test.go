package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

func newToolDefTestStore(t *testing.T) IToolDefinitionStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// Use raw DDL instead of AutoMigrate: datetime(3) is MySQL-specific; SQLite
	// stores it as text which go-sqlite3 can't scan back into time.Time.  Plain
	// DATETIME works correctly with the go-sqlite3 driver.
	ddl := `CREATE TABLE IF NOT EXISTS tool_definition (
		id                        INTEGER PRIMARY KEY AUTOINCREMENT,
		tool_name                 TEXT NOT NULL UNIQUE,
		display_name              TEXT NOT NULL,
		description               TEXT NOT NULL,
		tool_source               TEXT NOT NULL,
		risk_level                TEXT NOT NULL DEFAULT 'safe',
		requires_sandbox          INTEGER NOT NULL DEFAULT 0,
		requires_tenant_whitelist INTEGER NOT NULL DEFAULT 0,
		input_schema              TEXT,
		output_schema             TEXT,
		is_enabled                INTEGER NOT NULL DEFAULT 1,
		is_beta                   INTEGER NOT NULL DEFAULT 0,
		category                  TEXT,
		config_json               TEXT,
		created_at                DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at                DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	require.NoError(t, db.Exec(ddl).Error)
	return newToolDefinitionStore(db)
}

func TestToolDefinitionStore_Upsert_Insert(t *testing.T) {
	s := newToolDefTestStore(t)
	def := &model.ToolDefinition{
		ToolName: "kb_search", DisplayName: "知识库检索",
		Description: "...", ToolSource: "platform", Category: "RAG",
	}
	require.NoError(t, s.Upsert(context.Background(), def))

	got, err := s.Get(context.Background(), "kb_search")
	require.NoError(t, err)
	assert.Equal(t, "知识库检索", got.DisplayName)
}

func TestToolDefinitionStore_Upsert_UpdateExisting(t *testing.T) {
	s := newToolDefTestStore(t)
	// Insert initial
	require.NoError(t, s.Upsert(context.Background(), &model.ToolDefinition{
		ToolName: "kb", DisplayName: "v1", Description: "d1", ToolSource: "platform",
	}))
	// Operator disables it
	require.NoError(t, s.SetEnabled(context.Background(), "kb", false))

	// Upsert again with new display name; should NOT reset is_enabled
	require.NoError(t, s.Upsert(context.Background(), &model.ToolDefinition{
		ToolName: "kb", DisplayName: "v2", Description: "d2", ToolSource: "platform",
	}))

	got, err := s.Get(context.Background(), "kb")
	require.NoError(t, err)
	assert.Equal(t, "v2", got.DisplayName)
	assert.False(t, got.IsEnabled, "Upsert must NOT reset operator-set is_enabled")
}

func TestToolDefinitionStore_Get_NotFound(t *testing.T) {
	s := newToolDefTestStore(t)
	_, err := s.Get(context.Background(), "nonexistent")
	require.Error(t, err)
}

func TestToolDefinitionStore_ListEnabled(t *testing.T) {
	s := newToolDefTestStore(t)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, s.Upsert(context.Background(), &model.ToolDefinition{
			ToolName: name, DisplayName: name, Description: ".", ToolSource: "platform",
			IsEnabled: true,
		}))
	}
	require.NoError(t, s.SetEnabled(context.Background(), "b", false))

	enabled, err := s.ListEnabled(context.Background())
	require.NoError(t, err)
	assert.Len(t, enabled, 2)
	names := []string{enabled[0].ToolName, enabled[1].ToolName}
	assert.ElementsMatch(t, []string{"a", "c"}, names)
}

func TestToolDefinitionStore_ListBySource(t *testing.T) {
	s := newToolDefTestStore(t)
	require.NoError(t, s.Upsert(context.Background(), &model.ToolDefinition{
		ToolName: "p1", DisplayName: "p1", Description: ".", ToolSource: "platform",
	}))
	require.NoError(t, s.Upsert(context.Background(), &model.ToolDefinition{
		ToolName: "m1", DisplayName: "m1", Description: ".", ToolSource: "mcp",
	}))

	plat, err := s.ListBySource(context.Background(), "platform")
	require.NoError(t, err)
	assert.Len(t, plat, 1)
	assert.Equal(t, "p1", plat[0].ToolName)
}

// TestToolDefinitionStore_DefaultTrueGotcha documents the GORM default:true bool
// Create behaviour (see database.md §6). is_enabled has default:true in the model,
// so an explicit false on Create is silently overridden by GORM. SetEnabled() is
// the correct way to set false after Insert.
func TestToolDefinitionStore_DefaultTrueGotcha(t *testing.T) {
	s := newToolDefTestStore(t)
	def := &model.ToolDefinition{
		ToolName: "x", DisplayName: "x", Description: ".", ToolSource: "platform",
		IsEnabled: false, // <- explicit false; GORM default:true overrides this on Create
	}
	require.NoError(t, s.Upsert(context.Background(), def))
	got, err := s.Get(context.Background(), "x")
	require.NoError(t, err)
	// Documented limitation: is_enabled=false is overridden by DB default:true on INSERT.
	assert.True(t, got.IsEnabled, "documented limitation: GORM default:true overrides explicit false on Create")

	// Explicit SetEnabled call correctly persists false.
	require.NoError(t, s.SetEnabled(context.Background(), "x", false))
	got2, err := s.Get(context.Background(), "x")
	require.NoError(t, err)
	assert.False(t, got2.IsEnabled)
}

func TestToolDefinitionStore_JSONFields(t *testing.T) {
	s := newToolDefTestStore(t)
	def := &model.ToolDefinition{
		ToolName: "j", DisplayName: "j", Description: ".", ToolSource: "platform",
		InputSchema: datatypes.JSON(`{"type":"object"}`),
		ConfigJSON:  datatypes.JSON(`{"rate_limit":100}`),
	}
	require.NoError(t, s.Upsert(context.Background(), def))
	got, err := s.Get(context.Background(), "j")
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object"}`, string(got.InputSchema))
	assert.JSONEq(t, `{"rate_limit":100}`, string(got.ConfigJSON))
}
