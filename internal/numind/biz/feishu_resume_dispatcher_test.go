package biz

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
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
	mu      sync.Mutex
	results []agent.ExternalToolResult
	err     error
}

func (f *dispatcherAgentResumerFake) Resume(_ context.Context, result agent.ExternalToolResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, result)
	return f.err
}

func (f *dispatcherAgentResumerFake) snapshot() []agent.ExternalToolResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.ExternalToolResult(nil), f.results...)
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
	require.Equal(t, agent.ExternalToolResult{
		RunID:       41,
		ToolCallID:  "tool-1",
		OperationID: "operation-1",
		Result:      json.RawMessage(`{"document_id":"doc-1"}`),
	}, got[0])
}

func TestWorkspaceResumeDispatcher_NonSucceededOperationNeverBackfillsSuccess(t *testing.T) {
	for _, state := range []string{
		model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation,
		model.FeishuOperationFailed,
		model.FeishuOperationUnknown,
		model.FeishuOperationCancelled,
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
