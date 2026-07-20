package agent

import (
	"encoding/json"
	"sync"

	"numind-server/internal/numind/biz/feishu"
)

const larkExecuteMaxCorrectableAttempts = 5

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
	mu                  sync.Mutex
	phase               larkExecuteRetryPhase
	correctableFailures uint8
	terminalStop        bool
	lastCategory        string
}

// larkExecuteRetryRuns is process-local because one Agent run is executed by
// one process at a time. Durable external-action resume starts a new execution
// leg and no longer belongs to the rejected-command correction loop.
var larkExecuteRetryRuns sync.Map // map[uint64]*larkExecuteRetryState

// larkExecuteRetrySeedExternalResult restores the run guard from the durable,
// server-produced result before an external continuation reaches the model.
// Non-Feishu or malformed results leave the guard untouched.
func larkExecuteRetrySeedExternalResult(runID uint64, raw json.RawMessage) bool {
	if runID == 0 {
		return false
	}
	failure, ok := feishu.DecodeLarkTerminalFailure(raw)
	if !ok {
		return false
	}
	value, _ := larkExecuteRetryRuns.LoadOrStore(runID, &larkExecuteRetryState{})
	state := value.(*larkExecuteRetryState)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.lastCategory = failure.Category
	if larkFailureAllowsCorrection(failure) {
		state.correctableFailures = 1
		state.phase = larkRetryCorrectionAvailable
		state.terminalStop = false
		return true
	}
	state.phase = larkRetryExhausted
	state.terminalStop = true
	return true
}

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

	validAttempt := (attempt == larkExecuteNormalAttempt && state.phase == larkRetryNormalInFlight) ||
		(attempt == larkExecuteCorrectionAttempt && state.phase == larkRetryCorrectionInFlight)
	if !validAttempt {
		state.phase = larkRetryExhausted
		return true
	}
	state.correctableFailures++
	if state.correctableFailures >= larkExecuteMaxCorrectableAttempts {
		state.phase = larkRetryExhausted
		return true
	}
	state.phase = larkRetryCorrectionAvailable
	return false
}

func larkExecuteRetryProgress(state *larkExecuteRetryState) (attempts, remaining int) {
	if state == nil {
		return larkExecuteMaxCorrectableAttempts, 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	attempts = int(state.correctableFailures)
	remaining = larkExecuteMaxCorrectableAttempts - attempts
	if remaining < 0 {
		remaining = 0
	}
	return attempts, remaining
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
		state.correctableFailures = 0
		state.lastCategory = ""
	}
}

func larkExecuteRetryTerminalOutcome(
	state *larkExecuteRetryState,
	attempt larkExecuteRetryAttempt,
	failure *feishu.OperationFailure,
) (correctionExhausted bool) {
	if state == nil || failure == nil {
		larkExecuteRetryFailed(state, attempt)
		return true
	}
	if larkFailureAllowsCorrection(failure) {
		state.mu.Lock()
		state.lastCategory = failure.Category
		state.mu.Unlock()
		return larkExecuteRetryRejected(state, attempt)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.lastCategory = failure.Category
	state.terminalStop = true
	state.phase = larkRetryExhausted
	return false
}

func larkFailureAllowsCorrection(failure *feishu.OperationFailure) bool {
	return failure != nil && (failure.Category == "validation" || failure.Retryable)
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
