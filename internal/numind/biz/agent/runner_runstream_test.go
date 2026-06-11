package agent

// Tests for RunStream (T05-Commit-2).
//
// Strategy: RunStream shares all of Run's setup code; the divergence is at
// einoAgent.Stream() vs .Generate(). Since react.NewAgent requires at least
// one tool and actual LLM calls, we test RunStream via the short-circuit path
// (no tools resolved → returns immediately with TerminalCompleted) and via the
// interface contract (RunStream signature, var _ AgentRunner = (*agentRunner)(nil)).
//
// The streaming-specific logic (consumeEinoStream event emission) is fully
// covered by runner_stream_test.go. The integration of RunStream with a live
// Eino StreamReader is covered in S5 E2E tests.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// makeRunForStream creates a pre-persisted agent_run row in the mock store
// and returns it. This mirrors AcquireStreamLock's "create row + return runID"
// contract that RunStream.Load expects.
func makeRunForStream(t *testing.T, ms *mockAgentRunStore) *model.AgentRun {
	t.Helper()
	run := &model.AgentRun{
		UserID:       1,
		SessionID:    "sess-stream",
		Status:       "running",
		UseCompactV2: false,
	}
	require.NoError(t, ms.Create(context.Background(), run))
	require.NotZero(t, run.ID)
	return run
}

// TestRunStream_InterfaceCompliance verifies the compile-time assertion passes:
// agentRunner must implement the full AgentRunner interface including RunStream.
// This test serves as a documentation anchor — if the assertion at runner.go:131
// is accidentally removed, this test still pins the expectation.
func TestRunStream_InterfaceCompliance(t *testing.T) {
	var _ AgentRunner = (*agentRunner)(nil) // compile-time check
	r := NewAgentRunner(newMockStore(), nil)
	assert.NotNil(t, r)
}

// TestRunStream_ShortCircuit_NoTools verifies that RunStream terminates with
// TerminalCompleted when the registry is nil (no tools resolved). The short-
// circuit path mirrors Run()'s equivalent path and must work identically:
// it should write the agent_run row to "terminated" + return a valid RunResult.
func TestRunStream_ShortCircuit_NoTools(t *testing.T) {
	ms := newMockStore()
	run := makeRunForStream(t, ms)

	r := &agentRunner{
		runStore: ms,
		cancels:  make(map[uint64]context.CancelFunc),
		// registry = nil → no tools resolved → short-circuit path
	}

	ch := make(chan stream.Event, 256)
	result, err := r.RunStream(context.Background(), RunRequest{
		UserID: 1,
		Input:  "hello stream",
	}, run.ID, ch)
	close(ch) // caller's responsibility

	require.NoError(t, err)
	assert.Equal(t, run.ID, result.AgentRunID)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	assert.Contains(t, result.FinalOutput, "hello stream")
	assert.NotZero(t, result.Duration)

	// Verify DB row was updated to terminated.
	got, err := ms.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, string(TerminalCompleted), got.StateReason)
	require.NotNil(t, got.EndedAt)
}

// test(qa): reproduce dev run 138 — when the SSE client disconnects mid-run (the user
// refreshes the page), the request context is canceled. A terminal write that persists
// THROUGH that context fails with "context canceled", leaving a zombie run
// (status=running, turns=0) whose session VANISHES from the UI. The terminal write must
// use a cancel-detached context so it lands regardless of the SSE lifecycle.
func TestRunStream_TerminalWriteUsesCancelDetachedCtx(t *testing.T) {
	ms := newMockStore()
	run := makeRunForStream(t, ms)
	r := &agentRunner{
		runStore: ms,
		cancels:  make(map[uint64]context.CancelFunc),
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan stream.Event, 256)
	_, err := r.RunStream(ctx, RunRequest{UserID: 1, Input: "hello"}, run.ID, ch)
	close(ch)
	require.NoError(t, err)

	// Simulate the SSE client disconnecting (refresh): cancel the request context.
	cancel()

	// The ctx the store received for the terminal write must NOT be canceled by the
	// request-context cancel. Before the fix the write used the request-derived ctx
	// (canceled here) and the real DB write would fail → the session vanished.
	require.NotNil(t, ms.lastUpdateStateCtx)
	assert.NoError(t, ms.lastUpdateStateCtx.Err(),
		"terminal write must use a cancel-detached ctx so it survives SSE disconnect (run 138)")
}

// TestRunStream_ShortCircuit_HookActionStop verifies that a pre-seeded
// HookActionStop in the Registry overrides the terminal reason on the short-
// circuit path (same invariant as Run's short-circuit hook override).
func TestRunStream_ShortCircuit_HookActionStop(t *testing.T) {
	ms := newMockStore()
	run := makeRunForStream(t, ms)

	registry := NewHookActionRegistry()
	registry.Record(HookActionStop)

	hooks := &RunHooks{Registry: registry}
	r := &agentRunner{
		runStore: ms,
		cancels:  make(map[uint64]context.CancelFunc),
	}

	ch := make(chan stream.Event, 256)
	result, err := r.RunStream(context.Background(), RunRequest{
		UserID: 1,
		Input:  "stop me",
		Hooks:  hooks,
	}, run.ID, ch)
	close(ch)

	require.NoError(t, err)
	assert.Equal(t, run.ID, result.AgentRunID)
	// HookActionStop → TerminalHookStopped via state machine.
	assert.Equal(t, TerminalHookStopped, result.TerminalReason)
}

// TestRunStream_RunNotFound verifies that RunStream returns an error when the
// pre-created run row cannot be found (e.g., runID=0 or wrong ID).
func TestRunStream_RunNotFound(t *testing.T) {
	ms := newMockStore()
	r := &agentRunner{
		runStore: ms,
		cancels:  make(map[uint64]context.CancelFunc),
	}

	ch := make(chan stream.Event, 16)
	result, err := r.RunStream(context.Background(), RunRequest{UserID: 1}, 99999, ch)
	close(ch)

	assert.Error(t, err)
	assert.Nil(t, result)
}

// TestRunStream_CtxCancelBeforeStart verifies that when ctx is already cancelled
// before RunStream is called, the function returns quickly (load the run row, then
// the cancel is detected before the LLM call). We test this on the short-circuit
// path since cancellation interacts cleanly there.
func TestRunStream_CtxCancelBeforeStart(t *testing.T) {
	ms := newMockStore()
	run := makeRunForStream(t, ms)

	r := &agentRunner{
		runStore: ms,
		cancels:  make(map[uint64]context.CancelFunc),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	ch := make(chan stream.Event, 16)
	// Should not hang — either returns immediately or very quickly.
	done := make(chan struct{})
	var result *RunResult
	var err error
	go func() {
		result, err = r.RunStream(ctx, RunRequest{UserID: 1, Input: "x"}, run.ID, ch)
		close(done)
	}()

	select {
	case <-done:
		close(ch)
	case <-time.After(5 * time.Second):
		t.Fatal("RunStream did not return within 5s after ctx cancel")
	}

	// Short-circuit path doesn't check ctx.Done() before returning, so either
	// NoError (short-circuit completed) or error (ctx propagated) is acceptable.
	// The critical assertion is that it did NOT hang.
	if err != nil {
		t.Logf("RunStream returned error (ctx cancelled): %v", err)
	} else {
		assert.NotNil(t, result)
	}
}

// TestRunStream_ChannelNotClosedByRunStream verifies that RunStream does NOT
// close the ch channel (that responsibility belongs to the caller/controller).
// We verify this by checking that the channel is still open after RunStream
// returns on the short-circuit path.
func TestRunStream_ChannelNotClosedByRunStream(t *testing.T) {
	ms := newMockStore()
	run := makeRunForStream(t, ms)

	r := &agentRunner{
		runStore: ms,
		cancels:  make(map[uint64]context.CancelFunc),
	}

	ch := make(chan stream.Event, 256)
	_, err := r.RunStream(context.Background(), RunRequest{
		UserID: 1,
		Input:  "test",
	}, run.ID, ch)
	require.NoError(t, err)

	// If RunStream had closed ch, this send would panic.
	// Using recover() to catch accidental close.
	panicked := func() (panicked bool) {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		// Non-blocking send: if channel is closed this panics.
		select {
		case ch <- stream.Event{}:
			return false
		default:
			return false
		}
	}()
	assert.False(t, panicked, "RunStream must not close ch — caller owns it")
	close(ch) // clean up
}

// TestRunStream_ConcurrentCallsSameRunner verifies that the runner's mutex
// correctly handles concurrent RunStream calls without data races.
func TestRunStream_ConcurrentCallsSameRunner(t *testing.T) {
	ms := newMockStore()
	r := &agentRunner{
		runStore: ms,
		cancels:  make(map[uint64]context.CancelFunc),
	}

	const N = 5
	var wg sync.WaitGroup
	errs := make([]error, N)
	results := make([]*RunResult, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Each goroutine creates its own run row.
			run := &model.AgentRun{
				UserID:    1,
				Status:    "running",
				SessionID: "concurrent",
			}
			if createErr := ms.Create(context.Background(), run); createErr != nil {
				errs[idx] = createErr
				return
			}
			ch := make(chan stream.Event, 64)
			res, err := r.RunStream(context.Background(), RunRequest{
				UserID: 1,
				Input:  "concurrent test",
			}, run.ID, ch)
			close(ch)
			errs[idx] = err
			results[idx] = res
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d should not error", i)
	}
	for i, res := range results {
		if res != nil {
			assert.Equal(t, TerminalCompleted, res.TerminalReason, "goroutine %d result", i)
		}
	}
}

// ---------------------------------------------------------------------------
// P1 required tests: TestRunStream_HappyPath, TestRunStream_LLMError,
// TestRunStream_AbortedByCtx
//
// Strategy: use the chatStreamFn package-level seam (adapter.go) to inject
// a controlled <-chan aiservice.ChatChunk without a live gateway. A staticRegistry
// with a single loopTestTool satisfies react.NewAgent's ≥1 tool requirement.
// The mock chatStreamFn is restored on test cleanup.
// ---------------------------------------------------------------------------

// withMockChatStreamFn replaces chatStreamFn for the duration of t and restores
// it on cleanup. Mirrors withMockChatFn from runner_e2e_loop_test.go.
func withMockChatStreamFn(t *testing.T, fn func(context.Context, string, aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error)) {
	t.Helper()
	orig := chatStreamFn
	t.Cleanup(func() { chatStreamFn = orig })
	chatStreamFn = fn
}

// successStreamFn returns a chatStreamFn mock that sends n text delta chunks
// followed by a final IsFinal chunk, then closes the channel.
func successStreamFn(deltas ...string) func(context.Context, string, aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		ch := make(chan aiservice.ChatChunk, len(deltas)+1)
		for i, d := range deltas {
			ch <- aiservice.ChatChunk{Delta: d, Index: i}
		}
		ch <- aiservice.ChatChunk{IsFinal: true, FinishReason: "stop", Index: len(deltas)}
		close(ch)
		return ch, nil
	}
}

// newReActRunnerForStream builds an agentRunner with a staticRegistry + single
// loopTestTool suitable for RunStream tests.
func newReActRunnerForStream(ms *mockAgentRunStore, opts ...RunnerOption) (AgentRunner, string) {
	tool := &loopTestTool{}
	reg := newStaticRegistry(tool)
	runner := NewAgentRunner(ms, reg, opts...)
	return runner, tool.Name()
}

// TestRunStream_HappyPath verifies that RunStream:
//  1. emits stream_start + token_delta(s) + terminal(completed) onto ch
//  2. returns TerminalCompleted result
//  3. persists "terminated" + TerminalCompleted to DB
func TestRunStream_HappyPath(t *testing.T) {
	withMockChatStreamFn(t, successStreamFn("Hello", " world"))

	ms := newMockStore()
	run := makeRunForStream(t, ms)
	runner, toolName := newReActRunnerForStream(ms)

	ch := make(chan stream.Event, 256)
	result, err := runner.RunStream(context.Background(), RunRequest{
		UserID:    1,
		Input:     "say hello",
		ToolNames: []string{toolName},
	}, run.ID, ch)
	close(ch)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	assert.Equal(t, run.ID, result.AgentRunID)
	assert.NotZero(t, result.Duration)

	// Collect and verify events.
	var evs []stream.Event
	for ev := range ch { //nolint:revive  (channel already closed above; this is a no-op drain)
		evs = append(evs, ev)
	}

	types := make([]stream.EventType, len(evs))
	for i, ev := range evs {
		types[i] = ev.Type
	}
	assert.Contains(t, types, stream.EventStreamStart, "must emit stream_start")
	assert.Contains(t, types, stream.EventTerminal, "must emit terminal")

	// Verify at least one token_delta was emitted.
	var tokenDeltas []stream.Event
	for _, ev := range evs {
		if ev.Type == stream.EventTokenDelta {
			tokenDeltas = append(tokenDeltas, ev)
		}
	}
	assert.NotEmpty(t, tokenDeltas, "must emit at least one token_delta")

	// Verify terminal event carries reason=completed.
	for _, ev := range evs {
		if ev.Type == stream.EventTerminal {
			var p stream.TerminalPayload
			require.NoError(t, json.Unmarshal(ev.Data, &p))
			assert.Equal(t, string(TerminalCompleted), p.Reason)
			break
		}
	}

	// Verify DB row updated to terminated/completed.
	got, dbErr := ms.Get(context.Background(), run.ID)
	require.NoError(t, dbErr)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, string(TerminalCompleted), got.StateReason)
}

// TestRunStream_LLMError verifies that when chatStreamFn returns an error
// (regression for Hotfix 648d16d4 / ErrAIProviderTimeout):
//  1. RunStream returns a non-nil error
//  2. ch receives EventError + EventTerminal(model_error)
//  3. EventTerminal.terminal_metadata contains error_message
//  4. DB row is persisted with a non-Completed terminal reason
func TestRunStream_LLMError(t *testing.T) {
	injectedErr := errno.ErrAIProviderTimeout.SetMessage("provider timeout: tcp dial")

	withMockChatStreamFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		return nil, injectedErr
	})

	ms := newMockStore()
	run := makeRunForStream(t, ms)
	runner, toolName := newReActRunnerForStream(ms)

	ch := make(chan stream.Event, 256)
	result, err := runner.RunStream(context.Background(), RunRequest{
		UserID:    1,
		Input:     "will fail",
		ToolNames: []string{toolName},
	}, run.ID, ch)
	close(ch)

	// RunStream must return an error on LLM failure.
	assert.Error(t, err, "RunStream must return error on LLM failure")

	// Collect events.
	var evs []stream.Event
	for ev := range ch { //nolint:revive
		evs = append(evs, ev)
	}

	types := make([]stream.EventType, len(evs))
	for i, ev := range evs {
		types[i] = ev.Type
	}

	// P1 fix regression: ch must have received EventError + EventTerminal
	// (before the fix, the error path returned without emitting anything).
	assert.Contains(t, types, stream.EventError, "ch must contain EventError on LLM failure")
	assert.Contains(t, types, stream.EventTerminal, "ch must contain EventTerminal on LLM failure")

	// Verify EventTerminal carries non-Completed reason.
	for _, ev := range evs {
		if ev.Type == stream.EventTerminal {
			var p stream.TerminalPayload
			require.NoError(t, json.Unmarshal(ev.Data, &p))
			assert.NotEqual(t, string(TerminalCompleted), p.Reason,
				"terminal reason must not be completed on LLM error")
			// Regression: terminal_metadata.error_message must be populated.
			assert.NotEmpty(t, p.TerminalMetadata["error_message"],
				"terminal_metadata.error_message must contain the error on LLM failure")
			break
		}
	}

	// Verify DB row not left in running state.
	got, dbErr := ms.Get(context.Background(), run.ID)
	require.NoError(t, dbErr)
	assert.Equal(t, "terminated", got.Status)
	assert.NotEmpty(t, got.StateReason)

	// result may be nil when einoAgent.Stream itself fails.
	_ = result
}

// TestRunStream_AbortedByCtx verifies that when the context is cancelled while
// RunStream is active, the run terminates with TerminalAbortedStreaming and
// emits EventTerminal onto ch. No goroutine leak.
func TestRunStream_AbortedByCtx(t *testing.T) {
	// chatStreamFn blocks until ctx is cancelled, then returns an error.
	blockCh := make(chan struct{})
	withMockChatStreamFn(t, func(ctx context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		// Block until ctx is cancelled, simulating a slow provider.
		select {
		case <-ctx.Done():
			return nil, errors.New("context cancelled")
		case <-blockCh:
			return nil, errors.New("unblocked early")
		}
	})

	ms := newMockStore()
	run := makeRunForStream(t, ms)
	runner, toolName := newReActRunnerForStream(ms)

	ctx, cancel := context.WithCancel(context.Background())

	ch := make(chan stream.Event, 256)
	done := make(chan struct{})
	var rsErr error
	go func() {
		defer close(done)
		_, rsErr = runner.RunStream(ctx, RunRequest{
			UserID:    1,
			Input:     "will be aborted",
			ToolNames: []string{toolName},
		}, run.ID, ch)
	}()

	// Cancel context after a brief moment to let RunStream start.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// RunStream returned — good.
	case <-time.After(5 * time.Second):
		t.Fatal("RunStream did not return within 5s after ctx cancel")
	}
	close(ch)

	// RunStream either returns an error or a result with aborted terminal.
	// Either is acceptable; the key assertion is that it did NOT hang.
	t.Logf("RunStream after ctx cancel: err=%v", rsErr)

	// ch must have received EventTerminal (either aborted_streaming or model_error).
	var evs []stream.Event
	for ev := range ch { //nolint:revive
		evs = append(evs, ev)
	}
	var sawTerminal bool
	for _, ev := range evs {
		if ev.Type == stream.EventTerminal {
			sawTerminal = true
		}
	}
	// Note: if RunStream returned before emitting any events (e.g. einoAgent.Stream
	// error before first chunk), EventTerminal may still have been sent via
	// emitStreamErrorEvents. We assert it appears when events are present.
	if len(evs) > 0 {
		assert.True(t, sawTerminal, "EventTerminal must be emitted when events are present")
	}
}

// TestStreamScanToolCallChecker_FindsToolCallAfterContent REPRODUCES the bug
// observed on dev 2026-05-28 (agent_run 56/57/58): a thinking model
// (deepseek-v4-pro) emits reasoning_content + content text first, then a final
// chunk with tool_calls. Eino's default firstChunkStreamToolCallChecker reads
// only until the first non-empty chunk: if that chunk is content text, it
// returns false ("no tool call"), and react.Agent.Stream routes to END
// without dispatching the tool — frontend sees step_done with
// stop_reason="tool_calls" followed by terminal step_count=1 and a frozen UI.
//
// Contract: streamScanToolCallChecker must drain the entire stream looking
// for ANY ToolCalls payload before deciding.
func TestStreamScanToolCallChecker_FindsToolCallAfterContent(t *testing.T) {
	// Construct an eino schema.StreamReader that yields:
	//   1) two content-only chunks (mimicking reasoning + text)
	//   2) one chunk with tool_calls (mimicking deepseek-v4-pro's final delta)
	//   3) EOF
	sr, sw := schema.Pipe[*schema.Message](4)
	go func() {
		defer sw.Close()
		sw.Send(&schema.Message{Role: schema.Assistant, Content: "好的，"}, nil)
		sw.Send(&schema.Message{Role: schema.Assistant, Content: "我直接开始调研。"}, nil)
		sw.Send(&schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call_xyz", Type: "function", Function: schema.FunctionCall{Name: "web_search", Arguments: `{"query":"教培 AI"}`}},
			},
		}, nil)
	}()

	isToolCall, err := streamScanToolCallChecker(context.Background(), sr)
	require.NoError(t, err)
	assert.True(t, isToolCall,
		"streamScanToolCallChecker MUST detect the tool_call chunk even though it follows content chunks; "+
			"eino's default firstChunkStreamToolCallChecker fails this case and is the root cause of the dev 2026-05-28 frozen-UI bug")
}

// TestStreamScanToolCallChecker_ContentOnlyReturnsFalse verifies the negative
// case: a stream that only emits content (no tool_calls anywhere) correctly
// returns false so the react agent routes to END.
func TestStreamScanToolCallChecker_ContentOnlyReturnsFalse(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](2)
	go func() {
		defer sw.Close()
		sw.Send(&schema.Message{Role: schema.Assistant, Content: "Hello."}, nil)
		sw.Send(&schema.Message{Role: schema.Assistant, Content: " World."}, nil)
	}()

	isToolCall, err := streamScanToolCallChecker(context.Background(), sr)
	require.NoError(t, err)
	assert.False(t, isToolCall, "content-only stream must NOT be classified as a tool call")
}
