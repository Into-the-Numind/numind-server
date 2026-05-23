package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newTestAgentToolArtifactStore creates a SQLite DB with the agent_tool_artifact
// table created via explicit DDL. Mirrors newTestAgentRunStore pattern: model
// uses `datetime(3)` precision tags (MySQL ms precision) which the SQLite driver
// can't scan into *time.Time, so AutoMigrate-produced columns must be hand-rolled.
func newTestAgentToolArtifactStore(t *testing.T) (IAgentToolArtifactStore, *gorm.DB) {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/agent_tool_artifact_test.db?_busy_timeout=5000&_journal_mode=WAL"
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
	require.NoError(t, db.Exec(`CREATE INDEX IF NOT EXISTS idx_ata_run_tool_call ON agent_tool_artifact (agent_run_id, tool_call_id)`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX IF NOT EXISTS idx_ata_expires ON agent_tool_artifact (expires_at, is_expired)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return newAgentToolArtifactStore(db), db
}

// sampleArtifact builds a fully populated AgentToolArtifact for tests.
func sampleArtifact(runID uint64, toolCallID string) *model.AgentToolArtifact {
	mime := "text/plain"
	preview := "abc..."
	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	return &model.AgentToolArtifact{
		UUID:           uuid.NewString(),
		AgentRunID:     runID,
		ToolCallID:     toolCallID,
		ToolName:       "file_read",
		MimeType:       &mime,
		SizeBytes:      32_768,
		FilePath:       "1/abc-uuid",
		StorageBackend: "local",
		Preview:        &preview,
		ExpiresAt:      &expiresAt,
	}
}

// TestAgentToolArtifactStore_CreateAndGet — spec 验证 case 1.
func TestAgentToolArtifactStore_CreateAndGet(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	ctx := context.Background()

	art := sampleArtifact(42, "call-001")
	require.NoError(t, s.Create(ctx, art))
	require.NotZero(t, art.ID)

	got, err := s.Get(ctx, art.UUID)
	require.NoError(t, err)
	assert.Equal(t, art.UUID, got.UUID)
	assert.Equal(t, art.AgentRunID, got.AgentRunID)
	assert.Equal(t, art.ToolCallID, got.ToolCallID)
	assert.Equal(t, art.ToolName, got.ToolName)
	assert.Equal(t, art.SizeBytes, got.SizeBytes)
	assert.Equal(t, art.FilePath, got.FilePath)
	assert.Equal(t, art.StorageBackend, got.StorageBackend)
	require.NotNil(t, got.MimeType)
	assert.Equal(t, "text/plain", *got.MimeType)
	require.NotNil(t, got.Preview)
	assert.Equal(t, "abc...", *got.Preview)
	assert.False(t, got.IsExpired)
	require.NotNil(t, got.ExpiresAt)
}

// TestAgentToolArtifactStore_Create_RejectsNil verifies defensive guards.
func TestAgentToolArtifactStore_Create_RejectsNil(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	err := s.Create(context.Background(), nil)
	require.Error(t, err)
}

// TestAgentToolArtifactStore_Create_RejectsEmptyUUID verifies defensive guards.
func TestAgentToolArtifactStore_Create_RejectsEmptyUUID(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	art := sampleArtifact(42, "call-001")
	art.UUID = ""
	err := s.Create(context.Background(), art)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UUID")
}

// TestAgentToolArtifactStore_Get_NotFound verifies a missing uuid returns an error.
func TestAgentToolArtifactStore_Get_NotFound(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	_, err := s.Get(context.Background(), "missing-uuid")
	require.Error(t, err)
}

// TestAgentToolArtifactStore_GetByToolCallID — spec 验证 case 2.
func TestAgentToolArtifactStore_GetByToolCallID(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	ctx := context.Background()
	art := sampleArtifact(42, "call-xyz")
	require.NoError(t, s.Create(ctx, art))

	got, err := s.GetByToolCallID(ctx, 42, "call-xyz")
	require.NoError(t, err)
	assert.Equal(t, art.UUID, got.UUID)
	assert.Equal(t, "call-xyz", got.ToolCallID)
}

// TestAgentToolArtifactStore_GetByToolCallID_NotFound verifies error on miss.
func TestAgentToolArtifactStore_GetByToolCallID_NotFound(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	_, err := s.GetByToolCallID(context.Background(), 999, "nope")
	require.Error(t, err)
}

// TestAgentToolArtifactStore_GetByToolCallID_PicksEarliest verifies that if
// multiple rows share (run_id, tool_call_id) — shouldn't happen in practice
// but defensive — we return the smallest id (earliest written).
func TestAgentToolArtifactStore_GetByToolCallID_PicksEarliest(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	ctx := context.Background()

	a1 := sampleArtifact(42, "call-dup")
	a1.FilePath = "1/first"
	require.NoError(t, s.Create(ctx, a1))

	a2 := sampleArtifact(42, "call-dup")
	a2.FilePath = "1/second"
	require.NoError(t, s.Create(ctx, a2))

	got, err := s.GetByToolCallID(ctx, 42, "call-dup")
	require.NoError(t, err)
	assert.Equal(t, "1/first", got.FilePath)
	assert.Less(t, got.ID, a2.ID)
}

// TestAgentToolArtifactStore_ListExpiredBefore — spec 验证 case 3：
// 3 条插入，2 条 expires_at<cutoff，验证只返回 2 条.
func TestAgentToolArtifactStore_ListExpiredBefore(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	ctx := context.Background()

	now := time.Now()
	past1 := now.Add(-2 * time.Hour)
	past2 := now.Add(-1 * time.Hour)
	future := now.Add(24 * time.Hour)

	a := sampleArtifact(1, "call-1")
	a.ExpiresAt = &past1
	require.NoError(t, s.Create(ctx, a))

	b := sampleArtifact(1, "call-2")
	b.ExpiresAt = &past2
	require.NoError(t, s.Create(ctx, b))

	c := sampleArtifact(1, "call-3")
	c.ExpiresAt = &future
	require.NoError(t, s.Create(ctx, c))

	out, err := s.ListExpiredBefore(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, out, 2)
	// 排序：expires_at ASC
	assert.Equal(t, a.UUID, out[0].UUID)
	assert.Equal(t, b.UUID, out[1].UUID)
}

// TestAgentToolArtifactStore_ListExpiredBefore_SkipsAlreadyExpired verifies
// already-marked-expired rows are excluded (cleanup cron's intent).
func TestAgentToolArtifactStore_ListExpiredBefore_SkipsAlreadyExpired(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	ctx := context.Background()

	now := time.Now()
	past := now.Add(-2 * time.Hour)

	a := sampleArtifact(1, "call-1")
	a.ExpiresAt = &past
	require.NoError(t, s.Create(ctx, a))

	b := sampleArtifact(1, "call-2")
	b.ExpiresAt = &past
	b.IsExpired = true // 已经被前一轮 cron mark
	require.NoError(t, s.Create(ctx, b))

	out, err := s.ListExpiredBefore(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, a.UUID, out[0].UUID)
}

// TestAgentToolArtifactStore_ListExpiredBefore_RespectsLimit verifies the limit
// argument is honored.
func TestAgentToolArtifactStore_ListExpiredBefore_RespectsLimit(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	ctx := context.Background()

	now := time.Now()
	for i := 0; i < 5; i++ {
		past := now.Add(time.Duration(-i-1) * time.Hour)
		a := sampleArtifact(1, "call-"+uuid.NewString())
		a.ExpiresAt = &past
		require.NoError(t, s.Create(ctx, a))
	}
	out, err := s.ListExpiredBefore(ctx, now, 2)
	require.NoError(t, err)
	assert.Len(t, out, 2)
}

// TestAgentToolArtifactStore_MarkExpired — spec 验证 case 4.
func TestAgentToolArtifactStore_MarkExpired(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	ctx := context.Background()

	art := sampleArtifact(1, "call-mark")
	require.NoError(t, s.Create(ctx, art))
	require.NoError(t, s.MarkExpired(ctx, art.UUID))

	got, err := s.Get(ctx, art.UUID)
	require.NoError(t, err)
	assert.True(t, got.IsExpired)
}

// TestAgentToolArtifactStore_MarkExpired_NotFound verifies error.
func TestAgentToolArtifactStore_MarkExpired_NotFound(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	err := s.MarkExpired(context.Background(), "missing-uuid")
	require.Error(t, err)
}

// TestAgentToolArtifactStore_DeleteBatch — spec 验证 case 5.
func TestAgentToolArtifactStore_DeleteBatch(t *testing.T) {
	s, db := newTestAgentToolArtifactStore(t)
	ctx := context.Background()

	a := sampleArtifact(1, "call-1")
	require.NoError(t, s.Create(ctx, a))
	b := sampleArtifact(1, "call-2")
	require.NoError(t, s.Create(ctx, b))
	c := sampleArtifact(1, "call-3")
	require.NoError(t, s.Create(ctx, c))

	require.NoError(t, s.DeleteBatch(ctx, []string{a.UUID, b.UUID}))

	// a + b 应被删除，c 保留
	_, err := s.Get(ctx, a.UUID)
	require.Error(t, err)
	_, err = s.Get(ctx, b.UUID)
	require.Error(t, err)
	got, err := s.Get(ctx, c.UUID)
	require.NoError(t, err)
	assert.Equal(t, c.UUID, got.UUID)

	var count int64
	require.NoError(t, db.Model(&model.AgentToolArtifact{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestAgentToolArtifactStore_DeleteBatch_EmptyNoop verifies empty input returns nil.
func TestAgentToolArtifactStore_DeleteBatch_EmptyNoop(t *testing.T) {
	s, _ := newTestAgentToolArtifactStore(t)
	require.NoError(t, s.DeleteBatch(context.Background(), nil))
	require.NoError(t, s.DeleteBatch(context.Background(), []string{}))
}
