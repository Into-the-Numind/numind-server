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
	assert.Equal(t, "pending", q.AnswerStatus)
	assert.Equal(t, run.ID, q.RunID)
	require.Len(t, q.Questions, 1, "single-question pending wraps into a one-element array")
	assert.Equal(t, "贵公司的创办初心是什么？", q.Questions[0].Question)
	assert.Equal(t, "莫小派档案", q.Questions[0].Header)
	require.Len(t, q.Questions[0].Options, 2)
	assert.Equal(t, "创始人故事与初心", q.Questions[0].Options[0].Label)
	assert.Equal(t, "创始人是谁？为什么创办？", q.Questions[0].Options[0].Description)
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

func TestSynthesizeExternalAction_DurableResumeStatesKeepRunningCard(t *testing.T) {
	pendingAt := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		status string
		state  string
	}{
		{name: "ready", status: "terminated", state: "external_resume_ready"},
		{name: "starting", status: "running", state: "ext_resume:0123456789abcdef0123456789abcdef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &model.AgentRun{
				ID: 41, Status: tc.status, StateReason: tc.state, StartedAt: pendingAt,
				PendingExternalActionAt: &pendingAt,
				PendingExternalActionJSON: datatypes.JSON(
					`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`,
				),
			}
			msg, ok := synthesizeExternalAction(run)
			require.True(t, ok)
			assert.Equal(t, "running", frontendStatus(run.Status, run.StateReason))
			assert.Equal(t, "external_action", msg.Type)
			require.NotNil(t, msg.ExternalActionPayload)
			assert.Equal(t, "op-1", msg.OperationID)
			assert.Equal(t, "auth-1", msg.SessionID)
			assert.Equal(t, "tc-9", msg.ToolCallID)
			assert.Empty(t, msg.AuthURL, "restart-safe cards intentionally have no persisted URL")
		})
	}
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

// agent-multi-question: a waiting run that posed multiple questions synthesizes
// a single question_prompt message carrying ALL questions as an array.
func TestGetSessionSnapshot_WaitingRun_SynthesizesMultipleQuestions(t *testing.T) {
	svc, db := newSQServiceFull(t)

	pending := []byte(`{"questions":[` +
		`{"question":"陪跑周期多长？","header":"陪跑","options":[{"key":"a","label":"90天"},{"key":"b","label":"180天"}]},` +
		`{"question":"主要客群是谁？","multi_select":true,"options":[{"key":"a","label":"宝妈"},{"key":"b","label":"职场人"}]}` +
		`]}`)
	run := &model.AgentRun{
		UserID:              11,
		SessionID:           "snap-multi",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            datatypes.JSON(`[]`),
		PendingQuestionJSON: pending,
		StartedAt:           time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 11, run.SessionID)
	require.NoError(t, err)

	msgs := snap.Messages.([]agentMessage)
	require.Len(t, msgs, 1)
	q := msgs[0]
	assert.Equal(t, "question_prompt", q.Type)
	assert.Equal(t, "pending", q.AnswerStatus)
	require.Len(t, q.Questions, 2, "both questions must be synthesized into the array")
	assert.Equal(t, "陪跑周期多长？", q.Questions[0].Question)
	assert.Equal(t, "陪跑", q.Questions[0].Header)
	require.Len(t, q.Questions[0].Options, 2)
	assert.Equal(t, "90天", q.Questions[0].Options[0].Label)
	assert.False(t, q.Questions[0].MultiSelect)
	assert.Equal(t, "主要客群是谁？", q.Questions[1].Question)
	assert.True(t, q.Questions[1].MultiSelect)
	assert.Empty(t, q.Questions[1].Header, "second question has no header")
	require.Len(t, q.Questions[1].Options, 2, "second question's own options must not be cross-mapped")
	assert.Equal(t, "宝妈", q.Questions[1].Options[0].Label)
	assert.Equal(t, "职场人", q.Questions[1].Options[1].Label)
}

// agent-multi-question: a pure open-ended question (0 options) reloads with an
// empty options list (not null) — matching the live stream's omitempty shape.
func TestGetSessionSnapshot_WaitingRun_SynthesizesOpenEndedQuestion(t *testing.T) {
	svc, db := newSQServiceFull(t)

	pending := []byte(`{"questions":[{"question":"请提供你们的价格区间","options":[]}]}`)
	run := &model.AgentRun{
		UserID:              11,
		SessionID:           "snap-open",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            datatypes.JSON(`[]`),
		PendingQuestionJSON: pending,
		StartedAt:           time.Now(),
	}
	require.NoError(t, db.Create(run).Error)

	snap, err := svc.GetSessionSnapshot(context.Background(), 11, run.SessionID)
	require.NoError(t, err)

	msgs := snap.Messages.([]agentMessage)
	require.Len(t, msgs, 1)
	q := msgs[0]
	assert.Equal(t, "question_prompt", q.Type)
	require.Len(t, q.Questions, 1)
	assert.Equal(t, "请提供你们的价格区间", q.Questions[0].Question)
	assert.Empty(t, q.Questions[0].Options, "open-ended question has no options")

	// The serialized message omits the options key entirely (not null), matching
	// the live stream's omitempty contract the frontend parses.
	raw, mErr := json.Marshal(q)
	require.NoError(t, mErr)
	assert.NotContains(t, string(raw), `"options":null`)
}
