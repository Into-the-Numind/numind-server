package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

func newTestAgentSandboxSessionStore(t *testing.T) IAgentSandboxSessionStore {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/agent_sandbox_session_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// SQLite DDL — DATETIME(3) is MySQL-only so we use plain DATETIME.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_sandbox_session (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id        INTEGER NOT NULL,
			agent_run_id   INTEGER,
			container_id   TEXT    NOT NULL,
			image_tag      TEXT    NOT NULL DEFAULT 'python:3.11-slim',
			status         TEXT    NOT NULL DEFAULT 'running',
			mem_limit_mb   INTEGER NOT NULL DEFAULT 512,
			cpu_quota      REAL    NOT NULL DEFAULT 1.0,
			exit_code      INTEGER,
			error_msg      TEXT,
			started_at     DATETIME NOT NULL,
			ended_at       DATETIME,
			created_at     DATETIME,
			updated_at     DATETIME
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return newAgentSandboxSessionStore(db)
}

func TestAgentSandboxSessionStore_Create_Get(t *testing.T) {
	s := newTestAgentSandboxSessionStore(t)
	ctx := context.Background()
	runID := uint64(42)
	sess := &model.AgentSandboxSession{
		UserID:      30,
		AgentRunID:  &runID,
		ContainerID: "abc123def456",
		ImageTag:    "python:3.11-slim",
		Status:      "running",
		MemLimitMB:  512,
		CPUQuota:    1.0,
		StartedAt:   time.Now(),
	}
	require.NoError(t, s.Create(ctx, sess))
	assert.NotZero(t, sess.ID, "Create should populate auto-increment ID")

	got, err := s.GetByContainerID(ctx, "abc123def456")
	require.NoError(t, err)
	assert.Equal(t, sess.ID, got.ID)
	assert.Equal(t, uint(30), got.UserID)
	if got.AgentRunID == nil {
		t.Fatal("AgentRunID should be set")
	}
	assert.Equal(t, uint64(42), *got.AgentRunID)
	assert.Equal(t, "running", got.Status)
}

func TestAgentSandboxSessionStore_Create_NullAgentRunID(t *testing.T) {
	s := newTestAgentSandboxSessionStore(t)
	ctx := context.Background()
	sess := &model.AgentSandboxSession{
		UserID:      99,
		ContainerID: "no-run-id-container",
		ImageTag:    "python:3.11-slim",
		Status:      "running",
		MemLimitMB:  512,
		CPUQuota:    1.0,
		StartedAt:   time.Now(),
	}
	require.NoError(t, s.Create(ctx, sess))

	got, err := s.GetByContainerID(ctx, "no-run-id-container")
	require.NoError(t, err)
	assert.Nil(t, got.AgentRunID, "AgentRunID should remain NULL when not set")
}

func TestAgentSandboxSessionStore_UpdateState_Terminated(t *testing.T) {
	s := newTestAgentSandboxSessionStore(t)
	ctx := context.Background()
	sess := &model.AgentSandboxSession{
		UserID:      30,
		ContainerID: "term-container",
		ImageTag:    "python:3.11-slim",
		Status:      "running",
		MemLimitMB:  512,
		CPUQuota:    1.0,
		StartedAt:   time.Now(),
	}
	require.NoError(t, s.Create(ctx, sess))

	endedAt := time.Now()
	exit := 0
	require.NoError(t, s.UpdateState(ctx, sess.ID, "terminated", &exit, "", &endedAt))

	got, err := s.GetByContainerID(ctx, "term-container")
	require.NoError(t, err)
	assert.Equal(t, "terminated", got.Status)
	if got.ExitCode == nil {
		t.Fatal("ExitCode should be 0 after terminate")
	}
	assert.Equal(t, 0, *got.ExitCode)
	require.NotNil(t, got.EndedAt)
}

func TestAgentSandboxSessionStore_UpdateState_Failed(t *testing.T) {
	s := newTestAgentSandboxSessionStore(t)
	ctx := context.Background()
	sess := &model.AgentSandboxSession{
		UserID:      30,
		ContainerID: "failed-container",
		Status:      "running",
		MemLimitMB:  512,
		CPUQuota:    1.0,
		StartedAt:   time.Now(),
	}
	require.NoError(t, s.Create(ctx, sess))

	endedAt := time.Now()
	exit := -1
	require.NoError(t, s.UpdateState(ctx, sess.ID, "failed", &exit, "Exec timed out", &endedAt))

	got, err := s.GetByContainerID(ctx, "failed-container")
	require.NoError(t, err)
	assert.Equal(t, "failed", got.Status)
	assert.Equal(t, "Exec timed out", got.ErrorMsg)
	if got.ExitCode == nil {
		t.Fatal("ExitCode should be -1 after fail")
	}
	assert.Equal(t, -1, *got.ExitCode)
}

func TestAgentSandboxSessionStore_UpdateState_NotFound(t *testing.T) {
	s := newTestAgentSandboxSessionStore(t)
	ctx := context.Background()
	endedAt := time.Now()
	err := s.UpdateState(ctx, 999, "terminated", nil, "", &endedAt)
	if err == nil {
		t.Error("UpdateState on non-existent id should error")
	}
}

func TestAgentSandboxSessionStore_ListByUser(t *testing.T) {
	s := newTestAgentSandboxSessionStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		sess := &model.AgentSandboxSession{
			UserID:      30,
			ContainerID: "list-user-" + string(rune('a'+i)),
			Status:      "running",
			MemLimitMB:  512,
			CPUQuota:    1.0,
			StartedAt:   time.Now(),
		}
		require.NoError(t, s.Create(ctx, sess))
	}
	// One for another user
	other := &model.AgentSandboxSession{
		UserID:      99,
		ContainerID: "other-user",
		Status:      "running",
		MemLimitMB:  512,
		CPUQuota:    1.0,
		StartedAt:   time.Now(),
	}
	require.NoError(t, s.Create(ctx, other))

	rows, err := s.ListByUser(ctx, 30, 10)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
	for _, r := range rows {
		assert.Equal(t, uint(30), r.UserID)
	}
}

func TestAgentSandboxSessionStore_GetByContainerID_NotFound(t *testing.T) {
	s := newTestAgentSandboxSessionStore(t)
	ctx := context.Background()
	_, err := s.GetByContainerID(ctx, "nonexistent")
	if err == nil {
		t.Error("GetByContainerID for unknown container should error")
	}
}
