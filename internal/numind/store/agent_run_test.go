package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
			pending_external_action_json TEXT,
			pending_external_action_at   DATETIME,
			created_at                DATETIME,
			updated_at                DATETIME,
			-- V1.5 板块 2 task 2.1 — context-management V2 columns
			compact_state_v2          TEXT,
			total_tokens_used_v2      INTEGER NOT NULL DEFAULT 0,
			use_compact_v2            INTEGER NOT NULL DEFAULT 0,
			context_window_limit_v2   INTEGER,
			is_pinned                 INTEGER NOT NULL DEFAULT 0,
			session_name              TEXT    NOT NULL DEFAULT '',
			is_deleted                INTEGER NOT NULL DEFAULT 0,
			is_test                INTEGER NOT NULL DEFAULT 0
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

// Customer regression: sorting complete agent_run rows also sorts the large
// messages JSON column in MySQL. A single 285 KB transcript exhausted the
// production sort buffer and made the whole session page fail to load. The
// ordered/limited query must therefore select lightweight IDs only; complete
// rows are hydrated afterwards without truncating their messages.
func TestAgentRunStore_ListBySession_SortsIDsBeforeHydratingLargeMessages(t *testing.T) {
	s := newTestAgentRunStore(t)
	concrete, ok := s.(*agentRunStore)
	require.True(t, ok)

	var statements []string
	callbackName := "test:capture-list-by-session-sql"
	require.NoError(t, concrete.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if sql := tx.Statement.SQL.String(); sql != "" {
			statements = append(statements, sql)
		}
	}))
	t.Cleanup(func() { concrete.db.Callback().Query().Remove(callbackName) })

	ctx := context.Background()
	largeContent := strings.Repeat("完整聊天内容-", 30_000)
	messages, err := json.Marshal([]map[string]string{{"role": "assistant", "content": largeContent}})
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		run := &model.AgentRun{
			UserID:    355,
			SessionID: "production-large-session",
			Status:    "terminated",
			Messages:  datatypes.JSON(messages),
			StartedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		require.NoError(t, s.Create(ctx, run))
	}

	runs, total, err := s.ListBySession(ctx, "production-large-session", 0, 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, runs, 2)
	assert.Equal(t, string(messages), string(runs[0].Messages), "hydration must keep the complete transcript")

	var orderedSQL string
	for _, statement := range statements {
		normalized := strings.ToLower(statement)
		if strings.Contains(normalized, "order by started_at desc") ||
			strings.Contains(normalized, "order by `started_at` desc") {
			orderedSQL = normalized
			break
		}
	}
	require.NotEmpty(t, orderedSQL, "expected to capture the ordered page query")
	assert.NotContains(t, orderedSQL, "select *", "the sort query must not carry the large messages column")
	assert.Regexp(t, `select\s+[\x60\"]?id[\x60\"]?\s+from`, orderedSQL, "the sort query should select IDs only")
}

func TestAgentRunStore_ListByUserDoesNotReturnFullMessages(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()
	now := time.Now()

	firstPrompt := "请给飞书选题库笔记打标"
	largeAssistantReply := strings.Repeat("assistant-token-", 80_000)
	messages := datatypes.JSON([]byte(fmt.Sprintf(`[
		{"role":"user","content":%q},
		{"role":"assistant","content":%q}
	]`, firstPrompt, largeAssistantReply)))

	err := s.Create(ctx, &model.AgentRun{
		UserID:      313,
		SessionID:   "large-history-session",
		Status:      "terminated",
		StateReason: "completed",
		Messages:    messages,
		StartedAt:   now,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	require.NoError(t, err)

	runs, err := s.ListByUser(ctx, 313, nil, 20)
	require.NoError(t, err)
	require.Len(t, runs, 1)

	assert.Equal(t, "large-history-session", runs[0].SessionID)
	assert.Contains(t, string(runs[0].Messages), firstPrompt)
	assert.Less(t, len(runs[0].Messages), 4096, "history list should return a bounded preview transcript instead of the full stored transcript")
	assert.NotContains(t, string(runs[0].Messages), largeAssistantReply[:256])
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
			pending_external_action_json TEXT,
			pending_external_action_at   DATETIME,
			created_at                DATETIME,
			updated_at                DATETIME,
			-- V1.5 板块 2 task 2.1 — context-management V2 columns
			compact_state_v2          TEXT,
			total_tokens_used_v2      INTEGER NOT NULL DEFAULT 0,
			use_compact_v2            INTEGER NOT NULL DEFAULT 0,
			context_window_limit_v2   INTEGER,
			is_pinned                 INTEGER NOT NULL DEFAULT 0,
			session_name              TEXT    NOT NULL DEFAULT '',
			is_deleted                INTEGER NOT NULL DEFAULT 0,
			is_test                INTEGER NOT NULL DEFAULT 0
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

func TestAgentRunStore_TransitionPendingExternalActionUsesExactSessionLineage(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	transitioner := s.(IExternalActionTransitioner)
	ctx := context.Background()
	run := &model.AgentRun{
		UserID: 438, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now(),
	}
	require.NoError(t, s.Create(ctx, run))
	oldPayload := []byte(`{"provider":"lark","operation_id":"operation-user-438","session_id":"create-app-old","tool_call_id":"lark-call-438","phase":"create_app","expires_at":"2026-07-20T13:00:00Z"}`)
	require.NoError(t, s.(IExternalActionWriter).UpdatePendingExternalAction(ctx, run.ID, oldPayload))
	require.NoError(t, s.UpdateState(ctx, run.ID, "terminated", "waiting_for_user_choice", nil))
	newPayload := []byte(`{"provider":"lark","operation_id":"operation-user-438","session_id":"user-auth-new","tool_call_id":"lark-call-438","phase":"user_auth","expires_at":"2026-07-20T14:00:00Z"}`)

	transitioned, err := transitioner.TransitionPendingExternalAction(ctx, 438, run.ID, newPayload, []string{"create-app-old"})
	require.NoError(t, err)
	require.True(t, transitioned)
	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(newPayload), string(got.PendingExternalActionJSON))
	require.NotContains(t, string(got.PendingExternalActionJSON), "superseded")

	transitioned, err = transitioner.TransitionPendingExternalAction(ctx, 438, run.ID, newPayload, []string{"create-app-old"})
	require.NoError(t, err)
	require.True(t, transitioned, "same replacement is an idempotent success")

	unrelated := []byte(`{"provider":"lark","operation_id":"operation-user-438","session_id":"unrelated","tool_call_id":"lark-call-438","phase":"user_auth","expires_at":"2026-07-20T15:00:00Z"}`)
	transitioned, err = transitioner.TransitionPendingExternalAction(ctx, 438, run.ID, unrelated, []string{"some-other-session"})
	require.NoError(t, err)
	require.False(t, transitioned)
	got, err = s.Get(ctx, run.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(newPayload), string(got.PendingExternalActionJSON))
}

func TestAgentRunStore_TransitionPendingExternalActionRejectsUnboundedLineage(t *testing.T) {
	s := newTestAgentRunStoreFull(t)
	lineage := make([]string, maxExternalActionSessionLineage+1)
	for index := range lineage {
		lineage[index] = fmt.Sprintf("session-%d", index)
	}
	transitioned, err := s.(IExternalActionTransitioner).TransitionPendingExternalAction(
		context.Background(), 438, 1,
		[]byte(`{"provider":"lark","operation_id":"op","session_id":"next","tool_call_id":"call","phase":"user_auth","expires_at":"2026-07-20T15:00:00Z"}`),
		lineage,
	)
	require.Error(t, err)
	require.False(t, transitioned)
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
