package agent

// Regression tests for T4 (#4): plain-text double-emission in agent-mode streaming.
//
// Two consumers drain the SAME model output and emit per-step ECHO events
// (token_delta / reasoning_delta / assistant_message / step_done) to the SAME
// SSE channel:
//
//   - streamScanToolCallChecker (runner_runstream.go): eino's
//     StreamToolCallChecker, the intentional "live pump". For every model chunk
//     it emits token_delta/reasoning_delta, and on FinishReason emits
//     assistant_message + step_done — using the SHARED StreamSessionState (Seq /
//     CurrentMsgID / StepIdx) and writing to the SHARED channel state.Ch.
//   - consumeEinoStream (runner_stream.go): drains the END copy eino's
//     Agent.Stream returns and ALSO emitted the same ECHO events for the final
//     step to the same channel.
//
// eino feeds both consumers via schema.StreamReader.Copy(n): the checker gets
// one copy, the END drain gets the other; both replay the SAME messages. So for
// a plain-text answer (no tool call) the final assistant text was emitted TWICE
// → the user saw doubled text.
//
// The fix gates consumeEinoStream's four ECHO emits behind
// checkerActive = hasState && state.Ch != nil (mirroring the checker's own emit
// guard). When the checker is active, consumeEinoStream only ACCUMULATES content
// (for RunResult / persistence / stash) and emits the things it solely owns
// (stream_start, the single terminal, tool_call_*, error, yield).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
)

// runCheckerThenConsume mirrors the production wiring: a single model output is
// Copy(2)'d, one copy fed to streamScanToolCallChecker (the live pump) and the
// other to consumeEinoStream (the END drain), both sharing the same
// StreamSessionState and the same channel. Returns every event written to the
// channel, in order.
func runCheckerThenConsume(t *testing.T, msgs []*schema.Message) []stream.Event {
	t.Helper()
	r := makeRunner()
	run := makeRun(4444)
	st := &LoopState{}

	ch := make(chan stream.Event, 256)
	sharedState := &StreamSessionState{
		Ch:           ch,
		RunID:        run.ID,
		CurrentMsgID: "msg-shared-0",
	}
	ctx := WithStreamState(context.Background(), sharedState)

	// eino feeds the StreamToolCallChecker and the END node from copies of the
	// same model output. Replay that with Copy(2).
	src := makeStreamReader(msgs)
	copies := src.Copy(2)
	checkerSR, consumeSR := copies[0], copies[1]

	// 1) Live pump runs first (eino calls StreamToolCallChecker to decide routing).
	_, checkErr := streamScanToolCallChecker(ctx, checkerSR)
	require.NoError(t, checkErr)

	// 2) END drain runs next, sharing the SAME state + channel.
	_, consumeErr := r.consumeEinoStream(ctx, run, consumeSR, ch, st, time.Now())
	require.NoError(t, consumeErr)

	close(ch)
	return collectEvents(ch)
}

// concatTokenDeltaText returns the concatenation of all token_delta payload Text
// fields across the given events.
func concatTokenDeltaText(t *testing.T, evs []stream.Event) string {
	t.Helper()
	var out string
	for _, ev := range allEventsOfType(evs, stream.EventTokenDelta) {
		var p stream.TokenDeltaPayload
		require.NoError(t, json.Unmarshal(ev.Data, &p))
		out += p.Text
	}
	return out
}

// TestStreaming_PlainText_NoDoubleEmission is the RED→GREEN reproduction for
// T4 (#4). A plain-text answer is pumped through BOTH the live checker and
// consumeEinoStream sharing one channel; the final answer text must be emitted
// EXACTLY ONCE across token_delta events, and exactly one assistant_message /
// one step_done must fire for the single step.
//
// Pre-fix observed counts (current code, run before applying the gate):
//   - token_delta: 6 (3 from checker + 3 from consumeEinoStream) → text doubled
//   - assistant_message: 2 (1 each)
//   - step_done: 2 (1 each)
func TestStreaming_PlainText_NoDoubleEmission(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "Hello"},
		{Role: schema.Assistant, Content: " world"},
		{Role: schema.Assistant, Content: "!"},
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}

	evs := runCheckerThenConsume(t, msgs)

	tokenDeltas := allEventsOfType(evs, stream.EventTokenDelta)
	assistantMsgs := allEventsOfType(evs, stream.EventAssistantMessage)
	stepDones := allEventsOfType(evs, stream.EventStepDone)

	// The concatenated token_delta text must equal the answer exactly once.
	assert.Equal(t, "Hello world!", concatTokenDeltaText(t, evs),
		"concatenated token_delta text must equal the answer exactly once (not doubled)")

	// Exactly the live checker's per-step echoes survive — consumeEinoStream
	// must NOT re-emit them while the checker is active.
	assert.Len(t, tokenDeltas, 3,
		"token_delta must be emitted once per content chunk (live checker only), not doubled")
	assert.Len(t, assistantMsgs, 1,
		"assistant_message must fire once for the single step (live checker only)")
	assert.Len(t, stepDones, 1,
		"step_done must fire once for the single step (live checker only)")

	// consumeEinoStream still owns these regardless of the checker.
	assert.Len(t, allEventsOfType(evs, stream.EventStreamStart), 1,
		"consumeEinoStream must still emit exactly one stream_start")
	terminals := allEventsOfType(evs, stream.EventTerminal)
	require.Len(t, terminals, 1, "consumeEinoStream must still emit exactly one terminal")

	// The single terminal must still carry the accumulated final answer
	// (stash/rotation stays unconditional under the fix).
	var tp stream.TerminalPayload
	require.NoError(t, json.Unmarshal(terminals[0].Data, &tp))
	assert.Equal(t, string(TerminalCompleted), tp.Reason)
	assert.Equal(t, "Hello world!", tp.FinalOutput,
		"terminal.FinalOutput must still carry the accumulated answer (stash unconditional)")
}

// TestStreaming_Reasoning_NoDoubleEmission extends the repro to a thinking
// model: reasoning_content + content interleaved. Neither reasoning_delta nor
// token_delta may double when the checker is active.
func TestStreaming_Reasoning_NoDoubleEmission(t *testing.T) {
	msgs := []*schema.Message{
		{Role: schema.Assistant, ReasoningContent: "let me think"},
		{Role: schema.Assistant, Content: "The answer"},
		{Role: schema.Assistant, Content: " is 42", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}

	evs := runCheckerThenConsume(t, msgs)

	assert.Len(t, allEventsOfType(evs, stream.EventReasoningDelta), 1,
		"reasoning_delta must not double (live checker only)")
	assert.Len(t, allEventsOfType(evs, stream.EventTokenDelta), 2,
		"token_delta must not double (live checker only)")
	assert.Equal(t, "The answer is 42", concatTokenDeltaText(t, evs))
	assert.Len(t, allEventsOfType(evs, stream.EventAssistantMessage), 1)
	assert.Len(t, allEventsOfType(evs, stream.EventStepDone), 1)
}

// TestConsumeEinoStream_NoChecker_StillEmitsEchoes pins the fallback: when no
// live checker is active (hasState=false OR state.Ch==nil), consumeEinoStream
// must STILL emit the per-step echoes itself, so a hypothetical no-checker path
// is never silent. This is the !checkerActive branch.
func TestConsumeEinoStream_NoChecker_StillEmitsEchoes(t *testing.T) {
	r := makeRunner()
	run := makeRun(4445)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "Hello"},
		{Role: schema.Assistant, Content: " world"},
		{Role: schema.Assistant, ReasoningContent: "hmm"},
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 64)
	// No StreamState in ctx → checkerActive=false → consumeEinoStream is the
	// sole emitter and MUST emit the echoes.
	result, err := r.consumeEinoStream(context.Background(), run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	assert.Len(t, allEventsOfType(evs, stream.EventTokenDelta), 2,
		"no-checker fallback: consumeEinoStream must emit token_delta")
	assert.Len(t, allEventsOfType(evs, stream.EventReasoningDelta), 1,
		"no-checker fallback: consumeEinoStream must emit reasoning_delta")
	assert.Len(t, allEventsOfType(evs, stream.EventAssistantMessage), 1,
		"no-checker fallback: consumeEinoStream must emit assistant_message")
	assert.Len(t, allEventsOfType(evs, stream.EventStepDone), 1,
		"no-checker fallback: consumeEinoStream must emit step_done")
	assert.Equal(t, "Hello world", result.FinalOutput)
}

// TestConsumeEinoStream_CheckerActive_SuppressesEchoes is the focused unit
// counterpart: with checkerActive=true (StreamState present with a non-nil Ch),
// consumeEinoStream alone must emit ZERO per-step echoes but STILL emit
// stream_start + the single terminal, and STILL accumulate the final answer.
func TestConsumeEinoStream_CheckerActive_SuppressesEchoes(t *testing.T) {
	r := makeRunner()
	run := makeRun(4446)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "Solo"},
		{Role: schema.Assistant, Content: " answer"},
		{Role: schema.Assistant, ReasoningContent: "thinking"},
		{Role: schema.Assistant, Content: "", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 64)
	state := &StreamSessionState{Ch: ch, RunID: run.ID, CurrentMsgID: "msg-x"}
	ctx := WithStreamState(context.Background(), state)

	result, err := r.consumeEinoStream(ctx, run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	assert.Empty(t, allEventsOfType(evs, stream.EventTokenDelta),
		"checker active: consumeEinoStream must NOT emit token_delta")
	assert.Empty(t, allEventsOfType(evs, stream.EventReasoningDelta),
		"checker active: consumeEinoStream must NOT emit reasoning_delta")
	assert.Empty(t, allEventsOfType(evs, stream.EventAssistantMessage),
		"checker active: consumeEinoStream must NOT emit assistant_message")
	assert.Empty(t, allEventsOfType(evs, stream.EventStepDone),
		"checker active: consumeEinoStream must NOT emit step_done")

	// But it STILL owns stream_start + terminal, and STILL accumulates content.
	assert.Len(t, allEventsOfType(evs, stream.EventStreamStart), 1)
	terminals := allEventsOfType(evs, stream.EventTerminal)
	require.Len(t, terminals, 1)
	assert.Equal(t, "Solo answer", result.FinalOutput,
		"accumulation/stash must stay unconditional → terminal + RunResult carry the answer")
}

// TestConsumeEinoStream_CheckerActive_ToolEventsStillEmit verifies the gate does
// NOT suppress the tool-call events consumeEinoStream solely owns: with a tool
// call in the stream and the checker active, tool_call_start + tool_call_result
// must STILL fire (only the text/echo events are gated).
func TestConsumeEinoStream_CheckerActive_ToolEventsStillEmit(t *testing.T) {
	r := makeRunner()
	run := makeRun(4447)
	st := &LoopState{}

	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "searching"},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "tc1", Type: "function", Function: schema.FunctionCall{Name: "web_search", Arguments: `{"q":"x"}`}},
			},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
		},
		{Role: schema.Tool, ToolCallID: "tc1", Content: "results"},
	}
	sr := makeStreamReader(msgs)

	ch := make(chan stream.Event, 64)
	state := &StreamSessionState{Ch: ch, RunID: run.ID, CurrentMsgID: "msg-y"}
	ctx := WithStreamState(context.Background(), state)

	_, err := r.consumeEinoStream(ctx, run, sr, ch, st, time.Now())
	close(ch)
	require.NoError(t, err)

	evs := collectEvents(ch)
	// Echoes gated (checker owns them).
	assert.Empty(t, allEventsOfType(evs, stream.EventTokenDelta),
		"checker active: token_delta gated")
	assert.Empty(t, allEventsOfType(evs, stream.EventAssistantMessage),
		"checker active: assistant_message gated")
	assert.Empty(t, allEventsOfType(evs, stream.EventStepDone),
		"checker active: step_done gated")
	// Tool events are solely consumeEinoStream's — must still fire.
	assert.Len(t, allEventsOfType(evs, stream.EventToolCallStart), 1,
		"tool_call_start is owned by consumeEinoStream and must still emit")
	assert.Len(t, allEventsOfType(evs, stream.EventToolCallResult), 1,
		"tool_call_result is owned by consumeEinoStream and must still emit")
	assert.Len(t, allEventsOfType(evs, stream.EventTerminal), 1)
}
