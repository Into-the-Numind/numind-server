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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
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
