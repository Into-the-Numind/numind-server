package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/memory"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// newMemoryWriteTestDB creates an in-memory SQLite DB with UserGlobalMemory migrated.
func newMemoryWriteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test_mw.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	// SQLite-compat raw DDL: UserGlobalMemory uses CURRENT_TIMESTAMP(3) for MySQL ms precision.
	require.NoError(t, db.Exec(model.SQLiteCreateUserGlobalMemoryDDL).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// makeMemoryWriteTool builds a memoryWriteTool backed by a real SQLite Notepad.
func makeMemoryWriteTool(t *testing.T) (FullTool, *gorm.DB) {
	t.Helper()
	db := newMemoryWriteTestDB(t)
	np := memory.NewNotepad(store.NewUserGlobalMemoryStore(db))
	return NewMemoryWriteTool(np), db
}

// TestMemoryWriteTool_HappyPath verifies a valid write creates a DB row.
func TestMemoryWriteTool_HappyPath(t *testing.T) {
	tool, db := makeMemoryWriteTool(t)

	ctx := middleware.NewContextWithUserID(context.Background(), 1)
	input, _ := json.Marshal(memoryWriteToolInput{
		Kind:  "fact",
		Key:   "user_name",
		Value: "Alice",
	})

	result, err := tool.Execute(ctx, ToolInput(input))
	require.NoError(t, err)
	assert.Contains(t, string(result), `"ok": true`)

	// Verify the row exists in DB.
	var row model.UserGlobalMemory
	require.NoError(t, db.Where("user_id = ? AND key_name = ?", uint(1), "user_name").First(&row).Error)
	assert.Equal(t, "fact", row.Kind)
	assert.Equal(t, "agent_tool", row.SourceType)
}

// TestMemoryWriteTool_JSONInvalid verifies malformed JSON input returns an error.
func TestMemoryWriteTool_JSONInvalid(t *testing.T) {
	tool, _ := makeMemoryWriteTool(t)
	ctx := middleware.NewContextWithUserID(context.Background(), 1)

	_, err := tool.Execute(ctx, ToolInput([]byte("not-json")))
	require.Error(t, err)
}

// TestMemoryWriteTool_UserMissing verifies missing userID in context returns ErrMemoryUserRequired.
func TestMemoryWriteTool_UserMissing(t *testing.T) {
	tool, _ := makeMemoryWriteTool(t)

	input, _ := json.Marshal(memoryWriteToolInput{Kind: "fact", Key: "k", Value: "v"})
	_, err := tool.Execute(context.Background(), ToolInput(input))
	require.Error(t, err)
	assert.Equal(t, memory.ErrMemoryUserRequired, err)
}

// TestMemoryWriteTool_AgentDefIDZero verifies that when agentDefID is not in context
// (zero), source_agent_definition_id is stored as NULL.
func TestMemoryWriteTool_AgentDefIDZero(t *testing.T) {
	tool, db := makeMemoryWriteTool(t)

	ctx := middleware.NewContextWithUserID(context.Background(), 2)
	input, _ := json.Marshal(memoryWriteToolInput{Kind: "preference", Key: "theme", Value: "dark"})

	_, err := tool.Execute(ctx, ToolInput(input))
	require.NoError(t, err)

	var row model.UserGlobalMemory
	require.NoError(t, db.Where("user_id = ? AND key_name = ?", uint(2), "theme").First(&row).Error)
	assert.Nil(t, row.SourceAgentDefinitionID, "source_agent_definition_id should be NULL when agentDefID=0")
}

// TestMemoryWriteTool_AgentDefIDPresent verifies that when agentDefID is in context
// it is stored in source_agent_definition_id.
func TestMemoryWriteTool_AgentDefIDPresent(t *testing.T) {
	tool, db := makeMemoryWriteTool(t)

	ctx := middleware.NewContextWithUserID(context.Background(), 3)
	ctx = middleware.NewContextWithAgentDefinitionID(ctx, 100)
	input, _ := json.Marshal(memoryWriteToolInput{Kind: "learning", Key: "topic", Value: "math"})

	_, err := tool.Execute(ctx, ToolInput(input))
	require.NoError(t, err)

	var row model.UserGlobalMemory
	require.NoError(t, db.Where("user_id = ? AND key_name = ?", uint(3), "topic").First(&row).Error)
	require.NotNil(t, row.SourceAgentDefinitionID)
	assert.Equal(t, uint64(100), *row.SourceAgentDefinitionID)
}

// TestMemoryWriteTool_BaseTool_Methods verifies BaseTool-level method overrides.
func TestMemoryWriteTool_BaseTool_Methods(t *testing.T) {
	tool, _ := makeMemoryWriteTool(t)

	assert.Equal(t, "memory_write", tool.Name())
	assert.Equal(t, "记忆写入", tool.UserFacingName())
	assert.Equal(t, "记忆", tool.NarrationVerb())
	assert.False(t, tool.IsReadOnly(), "memory_write should not be read-only")
	assert.False(t, tool.IsDestructive(), "memory_write is not destructive (upsert)")
	assert.True(t, tool.AlwaysLoad(), "memory_write should always load")
}
