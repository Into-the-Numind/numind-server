// Package compactv2 — task 2.2 ProcessToolResult 单元测试。
//
// 测试覆盖 spec §设计要点 — 核心算法 与 §验证策略 中所有 case：
//   - 小输出 / 大输出 / 16KB 边界 / 二进制 / fallback 截断 / preview 格式
//
// SQLite in-memory store（newTestArtifactStore）复用 store 包测试的 DDL，
// 用 minimal mock 实现 ArtifactStore 接口，避免与 store 包测试纠缠。
package compactv2

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newTestArtifactStore creates a SQLite in-memory artifact store for compactv2
// tests. Uses a hand-rolled DDL identical to store/agent_tool_artifact_test.go
// to avoid AutoMigrate's datetime(3) precision issue.
func newTestArtifactStore(t *testing.T) ArtifactStore {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/compactv2_artifact_test.db?_busy_timeout=5000&_journal_mode=WAL"
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
	return &gormArtifactStore{db: db}
}

// gormArtifactStore 是用于本测试包的 ArtifactStore 实现，与 store.agentToolArtifactStore
// 完全等价但不 import store 包（避免 import cycle）。
type gormArtifactStore struct {
	db *gorm.DB
}

func (s *gormArtifactStore) Create(ctx context.Context, art *model.AgentToolArtifact) error {
	if art == nil {
		return errors.New("nil")
	}
	return s.db.WithContext(ctx).Create(art).Error
}

func (s *gormArtifactStore) Get(ctx context.Context, uuid string) (*model.AgentToolArtifact, error) {
	var art model.AgentToolArtifact
	if err := s.db.WithContext(ctx).Where("uuid = ?", uuid).First(&art).Error; err != nil {
		return nil, err
	}
	return &art, nil
}

func (s *gormArtifactStore) GetByToolCallID(ctx context.Context, runID uint64, toolCallID string) (*model.AgentToolArtifact, error) {
	var art model.AgentToolArtifact
	err := s.db.WithContext(ctx).
		Where("agent_run_id = ? AND tool_call_id = ?", runID, toolCallID).
		Order("id ASC").First(&art).Error
	if err != nil {
		return nil, err
	}
	return &art, nil
}

func (s *gormArtifactStore) MarkExpired(ctx context.Context, uuid string) error {
	r := s.db.WithContext(ctx).Model(&model.AgentToolArtifact{}).
		Where("uuid = ?", uuid).Update("is_expired", true)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return errors.New("not found")
	}
	return nil
}

func (s *gormArtifactStore) ListExpiredBefore(ctx context.Context, cutoff time.Time, limit int) ([]model.AgentToolArtifact, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []model.AgentToolArtifact
	err := s.db.WithContext(ctx).
		Where("expires_at IS NOT NULL AND expires_at < ? AND is_expired = ?", cutoff, false).
		Order("expires_at ASC").Limit(limit).Find(&out).Error
	return out, err
}

func (s *gormArtifactStore) DeleteBatch(ctx context.Context, uuids []string) error {
	if len(uuids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Where("uuid IN ?", uuids).
		Delete(&model.AgentToolArtifact{}).Error
}

// failingCreateStore wraps gormArtifactStore so Create always errors. Used to
// exercise the fallback inline truncation path (case 5).
type failingCreateStore struct {
	ArtifactStore
}

func (f *failingCreateStore) Create(_ context.Context, _ *model.AgentToolArtifact) error {
	return errors.New("simulated DB failure")
}

// ─── tests ─────────────────────────────────────────────────────────────────

// TestProcessToolResult_SmallOutput_NoArtifact — case 1
// output 15KB → 不写盘，返回单条 MessageV2，content == output 原文，DB 无新行。
func TestProcessToolResult_SmallOutput_NoArtifact(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("a", 15*1024) // 15KB
	deps := ArtifactDeps{Store: s, DataDir: dataDir}

	msgs, err := ProcessToolResult(ctx, deps, 1, "call-1", "file_read", output)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "tool", msgs[0].Role)
	assert.Equal(t, "call-1", msgs[0].ToolCallID)
	assert.Equal(t, output, msgs[0].Content)
	assert.Nil(t, msgs[0].Meta, "Meta 应当为 nil（不进入 L0 写盘路径）")

	// DB 不应当有新行（不计算 store 内部状态）
	rows, err := s.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	require.NoError(t, err)
	assert.Empty(t, rows, "小输出不应写 DB")

	// disk 不应当有 run 子目录
	_, statErr := os.Stat(filepath.Join(dataDir, "agent_artifacts", "1"))
	assert.True(t, os.IsNotExist(statErr), "小输出不应创建 run 目录")
}

// TestProcessToolResult_LargeOutput_WritesArtifact — case 2
// output 50KB → DB 新增 1 行；返回 MessageV2.Content 含 <persisted-output ... />，
// Meta.IsCompacted=true / CompactionPhase=L0，preview ≤ 1024 字节。
func TestProcessToolResult_LargeOutput_WritesArtifact(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("X", 50*1024) // 50KB
	deps := ArtifactDeps{Store: s, DataDir: dataDir}

	msgs, err := ProcessToolResult(ctx, deps, 42, "call-large", "file_read", output)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	m := msgs[0]
	assert.Equal(t, "tool", m.Role)
	assert.Equal(t, "call-large", m.ToolCallID)

	// content 必须含 <persisted-output ref="...">
	assert.Contains(t, m.Content, `<persisted-output ref="`)
	assert.Contains(t, m.Content, `tool="file_read"`)
	assert.Contains(t, m.Content, fmt.Sprintf(`size="%d"`, 50*1024))
	assert.Contains(t, m.Content, "</persisted-output>")

	// Meta 必须填充
	require.NotNil(t, m.Meta)
	assert.True(t, m.Meta.IsCompacted)
	assert.Equal(t, "L0", m.Meta.CompactionPhase)
	assert.Equal(t, int64(50*1024), m.Meta.OriginalSizeBytes)
	assert.NotEmpty(t, m.Meta.ArtifactRef)
	assert.Equal(t, "file_read", m.Meta.ToolName)
	assert.NotEmpty(t, m.Meta.Preview)
	// preview <= 1024 bytes（与常量对齐）
	assert.LessOrEqual(t, len(m.Meta.Preview), ArtifactPreviewBytes)

	// DB 行应当存在
	got, gerr := s.Get(ctx, m.Meta.ArtifactRef)
	require.NoError(t, gerr)
	assert.Equal(t, uint64(42), got.AgentRunID)
	assert.Equal(t, "call-large", got.ToolCallID)
	assert.Equal(t, "file_read", got.ToolName)
	assert.Equal(t, int64(50*1024), got.SizeBytes)
	assert.Equal(t, "local", got.StorageBackend)
	require.NotNil(t, got.Preview)
	assert.LessOrEqual(t, len(*got.Preview), ArtifactPreviewBytes)
	require.NotNil(t, got.ExpiresAt)
	assert.False(t, got.IsExpired)

	// 文件应当存在
	absPath := ArtifactAbsPath(dataDir, 42, m.Meta.ArtifactRef)
	body, rerr := os.ReadFile(absPath)
	require.NoError(t, rerr)
	assert.Equal(t, output, string(body))
}

// TestProcessToolResult_ExactBoundary16KB — case 3
// output 恰好 16KB → 不写盘（`<=` 边界）。
func TestProcessToolResult_ExactBoundary16KB(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("x", ToolArtifactSizeLimit) // 16384
	deps := ArtifactDeps{Store: s, DataDir: dataDir}

	msgs, err := ProcessToolResult(ctx, deps, 1, "call-boundary", "tool_x", output)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, output, msgs[0].Content)
	assert.Nil(t, msgs[0].Meta, "16KB 边界（==）不应写盘")

	// 确认 DB 无新行
	rows, err := s.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestProcessToolResult_BinaryInput_Base64 — case 4
// output 非 UTF-8 → 写盘内容 base64 编码，preview 含 [binary content] 标记。
func TestProcessToolResult_BinaryInput_Base64(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	// 生成 50KB 非 UTF-8 随机字节
	raw := make([]byte, 50*1024)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	// 故意写入一个肯定无效的 UTF-8 序列（顶层 0xFF 单字节）
	raw[0] = 0xFF
	raw[1] = 0xFE
	raw[2] = 0xFD
	output := string(raw)

	deps := ArtifactDeps{Store: s, DataDir: dataDir}
	msgs, err := ProcessToolResult(ctx, deps, 7, "call-bin", "bash_exec", output)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	m := msgs[0]
	require.NotNil(t, m.Meta)

	// preview 含 [binary content] 标记
	assert.Contains(t, m.Meta.Preview, "[binary content]",
		"二进制 input 的 preview 应当含 [binary content] 标记")
	assert.Contains(t, m.Content, "[binary content]")

	// 写盘内容应当是 base64 编码（即 ascii printable）
	absPath := ArtifactAbsPath(dataDir, 7, m.Meta.ArtifactRef)
	body, rerr := os.ReadFile(absPath)
	require.NoError(t, rerr)
	for _, b := range body {
		assert.True(t, b > 0x20 && b < 0x7F || b == '=' || b == '+' || b == '/',
			"写盘 body 应当全部为 base64 字符，遇到 0x%02x", b)
	}
}

// TestProcessToolResult_StoreCreateFails_FallbackInline — case 5
// mock store Create 永远 error → 返回 fallback inline 截断 message，
// 不返回 error，文件不留孤儿。
func TestProcessToolResult_StoreCreateFails_FallbackInline(t *testing.T) {
	base := newTestArtifactStore(t)
	failStore := &failingCreateStore{ArtifactStore: base}
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("Y", 50*1024)
	deps := ArtifactDeps{Store: failStore, DataDir: dataDir}

	msgs, err := ProcessToolResult(ctx, deps, 3, "call-fail-db", "file_read", output)
	require.NoError(t, err, "Create 失败不应返回 error（fallback inline）")
	require.Len(t, msgs, 1)
	m := msgs[0]
	assert.Equal(t, "tool", m.Role)
	// fallback 应当含截断警告字串
	assert.Contains(t, m.Content, "Output truncated due to artifact write failure",
		"fallback 应当含截断警告字串")
	assert.Nil(t, m.Meta, "fallback 不应产生 Meta（无 artifact ref）")

	// disk 不应留有任何 artifact 文件（Create 失败后已清理）
	runDir := filepath.Join(dataDir, "agent_artifacts", "3")
	entries, errStat := os.ReadDir(runDir)
	if errStat == nil {
		assert.Empty(t, entries, "fallback 后应当清理孤儿文件")
	}
	// 也确认 base store 没有新行
	rows, _ := base.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	assert.Empty(t, rows, "fallback 后 base store 不应残留行")
}

// TestProcessToolResult_MkdirFails_FallbackInline — case 6
// dataDir 指向一个不可创建的路径 → mkdir 失败 → fallback inline 截断。
func TestProcessToolResult_MkdirFails_FallbackInline(t *testing.T) {
	s := newTestArtifactStore(t)
	ctx := context.Background()

	// /dev/null 是文件不是目录；MkdirAll 在其下创建子路径会失败。
	// 用 /dev/null/x 作为 dataDir 触发 MkdirAll error。
	deps := ArtifactDeps{Store: s, DataDir: "/dev/null/cannot-mkdir"}

	output := strings.Repeat("Z", 50*1024)
	msgs, err := ProcessToolResult(ctx, deps, 1, "call-mkdir-fail", "file_read", output)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Content, "Output truncated due to artifact write failure")
	assert.Nil(t, msgs[0].Meta)

	// DB 不应当新增行
	rows, _ := s.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	assert.Empty(t, rows)
}

// TestProcessToolResult_PersistedRefFormat — case 7
// 50KB output → 解析返回的 ref content，确认含 spec 指定的 read_tool_artifact 提示。
func TestProcessToolResult_PersistedRefFormat(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("R", 50*1024)
	deps := ArtifactDeps{Store: s, DataDir: dataDir}

	msgs, err := ProcessToolResult(ctx, deps, 99, "call-fmt", "tool_x", output)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	c := msgs[0].Content

	// 引用提示（spec §设计要点 — Message 引用格式）
	assert.Contains(t, c, "Use read_tool_artifact tool with this ref to read more")
	// 含 size 数字
	assert.Contains(t, c, "51200 bytes total")
	// XML 包裹标签
	assert.True(t, strings.HasPrefix(c, `<persisted-output ref="`),
		"content 应当以 <persisted-output ref=\" 开头")
	assert.True(t, strings.HasSuffix(c, `</persisted-output>`),
		"content 应当以 </persisted-output> 结尾")
}

// TestProcessToolResult_WriteFileFails_FallbackInline — spec §验证策略 case 5
// （"写盘磁盘满 mock"路径）：MkdirAll 成功但 WriteFile 写入失败。
// 用一个 read-only 子目录触发 WriteFile EACCES。
//
// 与 _MkdirFails_ 分开测：spec 明确把"磁盘满"独立列为 case 5，与"mkdir 失败"语义不同。
func TestProcessToolResult_WriteFileFails_FallbackInline(t *testing.T) {
	s := newTestArtifactStore(t)
	ctx := context.Background()

	// 准备一个 read-only 的 dataDir：MkdirAll 在已存在的只读路径下创建子路径会成功
	// （路径已存在），但随后 WriteFile 会因目录不可写而 EACCES。
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "ro-data")
	// 预先建好 <DataDir>/agent_artifacts/<run_id>/ 目录
	runDir := filepath.Join(dataDir, "agent_artifacts", "7")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	// 然后把 runDir 改成只读 — WriteFile 在其中 create 文件会 EACCES
	require.NoError(t, os.Chmod(runDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o755) }) // 让 TempDir cleanup 可以删

	deps := ArtifactDeps{Store: s, DataDir: dataDir}
	output := strings.Repeat("W", 50*1024)
	msgs, err := ProcessToolResult(ctx, deps, 7, "call-write-fail", "file_read", output)
	require.NoError(t, err, "WriteFile 失败应 fallback inline，不返回 error")
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Content, "Output truncated due to artifact write failure",
		"fallback 应含截断警告字串")
	assert.Nil(t, msgs[0].Meta, "fallback 不应产生 Meta")

	// DB 不应新增行
	rows, _ := s.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	assert.Empty(t, rows, "WriteFile 失败后 DB 不应残留 artifact 行")
}

// TestProcessToolResult_NilStore_FallbackInline 额外覆盖 — Store nil 也应当 fallback
// 而不是 panic。文档化行为以防未来 wiring 误传 nil。
func TestProcessToolResult_NilStore_FallbackInline(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()
	deps := ArtifactDeps{Store: nil, DataDir: dataDir}

	output := strings.Repeat("Q", 50*1024)
	msgs, err := ProcessToolResult(ctx, deps, 1, "call-nil-store", "tool_x", output)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Content, "Output truncated due to artifact write failure")
	assert.Nil(t, msgs[0].Meta)
}

// TestSafePreviewUTF8 单独验证 UTF-8 边界回退逻辑（被 ProcessToolResult 间接使用）。
func TestSafePreviewUTF8(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{"empty input zero limit", "", 0, ""},
		{"empty input positive limit", "", 10, ""},
		{"short input", "abc", 10, "abc"},
		{"exact length", "abcdef", 6, "abcdef"},
		{"truncate ascii", "abcdef", 3, "abc"},
		// 中文：每个汉字 3 字节；limit=4 应回退到 limit=3（1 个汉字）
		{"truncate cjk fallback", "你好世界", 4, "你"},
		{"negative limit", "abc", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safePreviewUTF8(tt.input, tt.limit)
			assert.Equal(t, tt.want, got)
		})
	}
}
