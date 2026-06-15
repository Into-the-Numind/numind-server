package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

func withAgentGenTitleFn(t *testing.T, fn func(ctx context.Context, userMsg, assistantMsg string) (string, error)) {
	t.Helper()
	old := agentGenTitleFn
	agentGenTitleFn = fn
	t.Cleanup(func() { agentGenTitleFn = old })
}

// seedUnnamedRun inserts a completed agent_run with an empty session_name.
func seedUnnamedRun(t *testing.T, db *gorm.DB, userID uint, sessionID string) {
	t.Helper()
	msgs, _ := json.Marshal([]string{})
	require.NoError(t, db.Create(&model.AgentRun{
		UserID:      userID,
		SessionID:   sessionID,
		Status:      "completed",
		Messages:    msgs,
		StartedAt:   time.Now(),
		SessionName: "",
	}).Error)
}

func sessionNameOf(t *testing.T, db *gorm.DB, sessionID string) string {
	t.Helper()
	var run model.AgentRun
	require.NoError(t, db.Where("session_id = ?", sessionID).First(&run).Error)
	return run.SessionName
}

func TestMaybeGenerateSessionTitle_Unnamed_GeneratesAndPersists(t *testing.T) {
	db := newSQTestDB(t)
	rs := store.NewTestStore(db).AgentRuns()
	seedUnnamedRun(t, db, 1, "s1")

	withAgentGenTitleFn(t, func(_ context.Context, u, f string) (string, error) {
		assert.Equal(t, "帮我做竞品调研", u)
		assert.Contains(t, f, "竞品")
		return "竞品调研", nil
	})

	maybeGenerateSessionTitle(context.Background(), rs, "s1", "帮我做竞品调研", "竞品分析结果如下……")
	assert.Equal(t, "竞品调研", sessionNameOf(t, db, "s1"), "session_name persisted")
}

func TestMaybeGenerateSessionTitle_AlreadyNamed_Skips(t *testing.T) {
	db := newSQTestDB(t)
	rs := store.NewTestStore(db).AgentRuns()
	msgs, _ := json.Marshal([]string{})
	require.NoError(t, db.Create(&model.AgentRun{
		UserID: 1, SessionID: "s1", Status: "completed", Messages: msgs,
		StartedAt: time.Now(), SessionName: "用户已命名",
	}).Error)

	called := false
	withAgentGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		called = true
		return "x", nil
	})

	maybeGenerateSessionTitle(context.Background(), rs, "s1", "q", "a")
	assert.False(t, called, "named session → no generation")
	assert.Equal(t, "用户已命名", sessionNameOf(t, db, "s1"), "manual name preserved (US3)")
}

// CAS: the user renames the session DURING title generation; the compare-and-set
// (session_name=”) must not clobber it.
func TestMaybeGenerateSessionTitle_ConcurrentRename_NoClobber(t *testing.T) {
	db := newSQTestDB(t)
	rs := store.NewTestStore(db).AgentRuns()
	seedUnnamedRun(t, db, 1, "s1")

	withAgentGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		// Simulate a manual rename landing while the LLM call is in flight.
		require.NoError(t, db.Model(&model.AgentRun{}).
			Where("session_id = ?", "s1").Update("session_name", "用户中途改的").Error)
		return "自动标题", nil
	})

	maybeGenerateSessionTitle(context.Background(), rs, "s1", "q", "a")
	assert.Equal(t, "用户中途改的", sessionNameOf(t, db, "s1"), "concurrent manual rename must NOT be clobbered")
}

func TestMaybeGenerateSessionTitle_GenerateError_NoChange(t *testing.T) {
	db := newSQTestDB(t)
	rs := store.NewTestStore(db).AgentRuns()
	seedUnnamedRun(t, db, 1, "s1")

	withAgentGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		return "", errors.New("llm down")
	})

	maybeGenerateSessionTitle(context.Background(), rs, "s1", "q", "a")
	assert.Equal(t, "", sessionNameOf(t, db, "s1"), "generate failure → session stays unnamed")
}

func TestMaybeGenerateSessionTitle_EmptySessionID_NoOp(t *testing.T) {
	db := newSQTestDB(t)
	rs := store.NewTestStore(db).AgentRuns()
	called := false
	withAgentGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		called = true
		return "x", nil
	})
	maybeGenerateSessionTitle(context.Background(), rs, "", "q", "a")
	assert.False(t, called, "empty sessionID → no-op")
}
