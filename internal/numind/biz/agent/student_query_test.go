package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newSQTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use file-backed DB with WAL (avoids memory-mode datetime(3) type mismatch).
	tmp := t.TempDir()
	dsn := tmp + "/sq_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Explicit DDL: avoid datetime(3) MySQL precision tag → SQLite stores as TEXT
	// and cannot scan back to time.Time. Plain DATETIME works correctly.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS user (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			phone      TEXT    NOT NULL DEFAULT '',
			nickname   TEXT    NOT NULL DEFAULT '',
			avatar_url TEXT    NOT NULL DEFAULT '',
			parent_user_id INTEGER,
			username   TEXT    NOT NULL DEFAULT '',
			password   TEXT    NOT NULL DEFAULT '',
			is_admin   INTEGER NOT NULL DEFAULT 0,
			status     INTEGER NOT NULL DEFAULT 0,
			total_sop_runs INTEGER NOT NULL DEFAULT 0,
			last_login DATETIME,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run (
			id                        INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                   INTEGER NOT NULL DEFAULT 0,
			session_id                TEXT    NOT NULL DEFAULT '',
			status                    TEXT    NOT NULL DEFAULT 'running',
			state_reason              TEXT    NOT NULL DEFAULT '',
			terminal_metadata         TEXT,
			messages                  TEXT    NOT NULL DEFAULT '[]',
			reservation_id            INTEGER,
			started_at                DATETIME NOT NULL,
			ended_at                  DATETIME,
			compact_state             TEXT,
			compact_summary           TEXT,
			cancellation_requested_at DATETIME,
			agent_definition_id       INTEGER NOT NULL DEFAULT 0,
			created_at                DATETIME,
			updated_at                DATETIME
		)`).Error)
	return db
}

func newSQService(t *testing.T) (*StudentQueryService, *gorm.DB) {
	t.Helper()
	db := newSQTestDB(t)
	ds := store.NewTestStore(db)
	svc := NewStudentQueryService(ds.AgentRuns(), ds.Users())
	return svc, db
}

// seedRun creates an AgentRun for userID and returns its ID.
func seedRun(t *testing.T, db *gorm.DB, userID uint, sessionID string, status string) uint64 {
	t.Helper()
	msgs, _ := json.Marshal([]string{})
	run := &model.AgentRun{
		UserID:    userID,
		SessionID: sessionID,
		Status:    status,
		Messages:  msgs,
		StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)
	return run.ID
}

// ---------------------------------------------------------------------------
// TestListRecentSessions_FiltersByUser
// ---------------------------------------------------------------------------

// TestListRecentSessions_FiltersByUser ensures user A's sessions are not returned
// when querying for user B.
func TestListRecentSessions_FiltersByUser(t *testing.T) {
	svc, db := newSQService(t)

	seedRun(t, db, 101, "session-a1", "completed")
	seedRun(t, db, 101, "session-a2", "completed")
	seedRun(t, db, 202, "session-b1", "running")

	// Query for user 101 — must not see user 202's session.
	got, err := svc.ListRecentSessions(context.Background(), 101, 10)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	for _, s := range got {
		assert.Equal(t, uint(101), s.UserID)
		assert.NotEqual(t, "session-b1", s.SessionID)
	}

	// Query for user 202 — must see only their own.
	got2, err := svc.ListRecentSessions(context.Background(), 202, 10)
	require.NoError(t, err)
	assert.Len(t, got2, 1)
	assert.Equal(t, "session-b1", got2[0].SessionID)
}

// ---------------------------------------------------------------------------
// TestGetSessionSnapshot_Forbidden_OtherUser
// ---------------------------------------------------------------------------

// TestGetSessionSnapshot_Forbidden_OtherUser verifies that querying another
// user's session returns ErrForbidden.
func TestGetSessionSnapshot_Forbidden_OtherUser(t *testing.T) {
	svc, db := newSQService(t)

	runID := seedRun(t, db, 111, "sess-owner", "completed")

	// User 999 tries to read user 111's session snapshot — must be forbidden.
	_, err := svc.GetSessionSnapshot(context.Background(), 999, runID)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrForbidden)
}

// ---------------------------------------------------------------------------
// TestWriteFeedback_PersistsToAgentRun
// ---------------------------------------------------------------------------

// TestWriteFeedback_PersistsToAgentRun verifies that WriteFeedback appends the
// verdict to agent_run.terminal_metadata and can be read back.
func TestWriteFeedback_PersistsToAgentRun(t *testing.T) {
	svc, db := newSQService(t)

	runID := seedRun(t, db, 55, "sess-feedback", "completed")

	req := FeedbackRequest{Verdict: "up", Text: "great session"}
	err := svc.WriteFeedback(context.Background(), 55, runID, req)
	require.NoError(t, err)

	// Read back terminal_metadata and verify feedback key.
	var run model.AgentRun
	require.NoError(t, db.First(&run, runID).Error)
	require.NotEmpty(t, run.TerminalMetadata)

	var meta map[string]interface{}
	require.NoError(t, json.Unmarshal(run.TerminalMetadata, &meta))
	fb, ok := meta["feedback"].(map[string]interface{})
	require.True(t, ok, "terminal_metadata should contain 'feedback' object")
	assert.Equal(t, "up", fb["verdict"])
	assert.Equal(t, "great session", fb["text"])
}

// ---------------------------------------------------------------------------
// TestWriteFeedback_Forbidden_OtherUser
// ---------------------------------------------------------------------------

// TestWriteFeedback_Forbidden_OtherUser verifies that a different user cannot
// submit feedback for a run they do not own.
func TestWriteFeedback_Forbidden_OtherUser(t *testing.T) {
	svc, db := newSQService(t)

	runID := seedRun(t, db, 77, "sess-other", "completed")

	err := svc.WriteFeedback(context.Background(), 999, runID, FeedbackRequest{Verdict: "down"})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrForbidden)
}

// ---------------------------------------------------------------------------
// TestListAllHistorySessions_Last30Days
// ---------------------------------------------------------------------------

// TestListAllHistorySessions_Last30Days verifies that runs older than 30 days
// are excluded.
func TestListAllHistorySessions_Last30Days(t *testing.T) {
	svc, db := newSQService(t)

	// Recent run.
	msgs, _ := json.Marshal([]string{})
	recent := &model.AgentRun{
		UserID:    33,
		SessionID: "recent",
		Status:    "completed",
		Messages:  msgs,
		StartedAt: time.Now().AddDate(0, 0, -5),
	}
	require.NoError(t, db.Create(recent).Error)

	// Old run (35 days ago).
	old := &model.AgentRun{
		UserID:    33,
		SessionID: "old",
		Status:    "completed",
		Messages:  msgs,
		StartedAt: time.Now().AddDate(0, 0, -35),
	}
	require.NoError(t, db.Create(old).Error)
	// Force StartedAt to past via direct SQL update (GORM autoCreateTime would overwrite).
	require.NoError(t, db.Model(old).UpdateColumn("started_at", old.StartedAt).Error)
	require.NoError(t, db.Model(recent).UpdateColumn("started_at", recent.StartedAt).Error)

	got, err := svc.ListAllHistorySessions(context.Background(), 33)
	require.NoError(t, err)
	for _, s := range got {
		assert.NotEqual(t, "old", s.SessionID, "run older than 30 days must not appear")
	}
}
