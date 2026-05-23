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
	// V1.5 task 4.2 + 4.3: base tool count is now 17 (12 + 4 create + 1 chart).
	assert.Len(t, tools, 17)
	assert.Len(t, metadata, 17)
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
		"analyze_image",
		"annotate_image",
		"create_csv",
		"create_html",
		"create_json",
		"create_text",
		"create_png_chart",
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
	// V1.5 task 4.2 + 4.3: base count is now 17 (12 + 4 create + 1 chart);
	// with ds, memory_write + memory_read are appended → total 19.
	assert.Len(t, tools, 19, "non-nil ds should produce 19 tools (17 base + memory_write + memory_read)")
	assert.Len(t, metadata, 19, "non-nil ds should produce 19 metadata entries")

	// Verify the first 17 are unchanged.
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
		"analyze_image",
		"annotate_image",
		"create_csv",
		"create_html",
		"create_json",
		"create_text",
		"create_png_chart",
	}
	for i, want := range baseExpected {
		assert.Equal(t, want, tools[i].Name(), "tool[%d] name", i)
	}

	// Verify memory tools are appended at indices 17 and 18.
	assert.Equal(t, "memory_write", tools[17].Name(), "tools[17] should be memory_write")
	assert.Equal(t, "memory_read", tools[18].Name(), "tools[18] should be memory_read")
}

func TestPlatformToolFactory_LoadTools_WithDS_Metadata14(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := NewPlatformToolFactory(nil, ds)
	_, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	// V1.5 task 4.2 + 4.3: total is 19 (17 base + memory_write + memory_read).
	require.Len(t, metadata, 19)

	// memory_write metadata at index 17.
	mw := metadata[17]
	assert.Equal(t, "memory_write", mw.ToolName)
	assert.Equal(t, "记忆写入", mw.DisplayName)
	assert.Equal(t, "platform", mw.Source)
	assert.Equal(t, "记忆", mw.Category)
	assert.Equal(t, "moderate", mw.RiskLevel)

	// memory_read metadata at index 18.
	mr := metadata[18]
	assert.Equal(t, "memory_read", mr.ToolName)
	assert.Equal(t, "记忆读取", mr.DisplayName)
	assert.Equal(t, "platform", mr.Source)
	assert.Equal(t, "记忆", mr.Category)
	assert.Equal(t, "", mr.RiskLevel, "memory_read has no risk level")
}

// TestPlatformToolFactory_LoadTools_IncludesSimpleCreateTools verifies that all 4 task-4.2
// file-generation tools are present in the registry after LoadAll.
func TestPlatformToolFactory_LoadTools_IncludesSimpleCreateTools(t *testing.T) {
	f := NewPlatformToolFactory(nil, nil)
	tools, _, err := f.LoadTools(context.Background())
	require.NoError(t, err)

	// Build a name-to-tool map for O(1) lookup.
	toolMap := make(map[string]FullTool, len(tools))
	for _, tool := range tools {
		toolMap[tool.Name()] = tool
	}

	for _, name := range []string{"create_csv", "create_html", "create_json", "create_text"} {
		tool, ok := toolMap[name]
		require.True(t, ok, "tool %q must be registered", name)
		assert.NotNil(t, tool)
		// Verify basic properties.
		assert.True(t, tool.IsEnabled(ToolConfig{}), "%s.IsEnabled must be true", name)
		assert.False(t, tool.IsReadOnly(), "%s.IsReadOnly must be false (file generation writes)", name)
	}
}

// TestPlatformToolFactory_LoadTools_IncludesPNGChartTool verifies that the task-4.3
// create_png_chart tool is registered in the platform factory.
func TestPlatformToolFactory_LoadTools_IncludesPNGChartTool(t *testing.T) {
	f := NewPlatformToolFactory(nil, nil)
	tools, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)

	var found FullTool
	var foundMeta *ToolMetadata
	for i, tool := range tools {
		if tool.Name() == "create_png_chart" {
			found = tool
			foundMeta = &metadata[i]
			break
		}
	}
	require.NotNil(t, found, "create_png_chart must be registered")
	require.NotNil(t, foundMeta)
	assert.True(t, found.IsEnabled(ToolConfig{}), "create_png_chart.IsEnabled must be true")
	assert.False(t, found.IsReadOnly(), "create_png_chart writes (not read-only)")
	assert.Equal(t, "可视化", foundMeta.Category)
	assert.Equal(t, "safe", foundMeta.RiskLevel)
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
