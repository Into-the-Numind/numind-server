package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test DB helper
// ---------------------------------------------------------------------------

func newAgentAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/agent_admin_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Use explicit DDL to avoid datetime(3) MySQL precision in SQLite.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run (
			id                         INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                    INTEGER NOT NULL DEFAULT 0,
			session_id                 TEXT    NOT NULL DEFAULT '',
			status                     TEXT    NOT NULL DEFAULT 'running',
			state_reason               TEXT    NOT NULL DEFAULT '',
			messages                   TEXT    NOT NULL DEFAULT '[]',
			reservation_id             INTEGER,
			terminal_metadata          TEXT,
			started_at                 DATETIME NOT NULL,
			ended_at                   DATETIME,
			compact_state              TEXT,
			compact_summary            TEXT,
			cancellation_requested_at  DATETIME,
			agent_definition_id        INTEGER NOT NULL DEFAULT 0,
			pending_question_json      TEXT,
			pending_question_at        DATETIME,
			created_at                 DATETIME,
			updated_at                 DATETIME,
			-- V1.5 板块 2 task 2.1 — context-management V2 columns
			compact_state_v2           TEXT,
			total_tokens_used_v2       INTEGER NOT NULL DEFAULT 0,
			use_compact_v2             INTEGER NOT NULL DEFAULT 0,
			context_window_limit_v2    INTEGER,
			-- 会话管理字段
			is_pinned                  INTEGER NOT NULL DEFAULT 0,
			session_name               TEXT NOT NULL DEFAULT '',
			is_deleted                 INTEGER NOT NULL DEFAULT 0,
			is_test                 INTEGER NOT NULL DEFAULT 0
		)`).Error)

	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_definition (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id INTEGER NOT NULL DEFAULT 0,
			name           TEXT    NOT NULL DEFAULT ''
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newAgentAdminService(t *testing.T) (agent.IAgentAdminService, *gorm.DB) {
	t.Helper()
	db := newAgentAdminTestDB(t)
	s := store.NewTestStore(db)
	svc := agent.NewAgentAdminService(s.AgentRuns(), nil /* runner not needed for DB tests */)
	return svc, db
}

// ---------------------------------------------------------------------------
// TestAgentAdminService_CancelByAdmin_Happy
// ---------------------------------------------------------------------------

func TestAgentAdminService_CancelByAdmin_Happy(t *testing.T) {
	svc, db := newAgentAdminService(t)
	ctx := context.Background()

	// Insert a running agent_run.
	run := &model.AgentRun{
		UserID:    1,
		Status:    "running",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	err := svc.CancelByAdmin(ctx, run.ID, 99)
	require.NoError(t, err)

	// DB should have cancellation_requested_at set and terminal_metadata with admin attribution.
	var updated model.AgentRun
	require.NoError(t, db.First(&updated, run.ID).Error)
	assert.NotNil(t, updated.CancellationRequestedAt, "cancellation_requested_at should be set")
	assert.NotEmpty(t, updated.TerminalMetadata)
}

// ---------------------------------------------------------------------------
// TestAgentAdminService_CancelByAdmin_NotFound
// ---------------------------------------------------------------------------

func TestAgentAdminService_CancelByAdmin_NotFound(t *testing.T) {
	svc, _ := newAgentAdminService(t)
	ctx := context.Background()

	err := svc.CancelByAdmin(ctx, 999999, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrAgentRunNotFound)
}

// ---------------------------------------------------------------------------
// TestAgentAdminService_CancelByAdmin_AlreadyTerminal_409
// ---------------------------------------------------------------------------

func TestAgentAdminService_CancelByAdmin_AlreadyTerminal_409(t *testing.T) {
	svc, db := newAgentAdminService(t)
	ctx := context.Background()

	run := &model.AgentRun{
		UserID:    2,
		Status:    "completed", // terminal
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	err := svc.CancelByAdmin(ctx, run.ID, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrAgentRunNotCancellable)
}

// ---------------------------------------------------------------------------
// TestAgentAdminService_ListByStatus_Happy
// ---------------------------------------------------------------------------

func TestAgentAdminService_ListByStatus_Happy(t *testing.T) {
	svc, db := newAgentAdminService(t)
	ctx := context.Background()

	// Insert an agent_definition with parent_user_id=42.
	ad := struct {
		ID           uint64
		ParentUserID uint
		Name         string
	}{ParentUserID: 42, Name: "test-agent"}
	require.NoError(t, db.Table("agent_definition").Create(&ad).Error)

	// Insert two running runs for that definition.
	for i := 0; i < 2; i++ {
		run := &model.AgentRun{
			UserID:            uint(10 + i),
			Status:            "running",
			Messages:          datatypes.JSON(`[]`),
			StartedAt:         time.Now(),
			AgentDefinitionID: ad.ID,
		}
		require.NoError(t, db.Create(run).Error)
	}
	// Insert one completed run — should not appear in "running" filter.
	done := &model.AgentRun{
		UserID:            99,
		Status:            "completed",
		Messages:          datatypes.JSON(`[]`),
		StartedAt:         time.Now(),
		AgentDefinitionID: ad.ID,
	}
	require.NoError(t, db.Create(done).Error)

	dtos, total, err := svc.ListByStatus(ctx, 42, "running", 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, dtos, 2)
	for _, d := range dtos {
		assert.Equal(t, "running", d.Status)
	}
}

// ---------------------------------------------------------------------------
// TestAgentAdminService_ListByStatus_NoParentFilter
// ---------------------------------------------------------------------------

func TestAgentAdminService_ListByStatus_NoParentFilter(t *testing.T) {
	svc, db := newAgentAdminService(t)
	ctx := context.Background()

	// Insert runs with agent_definition_id=0 (historical — no join row).
	for i := 0; i < 3; i++ {
		run := &model.AgentRun{
			UserID:    uint(i + 1),
			Status:    "running",
			Messages:  datatypes.JSON(`[]`),
			StartedAt: time.Now(),
		}
		require.NoError(t, db.Create(run).Error)
	}

	// parentUserID=0 skips the join — returns all running.
	dtos, total, err := svc.ListByStatus(ctx, 0, "running", 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, dtos, 3)
}
