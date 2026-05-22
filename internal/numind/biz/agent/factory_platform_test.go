package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newFactoryTestDB creates an in-memory SQLite DB with UserGlobalMemory migrated,
// used by the WithDS factory tests.
func newFactoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/factory_test.db?_busy_timeout=5000&_journal_mode=WAL"
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

func TestPlatformToolFactory_LoadTools(t *testing.T) {
	// nil rag / ds: tools are instantiated but Execute is not called here,
	// so nil dependencies do not panic during construction.
	f := NewPlatformToolFactory(nil, nil)
	tools, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	assert.Len(t, tools, 10)
	assert.Len(t, metadata, 10)
	expected := []string{
		"kb_search",
		"learner_data_query",
		"document_generate",
		"image_gen",
		"bash_exec",
		"get_current_date",
		"web_search",
		"web_fetch",
		"ask_user_question",
		"file_read",
	}
	for i, want := range expected {
		assert.Equal(t, want, tools[i].Name(), "tool[%d]", i)
		assert.Equal(t, want, metadata[i].ToolName, "metadata[%d]", i)
	}
}

func TestPlatformToolFactory_LoadTools_WithDS_8Tools(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := NewPlatformToolFactory(nil, ds)
	tools, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	assert.Len(t, tools, 12, "non-nil ds should produce 12 tools (10 base + memory_write + memory_read)")
	assert.Len(t, metadata, 12, "non-nil ds should produce 12 metadata entries")

	// Verify the first 10 are unchanged.
	baseExpected := []string{
		"kb_search",
		"learner_data_query",
		"document_generate",
		"image_gen",
		"bash_exec",
		"get_current_date",
		"web_search",
		"web_fetch",
		"ask_user_question",
		"file_read",
	}
	for i, want := range baseExpected {
		assert.Equal(t, want, tools[i].Name(), "tool[%d] name", i)
	}

	// Verify memory tools are appended at indices 10 and 11.
	assert.Equal(t, "memory_write", tools[10].Name(), "tools[10] should be memory_write")
	assert.Equal(t, "memory_read", tools[11].Name(), "tools[11] should be memory_read")
}

func TestPlatformToolFactory_LoadTools_WithDS_Metadata12(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := NewPlatformToolFactory(nil, ds)
	_, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	require.Len(t, metadata, 12)

	// memory_write metadata at index 10.
	mw := metadata[10]
	assert.Equal(t, "memory_write", mw.ToolName)
	assert.Equal(t, "记忆写入", mw.DisplayName)
	assert.Equal(t, "platform", mw.Source)
	assert.Equal(t, "记忆", mw.Category)
	assert.Equal(t, "moderate", mw.RiskLevel)

	// memory_read metadata at index 11.
	mr := metadata[11]
	assert.Equal(t, "memory_read", mr.ToolName)
	assert.Equal(t, "记忆读取", mr.DisplayName)
	assert.Equal(t, "platform", mr.Source)
	assert.Equal(t, "记忆", mr.Category)
	assert.Equal(t, "", mr.RiskLevel, "memory_read has no risk level")
}

func TestPlatformToolFactory_Metadata(t *testing.T) {
	f := NewPlatformToolFactory(nil, nil).(*platformToolFactory)
	assert.Equal(t, "platform-builtin", f.FactoryID())
	assert.Equal(t, "platform", f.Source())
	assert.Equal(t, "平台内置工具", f.DisplayName())
}

func TestPlatformToolFactory_Watch_Noop(t *testing.T) {
	f := NewPlatformToolFactory(nil, nil)
	if err := f.Watch(context.Background(), nil); err != nil {
		t.Error("Watch should be noop, got:", err)
	}
}
