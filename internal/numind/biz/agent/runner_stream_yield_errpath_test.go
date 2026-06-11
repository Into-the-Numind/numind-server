package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/aiservice"
)

// yieldStreamFn returns a chatStreamFn mock whose stream requests an
// ask_user_question tool call (assembled ToolCalls surfaced on the
// IsFinal=true chunk with FinishReason="tool_calls", mirroring how
// OpenAI-compatible providers close a tool-call turn).
func yieldStreamFn() func(context.Context, string, aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		ch := make(chan aiservice.ChatChunk, 1)
		ch <- aiservice.ChatChunk{
			IsFinal:      true,
			FinishReason: "tool_calls",
			ToolCalls: []aiservice.ToolCall{{
				ID:   "call_yield_1",
				Type: "function",
				Function: aiservice.ToolCallFunction{
					Name:      "ask_user_question",
					Arguments: `{"questions":[{"question":"贵公司的创办初心是什么？","options":[{"key":"a","label":"我来口述"},{"key":"b","label":"上传资料"}]}]}`,
				},
			}},
		}
		close(ch)
		return ch, nil
	}
}

// test(qa): reproduce dev run #117 — ask_user_question yield on the STREAMING
// path killed the run with model_error ("服务暂时不可用") and persisted an empty
// transcript. Root cause: in multi-step ReAct, einoAgent.Stream() executes the
// graph synchronously, so a yield raised at a tools node surfaces as
// Stream()'s returned error ("[NodeRunError] failed to stream tool call ...:
// agent: yield for user question"). The streamErr branch in RunStream had no
// yield detection (neither sharedState.PendingYield nor errors.As), unlike
// consumeEinoStream which only covers yields arriving via the returned
// stream. Expected: the run pauses — TerminalWaitingForUserChoice, pending
// question persisted, question_prompt emitted — exactly like the non-stream
// Run path.
func TestRunStream_AskUserQuestion_PausesRun_NotModelError(t *testing.T) {
	withMockChatStreamFn(t, yieldStreamFn())

	ms := newMockStore()
	run := makeRunForStream(t, ms)
	reg := newStaticRegistry(NewAskUserQuestionTool())
	runner := NewAgentRunner(ms, reg)

	ch := make(chan stream.Event, 256)
	result, err := runner.RunStream(context.Background(), RunRequest{
		UserID:    1,
		Input:     "为莫小派做小红书定位调研",
		ToolNames: []string{"ask_user_question"},
	}, run.ID, ch)
	close(ch)

	require.NoError(t, err, "a yield is a pause, not an error")
	require.NotNil(t, result)
	assert.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason,
		"yield mid-ReAct must pause the run, not terminate it as model_error")

	// The pending question must be persisted so POST /answer can resume.
	stored, gErr := ms.Get(context.Background(), run.ID)
	require.NoError(t, gErr)
	assert.Equal(t, string(TerminalWaitingForUserChoice), stored.StateReason)
	assert.NotEmpty(t, stored.PendingQuestionJSON, "pending question payload must be persisted")

	// The SSE channel must carry a question_prompt so the UI can render it.
	sawQuestion := false
	for ev := range ch {
		if ev.Type == stream.EventQuestionPrompt {
			sawQuestion = true
		}
	}
	assert.True(t, sawQuestion, "question_prompt event must reach the SSE channel")
}
