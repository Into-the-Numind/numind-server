package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/model"
)

// makeRunner builds a minimal agentRunner for stream tests.
func makeRunner() *agentRunner {
	return &agentRunner{
		runStore: newMockStore(),
		cancels:  make(map[uint64]context.CancelFunc),
	}
}

// makeRun builds a minimal AgentRun for stream tests.
func makeRun(id uint64) *model.AgentRun {
	return &model.AgentRun{
		ID:        id,
		UserID:    1,
		SessionID: "sess-test",
		Status:    "running",
	}
}

// makeStreamReader wraps a slice of *schema.Message into a StreamReader.
// An io.EOF is appended automatically after all messages.
func makeStreamReader(msgs []*schema.Message) *schema.StreamReader[*schema.Message] {
	return schema.StreamReaderFromArray(msgs)
}

// collectEvents drains ch until closed and returns all events.
func collectEvents(ch <-chan stream.Event) []stream.Event {
	var evs []stream.Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

// eventTypes extracts the event type list from a slice of events.
func eventTypes(evs []stream.Event) []stream.EventType {
	types := make([]stream.EventType, len(evs))
	for i, ev := range evs {
		types[i] = ev.Type
	}
	return types
}

// allEventsOfType returns all events with the given type.
func allEventsOfType(evs []stream.Event, t stream.EventType) []stream.Event {
	var result []stream.Event
	for _, ev := range evs {
		if ev.Type == t {
			result = append(result, ev)
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Test: pure text stream — 3 text chunks + FinishReason chunk
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_PureText(t *testing.T) {
	r := makeRunner()
	run := makeRun(1)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "Hello"},
		{Role: schema.Assistant, Content: " world"},
		{Role: schema.Assistant, Content: "!"},
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 32)
	ctx := context.Background()

	result, err := r.consumeEinoStream(ctx, run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	types := eventTypes(evs)

	// Expect: stream_start, 3×token_delta, assistant_message, step_done, terminal
	assert.Contains(t, types, stream.EventStreamStart)
	assert.Contains(t, types, stream.EventAssistantMessage)
	assert.Contains(t, types, stream.EventStepDone)
	assert.Contains(t, types, stream.EventTerminal)

	// Count token_deltas
	deltas := allEventsOfType(evs, stream.EventTokenDelta)
	assert.Len(t, deltas, 3)

	// TerminalReason == completed
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	assert.Equal(t, TerminalCompleted, st.TerminalReason)
}

// TestConsumeEinoStream_FinalOutputCapturedAfterStepDoneReset REPRODUCES the
// dev 2026-05-28 bug: each step boundary (FinishReason chunk) calls
// currentText.Reset() to prepare for the next step. EOF afterwards emits the
// terminal event with `FinalOutput: currentText.String()` — but currentText
// was just reset, so FinalOutput is empty. The same empty string is then
// returned as RunResult.FinalOutput → finalizeRun writes assistant content=""
// to agent_run.messages. UI flow works (token_delta accumulated in the
// frontend store) but page reload / loadSessionSnapshot returns empty
// history.
//
// Contract: result.FinalOutput must equal the last step's accumulated content.
func TestConsumeEinoStream_FinalOutputCapturedAfterStepDoneReset(t *testing.T) {
	r := makeRunner()
	run := makeRun(99)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "Hello"},
		{Role: schema.Assistant, Content: " world"},
		{Role: schema.Assistant, Content: "!"},
		// Step-done chunk: currentText was "Hello world!", now FinishReason
		// triggers Reset. Pre-fix: result.FinalOutput is read AFTER reset and
		// comes back as "". Post-fix: lastStepContent stashes the string
		// before reset and EOF uses it.
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 32)
	result, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "Hello world!", result.FinalOutput,
		"result.FinalOutput must carry the last step's content; currentText.Reset() at the step boundary must NOT clobber the final answer")

	// The terminal event also carries FinalOutput — decode the JSON RawMessage
	// and assert it matches.
	terminals := allEventsOfType(collectEvents(ch), stream.EventTerminal)
	if len(terminals) > 0 {
		var payload stream.TerminalPayload
		if jsonErr := json.Unmarshal(terminals[0].Data, &payload); jsonErr == nil {
			assert.Equal(t, "Hello world!", payload.FinalOutput,
				"EventTerminal.FinalOutput must also carry last step content")
		}
	}
}

// ---------------------------------------------------------------------------
// Test: single tool call
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_WithToolCall(t *testing.T) {
	r := makeRunner()
	run := makeRun(2)
	st := &LoopState{}

	msgs := []*schema.Message{
		// LLM token
		{Role: schema.Assistant, Content: "Let me search"},
		// LLM emits tool call
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "tc1", Type: "function", Function: schema.FunctionCall{Name: "web_search", Arguments: `{"query":"test"}`}},
			},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
		},
		// Tool result
		{Role: schema.Tool, ToolCallID: "tc1", Content: "Search results here"},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 32)
	result, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	types := eventTypes(evs)

	assert.Contains(t, types, stream.EventTokenDelta)
	assert.Contains(t, types, stream.EventToolCallStart)
	assert.Contains(t, types, stream.EventToolCallResult)
	assert.Contains(t, types, stream.EventAssistantMessage)
	assert.Contains(t, types, stream.EventStepDone)
	assert.Contains(t, types, stream.EventTerminal)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
}

// ---------------------------------------------------------------------------
// Test: multi-step — two FinishReason boundaries → two different message IDs
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_MultiStep(t *testing.T) {
	r := makeRunner()
	run := makeRun(3)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "step1 text"},
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}},
		{Role: schema.Tool, ToolCallID: "tc1", Content: "result"},
		{Role: schema.Assistant, Content: "step2 text"},
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 64)
	_, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)

	// Should have 2 step_done events
	stepDones := allEventsOfType(evs, stream.EventStepDone)
	assert.Len(t, stepDones, 2)

	// Should have 2 assistant_message events with different message IDs
	asMsgs := allEventsOfType(evs, stream.EventAssistantMessage)
	assert.Len(t, asMsgs, 2)

	// Verify the message IDs differ (P2 fix: was unmarshalling into itself — dead assertion).
	if len(asMsgs) == 2 {
		type msgPayload struct {
			MessageID string `json:"message_id"`
		}
		var p1, p2 msgPayload
		require.NoError(t, json.Unmarshal(asMsgs[0].Data, &p1))
		require.NoError(t, json.Unmarshal(asMsgs[1].Data, &p2))
		assert.NotEqual(t, p1.MessageID, p2.MessageID,
			"each step boundary must produce a fresh UUID message_id")
	}
}

// ---------------------------------------------------------------------------
// Test: ctx cancel mid-stream → emit terminal(aborted_streaming)
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_CtxCancel(t *testing.T) {
	r := makeRunner()
	run := makeRun(4)
	st := &LoopState{}

	// Use a cancelled context from the start to test the ctx.Done path.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "never delivered"},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 32)
	result, err := r.consumeEinoStream(ctx, run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	assert.Equal(t, TerminalAbortedStreaming, result.TerminalReason)
	assert.Equal(t, TerminalAbortedStreaming, st.TerminalReason)
}

// ---------------------------------------------------------------------------
// Test: stream error → emit error + TerminalReason set
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_StreamErr(t *testing.T) {
	r := makeRunner()
	run := makeRun(5)
	st := &LoopState{}

	// Build a stream reader that returns an error.
	streamErr := errors.New("model_error: provider timeout")
	sr, sw := schema.Pipe[*schema.Message](4)
	sw.Send(nil, streamErr)
	sw.Close()

	ch := make(chan stream.Event, 32)
	result, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)

	// consumeEinoStream returns the original error on stream failure.
	assert.Error(t, err)
	assert.Equal(t, TerminalModelError, result.TerminalReason)
	assert.Equal(t, TerminalModelError, st.TerminalReason)

	evs := collectEvents(ch)
	types := eventTypes(evs)
	assert.Contains(t, types, stream.EventError)
	assert.Contains(t, types, stream.EventTerminal)
}

// ---------------------------------------------------------------------------
// Test: stream err classified as image_error
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_ImageError(t *testing.T) {
	r := makeRunner()
	run := makeRun(6)
	st := &LoopState{}

	imgErr := errors.New("image_decode_failed: unsupported format")
	sr, sw := schema.Pipe[*schema.Message](4)
	sw.Send(nil, imgErr)
	sw.Close()

	ch := make(chan stream.Event, 32)
	result, _ := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)

	assert.Equal(t, TerminalImageError, result.TerminalReason)
	assert.Equal(t, TerminalImageError, st.TerminalReason)
}

// ---------------------------------------------------------------------------
// Test: reasoning content interleaved with text content
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_ReasoningInterleaved(t *testing.T) {
	r := makeRunner()
	run := makeRun(7)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, ReasoningContent: "thinking..."},
		{Role: schema.Assistant, Content: "Answer"},
		{Role: schema.Assistant, ReasoningContent: " more thinking"},
		{Role: schema.Assistant, Content: " here", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 32)
	_, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	types := eventTypes(evs)

	reasoningDeltas := allEventsOfType(evs, stream.EventReasoningDelta)
	tokenDeltas := allEventsOfType(evs, stream.EventTokenDelta)

	assert.Len(t, reasoningDeltas, 2)
	assert.Len(t, tokenDeltas, 2)
	assert.Contains(t, types, stream.EventAssistantMessage)
}

// ---------------------------------------------------------------------------
// Test: channel buffer not deadlocking when full — uses select with ctx.Done
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_NoDeadlockFullChannel(t *testing.T) {
	r := makeRunner()
	run := makeRun(8)
	st := &LoopState{}

	// Use a context with a short timeout to unblock channel sends.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Build many messages to overflow a small channel.
	msgs := make([]*schema.Message, 0, 20)
	for i := 0; i < 18; i++ {
		msgs = append(msgs, &schema.Message{Role: schema.Assistant, Content: "x"})
	}
	msgs = append(msgs, &schema.Message{
		Role: schema.Assistant, Content: "",
		ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
	})
	sr := makeStreamReader(msgs)

	// Channel capacity = 4 (smaller than message count) to exercise the
	// select/ctx.Done path in emit().
	ch := make(chan stream.Event, 4)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.consumeEinoStream(ctx, run, sr, ch, st, time.Now())
		close(ch)
	}()

	// Drain slowly so we exercise the select path.
	go func() {
		for range ch { //nolint:revive
		}
	}()

	select {
	case <-done:
		// Success — no deadlock.
	case <-time.After(3 * time.Second):
		t.Fatal("consumeEinoStream deadlocked with full channel")
	}
}

// ---------------------------------------------------------------------------
// Test: tool call deduplication — same tc.ID in multiple chunks
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_ToolCallDedup(t *testing.T) {
	r := makeRunner()
	run := makeRun(9)
	st := &LoopState{}

	msgs := []*schema.Message{
		// Same tool call ID appears twice (simulates chunked streaming of ToolCalls).
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "tc1", Type: "function", Function: schema.FunctionCall{Name: "web_search", Arguments: `{}`}},
			},
		},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "tc1", Type: "function", Function: schema.FunctionCall{Name: "web_search", Arguments: `{}`}},
			},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
		},
		{Role: schema.Tool, ToolCallID: "tc1", Content: "result"},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 32)
	_, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	starts := allEventsOfType(evs, stream.EventToolCallStart)
	// Despite two chunks with the same ID, only one start should be emitted.
	assert.Len(t, starts, 1)
}

// ---------------------------------------------------------------------------
// Test: seq is monotonically increasing
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_SeqMonotonic(t *testing.T) {
	r := makeRunner()
	run := makeRun(10)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "a"},
		{Role: schema.Assistant, Content: "b"},
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 32)
	_, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	for i := 1; i < len(evs); i++ {
		assert.Greater(t, evs[i].Seq, evs[i-1].Seq,
			"event at index %d should have larger Seq than index %d", i, i-1)
	}
}

// TestConsumeEinoStream_SeqContinuesFromSharedState guards the T3 unification: when
// a StreamSessionState is in context, consumeEinoStream must draw its seq from the
// SHARED atomic counter (state.Seq) — continuing after whatever the checker/adapter
// already emitted during the graph — not restart at 1 with a private counter.
func TestConsumeEinoStream_SeqContinuesFromSharedState(t *testing.T) {
	r := makeRunner()
	run := makeRun(11)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "a"},
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	// Simulate the checker/adapter having advanced the shared counter to 7 during
	// the graph. Ch is nil so consumeEinoStream owns all emits (checkerActive=false),
	// but the seq source is still the shared state.Seq.
	shared := &StreamSessionState{RunID: run.ID}
	shared.Seq.Store(7)
	ctx := WithStreamState(context.Background(), shared)

	ch := make(chan stream.Event, 32)
	_, err := r.consumeEinoStream(ctx, run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	require.NotEmpty(t, evs)
	assert.Greater(t, evs[0].Seq, uint64(7),
		"first emitted seq must continue from the shared state.Seq (was 7), not restart at 1")
	for i := 1; i < len(evs); i++ {
		assert.Greater(t, evs[i].Seq, evs[i-1].Seq)
	}
}

// ---------------------------------------------------------------------------
// Test: RunID in all events matches run.ID
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_RunIDPropagated(t *testing.T) {
	r := makeRunner()
	run := makeRun(42)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "hi", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 32)
	_, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	for _, ev := range evs {
		assert.Equal(t, run.ID, ev.RunID, "all events must carry run.ID=%d", run.ID)
	}
}

// ---------------------------------------------------------------------------
// Helper: verify stream.Pipe and io.EOF behaviour
// ---------------------------------------------------------------------------

func TestConsumeEinoStream_EmptyStream(t *testing.T) {
	r := makeRunner()
	run := makeRun(11)
	st := &LoopState{}

	// Empty stream — no messages, just EOF.
	sr, sw := schema.Pipe[*schema.Message](4)
	sw.Send(nil, io.EOF)
	sw.Close()

	ch := make(chan stream.Event, 32)
	result, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	// Should still emit terminal(completed).
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	evs := collectEvents(ch)
	terminals := allEventsOfType(evs, stream.EventTerminal)
	assert.Len(t, terminals, 1)
}

// ---------------------------------------------------------------------------
// T3 (agent-stream-interactivity): ask_user_question yield on the STREAMING
// path. Before this fix the yield sentinel was classified as model_error and
// the user saw "任务失败" instead of the clarifying question; the run never
// reached waiting_for_user_choice and could not be answered/resumed.
// ---------------------------------------------------------------------------

// Yield captured into stream state by the tool adapter, surfacing as a clean
// EOF (sentinel did not propagate as a stream error). The EOF branch must
// detect PendingYield and emit question_prompt + waiting terminal, NOT completed.
func TestConsumeEinoStream_YieldViaPendingState(t *testing.T) {
	r := makeRunner()
	run := makeRun(77)
	// Register the run so UpdatePendingQuestion hits the success path (not the
	// not-found warn fallback) — lets us assert the question was persisted.
	r.runStore.(*mockAgentRunStore).runs[run.ID] = run
	st := &LoopState{}

	ch := make(chan stream.Event, 32)
	state := &StreamSessionState{
		Ch:    ch,
		RunID: run.ID,
		PendingYield: &YieldPayload{Questions: []YieldQuestion{{
			Question:    "你想要哪种格式?",
			Options:     []YieldOption{{Key: "pdf", Label: "PDF"}, {Key: "csv", Label: "CSV 表格"}},
			MultiSelect: false,
		}}},
	}
	ctx := WithStreamState(context.Background(), state)
	sr := makeStreamReader([]*schema.Message{}) // empty → clean EOF

	result, err := r.consumeEinoStream(ctx, run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)
	assert.Equal(t, TerminalWaitingForUserChoice, st.TerminalReason)

	evs := collectEvents(ch)
	qps := allEventsOfType(evs, stream.EventQuestionPrompt)
	require.Len(t, qps, 1, "exactly one question_prompt must be emitted")

	var qp stream.QuestionPromptPayload
	require.NoError(t, json.Unmarshal(qps[0].Data, &qp))
	require.Len(t, qp.Questions, 1)
	assert.Equal(t, "你想要哪种格式?", qp.Questions[0].Question)
	require.Len(t, qp.Questions[0].Options, 2, "structured options must be forwarded (not dropped/stringified)")
	assert.Equal(t, "PDF", qp.Questions[0].Options[0].Label)
	assert.Equal(t, "CSV 表格", qp.Questions[0].Options[1].Label)

	terms := allEventsOfType(evs, stream.EventTerminal)
	require.Len(t, terms, 1)
	var tp stream.TerminalPayload
	require.NoError(t, json.Unmarshal(terms[0].Data, &tp))
	assert.Equal(t, string(TerminalWaitingForUserChoice), tp.Reason)

	// A pause is not a failure — no error event.
	assert.Empty(t, allEventsOfType(evs, stream.EventError))

	// Pending question persisted for the /answer resume path.
	persisted := r.runStore.(*mockAgentRunStore).runs[run.ID]
	assert.NotEmpty(t, persisted.PendingQuestionJSON, "yield must persist pending_question for the resume path")
}

// Yield sentinel propagating as a stream error (no PendingYield set): the error
// branch's errors.As fallback must still surface the question, not model_error.
func TestConsumeEinoStream_YieldViaStreamError(t *testing.T) {
	r := makeRunner()
	run := makeRun(78)
	st := &LoopState{}

	ch := make(chan stream.Event, 32)
	state := &StreamSessionState{Ch: ch, RunID: run.ID} // no PendingYield
	ctx := WithStreamState(context.Background(), state)

	sr, sw := schema.Pipe[*schema.Message](4)
	_ = sw.Send(nil, &yieldError{Payload: YieldPayload{Questions: []YieldQuestion{{
		Question: "继续吗?",
		Options:  []YieldOption{{Key: "y", Label: "继续"}, {Key: "n", Label: "停止"}},
	}}}})
	sw.Close()

	result, err := r.consumeEinoStream(ctx, run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err) // yield is a pause, not an error outcome
	require.NotNil(t, result)
	assert.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)

	evs := collectEvents(ch)
	require.Len(t, allEventsOfType(evs, stream.EventQuestionPrompt), 1)
	assert.Empty(t, allEventsOfType(evs, stream.EventError))
}
