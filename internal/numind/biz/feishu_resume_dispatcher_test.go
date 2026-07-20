package biz

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/externalaction"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/require"
)

type dispatcherOperationFake struct {
	result  *feishu.OperationResult
	err     error
	started chan struct{}
	release <-chan struct{}

	mu    sync.Mutex
	calls int
}

func (f *dispatcherOperationFake) Resume(ctx context.Context, _ uint, _ string) (*feishu.OperationResult, error) {
	f.mu.Lock()
	f.calls++
	started := f.started
	release := f.release
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.result, f.err
}

func (f *dispatcherOperationFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type dispatcherAgentResumerFake struct {
	mu              sync.Mutex
	results         []agent.ExternalToolResult
	finalizations   []dispatcherFinalization
	handoffSessions []string
	err             error
	finalizeErr     error
	finalized       bool
}

func (f *dispatcherAgentResumerFake) HandoffExternalToolWait(
	_ context.Context,
	_ uint,
	_ uint64,
	_, _ string,
	_ []string,
	action agent.ExternalActionPayload,
) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handoffSessions = append(f.handoffSessions, action.SessionID)
	return true, nil
}

func (f *dispatcherAgentResumerFake) handoffSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.handoffSessions...)
}

type dispatcherFinalization struct {
	userID      uint
	runID       uint64
	operationID string
	toolCallID  string
	outcome     externalaction.TerminalOutcome
}

type dispatcherOperationObserverFake struct {
	mu     sync.Mutex
	events []feishu.OperationObservation
}

func (f *dispatcherOperationObserverFake) ObserveOperation(event feishu.OperationObservation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *dispatcherOperationObserverFake) snapshot() []feishu.OperationObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feishu.OperationObservation(nil), f.events...)
}

func (f *dispatcherAgentResumerFake) Resume(_ context.Context, result agent.ExternalToolResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, result)
	return f.err
}

func (f *dispatcherAgentResumerFake) FinalizeExternalToolWait(
	_ context.Context,
	userID uint,
	runID uint64,
	operationID string,
	toolCallID string,
	outcome externalaction.TerminalOutcome,
) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalizations = append(f.finalizations, dispatcherFinalization{
		userID: userID, runID: runID, operationID: operationID, toolCallID: toolCallID, outcome: outcome,
	})
	return f.finalized, f.finalizeErr
}

func TestWorkspaceResumeDispatcher_TerminalFinalizationNoopIsSuccessAndErrorRemainsRetryable(t *testing.T) {
	operationID := "operation-terminal-finalizer"
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: operationID,
		State:       model.FeishuOperationUnknown,
		AgentRunID:  48,
		ToolCallID:  "tool-terminal-finalizer",
	}}

	resumer := &dispatcherAgentResumerFake{finalized: false}
	dispatcher := NewWorkspaceResumeDispatcher(operations, resumer)
	require.NoError(t, dispatcher.DispatchResume(context.Background(), 7, operationID),
		"a durable already-finalized no-op is a successful terminal handoff")

	resumer.finalizeErr = errors.New("temporary finalizer failure")
	require.Error(t, dispatcher.DispatchResume(context.Background(), 7, operationID),
		"a storage/finalizer failure must remain retryable")
	require.Empty(t, resumer.snapshot())
	require.Len(t, resumer.finalizationSnapshot(), 2)
}

func (f *dispatcherAgentResumerFake) snapshot() []agent.ExternalToolResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.ExternalToolResult(nil), f.results...)
}

func (f *dispatcherAgentResumerFake) finalizationSnapshot() []dispatcherFinalization {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dispatcherFinalization(nil), f.finalizations...)
}

func (f *dispatcherAgentResumerFake) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// task11LeaseFake models Task11's tokenized durable claim. Every dispatcher
// callback may reach it, but only the first claim accepts/backfills the result;
// later callbacks are successful no-ops.
type task11LeaseFake struct {
	mu       sync.Mutex
	attempts int
	accepted map[string]agent.ExternalToolResult
}

// durableDispatcherOperationState models the database fence owned by the real
// operation service. Fresh service instances share the stored terminal result;
// only the instance that wins the durable claim executes the authorization CLI
// and final publication path.
type durableDispatcherOperationState struct {
	mu            sync.Mutex
	result        *feishu.OperationResult
	stored        *feishu.OperationResult
	businessRuns  int
	authCLICalls  int
	finalizeCalls int
}

type durableDispatcherOperationService struct {
	state *durableDispatcherOperationState
}

func newDurableDispatcherOperationState(result *feishu.OperationResult) *durableDispatcherOperationState {
	return &durableDispatcherOperationState{result: cloneDispatcherOperationResult(result)}
}

func (s *durableDispatcherOperationState) newService() *durableDispatcherOperationService {
	return &durableDispatcherOperationService{state: s}
}

func (s *durableDispatcherOperationState) businessRunCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.businessRuns
}

func (s *durableDispatcherOperationState) authCLICallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authCLICalls
}

func (s *durableDispatcherOperationState) finalizeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalizeCalls
}

func (s *durableDispatcherOperationService) Resume(context.Context, uint, string) (*feishu.OperationResult, error) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.stored == nil {
		s.state.businessRuns++
		s.state.authCLICalls++
		s.state.finalizeCalls++
		s.state.stored = cloneDispatcherOperationResult(s.state.result)
	}
	return cloneDispatcherOperationResult(s.state.stored), nil
}

func cloneDispatcherOperationResult(result *feishu.OperationResult) *feishu.OperationResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Data = append(json.RawMessage(nil), result.Data...)
	if result.Failure != nil {
		failure := *result.Failure
		failure.RequiredScopes = append([]string(nil), result.Failure.RequiredScopes...)
		clone.Failure = &failure
	}
	return &clone
}

type durableResponseLossAgentState struct {
	mu           sync.Mutex
	attempts     int
	lostResponse bool
	accepted     map[string]agent.ExternalToolResult
}

type durableResponseLossAgentResumer struct {
	state *durableResponseLossAgentState
}

type durableTerminalFinalizerState struct {
	mu       sync.Mutex
	attempts int
	commits  int
	lost     bool
}

type durableTerminalFinalizer struct {
	state *durableTerminalFinalizerState
}

func (f *durableTerminalFinalizer) Resume(context.Context, agent.ExternalToolResult) error {
	return errors.New("terminal operation must never enter continuation")
}

func (f *durableTerminalFinalizer) FinalizeExternalToolWait(
	_ context.Context,
	_ uint,
	_ uint64,
	_ string,
	_ string,
	_ externalaction.TerminalOutcome,
) (bool, error) {
	f.state.mu.Lock()
	defer f.state.mu.Unlock()
	f.state.attempts++
	if f.state.commits > 0 {
		return false, nil
	}
	f.state.commits++
	if !f.state.lost {
		f.state.lost = true
		return true, errors.New("synthetic response loss after durable terminal commit")
	}
	return true, nil
}

func (s *durableTerminalFinalizerState) snapshot() (attempts, commits int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts, s.commits
}

func newDurableResponseLossAgentState() *durableResponseLossAgentState {
	return &durableResponseLossAgentState{accepted: make(map[string]agent.ExternalToolResult)}
}

func (s *durableResponseLossAgentState) newResumer() *durableResponseLossAgentResumer {
	return &durableResponseLossAgentResumer{state: s}
}

func (r *durableResponseLossAgentResumer) Resume(_ context.Context, result agent.ExternalToolResult) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.attempts++
	if _, exists := r.state.accepted[result.OperationID]; exists {
		return nil
	}
	result.Result = append(json.RawMessage(nil), result.Result...)
	r.state.accepted[result.OperationID] = result
	if !r.state.lostResponse {
		r.state.lostResponse = true
		return errors.New("synthetic Agent response loss after durable acceptance")
	}
	return nil
}

func (r *durableResponseLossAgentResumer) FinalizeExternalToolWait(
	context.Context, uint, uint64, string, string, externalaction.TerminalOutcome,
) (bool, error) {
	return false, nil
}

func (s *durableResponseLossAgentState) snapshot() (int, []agent.ExternalToolResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accepted := make([]agent.ExternalToolResult, 0, len(s.accepted))
	for _, result := range s.accepted {
		accepted = append(accepted, result)
	}
	return s.attempts, accepted
}

func (f *task11LeaseFake) Resume(_ context.Context, result agent.ExternalToolResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.accepted == nil {
		f.accepted = make(map[string]agent.ExternalToolResult)
	}
	if _, exists := f.accepted[result.OperationID]; !exists {
		f.accepted[result.OperationID] = result
	}
	return nil
}

func (f *task11LeaseFake) FinalizeExternalToolWait(
	context.Context, uint, uint64, string, string, externalaction.TerminalOutcome,
) (bool, error) {
	return false, nil
}

func (f *task11LeaseFake) snapshot() (int, []agent.ExternalToolResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	accepted := make([]agent.ExternalToolResult, 0, len(f.accepted))
	for _, result := range f.accepted {
		accepted = append(accepted, result)
	}
	return f.attempts, accepted
}

func TestWorkspaceResumeDispatcher_SucceededOperationBackfillsOriginalToolResult(t *testing.T) {
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: "operation-1",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document_id":"doc-1"}`),
		AgentRunID:  41,
		ToolCallID:  "tool-1",
	}}
	resumer := &dispatcherAgentResumerFake{}
	dispatcher := NewWorkspaceResumeDispatcher(operations, resumer)

	require.NoError(t, dispatcher.DispatchResume(context.Background(), 7, "operation-1"))
	require.Equal(t, 1, operations.callCount())
	got := resumer.snapshot()
	require.Len(t, got, 1)
	require.EqualValues(t, 41, got[0].RunID)
	require.Equal(t, "tool-1", got[0].ToolCallID)
	require.Equal(t, "operation-1", got[0].OperationID)
	require.JSONEq(t, `{
		"ok":true,"state":"succeeded","operation_id":"operation-1",
		"data":{"document_id":"doc-1"}
	}`, string(got[0].Result))
}

func TestWorkspaceResumeDispatcherHandsCreateAppForwardToUserAuth(t *testing.T) {
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: "operation-user-438",
		State:       model.FeishuOperationWaitingUserAuth,
		AgentRunID:  261,
		ToolCallID:  "lark-call-438",
		Action: &feishu.OperationAction{
			Provider:    feishu.ProviderLark,
			OperationID: "operation-user-438",
			SessionID:   "user-auth-new",
			Phase:       model.FeishuAuthPhaseUserAuth,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	resumer := &dispatcherAgentResumerFake{}

	require.NoError(t, NewWorkspaceResumeDispatcher(operations, resumer).
		DispatchResume(context.Background(), 438, "operation-user-438"))
	require.Equal(t, []string{"user-auth-new"}, resumer.handoffSnapshot())
}

func TestWorkspaceResumeDispatcher_WaitingDoesNotBackfillAndFailuresFinalizeSafely(t *testing.T) {
	for _, state := range []string{
		model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation,
	} {
		t.Run(state, func(t *testing.T) {
			operations := &dispatcherOperationFake{result: &feishu.OperationResult{
				OperationID: "operation-terminal-" + state,
				State:       state,
				Data:        json.RawMessage(`{"must_not":"backfill"}`),
				AgentRunID:  42,
				ToolCallID:  "tool-terminal",
			}}
			resumer := &dispatcherAgentResumerFake{}

			require.NoError(t, NewWorkspaceResumeDispatcher(operations, resumer).DispatchResume(context.Background(), 7, "operation-terminal-"+state))
			require.Empty(t, resumer.snapshot())
		})
	}

	for _, test := range []struct {
		state, code, category string
		outcome               externalaction.TerminalOutcome
	}{
		{model.FeishuOperationFailed, feishu.PublicCodeNotFound, "not_found", externalaction.TerminalOutcomeFailed},
		{model.FeishuOperationUnknown, feishu.PublicCodeUnknownResult, "unknown_result", externalaction.TerminalOutcomeUnknown},
		{model.FeishuOperationCancelled, feishu.PublicCodeCancelled, "cancelled", externalaction.TerminalOutcomeCancelled},
	} {
		t.Run(test.state, func(t *testing.T) {
			operationID := "operation-terminal-" + test.state
			operations := &dispatcherOperationFake{result: &feishu.OperationResult{
				OperationID: operationID, State: test.state, AgentRunID: 42, ToolCallID: "tool-terminal",
				Failure: &feishu.OperationFailure{
					Code: test.code, Category: test.category, BusinessStarted: true,
				},
			}}
			resumer := &dispatcherAgentResumerFake{}
			require.NoError(t, NewWorkspaceResumeDispatcher(operations, resumer).DispatchResume(context.Background(), 7, operationID))
			require.Empty(t, resumer.snapshot())
			require.Equal(t, []dispatcherFinalization{{
				userID: 7, runID: 42, operationID: operationID,
				toolCallID: "tool-terminal", outcome: test.outcome,
			}}, resumer.finalizationSnapshot())
		})
	}
}

func TestWorkspaceResumeDispatcher_ConcurrentCallbacksBackfillOnce(t *testing.T) {
	release := make(chan struct{})
	operations := &dispatcherOperationFake{
		result: &feishu.OperationResult{
			OperationID: "operation-concurrent",
			State:       model.FeishuOperationSucceeded,
			Data:        json.RawMessage(`{"document_id":"doc-concurrent"}`),
			AgentRunID:  43,
			ToolCallID:  "tool-concurrent",
		},
		started: make(chan struct{}, 1),
		release: release,
	}
	resumer := &dispatcherAgentResumerFake{}
	dispatcher := NewWorkspaceResumeDispatcher(operations, resumer)

	firstErr := make(chan error, 1)
	go func() { firstErr <- dispatcher.DispatchResume(context.Background(), 7, "operation-concurrent") }()
	<-operations.started

	const callbacks = 12
	joined := make(chan struct{}, callbacks)
	dispatcher.joined = func() { joined <- struct{}{} }
	var workers sync.WaitGroup
	errs := make(chan error, callbacks)
	for range callbacks {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- dispatcher.DispatchResume(context.Background(), 7, "operation-concurrent")
		}()
	}
	for range callbacks {
		<-joined
	}
	close(release)
	require.NoError(t, <-firstErr)
	workers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, operations.callCount())
	require.Len(t, resumer.snapshot(), 1)
}

func TestWorkspaceResumeDispatcher_RepeatedSucceededOperationUsesTask11DurableIdempotence(t *testing.T) {
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: "operation-idempotent",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document_id":"doc-idempotent"}`),
		AgentRunID:  44,
		ToolCallID:  "tool-idempotent",
	}}
	resumer := &task11LeaseFake{}
	dispatcher := NewWorkspaceResumeDispatcher(operations, resumer)

	require.NoError(t, dispatcher.DispatchResume(context.Background(), 7, "operation-idempotent"))
	require.NoError(t, dispatcher.DispatchResume(context.Background(), 7, "operation-idempotent"))
	require.Equal(t, 2, operations.callCount())
	attempts, accepted := resumer.snapshot()
	require.Equal(t, 2, attempts)
	require.Len(t, accepted, 1)
}

func TestWorkspaceResumeDispatcher_DurableReadyResultIsNotCachedAsCompleted(t *testing.T) {
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: "operation-durable-ready",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document_id":"doc-durable-ready"}`),
		AgentRunID:  45,
		ToolCallID:  "tool-durable-ready",
	}}
	// AgentRunResumer intentionally returns nil after persisting durable-ready
	// when continuation capacity is saturated. The outer dispatcher cannot treat
	// that nil as a completed provider continuation.
	resumer := &dispatcherAgentResumerFake{}
	dispatcher := NewWorkspaceResumeDispatcher(operations, resumer)

	require.NoError(t, dispatcher.DispatchResume(context.Background(), 7, "operation-durable-ready"))
	require.NoError(t, dispatcher.DispatchResume(context.Background(), 7, "operation-durable-ready"))
	require.Equal(t, 2, operations.callCount())
	require.Len(t, resumer.snapshot(), 2)
}

func TestWorkspaceResumeDispatcher_ResumerErrorRemainsRetryable(t *testing.T) {
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: "operation-retryable",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document_id":"doc-retryable"}`),
		AgentRunID:  46,
		ToolCallID:  "tool-retryable",
	}}
	resumer := &dispatcherAgentResumerFake{err: errors.New("temporary resumer failure")}
	dispatcher := NewWorkspaceResumeDispatcher(operations, resumer)

	require.Error(t, dispatcher.DispatchResume(context.Background(), 7, "operation-retryable"))
	resumer.setError(nil)
	require.NoError(t, dispatcher.DispatchResume(context.Background(), 7, "operation-retryable"))
	require.Equal(t, 2, operations.callCount())
	require.Len(t, resumer.snapshot(), 2)
}

func TestWorkspaceResumeDispatcher_ResponseLossAcrossFreshInstancesUsesDurableClaims(t *testing.T) {
	operationState := newDurableDispatcherOperationState(&feishu.OperationResult{
		OperationID: "operation-response-loss",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document_id":"doc-response-loss"}`),
		AgentRunID:  47,
		ToolCallID:  "tool-response-loss",
	})
	agentState := newDurableResponseLossAgentState()

	instanceA := NewWorkspaceResumeDispatcher(operationState.newService(), agentState.newResumer())
	require.Error(t, instanceA.DispatchResume(context.Background(), 7, "operation-response-loss"),
		"the first Agent acceptance commits before its response is lost")

	instanceB := NewWorkspaceResumeDispatcher(operationState.newService(), agentState.newResumer())
	require.NoError(t, instanceB.DispatchResume(context.Background(), 7, "operation-response-loss"))
	require.Equal(t, 1, operationState.businessRunCount())
	require.Equal(t, 1, operationState.authCLICallCount())
	require.Equal(t, 1, operationState.finalizeCount())
	attempts, accepted := agentState.snapshot()
	require.Equal(t, 2, attempts)
	require.Len(t, accepted, 1, "the durable Agent claim accepts the result exactly once")
}

func TestWorkspaceResumeDispatcher_TerminalResponseLossAcrossFreshInstancesFinalizesOnce(t *testing.T) {
	operationID := "operation-terminal-response-loss"
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: operationID, State: model.FeishuOperationUnknown,
		AgentRunID: 49, ToolCallID: "tool-terminal-response-loss",
	}}
	state := &durableTerminalFinalizerState{}

	first := NewWorkspaceResumeDispatcher(operations, &durableTerminalFinalizer{state: state})
	require.Error(t, first.DispatchResume(context.Background(), 7, operationID),
		"the first durable commit succeeds before its response is lost")

	second := NewWorkspaceResumeDispatcher(operations, &durableTerminalFinalizer{state: state})
	require.NoError(t, second.DispatchResume(context.Background(), 7, operationID),
		"a fresh instance observes the durable finalization as a successful no-op")
	attempts, commits := state.snapshot()
	require.Equal(t, 2, attempts)
	require.Equal(t, 1, commits)
	require.Equal(t, 2, operations.callCount())
}

func TestWorkspaceResumeDispatcher_SeparateInstancesRelyOnTask11Lease(t *testing.T) {
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: "operation-cross-instance",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document_id":"doc-cross-instance"}`),
		AgentRunID:  47,
		ToolCallID:  "tool-cross-instance",
	}}
	resumer := &task11LeaseFake{}
	first := NewWorkspaceResumeDispatcher(operations, resumer)
	second := NewWorkspaceResumeDispatcher(operations, resumer)

	require.NoError(t, first.DispatchResume(context.Background(), 7, "operation-cross-instance"))
	require.NoError(t, second.DispatchResume(context.Background(), 7, "operation-cross-instance"))
	attempts, accepted := resumer.snapshot()
	require.Equal(t, 2, attempts)
	require.Len(t, accepted, 1)
}

func TestWorkspaceResumeDispatcher_OperationFailureDoesNotInvokeAgentResumer(t *testing.T) {
	operations := &dispatcherOperationFake{err: errors.New("operation unavailable")}
	resumer := &dispatcherAgentResumerFake{}
	err := NewWorkspaceResumeDispatcher(operations, resumer).DispatchResume(context.Background(), 7, "operation-error")
	require.Error(t, err)
	require.Empty(t, resumer.snapshot())
}

func TestWorkspaceResumeDispatcher_UnknownWriteDoesNotResumeAgentOrReturnInfrastructureError(t *testing.T) {
	operationID := "operation-base-create-unknown"
	operations := &dispatcherOperationFake{result: &feishu.OperationResult{
		OperationID: operationID,
		State:       model.FeishuOperationUnknown,
		AgentRunID:  236,
		ToolCallID:  "tool-base-create",
		Failure: &feishu.OperationFailure{
			Code:            feishu.PublicCodeUnknownResult,
			Category:        "unknown_result",
			BusinessStarted: true,
		},
	}}
	// Agent continuation rejects the terminal write result. The authorization
	// acknowledgement must not expose that expected business terminal as HTTP
	// 500, and must not call the model-continuation path at all.
	resumer := &dispatcherAgentResumerFake{err: errors.New("terminal result cannot continue the agent")}
	observer := &dispatcherOperationObserverFake{}

	err := NewWorkspaceResumeDispatcher(operations, resumer, observer).DispatchResume(
		context.Background(), 7, operationID,
	)

	require.NoError(t, err)
	require.Empty(t, resumer.snapshot())
	require.Equal(t, []dispatcherFinalization{{
		userID: 7, runID: 236, operationID: operationID,
		toolCallID: "tool-base-create", outcome: externalaction.TerminalOutcomeUnknown,
	}}, resumer.finalizationSnapshot())
	require.Equal(t, []feishu.OperationObservation{{
		UserID: 7, OperationID: operationID, Phase: "handoff",
		OutcomeClass: "terminal_finalized", ExitCode: -1,
	}}, observer.snapshot())
}
