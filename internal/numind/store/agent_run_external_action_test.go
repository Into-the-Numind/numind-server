package store

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

	"numind-server/internal/pkg/model"
)

func newExternalActionAgentRunStore(t *testing.T) (IAgentRunStore, IExternalActionWriter) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/external_action.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE agent_run (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			state_reason TEXT NOT NULL DEFAULT '',
			terminal_metadata TEXT,
			messages TEXT NOT NULL DEFAULT '[]',
			reservation_id INTEGER,
			started_at DATETIME NOT NULL,
			ended_at DATETIME,
			cancellation_requested_at DATETIME,
			agent_definition_id INTEGER,
			pending_question_json TEXT,
			pending_question_at DATETIME,
			pending_external_action_json TEXT,
			pending_external_action_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME,
			use_compact_v2 INTEGER NOT NULL DEFAULT 0,
			is_pinned INTEGER NOT NULL DEFAULT 0,
			session_name TEXT NOT NULL DEFAULT '',
			is_deleted INTEGER NOT NULL DEFAULT 0,
			is_test INTEGER NOT NULL DEFAULT 0
		)`).Error)

	s := newAgentRunStore(db)
	w, ok := s.(IExternalActionWriter)
	require.True(t, ok, "production agentRunStore must expose the narrow external-action writer")
	return s, w
}

func TestAgentRunStore_UpdatePendingExternalAction_PersistsIdentityOnly(t *testing.T) {
	s, writer := newExternalActionAgentRunStore(t)
	ctx := context.Background()
	questionAt := time.Now().Add(-time.Minute)
	run := &model.AgentRun{
		UserID:              7,
		SessionID:           "session-7",
		Status:              "running",
		Messages:            datatypes.JSON(`[]`),
		StartedAt:           time.Now(),
		PendingQuestionJSON: datatypes.JSON(`{"questions":[{"question":"stale"}]}`),
		PendingQuestionAt:   &questionAt,
	}
	require.NoError(t, s.Create(ctx, run))

	persistent := []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`)
	require.NoError(t, writer.UpdatePendingExternalAction(ctx, run.ID, persistent))

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.JSONEq(t, string(persistent), string(got.PendingExternalActionJSON))
	assert.NotNil(t, got.PendingExternalActionAt)
	assert.Empty(t, got.PendingQuestionJSON)
	assert.Nil(t, got.PendingQuestionAt)
	assert.Equal(t, "waiting_for_user_choice", got.StateReason)
	assert.NotContains(t, string(got.PendingExternalActionJSON), "url")
}

func TestAgentRunStore_UpdatePendingExternalAction_RejectsNonAllowlistedFields(t *testing.T) {
	s, writer := newExternalActionAgentRunStore(t)
	run := &model.AgentRun{UserID: 8, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(context.Background(), run))

	for name, raw := range map[string][]byte{
		"url":          []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z","url":"https://sensitive.example"}`),
		"device_code":  []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z","device_code":"ABC"}`),
		"secret":       []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z","secret":"shh"}`),
		"future_field": []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z","future_sensitive":"unknown"}`),
	} {
		t.Run(name, func(t *testing.T) {
			err := writer.UpdatePendingExternalAction(context.Background(), run.ID, raw)
			require.Error(t, err)
		})
	}

	got, err := s.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Empty(t, got.PendingExternalActionJSON)
	assert.Nil(t, got.PendingExternalActionAt)
	assert.Equal(t, "", got.StateReason)
}

func TestAgentRunStore_UpdatePendingExternalAction_RequiresCompleteRestartIdentity(t *testing.T) {
	_, writer := newExternalActionAgentRunStore(t)

	for name, raw := range map[string][]byte{
		"provider":     []byte(`{"operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		"operation_id": []byte(`{"provider":"feishu","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		"session_id":   []byte(`{"provider":"feishu","operation_id":"op-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		"tool_call_id": []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		"phase":        []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","expires_at":"2026-07-13T09:30:00Z"}`),
		"expires_at":   []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth"}`),
	} {
		t.Run(name, func(t *testing.T) {
			err := writer.UpdatePendingExternalAction(context.Background(), 1, raw)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid payload")
		})
	}
}

func TestAgentRunStore_UpdatePendingExternalAction_RejectsDuplicateAndCaseVariantKeys(t *testing.T) {
	for name, raw := range map[string][]byte{
		"exact_duplicate": []byte(`{"provider":"feishu","provider":"lark","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		"case_variant":    []byte(`{"Provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		"mixed_duplicate": []byte(`{"provider":"feishu","Provider":"lark","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		"trailing":        []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"} {}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, writer := newExternalActionAgentRunStore(t)
			err := writer.UpdatePendingExternalAction(context.Background(), 1, raw)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid payload")
		})
	}
}

func TestAgentRunStore_UpdatePendingExternalAction_PersistsCanonicalJSON(t *testing.T) {
	s, writer := newExternalActionAgentRunStore(t)
	run := &model.AgentRun{UserID: 10, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(context.Background(), run))
	raw := []byte(" \n {\"expires_at\":\"2026-07-13T09:30:00Z\",\"phase\":\"user_auth\",\"tool_call_id\":\"call-1\",\"session_id\":\"auth-1\",\"operation_id\":\"op-1\",\"provider\":\"feishu\"} \t")

	require.NoError(t, writer.UpdatePendingExternalAction(context.Background(), run.ID, raw))

	got, err := s.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t,
		`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`,
		string(got.PendingExternalActionJSON),
	)
}

func TestAgentRunStore_UpdatePendingExternalAction_NotFound(t *testing.T) {
	_, writer := newExternalActionAgentRunStore(t)
	err := writer.UpdatePendingExternalAction(context.Background(), 9999, []byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no row matched")
}

func TestAgentRunStore_UpdatePendingQuestionPrompt_ClearsStaleExternalAction(t *testing.T) {
	s, writer := newExternalActionAgentRunStore(t)
	run := &model.AgentRun{UserID: 9, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, s.Create(context.Background(), run))
	require.NoError(t, writer.UpdatePendingExternalAction(context.Background(), run.ID,
		[]byte(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`)))

	question := []byte(`{"questions":[{"question":"继续吗？","options":[],"multi_select":false}]}`)
	require.NoError(t, s.UpdatePendingQuestion(context.Background(), run.ID, question))

	got, err := s.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.JSONEq(t, string(question), string(got.PendingQuestionJSON))
	assert.NotNil(t, got.PendingQuestionAt)
	assert.Empty(t, got.PendingExternalActionJSON)
	assert.Nil(t, got.PendingExternalActionAt)
}
