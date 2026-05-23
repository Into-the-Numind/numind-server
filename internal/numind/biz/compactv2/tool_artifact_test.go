// Package compactv2 — ProcessToolResult 单元测试。
//
// 测试覆盖 spec §设计要点 — 核心算法 与 §验证策略 中所有 case：
//   - 小输出 / 大输出 / 16KB 边界 / 二进制 / fallback 截断 / preview 格式
//
// SQLite in-memory store（newTestArtifactStore）复用 store 包测试的 DDL，
// 用 minimal mock 实现 ArtifactStore 接口，避免与 store 包测试纠缠。
//
// 注：compactv2-internal-deadcode-cleanup 后 ProcessToolResult 返回 (string, error)
// 而非 []MessageV2 —— 调用方只关心要塞入 Eino tool message Content 的字符串；
// 元数据（ToolName / Preview / OriginalSizeBytes / 等）通过解析 content 里的
// ref UUID 然后查 agent_tool_artifact 表行验证。
package compactv2

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// gormArtifactStore is the in-test concrete ArtifactStore implementation that
// hits the local SQLite DB constructed above. Production uses
// store.IAgentToolArtifactStore which satisfies this interface structurally.
type gormArtifactStore struct {
	db *gorm.DB
}

func (s *gormArtifactStore) Create(ctx context.Context, art *model.AgentToolArtifact) error {
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
	return s.db.WithContext(ctx).Model(&model.AgentToolArtifact{}).
		Where("uuid = ?", uuid).Update("is_expired", true).Error
}

func (s *gormArtifactStore) ListExpiredBefore(ctx context.Context, cutoff time.Time, limit int) ([]model.AgentToolArtifact, error) {
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
	return s.db.WithContext(ctx).Where("uuid IN ?", uuids).Delete(&model.AgentToolArtifact{}).Error
}

// failingCreateStore wraps a real ArtifactStore but always returns error on Create.
type failingCreateStore struct {
	ArtifactStore
}

func (f *failingCreateStore) Create(_ context.Context, _ *model.AgentToolArtifact) error {
	return errors.New("simulated DB failure")
}

// extractRefUUID 从 <persisted-output ref="UUID" ...> 中抽 UUID。
var refRegex = regexp.MustCompile(`<persisted-output ref="([^"]+)"`)

func extractRefUUID(t *testing.T, content string) string {
	t.Helper()
	m := refRegex.FindStringSubmatch(content)
	require.Len(t, m, 2, "content should contain <persisted-output ref=\"UUID\" ...>; got %q", content)
	return m[1]
}

// ─── tests ─────────────────────────────────────────────────────────────────

// TestProcessToolResult_SmallOutput_NoArtifact — output 15KB → 不写盘，content == output 原文，DB 无新行。
func TestProcessToolResult_SmallOutput_NoArtifact(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("a", 15*1024) // 15KB
	deps := ArtifactDeps{Store: s, DataDir: dataDir}

	content, err := ProcessToolResult(ctx, deps, 1, "call-1", "file_read", output)
	require.NoError(t, err)
	assert.Equal(t, output, content, "小输出应原样返回")

	// DB 不应当有新行
	rows, err := s.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	require.NoError(t, err)
	assert.Empty(t, rows, "小输出不应写 DB")

	// disk 不应当有 run 子目录
	_, statErr := os.Stat(filepath.Join(dataDir, "agent_artifacts", "1"))
	assert.True(t, os.IsNotExist(statErr), "小输出不应创建 run 目录")
}

// TestProcessToolResult_LargeOutput_WritesArtifact — output 50KB → DB 新增 1 行；
// content 含 <persisted-output ref=".../>"，DB 行的元数据完整。
func TestProcessToolResult_LargeOutput_WritesArtifact(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("X", 50*1024) // 50KB
	deps := ArtifactDeps{Store: s, DataDir: dataDir}

	content, err := ProcessToolResult(ctx, deps, 42, "call-large", "file_read", output)
	require.NoError(t, err)

	// content 必须含 <persisted-output ref="...">
	assert.Contains(t, content, `<persisted-output ref="`)
	assert.Contains(t, content, `tool="file_read"`)
	assert.Contains(t, content, fmt.Sprintf(`size="%d"`, 50*1024))
	assert.Contains(t, content, "</persisted-output>")

	// 从 content 解出 ref UUID
	uuid := extractRefUUID(t, content)

	// DB 行应当存在且元数据完整
	got, gerr := s.Get(ctx, uuid)
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

	// 文件应当存在且内容完整
	absPath := ArtifactAbsPath(dataDir, 42, uuid)
	body, rerr := os.ReadFile(absPath)
	require.NoError(t, rerr)
	assert.Equal(t, output, string(body))
}

// TestProcessToolResult_ExactBoundary16KB — output 恰好 16KB → 不写盘（`<=` 边界）。
func TestProcessToolResult_ExactBoundary16KB(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("x", ToolArtifactSizeLimit) // 16384
	deps := ArtifactDeps{Store: s, DataDir: dataDir}

	content, err := ProcessToolResult(ctx, deps, 1, "call-boundary", "tool_x", output)
	require.NoError(t, err)
	assert.Equal(t, output, content, "16KB 边界（==）应原样返回")

	// 确认 DB 无新行
	rows, err := s.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestProcessToolResult_BinaryInput_Base64 — output 非 UTF-8 → 写盘内容 base64 编码，
// preview 含 [binary content] 标记。
func TestProcessToolResult_BinaryInput_Base64(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	// 生成 50KB 非 UTF-8 随机字节
	raw := make([]byte, 50*1024)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	raw[0] = 0xFF
	raw[1] = 0xFE
	raw[2] = 0xFD
	output := string(raw)

	deps := ArtifactDeps{Store: s, DataDir: dataDir}
	content, err := ProcessToolResult(ctx, deps, 7, "call-bin", "bash_exec", output)
	require.NoError(t, err)
	assert.Contains(t, content, "[binary content]", "binary input 的 content preview 应当含标记")

	uuid := extractRefUUID(t, content)

	// preview 字段也应当含 [binary content] 标记
	got, gerr := s.Get(ctx, uuid)
	require.NoError(t, gerr)
	require.NotNil(t, got.Preview)
	assert.Contains(t, *got.Preview, "[binary content]")

	// 写盘内容应当是 base64 编码
	absPath := ArtifactAbsPath(dataDir, 7, uuid)
	body, rerr := os.ReadFile(absPath)
	require.NoError(t, rerr)
	for _, b := range body {
		assert.True(t, (b > 0x20 && b < 0x7F) || b == '=' || b == '+' || b == '/',
			"写盘 body 应当全部为 base64 字符，遇到 0x%02x", b)
	}
}

// TestProcessToolResult_StoreCreateFails_FallbackInline — mock store Create 永远 error
// → 返回 fallback inline 截断字符串，不返回 error，文件不留孤儿。
func TestProcessToolResult_StoreCreateFails_FallbackInline(t *testing.T) {
	base := newTestArtifactStore(t)
	failStore := &failingCreateStore{ArtifactStore: base}
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("Y", 50*1024)
	deps := ArtifactDeps{Store: failStore, DataDir: dataDir}

	content, err := ProcessToolResult(ctx, deps, 3, "call-fail-db", "file_read", output)
	require.NoError(t, err, "Create 失败不应返回 error（fallback inline）")
	assert.Contains(t, content, "Output truncated due to artifact write failure",
		"fallback content 应当含截断警告字串")
	assert.NotContains(t, content, "<persisted-output", "fallback 不应有 ref 标签")

	// disk 不应留有任何 artifact 文件
	runDir := filepath.Join(dataDir, "agent_artifacts", "3")
	entries, errStat := os.ReadDir(runDir)
	if errStat == nil {
		assert.Empty(t, entries, "fallback 后应当清理孤儿文件")
	}
	rows, _ := base.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	assert.Empty(t, rows, "fallback 后 base store 不应残留行")
}

// TestProcessToolResult_MkdirFails_FallbackInline — dataDir 指向不可创建路径 → mkdir 失败。
func TestProcessToolResult_MkdirFails_FallbackInline(t *testing.T) {
	s := newTestArtifactStore(t)
	ctx := context.Background()

	deps := ArtifactDeps{Store: s, DataDir: "/dev/null/cannot-mkdir"}

	output := strings.Repeat("Z", 50*1024)
	content, err := ProcessToolResult(ctx, deps, 1, "call-mkdir-fail", "file_read", output)
	require.NoError(t, err)
	assert.Contains(t, content, "Output truncated due to artifact write failure")
	assert.NotContains(t, content, "<persisted-output")

	rows, _ := s.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	assert.Empty(t, rows)
}

// TestProcessToolResult_WriteFileFails_FallbackInline — MkdirAll 成功但 WriteFile 失败。
func TestProcessToolResult_WriteFileFails_FallbackInline(t *testing.T) {
	s := newTestArtifactStore(t)
	ctx := context.Background()

	parent := t.TempDir()
	dataDir := filepath.Join(parent, "ro-data")
	runDir := filepath.Join(dataDir, "agent_artifacts", "7")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.Chmod(runDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o755) })

	deps := ArtifactDeps{Store: s, DataDir: dataDir}
	output := strings.Repeat("W", 50*1024)
	content, err := ProcessToolResult(ctx, deps, 7, "call-write-fail", "file_read", output)
	require.NoError(t, err, "WriteFile 失败应 fallback inline")
	assert.Contains(t, content, "Output truncated due to artifact write failure")

	rows, _ := s.ListExpiredBefore(ctx, time.Now().Add(100*24*time.Hour), 100)
	assert.Empty(t, rows, "WriteFile 失败后 DB 不应残留 artifact 行")
}

// TestProcessToolResult_NilStore_FallbackInline — Store nil 也应当 fallback 不 panic。
func TestProcessToolResult_NilStore_FallbackInline(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()
	deps := ArtifactDeps{Store: nil, DataDir: dataDir}

	output := strings.Repeat("Q", 50*1024)
	content, err := ProcessToolResult(ctx, deps, 1, "call-nil-store", "tool_x", output)
	require.NoError(t, err)
	assert.Contains(t, content, "Output truncated due to artifact write failure")
}

// TestProcessToolResult_PersistedRefFormat — 50KB output → content 含 spec 指定的 read_tool_artifact 提示。
func TestProcessToolResult_PersistedRefFormat(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	output := strings.Repeat("R", 50*1024)
	deps := ArtifactDeps{Store: s, DataDir: dataDir}

	content, err := ProcessToolResult(ctx, deps, 99, "call-fmt", "tool_x", output)
	require.NoError(t, err)

	assert.Contains(t, content, "Use read_tool_artifact tool with this ref to read more")
	assert.Contains(t, content, "51200 bytes total")
	assert.True(t, strings.HasPrefix(content, `<persisted-output ref="`),
		"content 应当以 <persisted-output ref=\" 开头")
	assert.True(t, strings.HasSuffix(content, `</persisted-output>`),
		"content 应当以 </persisted-output> 结尾")
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
