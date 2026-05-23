// Package compactv2 — task 2.2 RunArtifactCleanup 单元测试。
//
// 覆盖 spec §验证策略 Cleanup cron case：
//   - 正常 sweep（部分过期 + 文件被物理删 + DB 标 is_expired）
//   - 文件已不存在（不报错，仍 MarkExpired）
//   - nil store 防御
package compactv2

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// createArtifact 把一条 artifact metadata 写到 store 并（可选）落盘。
func createArtifact(t *testing.T, s ArtifactStore, dataDir string, runID uint64, expiresAt time.Time, writeFile bool) *model.AgentToolArtifact {
	t.Helper()
	u := uuid.NewString()
	preview := "p"
	ea := expiresAt
	art := &model.AgentToolArtifact{
		UUID:           u,
		AgentRunID:     runID,
		ToolCallID:     "tc-" + u[:8],
		ToolName:       "file_read",
		SizeBytes:      4,
		FilePath:       artifactRelPath(runID, u),
		StorageBackend: "local",
		Preview:        &preview,
		ExpiresAt:      &ea,
	}
	require.NoError(t, s.Create(context.Background(), art))

	if writeFile {
		abs := ArtifactAbsPath(dataDir, runID, u)
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte("body"), 0o644))
	}
	return art
}

// TestRunArtifactCleanup_NormalSweep — case 1
// 插入 3 条 artifact：2 条 expires_at < now，1 条未来 → cleanup 后应当：
//   - 2 条物理文件被删
//   - 这 2 条 DB 标 is_expired=true
//   - 未来那条 DB 不动 + 文件保留
func TestRunArtifactCleanup_NormalSweep(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	past1 := time.Now().Add(-2 * time.Hour)
	past2 := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(24 * time.Hour)

	a1 := createArtifact(t, s, dataDir, 1, past1, true)
	a2 := createArtifact(t, s, dataDir, 1, past2, true)
	a3 := createArtifact(t, s, dataDir, 1, future, true) // 未来，应保留

	m, err := RunArtifactCleanup(ctx, s, dataDir)
	require.NoError(t, err)
	assert.Equal(t, 2, m.Processed, "应当扫到 2 条过期")
	assert.Equal(t, 2, m.FilesDeleted)
	assert.Equal(t, 0, m.FileErrors)
	assert.Equal(t, 2, m.MarkedExpired)
	assert.Equal(t, 0, m.MarkErrors)

	// a1 / a2 文件应当被物理删
	_, statErr1 := os.Stat(ArtifactAbsPath(dataDir, 1, a1.UUID))
	assert.True(t, os.IsNotExist(statErr1), "a1 文件应当被物理删")
	_, statErr2 := os.Stat(ArtifactAbsPath(dataDir, 1, a2.UUID))
	assert.True(t, os.IsNotExist(statErr2), "a2 文件应当被物理删")

	// a3 文件仍存在
	_, statErr3 := os.Stat(ArtifactAbsPath(dataDir, 1, a3.UUID))
	assert.NoError(t, statErr3, "a3 文件应当保留")

	// DB：a1 / a2 应当 is_expired=true，a3 不动
	got1, _ := s.Get(ctx, a1.UUID)
	require.NotNil(t, got1)
	assert.True(t, got1.IsExpired)

	got2, _ := s.Get(ctx, a2.UUID)
	require.NotNil(t, got2)
	assert.True(t, got2.IsExpired)

	got3, _ := s.Get(ctx, a3.UUID)
	require.NotNil(t, got3)
	assert.False(t, got3.IsExpired)
}

// TestRunArtifactCleanup_FileAlreadyMissing — case 2
// 插入 1 条 expired 但文件不存在 → cleanup 不报错，DB 仍标 is_expired=true。
func TestRunArtifactCleanup_FileAlreadyMissing(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	past := time.Now().Add(-2 * time.Hour)
	art := createArtifact(t, s, dataDir, 1, past, false) // 不写文件

	m, err := RunArtifactCleanup(ctx, s, dataDir)
	require.NoError(t, err)
	assert.Equal(t, 1, m.Processed)
	assert.Equal(t, 1, m.FilesDeleted, "ENOENT 应视为成功")
	assert.Equal(t, 0, m.FileErrors)
	assert.Equal(t, 1, m.MarkedExpired)

	got, _ := s.Get(ctx, art.UUID)
	require.NotNil(t, got)
	assert.True(t, got.IsExpired)
}

// TestRunArtifactCleanup_NilStore_ReturnsError — case 3
// 传 nil store → 返回 error，不 panic。
func TestRunArtifactCleanup_NilStore_ReturnsError(t *testing.T) {
	_, err := RunArtifactCleanup(context.Background(), nil, "/tmp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "store is nil")
}

// TestRunArtifactCleanup_NoExpired 验证 nothing-to-do 路径返回 Processed=0, 无 error。
func TestRunArtifactCleanup_NoExpired(t *testing.T) {
	s := newTestArtifactStore(t)
	dataDir := t.TempDir()
	ctx := context.Background()

	// 只插入未来 expires_at
	future := time.Now().Add(24 * time.Hour)
	_ = createArtifact(t, s, dataDir, 1, future, true)

	m, err := RunArtifactCleanup(ctx, s, dataDir)
	require.NoError(t, err)
	assert.Equal(t, 0, m.Processed)
	assert.Equal(t, 0, m.FilesDeleted)
	assert.Equal(t, 0, m.MarkedExpired)
}
