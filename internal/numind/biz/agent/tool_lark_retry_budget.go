package agent

import (
	"sync"
	"sync/atomic"
)

const (
	larkRetryReady int32 = iota
	larkRetryCorrectionAvailable
	larkRetryCorrectionInFlight
	larkRetryExhausted
)

type larkExecuteRetryAttempt uint8

const (
	larkExecuteNormalAttempt larkExecuteRetryAttempt = iota
	larkExecuteCorrectionAttempt
)

type larkExecuteRetryState struct {
	state atomic.Int32
}

// larkExecuteRetryRuns is process-local because one Agent run is executed by
// one process at a time. Durable external-action resume starts a new execution
// leg and no longer belongs to the rejected-command correction loop.
var larkExecuteRetryRuns sync.Map // map[uint64]*larkExecuteRetryState

func larkExecuteRetryBegin(runID uint64) (*larkExecuteRetryState, larkExecuteRetryAttempt, bool) {
	value, _ := larkExecuteRetryRuns.LoadOrStore(runID, &larkExecuteRetryState{})
	state := value.(*larkExecuteRetryState)
	for {
		switch state.state.Load() {
		case larkRetryReady:
			return state, larkExecuteNormalAttempt, true
		case larkRetryCorrectionAvailable:
			if state.state.CompareAndSwap(larkRetryCorrectionAvailable, larkRetryCorrectionInFlight) {
				return state, larkExecuteCorrectionAttempt, true
			}
		case larkRetryCorrectionInFlight, larkRetryExhausted:
			return state, larkExecuteCorrectionAttempt, false
		default:
			state.state.Store(larkRetryExhausted)
			return state, larkExecuteCorrectionAttempt, false
		}
	}
}

func larkExecuteRetryRejected(state *larkExecuteRetryState, attempt larkExecuteRetryAttempt) bool {
	if state == nil {
		return true
	}
	if attempt == larkExecuteCorrectionAttempt {
		state.state.Store(larkRetryExhausted)
		return true
	}
	if state.state.CompareAndSwap(larkRetryReady, larkRetryCorrectionAvailable) {
		return false
	}
	// Two nominally non-concurrent tool calls overlapped. Fail closed after the
	// two already-rejected executor calls rather than granting a third attempt.
	state.state.Store(larkRetryExhausted)
	return true
}

func larkExecuteRetryCompleted(state *larkExecuteRetryState) {
	if state != nil {
		state.state.Store(larkRetryReady)
	}
}

func larkExecuteRetryFailed(state *larkExecuteRetryState, attempt larkExecuteRetryAttempt) {
	if state != nil && attempt == larkExecuteCorrectionAttempt {
		state.state.Store(larkRetryExhausted)
	}
}

func larkExecuteRetryClearRun(runID uint64) {
	if runID != 0 {
		larkExecuteRetryRuns.Delete(runID)
	}
}
