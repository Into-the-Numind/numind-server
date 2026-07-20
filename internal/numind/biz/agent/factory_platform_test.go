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
	// V1.5 task 1.4: 12 vision-equipped base tools, minus learner_data_query
	// (removed: IDOR — read any user's profile by LLM-supplied id) = 11.
	// v2 marketplace: +1 load_skill.
	// V1.5 Track 4 task 4.2/4.3/4.9: +4 create + 1 chart + 1 run_python = +6.
	// agent-output-ux-followup3 BE-2: +1 create_docx.
	// Total base tool count: 11 + 1 + 6 + 1 = 19.
	assert.Len(t, tools, 19)
	assert.Len(t, metadata, 19)
	expected := []string{
		"kb_search",
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
		"load_skill", // v2 agent-mode-v2-skill-invocation
		"create_csv",
		"create_html",
		"create_json",
		"create_text",
		"create_docx", // agent-output-ux-followup3 BE-2
		"create_png_chart",
		"run_python",
	}
	for i, want := range expected {
		assert.Equal(t, want, tools[i].Name(), "tool[%d]", i)
		assert.Equal(t, want, metadata[i].ToolName, "metadata[%d]", i)
	}
}

func TestPlatformToolFactory_LoadTools_WithDS_22Tools(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := NewPlatformToolFactory(nil, ds)
	tools, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	// V1.5 + v2 marketplace + Track 4: base count is now 19 (11 + load_skill + 4 create
	// + create_docx + chart + run_python; learner_data_query removed for IDOR); with ds,
	// memory_write + memory_read + current-user xhs_note_list are appended → total 22.
	assert.Len(t, tools, 22, "non-nil ds should produce 22 tools (19 base + memory tools + xhs_note_list)")
	assert.Len(t, metadata, 22, "non-nil ds should produce 22 metadata entries")

	// Verify the first 19 are unchanged.
	baseExpected := []string{
		"kb_search",
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
		"load_skill",
		"create_csv",
		"create_html",
		"create_json",
		"create_text",
		"create_docx",
		"create_png_chart",
		"run_python",
	}
	for i, want := range baseExpected {
		assert.Equal(t, want, tools[i].Name(), "tool[%d] name", i)
	}
	fileReader, ok := tools[8].(*fileReadTool)
	require.True(t, ok)
	require.NotNil(t, fileReader.attachmentStore, "production factory must wire the canonical attachment cache")

	// Verify memory tools are appended at indices 19 and 20.
	assert.Equal(t, "memory_write", tools[19].Name(), "tools[19] should be memory_write")
	assert.Equal(t, "memory_read", tools[20].Name(), "tools[20] should be memory_read")
	assert.Equal(t, "xhs_note_list", tools[21].Name(), "tools[21] should be xhs_note_list")
	assert.True(t, tools[21].IsReadOnly())
	assert.True(t, tools[21].IsSearchOrReadCommand())
}

func TestPlatformToolFactory_LoadTools_WithDS_Metadata14(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := NewPlatformToolFactory(nil, ds)
	_, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	// V1.5 + v2 marketplace + Track 4 + BE-2 + XHS: total is 22.
	require.Len(t, metadata, 22)

	// memory_write metadata at index 19.
	mw := metadata[19]
	assert.Equal(t, "memory_write", mw.ToolName)
	assert.Equal(t, "记忆写入", mw.DisplayName)
	assert.Equal(t, "platform", mw.Source)
	assert.Equal(t, "记忆", mw.Category)
	assert.Equal(t, "moderate", mw.RiskLevel)

	// memory_read metadata at index 20.
	mr := metadata[20]
	assert.Equal(t, "memory_read", mr.ToolName)
	assert.Equal(t, "记忆读取", mr.DisplayName)
	assert.Equal(t, "platform", mr.Source)
	assert.Equal(t, "记忆", mr.Category)
	assert.Equal(t, "", mr.RiskLevel, "memory_read has no risk level")

	xhs := metadata[21]
	assert.Equal(t, "xhs_note_list", xhs.ToolName)
	assert.Equal(t, "读取小红书选题库", xhs.DisplayName)
	assert.Equal(t, "platform", xhs.Source)
	assert.Equal(t, "小红书", xhs.Category)
	assert.Equal(t, "safe", xhs.RiskLevel)
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

// TestPlatformFactory_NoLearnerDataQueryTool is the permanent guard that the
// learner_data_query tool stays removed. The tool was deleted because it had an
// IDOR: it read any user's profile by an LLM-supplied user_id with no caller
// check. Product decision was to remove it rather than scope it. This test fails
// if anyone re-registers it in the platform factory or re-adds it to the
// safeToolBaseline.
func TestPlatformFactory_NoLearnerDataQueryTool(t *testing.T) {
	const removed = "learner_data_query"

	// 1) Not registered in the platform factory tool list (with a real ds, the
	//    fullest tool set including memory tools).
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := NewPlatformToolFactory(nil, ds)
	tools, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	for _, tool := range tools {
		assert.NotEqual(t, removed, tool.Name(), "%s tool must stay removed (IDOR)", removed)
	}
	for _, m := range metadata {
		assert.NotEqual(t, removed, m.ToolName, "%s metadata must stay removed (IDOR)", removed)
	}

	// 2) Not present in the default-on safe baseline.
	assert.NotContains(t, safeToolBaseline, removed, "%s must not be in safeToolBaseline (IDOR)", removed)
}

func TestPlatformToolFactory_Watch_Noop(t *testing.T) {
	f := NewPlatformToolFactory(nil, nil)
	if err := f.Watch(context.Background(), nil); err != nil {
		t.Error("Watch should be noop, got:", err)
	}
}
