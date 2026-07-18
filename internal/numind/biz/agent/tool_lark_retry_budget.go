package agent

import (
	"sync"

	"numind-server/internal/numind/biz/feishu"
)

type larkExecuteRetryPhase uint8

const (
	larkRetryReady larkExecuteRetryPhase = iota
	larkRetryNormalInFlight
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
	mu           sync.Mutex
	phase        larkExecuteRetryPhase
	terminalStop bool
	lastCategory string
}

// larkExecuteRetryRuns is process-local because one Agent run is executed by
// one process at a time. Durable external-action resume starts a new execution
// leg and no longer belongs to the rejected-command correction loop.
var larkExecuteRetryRuns sync.Map // map[uint64]*larkExecuteRetryState

func larkExecuteRetryBegin(runID uint64) (*larkExecuteRetryState, larkExecuteRetryAttempt, bool) {
	value, _ := larkExecuteRetryRuns.LoadOrStore(runID, &larkExecuteRetryState{})
	state := value.(*larkExecuteRetryState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.terminalStop {
		return state, larkExecuteCorrectionAttempt, false
	}

	switch state.phase {
	case larkRetryReady:
		state.phase = larkRetryNormalInFlight
		return state, larkExecuteNormalAttempt, true
	case larkRetryCorrectionAvailable:
		state.phase = larkRetryCorrectionInFlight
		return state, larkExecuteCorrectionAttempt, true
	case larkRetryNormalInFlight, larkRetryCorrectionInFlight, larkRetryExhausted:
		return state, larkExecuteCorrectionAttempt, false
	default:
		state.phase = larkRetryExhausted
		return state, larkExecuteCorrectionAttempt, false
	}
}

func larkExecuteRetryBlockedByTerminal(state *larkExecuteRetryState) bool {
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.terminalStop
}

func larkExecuteRetryRejected(state *larkExecuteRetryState, attempt larkExecuteRetryAttempt) bool {
	if state == nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()

	switch {
	case attempt == larkExecuteNormalAttempt && state.phase == larkRetryNormalInFlight:
		state.phase = larkRetryCorrectionAvailable
		return false
	case attempt == larkExecuteCorrectionAttempt && state.phase == larkRetryCorrectionInFlight:
		state.phase = larkRetryExhausted
		return true
	default:
		state.phase = larkRetryExhausted
		return true
	}
}

func larkExecuteRetryCompleted(state *larkExecuteRetryState, attempt larkExecuteRetryAttempt) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if (attempt == larkExecuteNormalAttempt && state.phase == larkRetryNormalInFlight) ||
		(attempt == larkExecuteCorrectionAttempt && state.phase == larkRetryCorrectionInFlight) {
		state.phase = larkRetryReady
	}
}

func larkExecuteRetryTerminalOutcome(
	state *larkExecuteRetryState,
	attempt larkExecuteRetryAttempt,
	failure *feishu.OperationFailure,
) {
	if state == nil || failure == nil {
		larkExecuteRetryFailed(state, attempt)
		return
	}
	if failure.Category == "validation" || failure.Retryable {
		state.mu.Lock()
		state.lastCategory = failure.Category
		state.mu.Unlock()
		_ = larkExecuteRetryRejected(state, attempt)
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.lastCategory = failure.Category
	state.terminalStop = true
	state.phase = larkRetryExhausted
}

func larkExecuteRetryFailed(state *larkExecuteRetryState, attempt larkExecuteRetryAttempt) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	switch {
	case attempt == larkExecuteNormalAttempt && state.phase == larkRetryNormalInFlight:
		state.phase = larkRetryReady
	case attempt == larkExecuteCorrectionAttempt && state.phase == larkRetryCorrectionInFlight:
		state.phase = larkRetryExhausted
	}
}

func larkExecuteRetryClearRun(runID uint64) {
	if runID != 0 {
		larkExecuteRetryRuns.Delete(runID)
	}
}
