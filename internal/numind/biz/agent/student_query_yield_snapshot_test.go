package agent

import (
	"context"
	"encoding/json"

	"testing"
	"time"

	"gorm.io/datatypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// test(qa): reproduce dev run #117/#118 — a run paused at ask_user_question
// (state_reason=waiting_for_user_choice) persists no transcript, and
// GetSessionSnapshot returned an EMPTY message list with no trace of the
// pending question. On reload the session rendered "一片空白" (blank) with no
// way to resume. Expected: the snapshot synthesizes a question_prompt message
// (answer_status=pending) from pending_question_json so the UI can re-render
// the interactive card and the learner can answer.
func TestGetSessionSnapshot_WaitingRun_SynthesizesQuestionPrompt(t *testing.T) {
	svc, db := newSQServiceFull(t)

	pending, _ := json.Marshal(map[string]any{
		"question":     "贵公司的创办初心是什么？",
		"header":       "莫小派档案",
		"multi_select": false,
		"options": []map[string]string{
			{"key": "founder_story", "label": "创始人故事与初心", "description": "创始人是谁？为什么创办？"},
			{"key": "city_audience", "label": "城市与客群", "description": "在哪个城市？目标人群？"},
		},
	})
	run := &model.AgentRun{
		UserID:              11,
		SessionID:           "snap-waiting",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            datatypes.JSON(`[]`),
		PendingQuestionJSON: pending,
		StartedAt:           time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 11, run.SessionID)
	require.NoError(t, err)

	msgs, ok := snap.Messages.([]agentMessage)
	require.True(t, ok)
	require.Len(t, msgs, 1, "waiting run with empty transcript must still surface the pending question")

	q := msgs[0]
	assert.Equal(t, "question_prompt", q.Type)
	assert.Equal(t, "贵公司的创办初心是什么？", q.Question)
	assert.Equal(t, "莫小派档案", q.Header)
	assert.Equal(t, "pending", q.AnswerStatus)
	assert.Equal(t, run.ID, q.RunID)
	require.Len(t, q.Options, 2)
	assert.Equal(t, "创始人故事与初心", q.Options[0].Label)
	assert.Equal(t, "创始人是谁？为什么创办？", q.Options[0].Description)
}

// A waiting run that already has a transcript (multi-step before the yield)
// must surface the question AFTER the existing turns, not replace them.
func TestGetSessionSnapshot_WaitingRun_AppendsQuestionAfterTranscript(t *testing.T) {
	svc, db := newSQServiceFull(t)

	pending, _ := json.Marshal(map[string]any{
		"question":     "还需要补充什么？",
		"multi_select": false,
		"options": []map[string]string{
			{"key": "a", "label": "选项A"},
			{"key": "b", "label": "选项B"},
		},
	})
	transcript, _ := json.Marshal([]map[string]string{
		{"role": "user", "content": "帮我做调研"},
		{"role": "assistant", "content": "我先查一下"},
	})
	run := &model.AgentRun{
		UserID:              11,
		SessionID:           "snap-waiting-2",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            transcript,
		PendingQuestionJSON: pending,
		StartedAt:           time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 11, run.SessionID)
	require.NoError(t, err)

	msgs := snap.Messages.([]agentMessage)
	require.Len(t, msgs, 3, "2 transcript turns + 1 synthesized question")
	assert.Equal(t, "user", msgs[0].Type)
	assert.Equal(t, "question_prompt", msgs[2].Type)
}

// A completed run (not waiting) must NOT synthesize a question even if a stale
// pending_question_json lingers — only waiting_for_user_choice triggers it.
func TestGetSessionSnapshot_CompletedRun_NoQuestionSynthesized(t *testing.T) {
	svc, db := newSQServiceFull(t)

	stalePending, _ := json.Marshal(map[string]any{
		"question": "stale", "multi_select": false,
		"options": []map[string]string{{"key": "a", "label": "A"}, {"key": "b", "label": "B"}},
	})
	transcript, _ := json.Marshal([]map[string]string{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": "done"},
	})
	run := &model.AgentRun{
		UserID:              11,
		SessionID:           "snap-completed",
		Status:              "terminated",
		StateReason:         "completed",
		Messages:            transcript,
		PendingQuestionJSON: stalePending,
		StartedAt:           time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 11, run.SessionID)
	require.NoError(t, err)

	msgs := snap.Messages.([]agentMessage)
	for _, m := range msgs {
		assert.NotEqual(t, "question_prompt", m.Type, "completed run must not synthesize a question")
	}
}
