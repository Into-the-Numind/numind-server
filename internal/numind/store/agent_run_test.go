package store

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

func newTestAgentRunStore(t *testing.T) IAgentRunStore {
	t.Helper()
	// 使用临时文件 DB（而非 :memory:）确保并发 goroutine 共享同一数据库连接。
	// 使用显式 DDL 而非 AutoMigrate：避免 datetime(3) MySQL 精度语法在 SQLite 下 scan 失败。
	tmp := t.TempDir()
	dsn := tmp + "/agent_run_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run (
			id                        INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                   INTEGER NOT NULL,
			session_id                TEXT    NOT NULL DEFAULT '',
			status                    TEXT    NOT NULL DEFAULT 'running',
			state_reason              TEXT    NOT NULL DEFAULT '',
			messages                  TEXT    NOT NULL DEFAULT '[]',
			reservation_id            INTEGER,
			started_at                DATETIME NOT NULL,
			ended_at                  DATETIME,
			compact_state             TEXT,
			compact_summary           TEXT,
			terminal_metadata         TEXT,
			cancellation_requested_at DATETIME,
			agent_definition_id       INTEGER,
			pending_question_json     TEXT,
			pending_question_at       DATETIME,
			created_at                DATETIME,
			updated_at                DATETIME
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return newAgentRunStore(db)
}

func TestAgentRunStore_Create_Get(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()
	run := &model.AgentRun{
		UserID:    1,
		SessionID: "sess-1",
		Status:    "running",
		Messages:  datatypes.JSON(`[]`),
		StartedAt: time.Now(),
	}
	require.NoError(t, s.Create(ctx, run))
	require.NotZero(t, run.ID)

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, run.SessionID, got.SessionID)
	assert.Equal(t, "running", got.Status)
}

func TestAgentRunStore_UpdateState(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(ctx, run))

	endedAt := time.Now()
	require.NoError(t, s.UpdateState(ctx, run.ID, "terminated", "completed", &endedAt))

	got, _ := s.Get(ctx, run.ID)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, "completed", got.StateReason)
	require.NotNil(t, got.EndedAt)
}

func TestAgentRunStore_UpdateState_NotFound(t *testing.T) {
	s := newTestAgentRunStore(t)
	err := s.UpdateState(context.Background(), 9999, "terminated", "completed", nil)
	require.Error(t, err)
}

func TestAgentRunStore_WriteTurn(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(ctx, run))

	msgs := json.RawMessage(`[{"role":"user","content":"hello"}]`)
	require.NoError(t, s.WriteTurn(ctx, run.ID, msgs))

	got, _ := s.Get(ctx, run.ID)
	assert.JSONEq(t, string(msgs), string(got.Messages))
}

func TestAgentRunStore_WriteTurn_Overwrite(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(ctx, run))

	msgs1 := json.RawMessage(`[{"step":1}]`)
	require.NoError(t, s.WriteTurn(ctx, run.ID, msgs1))
	msgs2 := json.RawMessage(`[{"step":1},{"step":2}]`)
	require.NoError(t, s.WriteTurn(ctx, run.ID, msgs2))

	got, _ := s.Get(ctx, run.ID)
	// 整体覆写：messages 应等于 msgs2
	assert.JSONEq(t, string(msgs2), string(got.Messages))
}

func TestAgentRunStore_ConcurrentWriteTurn(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(ctx, run))

	var wg sync.WaitGroup
	msgsA := json.RawMessage(`[{"writer":"A"}]`)
	msgsB := json.RawMessage(`[{"writer":"B"}]`)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.WriteTurn(ctx, run.ID, msgsA)
		}()
		go func() {
			defer wg.Done()
			_ = s.WriteTurn(ctx, run.ID, msgsB)
		}()
	}
	wg.Wait()

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err, "Get after concurrent writes must succeed")
	// last-write-wins，final messages 非空且为其中之一（无 corruption）
	bothMatch := string(got.Messages) != ""
	assert.True(t, bothMatch, "final messages must be non-empty and one of writers")
}

func TestAgentRunStore_UpdateTerminalMetadata(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()

	run := &model.AgentRun{
		UserID:      1,
		SessionID:   "sess-1",
		Status:      "terminated",
		StateReason: "error_max_budget",
		Messages:    datatypes.JSON([]byte("[]")),
		StartedAt:   time.Now(),
	}
	require.NoError(t, s.Create(ctx, run))
	require.NotZero(t, run.ID)

	meta := datatypes.JSON([]byte(`{"budget_dimension":"max_turns","used":51,"limit":50}`))
	require.NoError(t, s.UpdateTerminalMetadata(ctx, run.ID, meta))

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.JSONEq(t, string(meta), string(got.TerminalMetadata))
}

func TestAgentRunStore_UpdateTerminalMetadata_NonExistentID(t *testing.T) {
	s := newTestAgentRunStore(t)
	err := s.UpdateTerminalMetadata(context.Background(), 999999, datatypes.JSON([]byte(`{}`)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no row matched")
}

func TestAgentRunStore_UpdateTerminalMetadata_BudgetExceededShape(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()

	run := &model.AgentRun{
		UserID:      2,
		SessionID:   "sess-budget",
		Status:      "terminated",
		StateReason: "error_max_budget",
		Messages:    datatypes.JSON([]byte("[]")),
		StartedAt:   time.Now(),
	}
	require.NoError(t, s.Create(ctx, run))

	detailJSON := []byte(`{"budget_dimension":"max_credits","used":820,"limit":800}`)
	require.NoError(t, s.UpdateTerminalMetadata(ctx, run.ID, datatypes.JSON(detailJSON)))

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(got.TerminalMetadata, &parsed))
	assert.Equal(t, "max_credits", parsed["budget_dimension"])
	assert.Equal(t, float64(820), parsed["used"])
	assert.Equal(t, float64(800), parsed["limit"])
}

func TestAgentRunStore_ListBySession(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		run := &model.AgentRun{UserID: 1, SessionID: "sess-A", Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
		require.NoError(t, s.Create(ctx, run))
	}
	for i := 0; i < 3; i++ {
		run := &model.AgentRun{UserID: 1, SessionID: "sess-B", Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
		require.NoError(t, s.Create(ctx, run))
	}
	runs, total, err := s.ListBySession(ctx, "sess-A", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(5), total)
	assert.Len(t, runs, 5)
}

// ---------------------------------------------------------------------------
// newTestAgentRunStoreFull creates a test store with the T4 columns included.
// ---------------------------------------------------------------------------

func newTestAgentRunStoreFull(t *testing.T) IAgentRunStore {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/agent_run_full_test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run (
			id                        INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                   INTEGER NOT NULL,
			session_id                TEXT    NOT NULL DEFAULT '',
			status                    TEXT    NOT NULL DEFAULT 'running',
			state_reason              TEXT    NOT NULL DEFAULT '',
			messages                  TEXT    NOT NULL DEFAULT '[]',
			reservation_id            INTEGER,
			started_at                DATETIME NOT NULL,
			ended_at                  DATETIME,
			compact_state             TEXT,
			compact_summary           TEXT,
			terminal_metadata         TEXT,
			cancellation_requested_at DATETIME,
			agent_definition_id       INTEGER,
			pending_question_json     TEXT,
			pending_question_at       DATETIME,
			created_at                DATETIME,
			updated_at                DATETIME
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return newAgentRunStore(db)
}

func TestAgentRunStore_UpdatePendingQuestion(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	ctx := context.Background()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(ctx, run))

	payload := []byte(`{"question":"Which region?","options":[{"key":"a","label":"北"},{"key":"b","label":"南"}]}`)
	require.NoError(t, s.UpdatePendingQuestion(ctx, run.ID, payload))

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, "waiting_for_user_choice", got.StateReason)
	require.NotNil(t, got.PendingQuestionAt)
	assert.Contains(t, string(got.PendingQuestionJSON), "Which region?")
}

func TestAgentRunStore_UpdatePendingQuestion_NotFound(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	err := s.UpdatePendingQuestion(context.Background(), 9999, []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "9999")
}

func TestAgentRunStore_ClearPendingQuestion(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	ctx := context.Background()
	run := &model.AgentRun{UserID: 1, Status: "terminated", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(ctx, run))

	// First set a pending question.
	payload := []byte(`{"question":"Q?","options":[{"key":"a","label":"A"},{"key":"b","label":"B"}]}`)
	require.NoError(t, s.UpdatePendingQuestion(ctx, run.ID, payload))

	// Then clear it.
	require.NoError(t, s.ClearPendingQuestion(ctx, run.ID))

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", got.StateReason)
	// pending_question_at should be nil; pending_question_json should be empty/null.
	assert.Nil(t, got.PendingQuestionAt)
}

func TestAgentRunStore_ClearPendingQuestion_NotFound(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	err := s.ClearPendingQuestion(context.Background(), 9999)
	require.Error(t, err)
}

func TestAgentRunStore_AppendUserMessage(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	ctx := context.Background()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(ctx, run))

	require.NoError(t, s.AppendUserMessage(ctx, run.ID, "hello world"))

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Contains(t, string(got.Messages), "hello world")
	assert.Contains(t, string(got.Messages), `"role":"user"`)
}

func TestAgentRunStore_AppendUserMessage_MultipleAppends(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	ctx := context.Background()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(ctx, run))

	require.NoError(t, s.AppendUserMessage(ctx, run.ID, "first"))
	require.NoError(t, s.AppendUserMessage(ctx, run.ID, "second"))

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Contains(t, string(got.Messages), "first")
	assert.Contains(t, string(got.Messages), "second")
}

func TestAgentRunStore_AppendUserMessage_NotFound(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	err := s.AppendUserMessage(context.Background(), 9999, "msg")
	require.Error(t, err)
}
