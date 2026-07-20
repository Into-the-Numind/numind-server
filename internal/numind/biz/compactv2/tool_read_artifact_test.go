// Package compactv2 — task 2.2 ReadArtifactTool.InvokableRun 单元测试。
//
// 覆盖 spec §设计要点 边界 case + §验证策略 read_tool_artifact 各 case：
//   - 成功读取 / 分页 / offset 超 / limit clamp / UUID 不存在 / 过期 / 跨用户 / 磁盘丢失
package compactv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// fakeRunStore 是 AgentRunReader 的 minimal mock，按 ID 返回预置 user_id。
// nil run / error 由字段控制以模拟各种异常路径。
type fakeRunStore struct {
	runs map[uint64]*model.AgentRun
	err  error
}

// transientMissArtifactStore reproduces Dev run 263: the artifact file and
// metadata have been created, but the first immediate lookup observes a
// transient record-not-found before the same UUID becomes readable.
type transientMissArtifactStore struct {
	ArtifactStore
	missUUID string
	getCalls int
}

func (s *transientMissArtifactStore) Get(ctx context.Context, artifactUUID string) (*model.AgentToolArtifact, error) {
	s.getCalls++
	if artifactUUID == s.missUUID && s.getCalls == 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return s.ArtifactStore.Get(ctx, artifactUUID)
}

func (f *fakeRunStore) Get(_ context.Context, id uint64) (*model.AgentRun, error) {
	if f.err != nil {
		return nil, f.err
	}
	r, ok := f.runs[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return r, nil
}

// userIDExtractor 用 closure 实现 UserIDExtractor — 测试时直接返回固定 user_id。
func userIDExtractor(uid uint, ok bool) UserIDExtractor {
	return func(_ context.Context) (uint, bool) { return uid, ok }
}

// makeArtifactOnDisk 在 dataDir/agent_artifacts/<runID>/<uuid> 写入 body，
// 返回写入的 artifact metadata（已 Create 到 store）。
func makeArtifactOnDisk(t *testing.T, s ArtifactStore, dataDir string, runID uint64, body []byte) *model.AgentToolArtifact {
	t.Helper()
	u := uuid.NewString()
	abs := ArtifactAbsPath(dataDir, runID, u)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, body, 0o644))

	preview := "preview..."
	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	art := &model.AgentToolArtifact{
		UUID:           u,
		AgentRunID:     runID,
		ToolCallID:     "call-x",
		ToolName:       "file_read",
		SizeBytes:      int64(len(body)),
		FilePath:       artifactRelPath(runID, u),
		StorageBackend: "local",
		Preview:        &preview,
		ExpiresAt:      &expiresAt,
	}
	require.NoError(t, s.Create(context.Background(), art))
	return art
}

// parseOutput marshal-roundtrip 解析 InvokableRun 返回的 JSON string 到结构。
func parseOutput(t *testing.T, s string) ReadArtifactOutput {
	t.Helper()
	var out ReadArtifactOutput
	require.NoError(t, json.Unmarshal([]byte(s), &out))
	return out
}

// ─── tests ─────────────────────────────────────────────────────────────────

// TestReadArtifact_Success_FullContent — case 1
// 写盘 5KB 文件，offset=0 limit=16K → content == 原文，has_more=false。
func TestReadArtifact_Success_FullContent(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{
		1: {ID: 1, UserID: 42},
	}}

	body := []byte(strings.Repeat("a", 5*1024))
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))

	args := fmt.Sprintf(`{"artifact_id":%q,"offset":0,"limit":%d}`, art.UUID, ToolArtifactReadMaxLimit)
	res, err := tool.InvokableRun(context.Background(), args)
	require.NoError(t, err)
	out := parseOutput(t, res)
	assert.Equal(t, string(body), out.Content)
	assert.Equal(t, 0, out.Offset)
	assert.Equal(t, 5*1024, out.Returned)
	assert.Equal(t, 5*1024, out.TotalSize)
	assert.False(t, out.HasMore)
	assert.Equal(t, "file_read", out.ToolName)
}

// TestReadArtifact_Pagination — case 2
// 写盘 30KB，offset=0 limit=16K → 前 16KB + has_more=true；
// 然后 offset=16K → 中段 + has_more=false（剩 14KB 不到 16KB）。
func TestReadArtifact_Pagination(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{
		2: {ID: 2, UserID: 42},
	}}

	body := []byte(strings.Repeat("p", 30*1024))
	art := makeArtifactOnDisk(t, s, dataDir, 2, body)

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))

	// 第一页
	res1, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q,"offset":0,"limit":16384}`, art.UUID))
	require.NoError(t, err)
	out1 := parseOutput(t, res1)
	assert.Equal(t, 0, out1.Offset)
	assert.Equal(t, 16384, out1.Returned)
	assert.Equal(t, 30*1024, out1.TotalSize)
	assert.True(t, out1.HasMore, "30KB > 16KB → has_more")

	// 第二页（剩 14KB）
	res2, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q,"offset":16384,"limit":16384}`, art.UUID))
	require.NoError(t, err)
	out2 := parseOutput(t, res2)
	assert.Equal(t, 16384, out2.Offset)
	assert.Equal(t, 30*1024-16384, out2.Returned)
	assert.False(t, out2.HasMore, "30KB - 16KB = 14KB 剩，第二页应当 has_more=false")
}

// TestReadArtifact_OffsetBeyondSize — case 3
// offset=999999 → content=""，has_more=false，无 error。
func TestReadArtifact_OffsetBeyondSize(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{1: {ID: 1, UserID: 42}}}

	body := []byte("small content")
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q,"offset":999999,"limit":16384}`, art.UUID))
	require.NoError(t, err, "offset 越界不应当报 error")
	out := parseOutput(t, res)
	assert.Equal(t, "", out.Content)
	assert.False(t, out.HasMore)
	assert.Equal(t, len(body), out.TotalSize)
}

// TestReadArtifact_LimitClamped — case 4
// limit=999999 → 实际返回 ≤ ToolArtifactReadMaxLimit (16KB)。
func TestReadArtifact_LimitClamped(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{1: {ID: 1, UserID: 42}}}

	body := []byte(strings.Repeat("L", 100*1024)) // 100KB
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q,"offset":0,"limit":999999}`, art.UUID))
	require.NoError(t, err)
	out := parseOutput(t, res)
	assert.LessOrEqual(t, out.Returned, ToolArtifactReadMaxLimit,
		"limit > 16KB 应当 clamp 到 16KB")
	assert.True(t, out.HasMore)
}

// TestReadArtifact_LimitZeroDefaultsTo16K 验证 limit=0 时使用默认 16KB（spec §设计要点 边界）。
func TestReadArtifact_LimitZeroDefaultsTo16K(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{1: {ID: 1, UserID: 42}}}

	body := []byte(strings.Repeat("d", 30*1024))
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q,"offset":0}`, art.UUID))
	require.NoError(t, err)
	out := parseOutput(t, res)
	assert.Equal(t, ToolArtifactReadMaxLimit, out.Returned,
		"limit 缺省/0 时应使用默认 16KB")
	assert.True(t, out.HasMore)
}

// TestReadArtifact_UUIDNotFound — case 5
// 传一个不存在的 UUID → 返回 error（spec §设计要点边界："artifact not found"）。
func TestReadArtifact_UUIDNotFound(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{}}

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	_, err := tool.InvokableRun(context.Background(),
		`{"artifact_id":"non-existent-uuid"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found",
		"未知 UUID 应当返回 'not found' error 让 LLM 知道引用失效")
}

// TestReadArtifact_TransientVisibilityMissIsRecoverable reproduces Dev run
// 263. A large lark_execute result was persisted successfully, the immediate
// read_tool_artifact lookup returned record-not-found, and Eino promoted that
// ordinary tool error to a fatal model_error for the whole Agent run. The same
// artifact was readable moments later. A transient miss must therefore remain
// model-visible and retryable instead of returning a Go error.
func TestReadArtifact_TransientVisibilityMissIsRecoverable(t *testing.T) {
	baseStore := newTestArtifactStore(t)
	dataDir := t.TempDir()
	runStore := &fakeRunStore{runs: map[uint64]*model.AgentRun{
		263: {ID: 263, UserID: 42},
	}}

	body := []byte("persisted profile document")
	art := makeArtifactOnDisk(t, baseStore, dataDir, 263, body)
	storeWithMiss := &transientMissArtifactStore{
		ArtifactStore: baseStore,
		missUUID:      art.UUID,
	}
	tool := NewReadArtifactTool(storeWithMiss, runStore, dataDir, userIDExtractor(42, true))
	args := fmt.Sprintf(`{"artifact_id":%q,"offset":0,"limit":1024}`, art.UUID)

	first, err := tool.InvokableRun(context.Background(), args)
	require.NoError(t, err, "transient artifact visibility miss must not terminate the Agent run")
	firstOutput := parseOutput(t, first)
	assert.Equal(t, "not_found", firstOutput.Note)
	assert.Empty(t, firstOutput.Content)

	second, err := tool.InvokableRun(context.Background(), args)
	require.NoError(t, err)
	secondOutput := parseOutput(t, second)
	assert.Equal(t, string(body), secondOutput.Content)
	assert.Equal(t, 2, storeWithMiss.getCalls)
}

// TestReadArtifact_ExpiredArtifact — case 6
// DB 行 is_expired=true → 返回 expired note（内容字段含说明文本，无 error）。
func TestReadArtifact_ExpiredArtifact(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{1: {ID: 1, UserID: 42}}}

	body := []byte("ignored")
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)
	require.NoError(t, s.MarkExpired(context.Background(), art.UUID))

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q}`, art.UUID))
	require.NoError(t, err, "expired artifact 应当返回 note 不返回 error")
	out := parseOutput(t, res)
	assert.Equal(t, "expired", out.Note)
	assert.Contains(t, out.Content, "expired")
}

// TestReadArtifact_CrossUserAccess — case 7
// artifact.AgentRunID 关联 user=42；ctx 注入 user=99 → not_accessible note（不暴露存在性）。
func TestReadArtifact_CrossUserAccess(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{
		1: {ID: 1, UserID: 42},
	}}

	body := []byte("secret content")
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	// ctx 注入 user=99 != run owner 42
	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(99, true))
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q}`, art.UUID))
	require.NoError(t, err, "cross-user 不应返回 error（避免暴露存在性）")
	out := parseOutput(t, res)
	assert.Equal(t, "not_accessible", out.Note)
	assert.NotContains(t, out.Content, "secret content",
		"cross-user 绝不可暴露原内容")
}

// TestReadArtifact_NoUserIDExtractor 验证 nil userIDFromCtx 一律 not accessible。
func TestReadArtifact_NoUserIDExtractor(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{1: {ID: 1, UserID: 42}}}

	body := []byte("xxx")
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	tool := NewReadArtifactTool(s, rs, dataDir, nil)
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q}`, art.UUID))
	require.NoError(t, err)
	out := parseOutput(t, res)
	assert.Equal(t, "not_accessible", out.Note)
}

// TestReadArtifact_DiskMissing_MarksExpired — case 8
// DB 行存在但文件不存在 → 自动 MarkExpired + 返回 disk_missing note。
func TestReadArtifact_DiskMissing_MarksExpired(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{1: {ID: 1, UserID: 42}}}

	body := []byte("will be removed")
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	// 物理删文件，模拟"DB 行 vs 磁盘"不一致
	absPath := ArtifactAbsPath(dataDir, 1, art.UUID)
	require.NoError(t, os.Remove(absPath))

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q}`, art.UUID))
	require.NoError(t, err)
	out := parseOutput(t, res)
	assert.Equal(t, "disk_missing", out.Note)

	// 必须自动 MarkExpired
	got, gerr := s.Get(context.Background(), art.UUID)
	require.NoError(t, gerr)
	assert.True(t, got.IsExpired, "ReadFile 失败应当自动 MarkExpired 避免下次再读")
}

// TestReadArtifact_InvalidJSONInput 验证非法 JSON 输入返回 error。
func TestReadArtifact_InvalidJSONInput(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{}}

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	_, err := tool.InvokableRun(context.Background(), `not valid json`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid input JSON")
}

// TestReadArtifact_MissingArtifactID 验证 artifact_id 缺失返回 error。
func TestReadArtifact_MissingArtifactID(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{}}

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	_, err := tool.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "artifact_id is required")
}

// TestReadArtifact_RunStoreError 验证 runStore.Get 报错时返回 not_accessible。
func TestReadArtifact_RunStoreError(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{err: errors.New("db connection lost")}

	body := []byte("xxx")
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q}`, art.UUID))
	require.NoError(t, err, "runStore error 应当返回 note 不暴露存在性")
	out := parseOutput(t, res)
	assert.Equal(t, "not_accessible", out.Note)
}

// TestReadArtifact_NegativeOffset 验证负 offset 被规范化为 0（spec §设计要点边界）。
func TestReadArtifact_NegativeOffset(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{runs: map[uint64]*model.AgentRun{1: {ID: 1, UserID: 42}}}

	body := []byte("abcdef")
	art := makeArtifactOnDisk(t, s, dataDir, 1, body)

	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))
	res, err := tool.InvokableRun(context.Background(),
		fmt.Sprintf(`{"artifact_id":%q,"offset":-10}`, art.UUID))
	require.NoError(t, err)
	out := parseOutput(t, res)
	assert.Equal(t, 0, out.Offset, "负 offset 应当规范化为 0")
	assert.Equal(t, "abcdef", out.Content)
}

// TestReadArtifact_Info 验证 Eino ToolInfo schema 含必要字段。
func TestReadArtifact_Info(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	rs := &fakeRunStore{}
	tool := NewReadArtifactTool(s, rs, dataDir, userIDExtractor(42, true))

	info, err := tool.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, ReadArtifactToolName, info.Name)
	assert.Contains(t, info.Desc, "persisted")
}
