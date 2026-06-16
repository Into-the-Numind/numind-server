package agent

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/aiservice"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverAgentRunPanic_PersistsTerminalAndEmits(t *testing.T) {
	ms := newMockStore()
	run := makeRunForStream(t, ms) // status=running
	ch := make(chan stream.Event, 8)

	err := recoverAgentRunPanic("boom", run.ID, ms, ch, time.Now())
	require.Error(t, err)
	close(ch)

	// Run row left in a clean terminal, never stuck "running".
	got, gerr := ms.Get(context.Background(), run.ID)
	require.NoError(t, gerr)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, string(TerminalModelError), got.StateReason)

	var sawError, sawTerminal bool
	for ev := range ch {
		switch ev.Type {
		case stream.EventError:
			sawError = true
		case stream.EventTerminal:
			sawTerminal = true
		}
	}
	assert.True(t, sawError, "must emit an error event for the streaming client")
	assert.True(t, sawTerminal, "must emit a terminal event")
}

func TestRecoverAgentRunPanic_NilChAndStore(t *testing.T) {
	// No ch (polling path) and no store must not panic; still returns an error.
	err := recoverAgentRunPanic("x", 0, nil, nil, time.Now())
	require.Error(t, err)
}

// TestRunStream_PanicContained confirms the RunStream-level backstop actually
// catches a panic raised deep in the run (here from the model stream fn) so the
// detached goroutine never crashes the process, and the run is left terminated.
func TestRunStream_PanicContained(t *testing.T) {
	withMockChatStreamFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		panic("simulated provider adapter panic")
	})
	ms := newMockStore()
	run := makeRunForStream(t, ms)
	runner, toolName := newReActRunnerForStream(ms)

	ch := make(chan stream.Event, 256)
	_, err := runner.RunStream(context.Background(), RunRequest{
		UserID:    1,
		Input:     "go",
		ToolNames: []string{toolName},
	}, run.ID, ch)
	close(ch)

	require.Error(t, err, "panic must be recovered into an error, not propagate")
	got, gerr := ms.Get(context.Background(), run.ID)
	require.NoError(t, gerr)
	assert.Equal(t, "terminated", got.Status)
}
