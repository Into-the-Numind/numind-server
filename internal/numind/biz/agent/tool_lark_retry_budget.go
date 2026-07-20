package agent

import (
	"encoding/json"
	"sync"

	"numind-server/internal/numind/biz/feishu"
)

const larkExecuteMaxCorrectableAttempts = 5
const larkExecuteMaxUnknownVerificationReads = 3

type larkExecuteRetryPhase uint8

const (
	larkRetryReady larkExecuteRetryPhase = iota
	larkRetryNormalInFlight
	larkRetryCorrectionAvailable
	larkRetryCorrectionInFlight
	larkRetryVerificationInFlight
	larkRetryExhausted
)

type larkExecuteRetryAttempt uint8

const (
	larkExecuteNormalAttempt larkExecuteRetryAttempt = iota
	larkExecuteCorrectionAttempt
	larkExecuteVerificationAttempt
)

type larkExecuteRetryBlockReason uint8

const (
	larkRetryNotBlocked larkExecuteRetryBlockReason = iota
	larkRetryBlockedTerminal
	larkRetryBlockedInFlight
	larkRetryBlockedExhausted
)

type larkExecuteRetryState struct {
	mu                  sync.Mutex
	phase               larkExecuteRetryPhase
	correctableFailures uint8
	terminalStop        bool
	lastCategory        string
	verificationReads   uint8
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
	// An unknown business write is the strongest durable fence. A later
	// continuation failure (for example, a scope check) must never downgrade it
	// back into a correctable state and accidentally permit a duplicate write.
	if state.terminalStop && state.lastCategory == "unknown_result" {
		return true
	}
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

// larkExecuteRetrySeedTranscript restores the durable unknown-result fence when
// a continuation is picked up by another process (or after a restart). A later
// successful verification read must not erase the ambiguity of the original
// write: reads may continue, but another write remains blocked for this run.
//
// Only the server-produced, closed Feishu result schema can arm the fence. The
// transcript can therefore make execution more restrictive, never less safe.
func larkExecuteRetrySeedTranscript(runID uint64, raw json.RawMessage) bool {
	if runID == 0 || len(raw) == 0 {
		return false
	}
	var turns []map[string]any
	if err := json.Unmarshal(raw, &turns); err != nil {
		return false
	}
	for _, turn := range turns {
		role, _ := turn["role"].(string)
		content, _ := turn["content"].(string)
		if role != "tool" || content == "" {
			continue
		}
		failure, ok := feishu.DecodeLarkTerminalFailure(json.RawMessage(content))
		if !ok || failure.Category != "unknown_result" {
			continue
		}
		value, _ := larkExecuteRetryRuns.LoadOrStore(runID, &larkExecuteRetryState{})
		state := value.(*larkExecuteRetryState)
		state.mu.Lock()
		state.lastCategory = failure.Category
		state.phase = larkRetryExhausted
		state.terminalStop = true
		state.mu.Unlock()
		return true
	}
	return false
}

func larkExecuteRetryBegin(runID uint64, catalogRead bool) (*larkExecuteRetryState, larkExecuteRetryAttempt, larkExecuteRetryBlockReason, bool) {
	value, _ := larkExecuteRetryRuns.LoadOrStore(runID, &larkExecuteRetryState{})
	state := value.(*larkExecuteRetryState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.terminalStop {
		if state.lastCategory == "unknown_result" && catalogRead {
			if state.phase == larkRetryVerificationInFlight {
				return state, larkExecuteVerificationAttempt, larkRetryBlockedInFlight, false
			}
			if state.verificationReads < larkExecuteMaxUnknownVerificationReads {
				state.verificationReads++
				state.phase = larkRetryVerificationInFlight
				return state, larkExecuteVerificationAttempt, larkRetryNotBlocked, true
			}
		}
		return state, larkExecuteCorrectionAttempt, larkRetryBlockedTerminal, false
	}

	switch state.phase {
	case larkRetryReady:
		state.phase = larkRetryNormalInFlight
		return state, larkExecuteNormalAttempt, larkRetryNotBlocked, true
	case larkRetryCorrectionAvailable:
		state.phase = larkRetryCorrectionInFlight
		return state, larkExecuteCorrectionAttempt, larkRetryNotBlocked, true
	case larkRetryNormalInFlight, larkRetryCorrectionInFlight:
		return state, larkExecuteCorrectionAttempt, larkRetryBlockedInFlight, false
	case larkRetryExhausted:
		return state, larkExecuteCorrectionAttempt, larkRetryBlockedExhausted, false
	default:
		state.phase = larkRetryExhausted
		return state, larkExecuteCorrectionAttempt, larkRetryBlockedExhausted, false
	}
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
		state.verificationReads = 0
	} else if attempt == larkExecuteVerificationAttempt && state.phase == larkRetryVerificationInFlight {
		state.phase = larkRetryExhausted
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
	case attempt == larkExecuteVerificationAttempt && state.phase == larkRetryVerificationInFlight:
		state.phase = larkRetryExhausted
	}
}

func larkExecuteRetryClearRun(runID uint64) {
	if runID != 0 {
		larkExecuteRetryRuns.Delete(runID)
	}
}
