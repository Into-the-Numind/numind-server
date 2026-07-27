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
	larkExecuteReconciliationRead
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
	activeWriteFenceKey string
	unknownWriteFences  map[string]struct{}
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
	if failure.Category == "unknown_result" && failure.WriteFenceKey != "" {
		state.addUnknownWriteFence(failure.WriteFenceKey)
		state.phase = larkRetryReady
		state.correctableFailures = 0
		state.activeWriteFenceKey = ""
		return true
	}
	if larkFailureAllowsCorrection(failure) {
		state.correctableFailures = 1
		state.phase = larkRetryCorrectionAvailable
		return true
	}
	// A terminal result for one command is evidence for the Agent, not authority
	// to freeze a different command. Only a trusted unknown-result fingerprint
	// above may fence anything, and then only its exact write.
	state.phase = larkRetryReady
	state.correctableFailures = 0
	state.activeWriteFenceKey = ""
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
	seeded := false
	for _, turn := range turns {
		role, _ := turn["role"].(string)
		content, _ := turn["content"].(string)
		if role != "tool" || content == "" {
			continue
		}
		failure, ok := feishu.DecodeLarkTerminalFailure(json.RawMessage(content))
		if !ok || failure.Category != "unknown_result" || failure.WriteFenceKey == "" {
			continue
		}
		value, _ := larkExecuteRetryRuns.LoadOrStore(runID, &larkExecuteRetryState{})
		state := value.(*larkExecuteRetryState)
		state.mu.Lock()
		state.addUnknownWriteFence(failure.WriteFenceKey)
		state.mu.Unlock()
		seeded = true
	}
	return seeded
}

func larkExecuteRetryBegin(runID uint64, writeFenceKey string) (*larkExecuteRetryState, larkExecuteRetryAttempt, larkExecuteRetryBlockReason, bool) {
	value, _ := larkExecuteRetryRuns.LoadOrStore(runID, &larkExecuteRetryState{})
	state := value.(*larkExecuteRetryState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if writeFenceKey != "" {
		if _, blocked := state.unknownWriteFences[writeFenceKey]; blocked {
			return state, larkExecuteNormalAttempt, larkRetryBlockedTerminal, false
		}
	}

	switch state.phase {
	case larkRetryReady:
		state.phase = larkRetryNormalInFlight
		state.activeWriteFenceKey = writeFenceKey
		return state, larkExecuteNormalAttempt, larkRetryNotBlocked, true
	case larkRetryCorrectionAvailable:
		state.phase = larkRetryCorrectionInFlight
		state.activeWriteFenceKey = writeFenceKey
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
	if attempt == larkExecuteReconciliationRead {
		return false
	}

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
	if state == nil || attempt == larkExecuteReconciliationRead {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if (attempt == larkExecuteNormalAttempt && state.phase == larkRetryNormalInFlight) ||
		(attempt == larkExecuteCorrectionAttempt && state.phase == larkRetryCorrectionInFlight) {
		state.phase = larkRetryReady
		state.correctableFailures = 0
		state.activeWriteFenceKey = ""
	}
}

func larkExecuteRetryTerminalOutcome(
	state *larkExecuteRetryState,
	attempt larkExecuteRetryAttempt,
	failure *feishu.OperationFailure,
) (correctionExhausted bool) {
	if attempt == larkExecuteReconciliationRead {
		return false
	}
	if state == nil || failure == nil {
		larkExecuteRetryFailed(state, attempt)
		return true
	}
	if larkFailureAllowsCorrection(failure) {
		return larkExecuteRetryRejected(state, attempt)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	validAttempt := (attempt == larkExecuteNormalAttempt && state.phase == larkRetryNormalInFlight) ||
		(attempt == larkExecuteCorrectionAttempt && state.phase == larkRetryCorrectionInFlight)
	if validAttempt && failure.Category == "unknown_result" && state.activeWriteFenceKey != "" {
		state.addUnknownWriteFence(state.activeWriteFenceKey)
	}
	state.phase = larkRetryReady
	state.correctableFailures = 0
	state.activeWriteFenceKey = ""
	return false
}

func larkFailureAllowsCorrection(failure *feishu.OperationFailure) bool {
	return failure != nil && (failure.Category == "validation" || failure.Retryable)
}

func larkExecuteRetryFailed(state *larkExecuteRetryState, _ larkExecuteRetryAttempt) {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.phase = larkRetryReady
	state.correctableFailures = 0
	state.activeWriteFenceKey = ""
}

// larkExecuteWriteFenceKey returns an opaque digest only for catalog-proven
// writes. Exact normalized-command scope is intentional: Feishu's own version
// history remains the recovery boundary, while this guard prevents the one
// accidental replay the platform can identify with certainty.
func larkExecuteWriteFenceKey(command *feishu.NormalizedCommand) string {
	return feishu.ExactWriteFenceKey(command)
}

// addUnknownWriteFence requires state.mu to be held.
func (state *larkExecuteRetryState) addUnknownWriteFence(key string) {
	if state.unknownWriteFences == nil {
		state.unknownWriteFences = make(map[string]struct{})
	}
	state.unknownWriteFences[key] = struct{}{}
}

func larkExecuteRetryClearRun(runID uint64) {
	if runID != 0 {
		larkExecuteRetryRuns.Delete(runID)
		larkExecuteTopicGuardClearRun(runID)
	}
}
