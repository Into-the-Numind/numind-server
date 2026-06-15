package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

// TestAnswerAndClear_MarksRunResumed reproduces dev run 148 (2026-06-12): a run
// paused at ask_user_question is persisted as status='terminated' +
// state_reason='waiting_for_user_choice' + ended_at set. Answering only flipped
// state_reason to 'running' — status stayed 'terminated' and ended_at stayed
// set for the WHOLE resumed leg (the detached-resume runner never corrects it
// either), so every poller saw a finished run while the agent kept working for
// 8.5 minutes. The frontend pushed the pre-question prose as a final answer
// and stopped following; the real report arrived unseen.
//
// Contract under test: AnswerAndClear atomically returns the row to a truthful
// running state — status='running', state_reason='running', ended_at NULL.
// Permanent regression protection (NDF Rule 11).
func TestAnswerAndClear_MarksRunResumed(t *testing.T) {
	s := newTestAgentRunStore(t)
	ctx := context.Background()

	run := &model.AgentRun{
		UserID:    1,
		SessionID: "sess-resume",
		Status:    "running",
		Messages:  datatypes.JSON(`[{"role":"user","content":"原始调研请求"}]`),
		StartedAt: time.Now(),
	}
	require.NoError(t, s.Create(ctx, run))

	// Simulate the ask_user_question yield terminal write (runner.go:1424).
	endedAt := time.Now()
	require.NoError(t, s.UpdateState(ctx, run.ID, "terminated", "waiting_for_user_choice", &endedAt))
	require.NoError(t, s.UpdatePendingQuestion(ctx, run.ID, datatypes.JSON(`{"questions":[{"question":"q","options":[]}]}`)))

	// The user answers (biz layer builds the full turn; store appends verbatim).
	answerTurn := json.RawMessage(`{"role":"user","content":"用户已回答你的问题：……"}`)
	require.NoError(t, s.AnswerAndClear(ctx, run.ID, answerTurn))

	got, err := s.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", got.Status,
		"an answered run is running again — advertising 'terminated' makes every poller declare it finished (dev run 148)")
	assert.Equal(t, "running", got.StateReason)
	assert.Nil(t, got.EndedAt, "ended_at written by the yield terminal must be cleared on resume")
	assert.Empty(t, got.PendingQuestionJSON, "pending question must be cleared")
}
