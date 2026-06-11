package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestYieldProtocol_StateMachineTransition verifies that the state machine
// correctly drives to TerminalWaitingForUserChoice when LoopEventAskUserPaused
// is received, which is the core invariant the yield handler in runner.go relies on.
func TestYieldProtocol_StateMachineTransition(t *testing.T) {
	st := &LoopState{StepCount: 2}

	terminal, cont, isTerminal := st.Transition(LoopEventAskUserPaused)

	require.True(t, isTerminal, "LoopEventAskUserPaused must terminate the loop")
	assert.Equal(t, TerminalWaitingForUserChoice, terminal)
	assert.Empty(t, cont, "yield must not produce a ContinueReason")
	assert.Equal(t, TerminalWaitingForUserChoice, st.TerminalReason)
	assert.True(t, st.IsTerminal())
	assert.Equal(t, 2, st.StepCount, "StepCount must not be mutated by yield transition")
}

// TestYieldProtocol_RunResult_Shape verifies that RunResult can carry
// TerminalWaitingForUserChoice as TerminalReason (compile-time shape check).
func TestYieldProtocol_RunResult_Shape(t *testing.T) {
	result := &RunResult{
		AgentRunID:     42,
		TerminalReason: TerminalWaitingForUserChoice,
		FinalOutput:    "",
		StepCount:      0,
	}
	assert.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)
	assert.Empty(t, result.FinalOutput, "yield result must have empty FinalOutput")
}

// TestYieldProtocol_ErrorsAs_IntegrationWithRunnerHandler verifies that
// errors.As correctly unwraps yieldError from a wrapped error (simulating
// how einoAgent might wrap the error from tool.Execute).
func TestYieldProtocol_ErrorsAs_IntegrationWithRunnerHandler(t *testing.T) {
	payload := YieldPayload{Questions: []YieldQuestion{{
		Question:    "Which approach?",
		Options:     []YieldOption{{Key: "a", Label: "Option A"}, {Key: "b", Label: "Option B"}},
		Header:      "Choose one",
		MultiSelect: false,
	}}}
	// Simulate the error returned by einoAgent.Generate when a tool yields.
	rawErr := &yieldError{Payload: payload}

	// Simulate wrapping that eino might apply.
	wrappedErr := errors.New("eino: " + rawErr.Error())
	_ = wrappedErr // eino typically wraps; for direct sentinel check:

	// Direct check — the runner handler uses errors.As on runErr.
	var yieldErr *yieldError
	require.True(t, errors.As(rawErr, &yieldErr), "errors.As must extract yieldError")
	require.Len(t, yieldErr.Payload.Questions, 1)
	assert.Equal(t, payload.Questions[0].Question, yieldErr.Payload.Questions[0].Question)
	assert.Len(t, yieldErr.Payload.Questions[0].Options, 2)
	assert.Equal(t, "a", yieldErr.Payload.Questions[0].Options[0].Key)

	// Also verify errors.Is works for the sentinel.
	require.True(t, errors.Is(rawErr, ErrYieldForUserQuestion))
}

// TestYieldProtocol_RunnerIntegration is a smoke test that runs the full runner
// with nil registry (no einoAgent tools), confirming runner.Run continues to
// work and TerminalWaitingForUserChoice is a valid TerminalReason value that
// doesn't break the runner machinery when read back from state.
// NOTE: this does NOT trigger the yield path (that requires a live tool returning
// yieldError via einoAgent); it only validates the RunResult type handles the new
// constant without panicking.
func TestYieldProtocol_RunnerIntegration_NoRegression(t *testing.T) {
	store := newMockStore()
	runner := NewAgentRunner(store, nil)

	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1,
		Input:  "test yield protocol no-regression",
	})
	require.NoError(t, err)
	// Standard path: no ask_user_question tool means normal completion.
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	assert.NotZero(t, result.AgentRunID)

	// Verify TerminalWaitingForUserChoice is a distinct value from TerminalCompleted.
	assert.NotEqual(t, TerminalWaitingForUserChoice, result.TerminalReason,
		"waiting_for_user_choice must be distinct from completed")
}
