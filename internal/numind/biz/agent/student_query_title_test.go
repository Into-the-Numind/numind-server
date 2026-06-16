package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

func TestGenerateSessionTitle_Agent(t *testing.T) {
	svc, db := newSQService(t)
	msgs, _ := json.Marshal([]string{})
	mk := func(uid uint, sid, name string) {
		require.NoError(t, db.Create(&model.AgentRun{
			UserID: uid, SessionID: sid, Status: "completed", Messages: msgs,
			StartedAt: time.Now(), SessionName: name,
		}).Error)
	}

	// owner + unnamed → generates + persists
	mk(1, "s-new", "")
	withAgentGenTitleFn(t, func(_ context.Context, p, a string) (string, error) {
		assert.Contains(t, p, "竞品调研")
		assert.Empty(t, a, "instant path passes prompt only (no assistant reply)")
		return "竞品调研", nil
	})
	title, err := svc.GenerateSessionTitle(context.Background(), 1, "s-new", "帮我做竞品调研")
	require.NoError(t, err)
	assert.Equal(t, "竞品调研", title)
	var row model.AgentRun
	require.NoError(t, db.Where("session_id = ?", "s-new").First(&row).Error)
	assert.Equal(t, "竞品调研", row.SessionName, "persisted")

	// wrong user → forbidden, no generation
	mk(2, "s-other", "")
	called := false
	withAgentGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) { called = true; return "x", nil })
	_, err = svc.GenerateSessionTitle(context.Background(), 999, "s-other", "q")
	require.Error(t, err, "non-owner must be rejected")
	assert.ErrorIs(t, err, errno.ErrForbidden, "non-owner must get ErrForbidden, not some other error")
	assert.False(t, called)

	// already named → "" (skip)
	mk(3, "s-named", "用户已命名")
	withAgentGenTitleFn(t, func(_ context.Context, _, _ string) (string, error) {
		t.Fatal("must not generate for already-named session")
		return "", nil
	})
	title, err = svc.GenerateSessionTitle(context.Background(), 3, "s-named", "q")
	require.NoError(t, err)
	assert.Empty(t, title)

	// no run for session → ErrAgentRunNotFound
	_, err = svc.GenerateSessionTitle(context.Background(), 1, "s-missing", "q")
	require.Error(t, err, "session with no run must error")
}
