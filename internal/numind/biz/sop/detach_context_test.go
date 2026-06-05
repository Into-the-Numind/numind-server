package sop

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetachStreamContext_SurvivesParentCancel reproduces problem 1: a SOP node's
// LLM generation must NOT be aborted when the originating HTTP request context is
// cancelled (client network blip / disconnect). Before the fix the LLM call ran
// on the request ctx, so a disconnect cancelled it → the node was marked Failed
// and credits refunded. The detached stream context must survive parent
// cancellation while preserving ctx values (langfuse trace / billing meta).
func TestDetachStreamContext_SurvivesParentCancel(t *testing.T) {
	type ctxKey string
	const traceKey ctxKey = "trace_id"

	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), traceKey, "trace-abc"))
	llmCtx, cancel := detachStreamContext(parent, 30*time.Minute)
	defer cancel()

	// Simulate the client disconnecting: the request ctx is cancelled.
	cancelParent()

	select {
	case <-llmCtx.Done():
		t.Fatal("stream context was cancelled by client disconnect — generation would abort (problem 1 regression)")
	case <-time.After(50 * time.Millisecond):
		// good: detached ctx is unaffected by parent cancellation
	}

	// Values (trace / billing / reservation ref) must be preserved.
	assert.Equal(t, "trace-abc", llmCtx.Value(traceKey), "ctx values must survive detach")
}

// TestDetachStreamContext_OverallTimeoutFires verifies the overall ceiling
// (problem 2): the detached context is still bounded by a deadline so a genuinely
// stuck generation cannot run forever.
func TestDetachStreamContext_OverallTimeoutFires(t *testing.T) {
	llmCtx, cancel := detachStreamContext(context.Background(), 30*time.Millisecond)
	defer cancel()

	select {
	case <-llmCtx.Done():
		require.ErrorIs(t, llmCtx.Err(), context.DeadlineExceeded, "overall timeout must be a deadline-exceeded")
	case <-time.After(time.Second):
		t.Fatal("overall timeout did not fire")
	}
}
