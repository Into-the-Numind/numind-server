package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

// newMemoryReadTestDB creates an in-memory SQLite DB for memory_read tests.
func newMemoryReadTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test_mr.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.UserGlobalMemory{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// makeMemoryReadTool builds a memoryReadTool backed by a real SQLite Notepad.
func makeMemoryReadTool(t *testing.T) (FullTool, memory.Notepad) {
	t.Helper()
	db := newMemoryReadTestDB(t)
	np := memory.NewNotepad(store.NewUserGlobalMemoryStore(db))
	return NewMemoryReadTool(np), np
}

// writeEntry is a test helper that writes a memory entry via Notepad.
func writeEntry(t *testing.T, np memory.Notepad, userID uint, kind memory.MemoryKind, key, value string) {
	t.Helper()
	err := np.Write(context.Background(), userID, kind, key, value, memory.WriteOpts{})
	require.NoError(t, err)
}

// TestMemoryReadTool_ByKey_Found verifies reading by key returns the stored value.
func TestMemoryReadTool_ByKey_Found(t *testing.T) {
	tool, np := makeMemoryReadTool(t)
	writeEntry(t, np, 10, memory.KindFact, "user_city", "Beijing")

	ctx := middleware.NewContextWithUserID(context.Background(), 10)
	input, _ := json.Marshal(memoryReadToolInput{Key: "user_city"})

	result, err := tool.Execute(ctx, ToolInput(input))
	require.NoError(t, err)

	var out []memoryReadOutItem
	require.NoError(t, json.Unmarshal(result, &out))
	require.Len(t, out, 1)
	assert.Equal(t, "user_city", out[0].Key)
	assert.Equal(t, "Beijing", out[0].Value)
	assert.Equal(t, "fact", out[0].Kind)
	assert.False(t, out[0].CreatedAt.IsZero())
}

// TestMemoryReadTool_ByKey_NotFound verifies reading a missing key returns an empty array.
func TestMemoryReadTool_ByKey_NotFound(t *testing.T) {
	tool, _ := makeMemoryReadTool(t)

	ctx := middleware.NewContextWithUserID(context.Background(), 10)
	input, _ := json.Marshal(memoryReadToolInput{Key: "does_not_exist"})

	result, err := tool.Execute(ctx, ToolInput(input))
	require.NoError(t, err)

	var out []memoryReadOutItem
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Empty(t, out)
}

// TestMemoryReadTool_ByKind verifies listing by kind returns correct entries.
func TestMemoryReadTool_ByKind(t *testing.T) {
	tool, np := makeMemoryReadTool(t)
	writeEntry(t, np, 20, memory.KindPreference, "lang", "Go")
	writeEntry(t, np, 20, memory.KindPreference, "editor", "Vim")
	writeEntry(t, np, 20, memory.KindFact, "age", "30") // different kind, should not appear

	ctx := middleware.NewContextWithUserID(context.Background(), 20)
	input, _ := json.Marshal(memoryReadToolInput{Kind: "preference", Limit: 10})

	result, err := tool.Execute(ctx, ToolInput(input))
	require.NoError(t, err)

	var out []memoryReadOutItem
	require.NoError(t, json.Unmarshal(result, &out))
	require.Len(t, out, 2)
	for _, item := range out {
		assert.Equal(t, "preference", item.Kind)
	}
}

// TestMemoryReadTool_UnescapeValue verifies HTML entities in stored values are
// unescaped before being returned to the LLM.
func TestMemoryReadTool_UnescapeValue(t *testing.T) {
	tool, np := makeMemoryReadTool(t)
	// Notepad.Write escapes the raw value before storing: "<script>" → "&lt;script&gt;"
	writeEntry(t, np, 30, memory.KindFact, "html_key", "<script>alert(1)</script>")

	ctx := middleware.NewContextWithUserID(context.Background(), 30)
	input, _ := json.Marshal(memoryReadToolInput{Key: "html_key"})

	result, err := tool.Execute(ctx, ToolInput(input))
	require.NoError(t, err)

	var out []memoryReadOutItem
	require.NoError(t, json.Unmarshal(result, &out))
	require.Len(t, out, 1)
	// After UnescapeForToolResponse the LLM should see the original string.
	assert.Equal(t, "<script>alert(1)</script>", out[0].Value)
}

// TestMemoryReadTool_LimitClamp verifies that out-of-range limits are clamped to 10.
func TestMemoryReadTool_LimitClamp(t *testing.T) {
	tool, np := makeMemoryReadTool(t)
	// Write 60 entries.
	for i := 0; i < 60; i++ {
		key := "key_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		writeEntry(t, np, 40, memory.KindLearning, key, "value")
	}

	ctx := middleware.NewContextWithUserID(context.Background(), 40)

	// Limit 0 → clamped to 10.
	input0, _ := json.Marshal(memoryReadToolInput{Kind: "learning", Limit: 0})
	result0, err := tool.Execute(ctx, ToolInput(input0))
	require.NoError(t, err)
	var out0 []memoryReadOutItem
	require.NoError(t, json.Unmarshal(result0, &out0))
	assert.LessOrEqual(t, len(out0), 10, "limit=0 should be clamped to 10")

	// Limit 999 → clamped to 10.
	input999, _ := json.Marshal(memoryReadToolInput{Kind: "learning", Limit: 999})
	result999, err := tool.Execute(ctx, ToolInput(input999))
	require.NoError(t, err)
	var out999 []memoryReadOutItem
	require.NoError(t, json.Unmarshal(result999, &out999))
	assert.LessOrEqual(t, len(out999), 10, "limit=999 should be clamped to 10")

	// Limit 5 → exactly 5.
	input5, _ := json.Marshal(memoryReadToolInput{Kind: "learning", Limit: 5})
	result5, err := tool.Execute(ctx, ToolInput(input5))
	require.NoError(t, err)
	var out5 []memoryReadOutItem
	require.NoError(t, json.Unmarshal(result5, &out5))
	assert.Equal(t, 5, len(out5), "limit=5 should return exactly 5")
}

// TestMemoryReadTool_CrossUserIsolation verifies user A cannot read user B's memories.
func TestMemoryReadTool_CrossUserIsolation(t *testing.T) {
	tool, np := makeMemoryReadTool(t)
	// Write an entry for userID=50.
	writeEntry(t, np, 50, memory.KindFact, "secret", "user50_data")

	// Read as userID=51 — should get empty result.
	ctx := middleware.NewContextWithUserID(context.Background(), 51)
	input, _ := json.Marshal(memoryReadToolInput{Key: "secret"})

	result, err := tool.Execute(ctx, ToolInput(input))
	require.NoError(t, err)

	var out []memoryReadOutItem
	require.NoError(t, json.Unmarshal(result, &out))
	assert.Empty(t, out, "user 51 should not see user 50's memories")
}

// TestMemoryReadTool_BaseTool_Methods verifies BaseTool-level method overrides.
func TestMemoryReadTool_BaseTool_Methods(t *testing.T) {
	// Use a nil notepad — metadata methods don't call it.
	tool := NewMemoryReadTool(nil)

	assert.Equal(t, "memory_read", tool.Name())
	assert.Equal(t, "记忆读取", tool.UserFacingName())
	assert.Equal(t, "查阅", tool.NarrationVerb())
	assert.True(t, tool.IsReadOnly(), "memory_read should be read-only (BaseTool default)")
	assert.False(t, tool.IsDestructive())
	assert.True(t, tool.AlwaysLoad())
	assert.True(t, tool.IsSearchOrReadCommand())
	assert.NotEmpty(t, tool.Description())
}

// Ensure memoryReadOutItem is used so the import of "time" is satisfied
// when no explicit time.Time is referenced directly in test assertions.
var _ = time.Time{}
