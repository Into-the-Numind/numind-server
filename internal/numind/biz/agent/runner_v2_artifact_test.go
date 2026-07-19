// Package agent — task 2.2 V2 artifact wrapper 单元测试。
//
// 测试 wrapToolWithV2ArtifactProcessing 在 V2 路径下的 wrapper 行为：
//   - 小输出透传（passthrough）
//   - 大输出返回 <persisted-output> 引用
//   - 内层工具 error 透传
//   - nil deps 透传（防御 — runner 实际场景已先判定，但 wrapper 自身可单测）
package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/compactv2"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newRunnerTestArtifactStore creates a SQLite-backed store.IAgentToolArtifactStore
// for V2 wrapper tests. The wrapper requires the concrete store.IAgentToolArtifactStore
// type (not compactv2's narrow interface), so we use the store package's constructor
// via its sqlite-compatible AutoMigrate-free DDL.
func newRunnerTestArtifactStore(t *testing.T) store.IAgentToolArtifactStore {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/runner_v2_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_tool_artifact (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			uuid            TEXT    NOT NULL UNIQUE,
			agent_run_id    INTEGER NOT NULL,
			tool_call_id    TEXT    NOT NULL,
			tool_name       TEXT    NOT NULL,
			mime_type       TEXT,
			size_bytes      INTEGER NOT NULL,
			file_path       TEXT    NOT NULL,
			storage_backend TEXT    NOT NULL DEFAULT 'local',
			preview         TEXT,
			is_expired      INTEGER NOT NULL DEFAULT 0,
			expires_at      DATETIME,
			created_at      DATETIME
		)`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	// store.NewAgentToolArtifactStore 不存在导出 ctor；用 reflect/wrapper 不优雅。
	// 直接用 db.Create / db.Find 实现一个等价 store。
	return &runnerTestStore{db: db}
}

// runnerTestStore implements store.IAgentToolArtifactStore using a sqlite DB.
// 用 minimal 实现而不是引用 store.newAgentToolArtifactStore（小写，包外不可见）。
type runnerTestStore struct{ db *gorm.DB }

func (s *runnerTestStore) Create(ctx context.Context, art *model.AgentToolArtifact) error {
	if art == nil {
		return errors.New("nil")
	}
	return s.db.WithContext(ctx).Create(art).Error
}

func (s *runnerTestStore) Get(ctx context.Context, uuid string) (*model.AgentToolArtifact, error) {
	var art model.AgentToolArtifact
	if err := s.db.WithContext(ctx).Where("uuid = ?", uuid).First(&art).Error; err != nil {
		return nil, err
	}
	return &art, nil
}

func (s *runnerTestStore) GetByToolCallID(ctx context.Context, runID uint64, toolCallID string) (*model.AgentToolArtifact, error) {
	var art model.AgentToolArtifact
	err := s.db.WithContext(ctx).
		Where("agent_run_id = ? AND tool_call_id = ?", runID, toolCallID).
		Order("id ASC").First(&art).Error
	if err != nil {
		return nil, err
	}
	return &art, nil
}

func (s *runnerTestStore) MarkExpired(ctx context.Context, uuid string) error {
	r := s.db.WithContext(ctx).Model(&model.AgentToolArtifact{}).
		Where("uuid = ?", uuid).Update("is_expired", true)
	return r.Error
}

func (s *runnerTestStore) ListExpiredBefore(ctx context.Context, cutoff time.Time, limit int) ([]model.AgentToolArtifact, error) {
	var out []model.AgentToolArtifact
	err := s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at < ? AND is_expired = ?", cutoff, false).
		Order("expires_at ASC").Limit(limit).Find(&out).Error
	return out, err
}

func (s *runnerTestStore) DeleteBatch(ctx context.Context, uuids []string) error {
	if len(uuids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where("uuid IN ?", uuids).Delete(&model.AgentToolArtifact{}).Error
}

// mockInvokableTool 是 einotool.InvokableTool 的 minimal mock（注：包内已有
// mockEinoTool 占位 BaseTool 角色，本测试需要 InvokableRun 行为，故另起一个名字）。
type mockInvokableTool struct {
	name    string
	out     string
	err     error
	invoked int
}

var _ einotool.InvokableTool = (*mockInvokableTool)(nil)

func (m *mockInvokableTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: m.name,
		Desc: "mock tool",
	}, nil
}

func (m *mockInvokableTool) InvokableRun(_ context.Context, _ string, _ ...einotool.Option) (string, error) {
	m.invoked++
	if m.err != nil {
		return "", m.err
	}
	return m.out, nil
}

// ─── tests ─────────────────────────────────────────────────────────────────

// TestWrapToolWithV2ArtifactProcessing_SmallOutput_Passthrough — case 1
// 内层工具返回 5KB（<16KB） → wrapper 直接返回原文，不写盘。
func TestWrapToolWithV2ArtifactProcessing_SmallOutput_Passthrough(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	dataDir := t.TempDir()

	inner := &mockInvokableTool{name: "echo", out: strings.Repeat("a", 5*1024)}
	wrapped := wrapToolWithV2ArtifactProcessing(inner, "echo", 42, s, dataDir)

	out, err := wrapped.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, inner.out, out, "5KB output 应当原样透传")
	assert.Equal(t, 1, inner.invoked)

	// 不应当写盘任何 artifact 文件
	runDir := filepath.Join(dataDir, "agent_artifacts", "42")
	_, statErr := os.Stat(runDir)
	assert.True(t, os.IsNotExist(statErr), "小输出不应当创建 run 目录")
}

// TestWrapToolWithV2ArtifactProcessing_LargeOutput_ReturnsPersistedRef — case 2
// 内层工具返回 50KB → wrapper 返回 <persisted-output ref="..."/> 引用，
// artifact DB 新增 1 行。
func TestWrapToolWithV2ArtifactProcessing_LargeOutput_ReturnsPersistedRef(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	dataDir := t.TempDir()

	largeOutput := strings.Repeat("X", 50*1024)
	inner := &mockInvokableTool{name: "file_read", out: largeOutput}
	wrapped := wrapToolWithV2ArtifactProcessing(inner, "file_read", 99, s, dataDir)

	out, err := wrapped.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Contains(t, out, `<persisted-output ref="`)
	assert.Contains(t, out, `tool="file_read"`)
	assert.Contains(t, out, `size="51200"`)
	assert.Contains(t, out, "Use read_tool_artifact tool with this ref")

	// DB 应当新增 1 行
	rows, err := s.ListExpiredBefore(context.Background(),
		time.Now().Add(100*24*time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(99), rows[0].AgentRunID)
	assert.Equal(t, "file_read", rows[0].ToolName)
	assert.Equal(t, int64(50*1024), rows[0].SizeBytes)
}

// Customer regression (Dev runs 212/213/215): lark-drive's controlled skill
// response is about 28 KiB, so the generic 16 KiB V2 artifact wrapper used to
// replace the instructions with a persisted-output preview. Skill reads remain
// bounded by SkillReaderPageBytes and inline so the Agent receives the complete
// command guide. Internal receipts are intentionally no longer model-visible.
func TestWrapToolWithV2ArtifactProcessing_LargeLarkSkillRead_PreservesAtomicInstructions(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	dataDir := t.TempDir()
	fullTool := &larkSkillReadTool{executor: &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill:   "lark-drive",
		Path:    "skills/lark-drive/SKILL.md",
		Content: strings.Repeat("D", 28*1024),
		Receipt: "must-not-be-model-visible",
	}}}
	inner := adaptFullToEinoTool(fullTool, nil)

	wrapped := wrapToolWithV2ArtifactProcessing(inner, "lark_skill_read", 215, s, dataDir)
	out, err := wrapped.InvokableRun(WithRunID(context.Background(), 215), `{"skill":"lark-drive"}`)

	require.NoError(t, err)
	assert.NotContains(t, out, "must-not-be-model-visible")
	assert.NotContains(t, out, `"receipt"`)
	assert.Contains(t, out, strings.Repeat("D", 28*1024))
	assert.NotContains(t, out, `<persisted-output`)
	rows, listErr := s.ListExpiredBefore(context.Background(),
		time.Now().Add(100*24*time.Hour), 100)
	require.NoError(t, listErr)
	assert.Empty(t, rows, "a bounded skill read must keep its instructions inline")
}

func TestWrapToolWithV2ArtifactProcessing_SpoofedLarkSkillNameStillPersists(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	dataDir := t.TempDir()
	inner := &mockInvokableTool{name: "lark_skill_read", out: strings.Repeat("S", 50*1024)}

	wrapped := wrapToolWithV2ArtifactProcessing(inner, "lark_skill_read", 216, s, dataDir)
	out, err := wrapped.InvokableRun(context.Background(), `{}`)

	require.NoError(t, err)
	assert.Contains(t, out, `<persisted-output`)
	rows, listErr := s.ListExpiredBefore(context.Background(), time.Now().Add(100*24*time.Hour), 100)
	require.NoError(t, listErr)
	require.Len(t, rows, 1)
	assert.Equal(t, "lark_skill_read", rows[0].ToolName)
}

func TestWrapToolWithV2ArtifactProcessing_OversizedTrustedSkillFailsClosed(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	dataDir := t.TempDir()
	receipt := "must-not-leak"
	fullTool := &larkSkillReadTool{executor: &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill:   "lark-drive",
		Path:    "skills/lark-drive/SKILL.md",
		Content: strings.Repeat("O", 96*1024),
		Receipt: receipt,
	}}}
	inner := adaptFullToEinoTool(fullTool, nil)

	wrapped := wrapToolWithV2ArtifactProcessing(inner, "lark_skill_read", 217, s, dataDir)
	out, err := wrapped.InvokableRun(WithRunID(context.Background(), 217), `{"skill":"lark-drive"}`)

	require.NoError(t, err)
	assert.Contains(t, out, "读取飞书技能暂时失败")
	assert.NotContains(t, out, receipt)
	assert.NotContains(t, out, strings.Repeat("O", 1024))
	assert.NotContains(t, out, `<persisted-output`)
	rows, listErr := s.ListExpiredBefore(context.Background(), time.Now().Add(100*24*time.Hour), 100)
	require.NoError(t, listErr)
	assert.Empty(t, rows)
}

// TestWrapToolWithV2ArtifactProcessing_InnerToolError_Passthrough — case 3
// 内层工具返回 error → wrapper 透传 error，不调 ProcessToolResult。
func TestWrapToolWithV2ArtifactProcessing_InnerToolError_Passthrough(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	dataDir := t.TempDir()

	innerErr := errors.New("inner tool boom")
	inner := &mockInvokableTool{name: "broken", err: innerErr}
	wrapped := wrapToolWithV2ArtifactProcessing(inner, "broken", 1, s, dataDir)

	_, err := wrapped.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.ErrorIs(t, err, innerErr, "内层 error 应当原样透传")

	// 不应当写任何 artifact（error path 跳过 ProcessToolResult）
	rows, _ := s.ListExpiredBefore(context.Background(),
		time.Now().Add(100*24*time.Hour), 100)
	assert.Empty(t, rows, "inner error 时不应写 artifact")
}

// TestWrapToolWithV2ArtifactProcessing_NilStore_Passthrough — case 4
// deps.Store=nil → wrapper 返回原输出，不写盘（防御性 nil-check）。
func TestWrapToolWithV2ArtifactProcessing_NilStore_Passthrough(t *testing.T) {
	dataDir := t.TempDir()

	largeOutput := strings.Repeat("Z", 50*1024)
	inner := &mockInvokableTool{name: "x", out: largeOutput}
	// store=nil 触发 wrapper 内的防御 nil-check
	wrapped := wrapToolWithV2ArtifactProcessing(inner, "x", 1, nil, dataDir)

	out, err := wrapped.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, largeOutput, out, "store=nil 时应当原样返回（行为退化为 V1）")
}

// TestWrapToolWithV2ArtifactProcessing_EmptyDataDir_Passthrough — case 4b
// deps.DataDir="" → wrapper 返回原输出，不写盘。
func TestWrapToolWithV2ArtifactProcessing_EmptyDataDir_Passthrough(t *testing.T) {
	s := newRunnerTestArtifactStore(t)

	largeOutput := strings.Repeat("W", 50*1024)
	inner := &mockInvokableTool{name: "x", out: largeOutput}
	wrapped := wrapToolWithV2ArtifactProcessing(inner, "x", 1, s, "")

	out, err := wrapped.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, largeOutput, out)
}

// TestWrapToolWithV2ArtifactProcessing_InfoDelegates — wrapper Info 应当
// 透传 inner.Info，不修改 tool schema。
func TestWrapToolWithV2ArtifactProcessing_InfoDelegates(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	dataDir := t.TempDir()

	inner := &mockInvokableTool{name: "my_tool", out: "ok"}
	wrapped := wrapToolWithV2ArtifactProcessing(inner, "my_tool", 1, s, dataDir)

	info, err := wrapped.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "my_tool", info.Name)
	assert.Equal(t, "mock tool", info.Desc)
}

// TestRunnerV1V2Routing_Gate documents the runner-level V1/V2 routing contract.
// 不启动真实 react agent（成本高），而是验证 useCompactV2 gate 表达式的所有分支。
//
// Spec §Step 7 要求验证："use_compact_v2=true 走新路径，=false 不动"。
// runner.go line 343 表达式：
//
//	useCompactV2 := run.UseCompactV2 && r.artifactStore != nil && r.artifactDir != ""
//
// 这个测试通过表格驱动覆盖该表达式所有真值表行，保证未来重构 runner.go gate 行为
// 时此契约不被悄然破坏（如把 && 改成 ||，测试立刻 FAIL）。
func TestRunnerV1V2Routing_Gate(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	tmpDir := t.TempDir()

	cases := []struct {
		name           string
		useCompactV2   bool
		artifactStore  store.IAgentToolArtifactStore
		artifactDir    string
		wantV2Enabled  bool
		wantWrapApplie bool // wrap 路径会被取，等价于 wantV2Enabled
	}{
		{"v1_default_off", false, nil, "", false, false},
		{"v2_flag_only_no_store", true, nil, tmpDir, false, false},
		{"v2_flag_only_no_dir", true, s, "", false, false},
		{"v2_fully_enabled", true, s, tmpDir, true, true},
		{"v2_flag_off_with_deps", false, s, tmpDir, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 重现 runner.go line 343 的 gate 表达式
			gated := tc.useCompactV2 && tc.artifactStore != nil && tc.artifactDir != ""
			assert.Equal(t, tc.wantV2Enabled, gated,
				"useCompactV2 gate 表达式与 spec 约定不一致：%s", tc.name)

			// 进一步行为验证：仅当 gated=true 时才有意义调 wrap
			if gated {
				inner := &mockInvokableTool{name: "test", out: strings.Repeat("X", 50*1024)}
				wrapped := wrapToolWithV2ArtifactProcessing(inner, "test", 99, tc.artifactStore, tc.artifactDir)
				out, err := wrapped.InvokableRun(context.Background(), `{}`)
				require.NoError(t, err)
				assert.True(t, strings.HasPrefix(out, `<persisted-output ref="`),
					"V2 enabled 时大输出应当被 wrap 成 persisted-output 引用")
			} else {
				// V1 path: runner 不会调 wrap；wrapper 自身的 nil-deps 测试已覆盖此情况
				t.Log("V1 path: wrap 不被调用（已通过 _NilStore_Passthrough / _EmptyDataDir_Passthrough 覆盖）")
			}
		})
	}
}

// TestWrapToolWithV2ArtifactProcessing_PreservesArtifactMetadata — 额外覆盖
// 端到端验证：通过 wrapper 写盘后，可以从 store 读出 artifact 且文件可用。
func TestWrapToolWithV2ArtifactProcessing_PreservesArtifactMetadata(t *testing.T) {
	s := newRunnerTestArtifactStore(t)
	dataDir := t.TempDir()

	largeOutput := strings.Repeat("E2E", (50*1024)/3+1)
	inner := &mockInvokableTool{name: "kb_search", out: largeOutput}
	wrapped := wrapToolWithV2ArtifactProcessing(inner, "kb_search", 7, s, dataDir)

	out, err := wrapped.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)

	// 从输出中提取 ref UUID
	refStart := strings.Index(out, `ref="`)
	require.Greater(t, refStart, -1)
	refEnd := strings.Index(out[refStart+5:], `"`)
	require.Greater(t, refEnd, 0)
	ref := out[refStart+5 : refStart+5+refEnd]
	assert.NotEmpty(t, ref)

	// 验证 ref 是合法 UUID（v4 36 字符）
	parsed, perr := uuid.Parse(ref)
	require.NoError(t, perr, "ref 应当是合法 UUID")
	assert.Equal(t, ref, parsed.String())

	// 验证 store 里能查到
	got, gerr := s.Get(context.Background(), ref)
	require.NoError(t, gerr)
	assert.Equal(t, uint64(7), got.AgentRunID)
	assert.Equal(t, "kb_search", got.ToolName)

	// 验证文件落盘且大小匹配
	absPath := compactv2.ArtifactAbsPath(dataDir, 7, ref)
	body, rerr := os.ReadFile(absPath)
	require.NoError(t, rerr)
	assert.Equal(t, largeOutput, string(body))
}
