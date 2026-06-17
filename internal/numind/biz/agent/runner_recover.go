package agent

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
)

// recoverAgentRunPanic converts a recovered panic from a run goroutine into a clean
// terminal. It logs the panic + stack, persists a model_error terminal so the run
// row never stays stuck in "running", and (when ch != nil) emits an error+terminal
// SSE pair so a streaming client sees a structured close instead of a dropped
// connection. Returns a non-nil error describing the panic.
//
// This is a BACKSTOP for panics OUTSIDE tool Execute (run prep, hooks, finalize)
// that surface in the detached run goroutine — Gin's recovery middleware does not
// cover spawned goroutines, so without this a single panic crashes the whole
// process. Tool Execute panics are contained earlier and more gracefully by
// invokeToolGuarded (the run survives instead of terminating).
func recoverAgentRunPanic(rec any, runID uint64, runStore store.IAgentRunStore, ch chan<- stream.Event, startTime time.Time) error {
	err := fmt.Errorf("agent run panicked: %v", rec)
	log.Errorw("agent run goroutine panic recovered (process protected)",
		"agent_run_id", runID, "panic", rec, "stack", string(debug.Stack()))
	if runStore != nil && runID != 0 {
		now := time.Now()
		// Detached context: the run ctx may already be cancelling during the unwind.
		if uerr := runStore.UpdateState(context.Background(), runID, "terminated", string(TerminalModelError), &now); uerr != nil {
			log.Warnw("recoverAgentRunPanic: persist terminal failed", "agent_run_id", runID, "error", uerr)
		}
	}
	if ch != nil {
		// seqBase=0: the panic-recovery backstop has no shared StreamSessionState in
		// scope; error/terminal use seq 1/2 (the FE never reorders these events).
		emitStreamErrorEvents(ch, runID, 0, err, TerminalModelError, startTime)
	}
	return err
}
