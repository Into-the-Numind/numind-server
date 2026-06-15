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
// Extended DDL helper (includes credit_transaction + agent_definition)
// ---------------------------------------------------------------------------

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
			pending_question_json     TEXT,
			pending_question_at       DATETIME,
			created_at                DATETIME,
			updated_at                DATETIME,
			-- V1.5 板块 2 task 2.1 — context-management V2 columns
			compact_state_v2          TEXT,
			total_tokens_used_v2      INTEGER NOT NULL DEFAULT 0,
			use_compact_v2            INTEGER NOT NULL DEFAULT 0,
			context_window_limit_v2   INTEGER,
			-- 会话管理字段
			is_pinned                 INTEGER NOT NULL DEFAULT 0,
			session_name              TEXT NOT NULL DEFAULT '',
			is_deleted                INTEGER NOT NULL DEFAULT 0,
			is_test                INTEGER NOT NULL DEFAULT 0
		)`).Error)
	return db
}

func newSQTestDBFull(t *testing.T) *gorm.DB {
	t.Helper()
	db := newSQTestDB(t)
	// Add credit_transaction table for Fix 1 tests.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS credit_transaction (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id        INTEGER NOT NULL DEFAULT 0,
			package_id     INTEGER NOT NULL DEFAULT 0,
			source_type    TEXT,
			source_id      INTEGER,
			amount         INTEGER NOT NULL DEFAULT 0,
			operation      TEXT    NOT NULL DEFAULT '',
			usage_record_id INTEGER,
			biz_ref_type   TEXT    NOT NULL DEFAULT '',
			biz_ref_id     TEXT    NOT NULL DEFAULT '',
			created_at     DATETIME
		)`).Error)
	// Add agent_definition table for Fix 2 tests.
	require.NoError(t, db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_definition (
			id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id        INTEGER NOT NULL DEFAULT 0,
			name                  TEXT    NOT NULL DEFAULT '',
			description           TEXT    NOT NULL DEFAULT '',
			icon_url              TEXT    NOT NULL DEFAULT '',
			welcome_message       TEXT    NOT NULL DEFAULT '',
			starters              TEXT,
			questionnaire_answers TEXT,
			generated_skill_body  TEXT    NOT NULL DEFAULT '',
			advanced_mode         INTEGER NOT NULL DEFAULT 0,
			custom_skill_body     TEXT    NOT NULL DEFAULT '',
			system_prompt         TEXT    NOT NULL DEFAULT '',
			tool_flags            TEXT,
			credit_cap_per_session INTEGER,
			daily_credit_cap      INTEGER,
			version               INTEGER NOT NULL DEFAULT 1,
			is_active             INTEGER NOT NULL DEFAULT 1,
			source_template_id    INTEGER,
			created_by            INTEGER NOT NULL DEFAULT 0,
			created_at            DATETIME,
			updated_at            DATETIME
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

func newSQServiceFull(t *testing.T) (*StudentQueryService, *gorm.DB) {
	t.Helper()
	db := newSQTestDBFull(t)
	ds := store.NewTestStore(db)
	svc := NewStudentQueryService(
		ds.AgentRuns(),
		ds.Users(),
		WithQuerySkillStore(ds.AgentDefinitions()),
		WithQueryCreditStore(ds.Credits()),
	)
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

	// Hotfix session-snapshot-uuid-contract: lookup is by session_id (string),
	// no longer by run.id (uint64). Seed and query with the session_id string.
	_ = seedRun(t, db, 111, "sess-owner", "completed")

	// User 999 tries to read user 111's session snapshot — must be forbidden.
	_, err := svc.GetSessionSnapshot(context.Background(), 999, "sess-owner")
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

// ---------------------------------------------------------------------------
// Fix 1: credits_* fields
// ---------------------------------------------------------------------------

// TestStudentQuery_CreditsUsed_ComputedFromReservation verifies that
// credits_used is summed from credit_transaction rows where
// biz_ref_type='reservation' and biz_ref_id=<reservation_id>, and that
// credits_threshold_state reflects the ratio correctly.
func TestStudentQuery_CreditsUsed_ComputedFromReservation(t *testing.T) {
	svc, db := newSQServiceFull(t)

	// Seed an agent_definition with credit_cap_per_session=100.
	agentDef := &model.AgentDefinition{
		ParentUserID: 1,
		Name:         "TestAgent",
		IsActive:     true,
		CreatedBy:    1,
	}
	cap := uint(100)
	agentDef.CreditCapPerSession = &cap
	require.NoError(t, db.Create(agentDef).Error)

	// Seed a run with reservation_id=42 and agent_definition_id.
	rsvID := uint64(42)
	msgs, _ := json.Marshal([]map[string]string{{"role": "user", "content": "hello"}})
	run := &model.AgentRun{
		UserID:            77,
		SessionID:         "sess-credits",
		Status:            "terminated",
		StateReason:       "completed",
		Messages:          msgs,
		StartedAt:         time.Now(),
		AgentDefinitionID: agentDef.ID,
		ReservationID:     &rsvID,
	}
	require.NoError(t, db.Create(run).Error)

	// Seed two credit_transaction debit rows for reservation 42 (amounts are negative).
	require.NoError(t, db.Exec(
		`INSERT INTO credit_transaction (user_id, package_id, amount, operation, biz_ref_type, biz_ref_id, created_at)
		 VALUES (77, 0, -30, 'agent_run', 'reservation', '42', datetime('now')),
		        (77, 0, -25, 'agent_run', 'reservation', '42', datetime('now'))`).Error)

	// credits_used should be 55, budget=100, state=under_60.
	got, err := svc.ListRecentSessions(context.Background(), 77, 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 55, got[0].CreditsUsed)
	assert.Equal(t, 100, got[0].CreditsBudget)
	assert.Equal(t, "under_60", got[0].CreditsThresholdState)
}

// TestStudentQuery_CreditsThreshold_Warning verifies the warning_60 band.
func TestStudentQuery_CreditsThreshold_Warning(t *testing.T) {
	svc, db := newSQServiceFull(t)

	agentDef := &model.AgentDefinition{ParentUserID: 1, Name: "A", IsActive: true, CreatedBy: 1}
	cap := uint(100)
	agentDef.CreditCapPerSession = &cap
	require.NoError(t, db.Create(agentDef).Error)

	rsvID := uint64(99)
	msgs, _ := json.Marshal([]map[string]string{})
	run := &model.AgentRun{
		UserID: 88, SessionID: "warn-sess", Status: "running",
		Messages: msgs, StartedAt: time.Now(),
		AgentDefinitionID: agentDef.ID, ReservationID: &rsvID,
	}
	require.NoError(t, db.Create(run).Error)

	// 75 credits spent → 75% of 100 → warning_60.
	require.NoError(t, db.Exec(
		`INSERT INTO credit_transaction (user_id, package_id, amount, operation, biz_ref_type, biz_ref_id, created_at)
		 VALUES (88, 0, -75, 'agent_run', 'reservation', '99', datetime('now'))`).Error)

	got, err := svc.ListRecentSessions(context.Background(), 88, 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "warning_60", got[0].CreditsThresholdState)
}

// TestStudentQuery_CreditsThreshold_Blocked verifies the blocked_100 band.
func TestStudentQuery_CreditsThreshold_Blocked(t *testing.T) {
	svc, db := newSQServiceFull(t)

	agentDef := &model.AgentDefinition{ParentUserID: 1, Name: "B", IsActive: true, CreatedBy: 1}
	cap := uint(50)
	agentDef.CreditCapPerSession = &cap
	require.NoError(t, db.Create(agentDef).Error)

	rsvID := uint64(200)
	msgs, _ := json.Marshal([]map[string]string{})
	run := &model.AgentRun{
		UserID: 99, SessionID: "blocked-sess", Status: "terminated",
		StateReason: "error_max_budget",
		Messages:    msgs, StartedAt: time.Now(),
		AgentDefinitionID: agentDef.ID, ReservationID: &rsvID,
	}
	require.NoError(t, db.Create(run).Error)

	// 50 credits spent == 100% of cap=50 → blocked_100.
	require.NoError(t, db.Exec(
		`INSERT INTO credit_transaction (user_id, package_id, amount, operation, biz_ref_type, biz_ref_id, created_at)
		 VALUES (99, 0, -50, 'agent_run', 'reservation', '200', datetime('now'))`).Error)

	got, err := svc.ListRecentSessions(context.Background(), 99, 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "blocked_100", got[0].CreditsThresholdState)
	assert.Equal(t, "budget_exhausted", got[0].Status)
}

// TestStudentQuery_CreditsBudget_DefaultWhenNoCap verifies the default budget
// of 200 is used when credit_cap_per_session is NULL.
func TestStudentQuery_CreditsBudget_DefaultWhenNoCap(t *testing.T) {
	svc, db := newSQServiceFull(t)

	agentDef := &model.AgentDefinition{ParentUserID: 1, Name: "NoCap", IsActive: true, CreatedBy: 1}
	// CreditCapPerSession left nil.
	require.NoError(t, db.Create(agentDef).Error)

	msgs, _ := json.Marshal([]map[string]string{})
	run := &model.AgentRun{
		UserID: 44, SessionID: "nocap-sess", Status: "running",
		Messages: msgs, StartedAt: time.Now(),
		AgentDefinitionID: agentDef.ID,
	}
	require.NoError(t, db.Create(run).Error)

	got, err := svc.ListRecentSessions(context.Background(), 44, 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, defaultCreditBudget, got[0].CreditsBudget)
	assert.Equal(t, 0, got[0].CreditsUsed)
	assert.Equal(t, "under_60", got[0].CreditsThresholdState)
}

// ---------------------------------------------------------------------------
// Fix 2: RunSummary enrichment — agent_name + preview_text + last_active_at
// ---------------------------------------------------------------------------

// TestStudentQuery_RunSummary_AgentNameAndPreview verifies that agent_name,
// preview_text, and last_active_at are populated on RunSummary.
func TestStudentQuery_RunSummary_AgentNameAndPreview(t *testing.T) {
	svc, db := newSQServiceFull(t)

	agentDef := &model.AgentDefinition{ParentUserID: 1, Name: "售前助手", IsActive: true, CreatedBy: 1}
	require.NoError(t, db.Create(agentDef).Error)

	now := time.Now()
	ended := now.Add(30 * time.Second)
	msgs, _ := json.Marshal([]map[string]string{
		{"role": "user", "content": "我想了解产品定价方案"},
		{"role": "assistant", "content": "好的，我来介绍一下"},
	})
	run := &model.AgentRun{
		UserID:            55,
		SessionID:         "enrich-sess",
		Status:            "terminated",
		StateReason:       "completed",
		Messages:          msgs,
		StartedAt:         now,
		EndedAt:           &ended,
		AgentDefinitionID: agentDef.ID,
	}
	require.NoError(t, db.Create(run).Error)

	got, err := svc.ListRecentSessions(context.Background(), 55, 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	s := got[0]
	assert.Equal(t, "售前助手", s.AgentName, "agent_name must come from agent_definition")
	assert.NotEmpty(t, s.PreviewText, "preview_text must be set from first user turn")
	assert.Contains(t, s.PreviewText, "产品定价", "preview should contain first user message")
	assert.NotEmpty(t, s.LastActiveAt, "last_active_at must be set")
}

// TestStudentQuery_PreviewText_TruncatesLong verifies preview_text is truncated to ~60 chars.
func TestStudentQuery_PreviewText_TruncatesLong(t *testing.T) {
	svc, db := newSQServiceFull(t)

	// Long message >60 runes.
	longMsg := "这是一条非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常非常长的消息内容"
	msgs, _ := json.Marshal([]map[string]string{{"role": "user", "content": longMsg}})
	run := &model.AgentRun{
		UserID: 66, SessionID: "trunc-sess", Status: "running",
		Messages: msgs, StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	got, err := svc.ListRecentSessions(context.Background(), 66, 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	// Preview must be at most 60 runes.
	runes := []rune(got[0].PreviewText)
	assert.LessOrEqual(t, len(runes), 60, "preview_text must be at most 60 codepoints")
}

// ---------------------------------------------------------------------------
// Fix 3: SessionSnapshot.Messages shape
// ---------------------------------------------------------------------------

// TestStudentQuery_SessionSnapshot_MessageTransform verifies that
// GetSessionSnapshot returns frontend-shaped AgentMessage objects.
func TestStudentQuery_SessionSnapshot_MessageTransform(t *testing.T) {
	svc, db := newSQServiceFull(t)

	msgs, _ := json.Marshal([]map[string]string{
		{"role": "user", "content": "什么是AI？"},
		{"role": "assistant", "content": "AI是人工智能"},
	})
	run := &model.AgentRun{
		UserID:      11,
		SessionID:   "snap-transform",
		Status:      "terminated",
		StateReason: "completed",
		Messages:    msgs,
		StartedAt:   time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 11, run.SessionID)
	require.NoError(t, err)

	// Messages must be []agentMessage, not raw JSON.
	rawMsgs, ok := snap.Messages.([]agentMessage)
	require.True(t, ok, "snap.Messages must be []agentMessage")
	require.Len(t, rawMsgs, 2)

	// First message: user turn.
	assert.Equal(t, "user", rawMsgs[0].Type)
	assert.Equal(t, "什么是AI？", rawMsgs[0].Text)
	assert.Empty(t, rawMsgs[0].Markdown)

	// Second message: last assistant turn in a completed run → final_answer.
	assert.Equal(t, "final_answer", rawMsgs[1].Type)
	assert.Equal(t, "AI是人工智能", rawMsgs[1].Markdown)
	assert.Equal(t, run.ID, rawMsgs[1].RunID)
}

// TestStudentQuery_SessionSnapshot_AnsweredQuestionCardSurvivesReload is the
// issue1 customer-bug reproduction (NDF rule 11): after the user answers an
// ask_user_question and the run completes, reopening the session must keep the
// question CARD (answered/collapsed), not degrade the answer into an orphan
// "用户已回答你的问题…" user bubble. The answer turn embeds a question_answer
// structure (written by AnswerAndClear); transformMessages must reconstruct it
// as an answered question_prompt card. EXPECTED TO FAIL before the fix
// (transformMessages emits a plain user bubble for the answer turn).
func TestStudentQuery_SessionSnapshot_AnsweredQuestionCardSurvivesReload(t *testing.T) {
	svc, db := newSQServiceFull(t)

	msgs, _ := json.Marshal([]map[string]any{
		{"role": "user", "content": "帮我做个调研"},
		{
			"role":    "user",
			"content": "用户已回答你的问题：\n- 「目标受众是谁？」→ 年轻女性\n请据此继续，不要重复已回答的问题。",
			"question_answer": map[string]any{
				"questions": []map[string]any{
					{
						"question":     "目标受众是谁？",
						"header":       "受众",
						"multi_select": false,
						"options": []map[string]any{
							{"label": "年轻女性", "description": "18-30"},
							{"label": "职场人士"},
						},
						"answer": "年轻女性",
					},
				},
			},
		},
		{"role": "assistant", "content": "好的，调研完成"},
	})
	run := &model.AgentRun{
		UserID:      12,
		SessionID:   "snap-answered-card",
		Status:      "terminated",
		StateReason: "completed",
		Messages:    msgs,
		StartedAt:   time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 12, run.SessionID)
	require.NoError(t, err)
	rawMsgs, ok := snap.Messages.([]agentMessage)
	require.True(t, ok, "snap.Messages must be []agentMessage")

	var card *agentMessage
	for i := range rawMsgs {
		if rawMsgs[i].Type == "question_prompt" {
			card = &rawMsgs[i]
		}
		if rawMsgs[i].Type == "user" {
			assert.NotContains(t, rawMsgs[i].Text, "用户已回答你的问题",
				"answered question turn must NOT render as a plain user bubble (issue1)")
		}
	}
	require.NotNil(t, card, "answered question turn must reconstruct a question_prompt card on reload")
	assert.Equal(t, "answered", card.AnswerStatus)
	assert.Equal(t, run.ID, card.RunID)
	require.Len(t, card.Questions, 1)
	assert.Equal(t, "目标受众是谁？", card.Questions[0].Question)
	assert.Equal(t, "年轻女性", card.Questions[0].Answer, "reconstructed card must carry the user's actual answer")
	require.Len(t, card.Questions[0].Options, 2, "options must survive reconstruction (non-nil array)")
}

func TestReconstructAnsweredQuestions(t *testing.T) {
	// nil / corrupt / empty all degrade to ok=false (turn stays a plain bubble).
	_, ok := reconstructAnsweredQuestions(nil)
	assert.False(t, ok, "nil → false")
	_, ok = reconstructAnsweredQuestions("not-an-object")
	assert.False(t, ok, "non-object JSON → false")
	_, ok = reconstructAnsweredQuestions(map[string]any{"questions": []any{}})
	assert.False(t, ok, "empty questions → false")

	// Valid payload → items; a nil Options becomes a non-nil empty slice so the
	// frontend never reads options.length on null (dev run 147).
	items, ok := reconstructAnsweredQuestions(map[string]any{
		"questions": []any{map[string]any{"question": "Q1", "answer": "A1"}},
	})
	require.True(t, ok)
	require.Len(t, items, 1)
	assert.Equal(t, "Q1", items[0].Question)
	assert.Equal(t, "A1", items[0].Answer)
	assert.NotNil(t, items[0].Options, "nil options must become a non-nil empty slice")
}

// TestStudentQuery_SessionSnapshot_ToolGroupSurvivesReload is the customer-bug
// reproduction (NDF rule 11): after an agent run that used tools, reopening the
// session must still show the tool-call process. transformMessages previously
// dropped any non-user/assistant turn (default: continue), so a persisted
// tool_group turn vanished on reload. EXPECTED TO FAIL before the fix.
func TestStudentQuery_SessionSnapshot_ToolGroupSurvivesReload(t *testing.T) {
	svc, db := newSQServiceFull(t)

	msgs, _ := json.Marshal([]map[string]any{
		{"role": "user", "content": "查一下天气"},
		{"role": "tool_group", "tool_calls": []map[string]any{
			{
				"tool_call_id":  "tc-1",
				"tool_name":     "web_search",
				"current_state": "result",
				"events": []map[string]any{
					{
						"run_id":       1,
						"tool_call_id": "tc-1",
						"tool_name":    "web_search",
						"state":        "result",
						"message":      "完成",
						"timestamp":    "2026-06-09T00:00:00Z",
					},
				},
			},
		}},
		{"role": "assistant", "content": "今天晴", "reasoning": "先搜索再回答"},
	})
	run := &model.AgentRun{
		UserID:      33,
		SessionID:   "snap-toolgroup",
		Status:      "terminated",
		StateReason: "completed",
		Messages:    msgs,
		StartedAt:   time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 33, run.SessionID)
	require.NoError(t, err)
	rawMsgs, ok := snap.Messages.([]agentMessage)
	require.True(t, ok)

	// Expect user → tool_group → final_answer (tool-call process survives reload).
	require.Len(t, rawMsgs, 3)
	assert.Equal(t, "user", rawMsgs[0].Type)
	assert.Equal(t, "tool_group", rawMsgs[1].Type)
	assert.Equal(t, "final_answer", rawMsgs[2].Type)
	assert.Equal(t, "先搜索再回答", rawMsgs[2].Reasoning)

	// tool_group content is reconstructed 1:1 for the frontend.
	require.Len(t, rawMsgs[1].ToolCalls, 1)
	tc := rawMsgs[1].ToolCalls[0]
	assert.Equal(t, "tc-1", tc.ToolCallID)
	assert.Equal(t, "web_search", tc.ToolName)
	assert.Equal(t, "result", tc.CurrentState)
	require.Len(t, tc.Events, 1)
	assert.Equal(t, "完成", tc.Events[0].Message)
}

// Option C: a persisted FULL transcript ([user, assistant, tool_group, assistant])
// reloads as user → intermediate assistant (kept, with its own reasoning) →
// tool_group → final_answer (last assistant promoted). Verbatim replay.
func TestStudentQuery_SessionSnapshot_FullTranscriptReplay(t *testing.T) {
	svc, db := newSQServiceFull(t)
	msgs, _ := json.Marshal([]map[string]any{
		{"role": "user", "content": "查销量并画图"},
		{"role": "assistant", "content": "我先查数据", "reasoning": "需要先搜索"},
		{"role": "tool_group", "tool_calls": []map[string]any{
			{
				"tool_call_id":  "tc1",
				"tool_name":     "web_search",
				"current_state": "result",
				"events": []map[string]any{
					{"run_id": 1, "tool_call_id": "tc1", "tool_name": "web_search", "state": "result", "message": "已获取搜索结果", "timestamp": "2026-06-09T00:00:00Z"},
				},
			},
		}},
		{"role": "assistant", "content": "这是趋势图与解读", "reasoning": "整理结论"},
	})
	run := &model.AgentRun{
		UserID: 44, SessionID: "snap-transcript", Status: "terminated",
		StateReason: "completed", Messages: msgs, StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 44, run.SessionID)
	require.NoError(t, err)
	rawMsgs, ok := snap.Messages.([]agentMessage)
	require.True(t, ok)
	require.Len(t, rawMsgs, 4)

	assert.Equal(t, "user", rawMsgs[0].Type)
	assert.Equal(t, "assistant", rawMsgs[1].Type) // intermediate step kept
	assert.Equal(t, "我先查数据", rawMsgs[1].Markdown)
	assert.Equal(t, "需要先搜索", rawMsgs[1].Reasoning)
	assert.Equal(t, "tool_group", rawMsgs[2].Type)
	assert.Equal(t, "final_answer", rawMsgs[3].Type) // last assistant promoted
	assert.Equal(t, "这是趋势图与解读", rawMsgs[3].Markdown)
	assert.Equal(t, "整理结论", rawMsgs[3].Reasoning)
}

// Defensive read-path guard: an empty intermediate assistant turn is dropped.
func TestStudentQuery_SessionSnapshot_SkipsEmptyAssistantStep(t *testing.T) {
	svc, db := newSQServiceFull(t)
	msgs, _ := json.Marshal([]map[string]any{
		{"role": "user", "content": "q"},
		{"role": "assistant", "content": "", "reasoning": ""}, // empty intermediate → skipped
		{"role": "assistant", "content": "最终答案", "reasoning": ""},
	})
	run := &model.AgentRun{
		UserID: 45, SessionID: "snap-emptyasst", Status: "terminated",
		StateReason: "completed", Messages: msgs, StartedAt: time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 45, run.SessionID)
	require.NoError(t, err)
	rawMsgs, _ := snap.Messages.([]agentMessage)
	require.Len(t, rawMsgs, 2) // [user, final_answer] — empty assistant dropped
	assert.Equal(t, "user", rawMsgs[0].Type)
	assert.Equal(t, "final_answer", rawMsgs[1].Type)
}

// TestStudentQuery_SessionSnapshot_AssistantMidTurnNotFinalAnswer verifies that
// intermediate assistant turns are typed 'assistant', not 'final_answer'.
func TestStudentQuery_SessionSnapshot_AssistantMidTurnNotFinalAnswer(t *testing.T) {
	svc, db := newSQServiceFull(t)

	msgs, _ := json.Marshal([]map[string]string{
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "turn1"},
		{"role": "user", "content": "continue"},
		{"role": "assistant", "content": "turn2-final"},
	})
	run := &model.AgentRun{
		UserID:      22,
		SessionID:   "snap-multi",
		Status:      "terminated",
		StateReason: "completed",
		Messages:    msgs,
		StartedAt:   time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 22, run.SessionID)
	require.NoError(t, err)
	rawMsgs, ok := snap.Messages.([]agentMessage)
	require.True(t, ok)
	require.Len(t, rawMsgs, 4)

	// First assistant turn must be 'assistant', not 'final_answer'.
	assert.Equal(t, "assistant", rawMsgs[1].Type)
	// Last assistant turn must be 'final_answer' (completed run).
	assert.Equal(t, "final_answer", rawMsgs[3].Type)
}

// TestGetSessionSnapshot_UUIDSessionID is the regression for the hotfix
// session-snapshot-uuid-contract. Before the fix, the controller parsed :id
// as uint64 and the biz layer looked up by run.id; the frontend (which has
// always sent agent_run.session_id, a varchar UUID) hit
// `invalid id: <uuid>` 400 on every history click. This test seeds a row
// with a UUID-shaped session_id, queries via that string, and asserts the
// snapshot returns the expected run.
func TestGetSessionSnapshot_UUIDSessionID(t *testing.T) {
	svc, db := newSQServiceFull(t)

	msgs, _ := json.Marshal([]map[string]string{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": "hello"},
	})
	uuid := "5502952a-1732-453d-936b-0cb4c675cdd5" // real shape from dev incident
	run := &model.AgentRun{
		UserID:      77,
		SessionID:   uuid,
		Status:      "terminated",
		StateReason: "completed",
		Messages:    msgs,
		StartedAt:   time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 77, uuid)
	require.NoError(t, err, "UUID session_id must round-trip through GetSessionSnapshot")
	require.NotNil(t, snap)
	rawMsgs, ok := snap.Messages.([]agentMessage)
	require.True(t, ok)
	require.Len(t, rawMsgs, 2)
	assert.Equal(t, run.ID, snap.Run.ID, "snapshot must surface the underlying run.id")
	assert.Equal(t, uuid, snap.Run.SessionID, "snapshot must echo the session_id used to look up")
}

// TestGetSessionSnapshot_NotFound covers the case where the session_id is
// well-formed but no run exists for it. Before the fix this could not even
// be exercised because the controller short-circuited at 400 on UUIDs;
// after the fix the biz layer is reachable and must return ErrAgentRunNotFound.
func TestGetSessionSnapshot_NotFound(t *testing.T) {
	svc, _ := newSQServiceFull(t)

	_, err := svc.GetSessionSnapshot(context.Background(), 88, "does-not-exist-uuid")
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrAgentRunNotFound)
}
