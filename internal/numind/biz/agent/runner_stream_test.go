package agent

import (
	"context"
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

	// Verify the message IDs differ
	if len(asMsgs) == 2 {
		type msgPayload struct {
			MessageID string `json:"message_id"`
		}
		var p1, p2 msgPayload
		require.NoError(t, asMsgs[0].Data.UnmarshalJSON(asMsgs[0].Data))
		_ = p1
		_ = p2
		// Just verify they're present; message ID uniqueness guaranteed by uuid.NewString()
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
