package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/narration"
	storepkg "numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/externalaction"
	"numind-server/internal/pkg/model"
)

type externalResumeStoreStub struct {
	mu                     sync.Mutex
	claimed                bool
	leaseSeq               int
	lease                  string
	calls                  int
	runStore               *mockAgentRunStore
	result                 json.RawMessage
	returnOK               bool
	err                    error
	releases               int
	completes              int
	completeErr            error
	completeWaitForContext bool
	releaseErr             error
	releaseCtxErr          error
	candidates             []model.AgentRun
	lists                  int
	touches                int
	touchErrAfter          int
}

func registerTestSupervisorStop(t *testing.T, supervisor *ExternalContinuationSupervisor) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, supervisor.Stop(ctx))
	})
}

func newTestAgentRunResumer(t *testing.T, runStore storepkg.IExternalToolResumeLease, studentRuns *StudentRunService) *AgentRunResumer {
	t.Helper()
	supervisor := NewExternalContinuationSupervisor(ExternalContinuationLimit)
	registerTestSupervisorStop(t, supervisor)
	return NewAgentRunResumer(runStore, studentRuns, supervisor)
}

func TestNewAgentRunResumer_RequiresExplicitLifecycleOwner(t *testing.T) {
	constructor := reflect.TypeOf(NewAgentRunResumer)
	assert.False(t, constructor.IsVariadic())
	require.Equal(t, 3, constructor.NumIn())
	assert.Equal(t, reflect.TypeOf((*ExternalContinuationSupervisor)(nil)), constructor.In(2))
}

type terminalExternalWaitRunner struct {
	mu        sync.Mutex
	cancelled []uint64
}

func (*terminalExternalWaitRunner) Run(context.Context, RunRequest) (*RunResult, error) {
	return nil, errors.New("not used")
}

func (*terminalExternalWaitRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}

func (r *terminalExternalWaitRunner) Cancel(runID uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelled = append(r.cancelled, runID)
	return true
}

func (r *terminalExternalWaitRunner) snapshot() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.cancelled...)
}

func TestAgentRunResumerFinalizeExternalToolWaitDurablyFencesAndCancelsRunnerOnce(t *testing.T) {
	db := newSQTestDB(t)
	ds := storepkg.NewTestStore(db)
	runStore := ds.AgentRuns()
	lease, ok := runStore.(storepkg.IExternalToolResumeLease)
	require.True(t, ok)
	writer, ok := runStore.(storepkg.IExternalActionWriter)
	require.True(t, ok)
	run := &model.AgentRun{
		UserID: 7, SessionID: "terminal-wait", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages: datatypes.JSON(`[{"role":"user","content":"写入飞书"}]`), StartedAt: time.Now().UTC(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	require.NoError(t, writer.UpdatePendingExternalAction(context.Background(), run.ID, []byte(
		`{"provider":"feishu","operation_id":"op-terminal","session_id":"auth-terminal","tool_call_id":"tool-terminal","phase":"user_auth","expires_at":"2027-01-01T00:00:00Z"}`,
	)))
	runner := &terminalExternalWaitRunner{}
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	resumer := newTestAgentRunResumer(t, lease, studentRuns)

	finalized, err := resumer.FinalizeExternalToolWait(
		context.Background(), 7, run.ID, "op-terminal", "tool-terminal", externalaction.TerminalOutcomeUnknown,
	)
	require.NoError(t, err)
	require.True(t, finalized)
	require.Equal(t, []uint64{run.ID}, runner.snapshot(), "the in-process continuation receives only a post-commit cancel")

	finalized, err = resumer.FinalizeExternalToolWait(
		context.Background(), 7, run.ID, "op-terminal", "tool-terminal", externalaction.TerminalOutcomeUnknown,
	)
	require.NoError(t, err)
	require.False(t, finalized)
	require.Equal(t, []uint64{run.ID}, runner.snapshot(), "duplicate lifecycle delivery cannot issue another continuation or runner cancel")
	stored, err := runStore.Get(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, "aborted_tools", stored.StateReason)
	require.NotNil(t, stored.CancellationRequestedAt)
	require.Empty(t, stored.PendingExternalActionJSON)
}

// updateRunCopyOnWrite mirrors the production store's snapshot semantics:
// GORM Get returns a fresh AgentRun value, while mockAgentRunStore.Get returns
// its map pointer. Replacing a cloned value keeps snapshots already handed to
// the resumer immutable and prevents the test double from inventing data races
// that cannot occur in the production store.
func (s *externalResumeStoreStub) updateRunCopyOnWrite(runID uint64, mutate func(*model.AgentRun) error) error {
	s.runStore.mu.Lock()
	defer s.runStore.mu.Unlock()
	current, ok := s.runStore.runs[runID]
	if !ok {
		return errors.New("run not found")
	}
	cloned := *current
	cloned.Messages = append(datatypes.JSON(nil), current.Messages...)
	cloned.PendingExternalActionJSON = append(datatypes.JSON(nil), current.PendingExternalActionJSON...)
	if current.PendingExternalActionAt != nil {
		pendingAt := *current.PendingExternalActionAt
		cloned.PendingExternalActionAt = &pendingAt
	}
	if current.EndedAt != nil {
		endedAt := *current.EndedAt
		cloned.EndedAt = &endedAt
	}
	if err := mutate(&cloned); err != nil {
		return err
	}
	s.runStore.runs[runID] = &cloned
	return nil
}

func (s *externalResumeStoreStub) ClaimExternalToolResume(_ context.Context, runID uint64, operationID, toolCallID string, result json.RawMessage) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return "", false, s.err
	}
	if !s.returnOK || s.claimed {
		return "", false, nil
	}
	s.claimed = true
	s.leaseSeq++
	s.lease = fmt.Sprintf("lease-%d", s.leaseSeq)
	s.result = append(json.RawMessage(nil), result...)
	err := s.updateRunCopyOnWrite(runID, func(run *model.AgentRun) error {
		var turns []json.RawMessage
		if err := json.Unmarshal(run.Messages, &turns); err != nil {
			return err
		}
		toolMessage := schema.ToolMessage(string(result), toolCallID)
		toolMessage.Extra = map[string]any{externalOperationIDExtraKey: operationID}
		turn, err := json.Marshal(toolMessage)
		if err != nil {
			return err
		}
		turns = append(turns, turn)
		messages, err := json.Marshal(turns)
		if err != nil {
			return err
		}
		run.Messages = datatypes.JSON(messages)
		run.Status = "running"
		run.StateReason = "ext_resume:" + s.lease
		run.EndedAt = nil
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return s.lease, true, nil
}

func (s *externalResumeStoreStub) CompleteExternalToolResume(ctx context.Context, runID uint64, operationID, toolCallID, leaseToken string) error {
	s.mu.Lock()
	s.completes++
	waitForContext := s.completeWaitForContext
	completeErr := s.completeErr
	s.mu.Unlock()
	if waitForContext {
		<-ctx.Done()
		if completeErr != nil {
			return completeErr
		}
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completeErr != nil {
		return s.completeErr
	}
	if !s.claimed || leaseToken != s.lease {
		return errors.New("external resume lease changed")
	}
	_, _ = operationID, toolCallID
	return s.updateRunCopyOnWrite(runID, func(run *model.AgentRun) error {
		if run.CancellationRequestedAt != nil || run.IsDeleted {
			return errors.New("external resume was cancelled or deleted")
		}
		if run.Status != "running" || run.StateReason != "ext_resume:"+leaseToken {
			return errors.New("external resume lease state changed")
		}
		run.PendingExternalActionJSON = nil
		run.PendingExternalActionAt = nil
		run.Status = "running"
		run.StateReason = "running"
		return nil
	})
}

func (s *externalResumeStoreStub) ReleaseExternalToolResume(ctx context.Context, runID uint64, operationID, toolCallID, leaseToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases++
	s.releaseCtxErr = ctx.Err()
	if s.releaseCtxErr != nil {
		return s.releaseCtxErr
	}
	if s.releaseErr != nil {
		return s.releaseErr
	}
	transitioned := false
	err := s.updateRunCopyOnWrite(runID, func(run *model.AgentRun) error {
		if run.CancellationRequestedAt != nil || run.IsDeleted {
			return nil
		}
		if !s.claimed || leaseToken != s.lease {
			return errors.New("external resume lease changed")
		}
		if run.Status != "running" || run.StateReason != "ext_resume:"+leaseToken {
			return errors.New("external resume lease state changed")
		}
		run.Status = "terminated"
		run.StateReason = "external_resume_ready"
		transitioned = true
		return nil
	})
	if err != nil {
		return err
	}
	if transitioned {
		s.claimed = false
	}
	_, _ = operationID, toolCallID
	return nil
}

func (s *externalResumeStoreStub) TouchExternalToolResume(_ context.Context, runID uint64, operationID, toolCallID, leaseToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touches++
	if s.touchErrAfter > 0 && s.touches >= s.touchErrAfter {
		return errors.New("external resume heartbeat lost lease")
	}
	if !s.claimed || leaseToken != s.lease {
		return errors.New("external resume lease changed")
	}
	_, _ = operationID, toolCallID
	return s.updateRunCopyOnWrite(runID, func(run *model.AgentRun) error {
		if run.CancellationRequestedAt != nil || run.IsDeleted || run.StateReason != "ext_resume:"+leaseToken {
			return errors.New("external resume was cancelled, deleted, or reclaimed")
		}
		now := time.Now()
		run.PendingExternalActionAt = &now
		return nil
	})
}

func TestExternalContinuationGate_FirstProviderCallIsOneShotAndCompletesAfterResponse(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		Messages:                  datatypes.JSON(`[]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	started := make(chan error, 1)
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", started)
	baseAdapter := &aiserviceAdapter{taskID: "agent.run", usageStore: &sync.Map{}, externalContinuationGate: gate}
	cloned, err := baseAdapter.WithTools(nil)
	require.NoError(t, err)
	adapter := cloned.(*aiserviceAdapter)

	original := chatFn
	t.Cleanup(func() { chatFn = original })
	providerRelease := make(chan struct{})
	var providerCalls int
	chatFn = func(ctx context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		providerCalls++
		select {
		case <-providerRelease:
			return &aiservice.ChatResponse{Content: "ok", FinishReason: "stop"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := adapter.Generate(context.Background(), []*schema.Message{schema.UserMessage("continue")})
		errCh <- err
	}()
	select {
	case err := <-started:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("first provider boundary was not acknowledged")
	}
	resumeStore.mu.Lock()
	assert.Zero(t, resumeStore.completes, "lease must remain pending until an actual model response")
	resumeStore.mu.Unlock()
	close(providerRelease)
	require.NoError(t, <-errCh)

	_, err = adapter.Generate(context.Background(), []*schema.Message{schema.UserMessage("second round")})
	require.NoError(t, err)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.touches)
	assert.Equal(t, 1, resumeStore.completes)
	resumeStore.mu.Unlock()
	assert.Equal(t, 2, providerCalls)
}

func TestExternalContinuationGate_DeleteBeforeProviderBoundaryMakesNoModelCall(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1", IsDeleted: true,
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	adapter := &aiserviceAdapter{taskID: "agent.run", usageStore: &sync.Map{}, externalContinuationGate: gate}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	providerCalls := 0
	chatFn = func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		providerCalls++
		return &aiservice.ChatResponse{Content: "must not happen"}, nil
	}
	_, err := adapter.Generate(context.Background(), []*schema.Message{schema.UserMessage("continue")})
	require.ErrorIs(t, err, errExternalContinuationFirstCall)
	assert.Zero(t, providerCalls)
}

func TestExternalContinuationGate_HeartbeatFailureCancelsProviderAndReleases(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1", touchErrAfter: 2}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	gate.heartbeatInterval = 5 * time.Millisecond
	adapter := &aiserviceAdapter{taskID: "agent.run", usageStore: &sync.Map{}, externalContinuationGate: gate}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	chatFn = func(ctx context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, err := adapter.Generate(context.Background(), []*schema.Message{schema.UserMessage("continue")})
	require.ErrorIs(t, err, errExternalContinuationFirstCall)
	resumeStore.mu.Lock()
	assert.GreaterOrEqual(t, resumeStore.touches, 2)
	assert.Equal(t, 1, resumeStore.releases)
	assert.Zero(t, resumeStore.completes)
	resumeStore.mu.Unlock()
	got, getErr := runStore.Get(context.Background(), run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "external_resume_ready", got.StateReason)
}

func TestExternalContinuationGate_DeletedRunBlocksAutocompactProvider(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1", IsDeleted: true,
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	adapter := &aiserviceAdapter{
		taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000), usageStore: &sync.Map{}, externalContinuationGate: gate,
	}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	providerCalls := 0
	chatFn = func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		providerCalls++
		return &aiservice.ChatResponse{Content: validSummary}, nil
	}
	_, err := adapter.Generate(context.Background(), buildLongConversation(5_000))
	require.ErrorIs(t, err, errExternalContinuationFirstCall)
	assert.Zero(t, providerCalls, "durable delete fencing must run before the compaction LLM")
}

func TestExternalContinuationGate_AutocompactFailureReleasesLease(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	adapter := &aiserviceAdapter{
		taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000), usageStore: &sync.Map{}, externalContinuationGate: gate,
	}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	chatFn = func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, errors.New("compact provider failed")
	}
	_, err := adapter.Generate(context.Background(), buildLongConversation(5_000))
	require.ErrorIs(t, err, errExternalContinuationFirstCall)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.releases)
	assert.Zero(t, resumeStore.completes)
	resumeStore.mu.Unlock()
}

func TestExternalContinuationGate_AutocompactAndMainSuccessCompletesOnce(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	adapter := &aiserviceAdapter{
		taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000), usageStore: &sync.Map{}, externalContinuationGate: gate,
	}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	calls := 0
	chatFn = func(_ context.Context, taskID string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		calls++
		if taskID == profile.AgentCompact {
			return &aiservice.ChatResponse{Content: validSummary}, nil
		}
		return &aiservice.ChatResponse{Content: "done", FinishReason: "stop"}, nil
	}
	_, err := adapter.Generate(context.Background(), buildLongConversation(5_000))
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.completes)
	assert.Zero(t, resumeStore.releases)
	resumeStore.mu.Unlock()
}

func TestExternalContinuationGate_MainProviderFailureAfterAutocompactReleasesLease(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	adapter := &aiserviceAdapter{
		taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000), usageStore: &sync.Map{}, externalContinuationGate: gate,
	}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	chatFn = func(_ context.Context, taskID string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		if taskID == profile.AgentCompact {
			return &aiservice.ChatResponse{Content: validSummary}, nil
		}
		return nil, errors.New("main provider failed")
	}

	_, err := adapter.Generate(context.Background(), buildLongConversation(5_000))
	require.ErrorIs(t, err, errExternalContinuationFirstCall)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.releases)
	assert.Zero(t, resumeStore.completes)
	resumeStore.mu.Unlock()
}

func TestExternalContinuationGate_CompleteFailureImmediatelyReleasesAndAllowsReclaim(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		Messages:                  datatypes.JSON(`[]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	completeErr := errors.New("temporary complete failure")
	resumeStore := &externalResumeStoreStub{
		runStore: runStore, claimed: true, lease: "lease-1", returnOK: true, completeErr: completeErr,
	}
	result := ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`)}
	gate := newExternalContinuationGate(resumeStore, result, "lease-1", make(chan error, 1))
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, usageStore: &sync.Map{}, externalContinuationGate: gate}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	providerCalls := 0
	chatFn = func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		providerCalls++
		return &aiservice.ChatResponse{Content: "done", FinishReason: "stop"}, nil
	}

	_, err := adapter.Generate(context.Background(), []*schema.Message{schema.UserMessage("continue")})
	require.ErrorIs(t, err, completeErr)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.completes)
	assert.Equal(t, 1, resumeStore.releases, "Complete failure must immediately best-effort Release")
	assert.False(t, resumeStore.claimed)
	resumeStore.completeErr = nil
	resumeStore.mu.Unlock()

	newToken, claimed, claimErr := resumeStore.ClaimExternalToolResume(
		context.Background(), result.RunID, result.OperationID, result.ToolCallID, result.Result,
	)
	require.NoError(t, claimErr)
	require.True(t, claimed, "released result must be reclaimable immediately")
	assert.NotEmpty(t, newToken)
	assert.Equal(t, 1, providerCalls, "reclaim itself must not make a second provider call")
}

func TestExternalContinuationGate_CompleteAndReleaseFailuresAreBothPreserved(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	completeErr := errors.New("complete failed")
	releaseErr := errors.New("release failed")
	resumeStore := &externalResumeStoreStub{
		runStore: runStore, claimed: true, lease: "lease-1", completeErr: completeErr, releaseErr: releaseErr,
	}
	gate := newExternalContinuationGate(
		resumeStore,
		ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"},
		"lease-1",
		make(chan error, 1),
	)
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, usageStore: &sync.Map{}, externalContinuationGate: gate}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	chatFn = func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: "done", FinishReason: "stop"}, nil
	}

	_, err := adapter.Generate(context.Background(), []*schema.Message{schema.UserMessage("continue")})
	require.ErrorIs(t, err, completeErr)
	require.ErrorIs(t, err, releaseErr)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.completes)
	assert.Equal(t, 1, resumeStore.releases)
	resumeStore.mu.Unlock()
}

func TestExternalContinuationGate_CompleteDeadlineGetsFreshReleaseBudget(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		Messages:                  datatypes.JSON(`[]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{
		runStore: runStore, claimed: true, lease: "lease-1", returnOK: true, completeWaitForContext: true,
	}
	result := ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`)}
	gate := newExternalContinuationGate(resumeStore, result, "lease-1", make(chan error, 1))
	gate.transitionTimeout = 20 * time.Millisecond
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, usageStore: &sync.Map{}, externalContinuationGate: gate}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	providerCalls := 0
	chatFn = func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		providerCalls++
		return &aiservice.ChatResponse{Content: "done", FinishReason: "stop"}, nil
	}

	_, err := adapter.Generate(context.Background(), []*schema.Message{schema.UserMessage("continue")})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.completes)
	assert.Equal(t, 1, resumeStore.releases)
	assert.NoError(t, resumeStore.releaseCtxErr, "Release must receive a fresh, live detached context")
	assert.False(t, resumeStore.claimed)
	resumeStore.mu.Unlock()
	got, getErr := runStore.Get(context.Background(), run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "external_resume_ready", got.StateReason)

	newToken, claimed, claimErr := resumeStore.ClaimExternalToolResume(
		context.Background(), result.RunID, result.OperationID, result.ToolCallID, result.Result,
	)
	require.NoError(t, claimErr)
	require.True(t, claimed)
	assert.NotEmpty(t, newToken)
	assert.Equal(t, 1, providerCalls, "compensating Release and re-claim must not call the provider")
}

func TestExternalContinuationGate_HeartbeatCoversAutocompactAndMainProvider(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	gate.heartbeatInterval = 5 * time.Millisecond
	adapter := &aiserviceAdapter{
		taskID: profile.AgentRun, compactor: newAdapterCompactor(1_000), usageStore: &sync.Map{}, externalContinuationGate: gate,
	}
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	compactStarted := make(chan struct{})
	compactRelease := make(chan struct{})
	mainStarted := make(chan struct{})
	mainRelease := make(chan struct{})
	chatFn = func(ctx context.Context, taskID string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		if taskID == profile.AgentCompact {
			close(compactStarted)
			select {
			case <-compactRelease:
				return &aiservice.ChatResponse{Content: validSummary}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		close(mainStarted)
		select {
		case <-mainRelease:
			return &aiservice.ChatResponse{Content: "done", FinishReason: "stop"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := adapter.Generate(context.Background(), buildLongConversation(5_000))
		errCh <- err
	}()

	select {
	case <-compactStarted:
	case <-time.After(time.Second):
		t.Fatal("autocompact provider did not start")
	}
	require.Eventually(t, func() bool {
		resumeStore.mu.Lock()
		defer resumeStore.mu.Unlock()
		return resumeStore.touches >= 2
	}, time.Second, 5*time.Millisecond, "heartbeat must run while autocompact is blocked")
	resumeStore.mu.Lock()
	touchesAfterCompact := resumeStore.touches
	resumeStore.mu.Unlock()
	close(compactRelease)
	select {
	case <-mainStarted:
	case <-time.After(time.Second):
		t.Fatal("main provider did not start after autocompact")
	}
	require.Eventually(t, func() bool {
		resumeStore.mu.Lock()
		defer resumeStore.mu.Unlock()
		return resumeStore.touches > touchesAfterCompact
	}, time.Second, 5*time.Millisecond, "heartbeat must remain active while the main provider is blocked")
	close(mainRelease)
	require.NoError(t, <-errCh)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.completes)
	assert.Zero(t, resumeStore.releases)
	resumeStore.mu.Unlock()
}

func TestExternalContinuationGate_StreamKeepsStreamingBoundaryAndCompletesOnFirstChunk(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, usageStore: &sync.Map{}, externalContinuationGate: gate}

	originalChat, originalStream := chatFn, chatStreamFn
	t.Cleanup(func() { chatFn, chatStreamFn = originalChat, originalStream })
	generateCalls, streamCalls := 0, 0
	chatFn = func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		generateCalls++
		return nil, errors.New("stream path must not fall back to Generate")
	}
	chunks := make(chan aiservice.ChatChunk, 2)
	chatStreamFn = func(context.Context, string, aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		streamCalls++
		return chunks, nil
	}

	sr, err := adapter.Stream(context.Background(), []*schema.Message{schema.UserMessage("continue")})
	require.NoError(t, err)
	resumeStore.mu.Lock()
	assert.Zero(t, resumeStore.completes, "opening a stream is not yet a provider response")
	resumeStore.mu.Unlock()
	chunks <- aiservice.ChatChunk{Delta: "hello"}
	msg, err := sr.Recv()
	require.NoError(t, err)
	assert.Equal(t, "hello", msg.Content)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.completes)
	resumeStore.mu.Unlock()
	chunks <- aiservice.ChatChunk{IsFinal: true, FinishReason: "stop"}
	close(chunks)
	for {
		if _, recvErr := sr.Recv(); recvErr != nil {
			break
		}
	}
	sr.Close()
	assert.Zero(t, generateCalls)
	assert.Equal(t, 1, streamCalls)
}

func TestExternalContinuationGate_CompletingLeaseDoesNotCancelRemainingProviderStream(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(
		resumeStore,
		ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"},
		"lease-1",
		make(chan error, 1),
	)
	adapter := &aiserviceAdapter{taskID: profile.AgentRun, usageStore: &sync.Map{}, externalContinuationGate: gate}

	originalStream := chatStreamFn
	t.Cleanup(func() { chatStreamFn = originalStream })
	continueStream := make(chan struct{})
	providerCancelled := make(chan struct{})
	chatStreamFn = func(ctx context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		chunks := make(chan aiservice.ChatChunk, 2)
		go func() {
			defer close(chunks)
			chunks <- aiservice.ChatChunk{ReasoningDelta: "Let me"}
			select {
			case <-continueStream:
				chunks <- aiservice.ChatChunk{Delta: " continue", IsFinal: true, FinishReason: "stop"}
			case <-ctx.Done():
				close(providerCancelled)
			}
		}()
		return chunks, nil
	}

	sr, err := adapter.Stream(context.Background(), []*schema.Message{schema.UserMessage("continue")})
	require.NoError(t, err)
	t.Cleanup(func() { sr.Close() })

	first, err := sr.Recv()
	require.NoError(t, err)
	assert.Equal(t, "Let me", first.ReasoningContent)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.completes, "the external-result lease completes at the first provider chunk")
	resumeStore.mu.Unlock()
	select {
	case <-providerCancelled:
		t.Fatal("completing the external-result lease cancelled the still-active provider stream")
	default:
	}

	close(continueStream)
	final, err := sr.Recv()
	require.NoError(t, err)
	require.NotNil(t, final.ResponseMeta)
	assert.Equal(t, "stop", final.ResponseMeta.FinishReason)
}

func (s *externalResumeStoreStub) ListExternalToolResumeCandidates(context.Context, time.Time, int) ([]model.AgentRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	return append([]model.AgentRun(nil), s.candidates...), nil
}

type externalResumeRunner struct {
	mu       sync.Mutex
	calls    int
	req      RunRequest
	messages []*schema.Message
	done     chan struct{}
}

func (r *externalResumeRunner) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	if req.ExternalContinuationGate != nil {
		if _, _, err := req.ExternalContinuationGate.BeginCall(context.Background()); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	r.calls++
	r.req = req
	r.messages = buildEinoMessages(req)
	done := r.done
	r.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		default:
			close(done)
		}
	}
	return &RunResult{AgentRunID: req.ExistingRunID, TerminalReason: TerminalCompleted}, nil
}

func (r *externalResumeRunner) RunStream(_ context.Context, _ RunRequest, _ uint64, _ chan<- stream.Event) (*RunResult, error) {
	return nil, nil
}
func (r *externalResumeRunner) Cancel(_ uint64) bool { return false }

type requestDetachedResumeRunner struct {
	started     chan struct{}
	allowFinish chan struct{}
	ctxCanceled chan struct{}
	probe       chan chan error
}

func (r *requestDetachedResumeRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if _, _, err := req.ExternalContinuationGate.BeginCall(ctx); err != nil {
		return nil, err
	}
	close(r.started)
	for {
		select {
		case <-r.allowFinish:
			return &RunResult{AgentRunID: req.ExistingRunID, TerminalReason: TerminalCompleted}, nil
		case <-ctx.Done():
			close(r.ctxCanceled)
			return nil, ctx.Err()
		case response := <-r.probe:
			response <- ctx.Err()
		}
	}
}

func (r *requestDetachedResumeRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *requestDetachedResumeRunner) Cancel(uint64) bool { return false }

func TestExternalToolResume_HTTPRequestCancellationDoesNotCancelAcceptedRunner(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "request-detached", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &requestDetachedResumeRunner{
		started: make(chan struct{}), allowFinish: make(chan struct{}), ctxCanceled: make(chan struct{}), probe: make(chan chan error),
	}
	t.Cleanup(func() {
		select {
		case <-runner.allowFinish:
		default:
			close(runner.allowFinish)
		}
	})
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	supervisor := NewExternalContinuationSupervisor(externalResumeWorkerLimit)
	registerTestSupervisorStop(t, supervisor)
	provider := newExternalResumeNarrationProvider(t)
	buffer := NewNarrationBuffer(8, time.Minute)
	resumer := NewAgentRunResumer(resumeStore, NewStudentRunService(runner, runStore, nil, nil, provider, buffer), supervisor)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	require.NoError(t, resumer.Resume(requestCtx, ExternalToolResult{
		RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`),
	}))
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("accepted HTTP continuation did not start")
	}
	provider.Emit(context.Background(), run.ID, "test", narration.StateUse, narration.EmitPayload{})
	require.Eventually(t, func() bool {
		return len(buffer.QuerySince(run.ID, time.Time{})) == 1
	}, time.Second, time.Millisecond, "accepted continuation narration forwarder never subscribed")
	cancelRequest()
	probeResult := make(chan error, 1)
	runner.probe <- probeResult
	assert.NoError(t, <-probeResult, "ordinary HTTP request cancellation must not reach an accepted continuation")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	require.NoError(t, supervisor.Stop(shutdownCtx))
	select {
	case <-runner.ctxCanceled:
	default:
		t.Fatal("application shutdown must cancel and join the accepted continuation")
	}
	provider.Emit(context.Background(), run.ID, "test", narration.StateUse, narration.EmitPayload{})
	assert.Len(t, buffer.QuerySince(run.ID, time.Time{}), 1,
		"application shutdown returned while the accepted continuation narration forwarder was still subscribed")
}

func TestExternalContinuationSupervisor_FullCapacityPublishesDurableReady(t *testing.T) {
	supervisor := NewExternalContinuationSupervisor(externalResumeWorkerLimit)
	releases := make([]func(), 0, externalResumeWorkerLimit)
	for i := 0; i < externalResumeWorkerLimit; i++ {
		_, release, err := supervisor.Acquire()
		require.NoError(t, err)
		releases = append(releases, release)
	}
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = supervisor.Stop(ctx)
	})

	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "capacity", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages: datatypes.JSON(`[{"role":"user","content":"写飞书"}]`), PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`), StartedAt: time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	resumer := NewAgentRunResumer(resumeStore, NewStudentRunService(&externalResumeRunner{}, runStore, nil, nil, nil, nil), supervisor)
	err := resumer.Resume(context.Background(), ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`)})
	require.NoError(t, err)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.calls)
	assert.Equal(t, 1, resumeStore.releases)
	resumeStore.mu.Unlock()
	got, getErr := runStore.Get(context.Background(), run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "external_resume_ready", got.StateReason)
	assert.Contains(t, string(got.Messages), `\"ok\":true`)
}

func TestExternalContinuationSupervisor_JobExitReleasesSlot(t *testing.T) {
	supervisor := NewExternalContinuationSupervisor(1)
	_, release, err := supervisor.Acquire()
	require.NoError(t, err)
	_, _, err = supervisor.Acquire()
	require.ErrorIs(t, err, ErrExternalContinuationCapacity)
	release()
	_, releaseAgain, err := supervisor.Acquire()
	require.NoError(t, err)
	releaseAgain()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, supervisor.Stop(ctx))
	_, _, err = supervisor.Acquire()
	require.ErrorIs(t, err, ErrExternalContinuationStopped)
}

func TestExternalContinuationSupervisor_TimeoutKeepsJobTracked(t *testing.T) {
	supervisor := NewExternalContinuationSupervisor(1)
	_, release, err := supervisor.Acquire()
	require.NoError(t, err)
	timedOut, cancelTimedOut := context.WithCancel(context.Background())
	cancelTimedOut()
	require.ErrorIs(t, supervisor.Stop(timedOut), context.Canceled)
	release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, supervisor.Stop(ctx), "a timed-out shutdown must retain the job tracking needed to join later")
}

func TestExternalToolResume_ContinuesOriginalToolCallWithoutUserInput(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID:                    77,
		SessionID:                 "session-77",
		AgentDefinitionID:         99,
		IsTest:                    true,
		Status:                    "terminated",
		StateReason:               string(TerminalWaitingForUserChoice),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		Messages: datatypes.JSON(`[
			{"role":"user","content":"把分析写成飞书文档"},
			{"role":"assistant","content":"我来创建文档"}
		]`),
		StartedAt: time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &externalResumeRunner{done: make(chan struct{})}
	skillStore := newLifecycleSkillStore()
	skillStore.defs[99] = &model.AgentDefinition{ID: 99, ToolFlags: datatypes.JSON(`{"web_search":false,"custom_resume_tool":true}`)}
	studentRuns := NewStudentRunService(runner, runStore, skillStore, nil, nil, nil)
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	resumer := newTestAgentRunResumer(t, resumeStore, studentRuns)

	err := resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, ToolCallID: "tc-9", OperationID: "op-1", Result: json.RawMessage(`{"ok":true,"state":"succeeded","operation_id":"op-1"}`),
	})
	require.NoError(t, err)
	select {
	case <-runner.done:
	case <-time.After(2 * time.Second):
		t.Fatal("external resume did not start the original run")
	}

	runner.mu.Lock()
	req := runner.req
	msgs := append([]*schema.Message(nil), runner.messages...)
	calls := runner.calls
	runner.mu.Unlock()
	assert.Equal(t, 1, calls)
	assert.Equal(t, run.ID, req.ExistingRunID)
	assert.Equal(t, uint(77), req.UserID)
	assert.Equal(t, "session-77", req.SessionID)
	assert.Equal(t, uint64(99), req.AgentDefinitionID)
	assert.True(t, req.EnableMemory)
	assert.True(t, req.IsTest)
	assert.Contains(t, req.ToolNames, "custom_resume_tool")
	assert.NotContains(t, req.ToolNames, "web_search")
	assert.True(t, req.ContinueWithoutUserInput)
	assert.JSONEq(t, `{"ok":true,"state":"succeeded","operation_id":"op-1"}`, string(req.ExternalContinuationResult))
	assert.Empty(t, req.Input)
	require.GreaterOrEqual(t, len(msgs), 4)
	assistantCall := msgs[len(msgs)-2]
	toolResult := msgs[len(msgs)-1]
	require.Equal(t, schema.Assistant, assistantCall.Role)
	require.Len(t, assistantCall.ToolCalls, 1)
	assert.Equal(t, "tc-9", assistantCall.ToolCalls[0].ID)
	assert.Equal(t, "lark_execute", assistantCall.ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{}`, assistantCall.ToolCalls[0].Function.Arguments)
	assert.Equal(t, schema.Tool, toolResult.Role)
	assert.Equal(t, "tc-9", toolResult.ToolCallID)
	assert.JSONEq(t, `{"ok":true,"state":"succeeded","operation_id":"op-1"}`, toolResult.Content)
	for _, msg := range msgs {
		assert.False(t, msg.Role == schema.User && msg.Content == "", "server continuation must never append an empty user message")
		assert.NotContains(t, msg.Content, "我已完成")
	}

	// The operation worker and the user's manual resume can race. The second
	// callback must neither start a second runner nor execute lark_execute again.
	require.NoError(t, resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, ToolCallID: "tc-9", OperationID: "op-1", Result: json.RawMessage(`{"ok":true,"state":"succeeded","operation_id":"op-1"}`),
	}))
	runner.mu.Lock()
	assert.Equal(t, 1, runner.calls)
	runner.mu.Unlock()
}

func TestExternalToolResume_StoreNoopDoesNotStartRunner(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &externalResumeRunner{done: make(chan struct{})}
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: false}
	resumer := newTestAgentRunResumer(t, resumeStore, studentRuns)

	require.NoError(t, resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, ToolCallID: "tc-9", OperationID: "op-1", Result: json.RawMessage(`{"ok":true}`),
	}))
	runner.mu.Lock()
	assert.Zero(t, runner.calls)
	runner.mu.Unlock()
}

func TestExternalResumeHistory_RebuildsProviderValidToolPair(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": "写飞书"},
		{"role": "assistant", "content": "开始执行"},
		{"role": "tool", "content": `{"ok":true}`, "tool_call_id": "tc-original"},
	}
	history, err := turnsToExternalResumeHistoryMessages(turns, "tc-original")
	require.NoError(t, err)
	req := RunRequest{History: history, ContinueWithoutUserInput: true}
	msgs := buildEinoMessages(req)
	require.Len(t, msgs, 4)
	call, result := msgs[2], msgs[3]
	require.Equal(t, schema.Assistant, call.Role)
	require.Len(t, call.ToolCalls, 1)
	assert.Equal(t, "tc-original", call.ToolCalls[0].ID)
	assert.Equal(t, "lark_execute", call.ToolCalls[0].Function.Name)
	assert.Equal(t, schema.Tool, result.Role)
	assert.Equal(t, "tc-original", result.ToolCallID)
	assert.Equal(t, `{"ok":true}`, result.Content)
}

func TestExternalResumeHistory_PreservesExplicitConnectToolName(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": "帮我连接飞书"},
		{"role": "tool_group", "tool_calls": []any{map[string]any{
			"tool_call_id": "tc-connect", "tool_name": "lark_connect", "current_state": "use",
		}}},
		{"role": "tool", "content": `{"ok":true,"state":"succeeded","operation_id":"op-connect"}`, "tool_call_id": "tc-connect"},
	}

	history, err := turnsToExternalResumeHistoryMessages(turns, "tc-connect")
	require.NoError(t, err)
	require.Len(t, history, 3)
	require.Len(t, history[1].ToolCalls, 1)
	assert.Equal(t, "lark_connect", history[1].ToolCalls[0].Function.Name)
	assert.Equal(t, "lark_connect", history[2].ToolName)
	assert.JSONEq(t, `{}`, history[1].ToolCalls[0].Function.Arguments)
}

func TestExternalResumeHistory_RejectsNonFeishuToolNameFromToolGroup(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": "帮我连接飞书"},
		{"role": "tool_group", "tool_calls": []any{map[string]any{
			"tool_call_id": "tc-connect", "tool_name": "bash_exec", "current_state": "use",
		}}},
		{"role": "tool", "content": `{"ok":true}`, "tool_call_id": "tc-connect"},
	}

	history, err := turnsToExternalResumeHistoryMessages(turns, "tc-connect")
	require.NoError(t, err)
	require.Len(t, history, 3)
	assert.Equal(t, "lark_execute", history[1].ToolCalls[0].Function.Name)
	assert.Equal(t, "lark_execute", history[2].ToolName)
}

func TestExternalResumeHistory_PreservesThinkingContextForSyntheticToolCall(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": "读取飞书多维表格名称和数据表"},
		{
			"role":      "assistant",
			"content":   "拿到 base_token。现在同时获取多维表格名称和数据表列表。",
			"reasoning": "已经完成授权，下一步调用 lark_execute 读取 Base 信息。",
		},
		{"role": "tool", "content": `{"ok":true,"state":"succeeded"}`, "tool_call_id": "tc-base-get"},
	}

	history, err := turnsToExternalResumeHistoryMessages(turns, "tc-base-get")
	require.NoError(t, err)
	require.Len(t, history, 4)
	assert.Equal(t, "已经完成授权，下一步调用 lark_execute 读取 Base 信息。", history[1].ReasoningContent,
		"persisted assistant reasoning must survive detached continuation reconstruction")
	require.Len(t, history[2].ToolCalls, 1)
	assert.Equal(t, "tc-base-get", history[2].ToolCalls[0].ID)
	assert.Equal(t, "已经完成授权，下一步调用 lark_execute 读取 Base 信息。", history[2].ReasoningContent,
		"thinking providers require reasoning_content on the reconstructed assistant tool call")
}

func TestExternalResumeHistory_EmptySyntheticAssistantDoesNotEraseThinkingContext(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": "创建并写入飞书多维表格"},
		{
			"role": "assistant", "content": "先创建 Base。",
			"reasoning": "已确定表结构，调用 lark_execute 创建 Base。",
		},
		{
			"role": "assistant", "content": "",
			"tool_calls": []any{map[string]any{
				"id": "tc-base-create", "type": "function",
				"function": map[string]any{"name": "lark_execute", "arguments": `{}`},
			}},
		},
		{"role": "tool", "content": `{"ok":true,"state":"succeeded"}`, "tool_call_id": "tc-base-create"},
	}

	history, err := turnsToExternalResumeHistoryMessages(turns, "tc-base-create")
	require.NoError(t, err)
	require.Len(t, history, 4)
	require.Len(t, history[2].ToolCalls, 1)
	assert.Equal(t, "已确定表结构，调用 lark_execute 创建 Base。", history[2].ReasoningContent)
}

type detachedStreamingOptInRunner struct {
	runCalls    int
	streamCalls int
}

func (r *detachedStreamingOptInRunner) Run(context.Context, RunRequest) (*RunResult, error) {
	r.runCalls++
	return nil, errors.New("non-stream path must not run")
}

func (r *detachedStreamingOptInRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("public stream entry is not used directly")
}

func (r *detachedStreamingOptInRunner) RunExternalContinuationStream(
	_ context.Context,
	req RunRequest,
	ch chan<- stream.Event,
) (*RunResult, error) {
	r.streamCalls++
	ch <- stream.Event{}
	return &RunResult{AgentRunID: req.ExistingRunID, TerminalReason: TerminalCompleted}, nil
}

func (r *detachedStreamingOptInRunner) Cancel(uint64) bool { return false }

func TestExternalToolResume_ProductionOptInUsesDrainedStreamingRunner(t *testing.T) {
	runner := &detachedStreamingOptInRunner{}
	resumer := &AgentRunResumer{studentRuns: &StudentRunService{runner: runner}}
	err := resumer.callRunner(context.Background(), RunRequest{UserID: 7, ExistingRunID: 283})
	require.NoError(t, err)
	assert.Zero(t, runner.runCalls)
	assert.Equal(t, 1, runner.streamCalls)
}

func TestExternalResumeHistory_StripsPersistedSecretToolArguments(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": "写飞书"},
		{
			"role": "assistant", "content": "开始执行",
			"tool_calls": []any{map[string]any{
				"id": "tc-original", "type": "function",
				"function": map[string]any{"name": "lark_execute", "arguments": `{"argv":["docs","--secret","LEAK"]}`},
			}},
		},
		{"role": "tool", "content": `{"ok":true}`, "tool_call_id": "tc-original"},
	}
	history, err := turnsToExternalResumeHistoryMessages(turns, "tc-original")
	require.NoError(t, err)
	raw, err := json.Marshal(history)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "LEAK")
	assert.NotContains(t, string(raw), "--secret")
	require.GreaterOrEqual(t, len(history), 4)
	call := history[len(history)-2]
	require.Len(t, call.ToolCalls, 1)
	assert.Equal(t, "tc-original", call.ToolCalls[0].ID)
	assert.Equal(t, "lark_execute", call.ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{}`, call.ToolCalls[0].Function.Arguments)
}

func TestExternalResumeHistory_PreservesNonTargetToolNamesButStripsAllArguments(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": "先搜索再写飞书"},
		{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "tc-search", "function": map[string]any{"name": "web_search", "arguments": `{"query":"SECRET"}`}},
			map[string]any{"id": "tc-target", "function": map[string]any{"name": "lark_execute", "arguments": `{"argv":["--token","LEAK"]}`}},
			map[string]any{"id": "tc-unknown", "function": map[string]any{"name": "", "arguments": `{"secret":"DROP"}`}},
		}},
		{"role": "tool", "content": `{"items":[]}`, "tool_call_id": "tc-search"},
		{"role": "tool", "content": `{"ok":true}`, "tool_call_id": "tc-target"},
		{"role": "tool", "content": `{"ignored":true}`, "tool_call_id": "tc-unknown"},
	}
	history, err := turnsToExternalResumeHistoryMessages(turns, "tc-target")
	require.NoError(t, err)
	raw, err := json.Marshal(history)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "SECRET")
	assert.NotContains(t, string(raw), "LEAK")
	assert.NotContains(t, string(raw), "DROP")
	var names []string
	for _, msg := range history {
		for _, call := range msg.ToolCalls {
			names = append(names, call.Function.Name)
			assert.JSONEq(t, `{}`, call.Function.Arguments)
		}
	}
	assert.Equal(t, []string{"web_search", "lark_execute"}, names)
}

func TestExternalResumeReclaimer_ImmediateScanStartsOneFencedContinuation(t *testing.T) {
	runStore := newMockStore()
	toolResult := schema.ToolMessage(`{"ok":true}`, "tc-9")
	toolResult.Extra = map[string]any{externalOperationIDExtraKey: "op-1"}
	toolRaw, err := json.Marshal(toolResult)
	require.NoError(t, err)
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: "external_resume_ready",
		Messages:                  datatypes.JSON(fmt.Sprintf(`[{"role":"user","content":"写飞书"},%s]`, toolRaw)),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &externalResumeRunner{done: make(chan struct{})}
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true, candidates: []model.AgentRun{*run}}
	resumer := newTestAgentRunResumer(t, resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))
	reclaimer := NewExternalResumeReclaimer(resumeStore, resumer, 10*time.Millisecond)
	reclaimer.Start()
	select {
	case <-runner.done:
	case <-time.After(time.Second):
		t.Fatal("startup scan did not resume durable external result")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, reclaimer.Stop(ctx))
	runner.mu.Lock()
	assert.Equal(t, 1, runner.calls)
	runner.mu.Unlock()
}

func TestExternalResumeReclaimer_TwoInstancesFenceToOneRunner(t *testing.T) {
	db := newSQTestDB(t)
	ds := storepkg.NewTestStore(db)
	runStore := ds.AgentRuns()
	lease, ok := runStore.(storepkg.IExternalToolResumeLease)
	require.True(t, ok)
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	token, claimed, err := lease.ClaimExternalToolResume(context.Background(), run.ID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, lease.ReleaseExternalToolResume(context.Background(), run.ID, "op-1", "tc-9", token))

	runner := &externalResumeRunner{done: make(chan struct{})}
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	r1 := NewExternalResumeReclaimer(lease, newTestAgentRunResumer(t, lease, studentRuns), 5*time.Millisecond)
	r2 := NewExternalResumeReclaimer(lease, newTestAgentRunResumer(t, lease, studentRuns), 5*time.Millisecond)
	r1.Start()
	r2.Start()
	select {
	case <-runner.done:
	case <-time.After(time.Second):
		t.Fatal("neither reclaimer started the durable continuation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, r1.Stop(ctx))
	require.NoError(t, r2.Stop(ctx))
	runner.mu.Lock()
	assert.Equal(t, 1, runner.calls)
	runner.mu.Unlock()
}

func TestExternalResumeReclaimer_PeriodicallyScansAndStops(t *testing.T) {
	runStore := newMockStore()
	resumeStore := &externalResumeStoreStub{runStore: runStore}
	resumer := newTestAgentRunResumer(t, resumeStore, NewStudentRunService(&externalResumeRunner{}, runStore, nil, nil, nil, nil))
	reclaimer := NewExternalResumeReclaimer(resumeStore, resumer, 5*time.Millisecond)
	reclaimer.Start()
	require.Eventually(t, func() bool {
		resumeStore.mu.Lock()
		defer resumeStore.mu.Unlock()
		return resumeStore.lists >= 2
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, reclaimer.Stop(ctx))
	resumeStore.mu.Lock()
	listsAtStop := resumeStore.lists
	resumeStore.mu.Unlock()
	assert.GreaterOrEqual(t, listsAtStop, 2)
	resumeStore.mu.Lock()
	assert.Equal(t, listsAtStop, resumeStore.lists, "Stop must terminate the ticker goroutine")
	resumeStore.mu.Unlock()
}

func TestExternalToolResume_InvalidHistoryDoesNotClaimDurableResult(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"system","content":"corrupt for external resume"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &externalResumeRunner{done: make(chan struct{})}
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	resumer := newTestAgentRunResumer(t, resumeStore, studentRuns)

	err := resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`),
	})
	require.Error(t, err)
	resumeStore.mu.Lock()
	assert.Zero(t, resumeStore.calls, "continuation must be fully built and validated before the irreversible claim")
	resumeStore.mu.Unlock()
}

type immediatePreflightFailureRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *immediatePreflightFailureRunner) Run(_ context.Context, _ RunRequest) (*RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return nil, errors.New("synchronous runner preflight failed")
}
func (r *immediatePreflightFailureRunner) RunStream(_ context.Context, _ RunRequest, _ uint64, _ chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *immediatePreflightFailureRunner) Cancel(_ uint64) bool { return false }

func TestExternalToolResume_RunnerPreludeFailureReleasesForImmediateRetry(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &immediatePreflightFailureRunner{}
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	resumer := newTestAgentRunResumer(t, resumeStore, studentRuns)
	result := ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`)}

	require.Error(t, resumer.Resume(context.Background(), result))
	require.Error(t, resumer.Resume(context.Background(), result), "released durable result must be immediately reclaimable")
	runner.mu.Lock()
	assert.Equal(t, 2, runner.calls)
	runner.mu.Unlock()
	resumeStore.mu.Lock()
	assert.Equal(t, 2, resumeStore.releases)
	assert.Equal(t, 0, resumeStore.completes)
	resumeStore.mu.Unlock()
}

type panicPreflightRunner struct{}

func (r *panicPreflightRunner) Run(context.Context, RunRequest) (*RunResult, error) {
	panic("preflight panic")
}
func (r *panicPreflightRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *panicPreflightRunner) Cancel(uint64) bool { return false }

func TestExternalToolResume_RunnerPreludePanicReleasesWithoutHanging(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	resumer := newTestAgentRunResumer(t, resumeStore, NewStudentRunService(&panicPreflightRunner{}, runStore, nil, nil, nil, nil))
	result := ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`)}

	errCh := make(chan error, 1)
	go func() { errCh <- resumer.Resume(context.Background(), result) }()
	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "panic")
	case <-time.After(time.Second):
		t.Fatal("runner preflight panic stranded the resume lease")
	}
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.releases)
	assert.False(t, resumeStore.claimed)
	resumeStore.mu.Unlock()
}

type duplicateReadyRunner struct {
	done chan struct{}
}

func (r *duplicateReadyRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if _, _, err := req.ExternalContinuationGate.BeginCall(ctx); err != nil {
		return nil, err
	}
	_, _, err := req.ExternalContinuationGate.BeginCall(ctx)
	close(r.done)
	return nil, err
}
func (r *duplicateReadyRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *duplicateReadyRunner) Cancel(uint64) bool { return false }

func TestExternalToolResume_DuplicateReadinessCallbackCannotLeakRunner(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &duplicateReadyRunner{done: make(chan struct{})}
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	resumer := newTestAgentRunResumer(t, resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))

	require.NoError(t, resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`),
	}))
	select {
	case <-runner.done:
	case <-time.After(time.Second):
		t.Fatal("a duplicate readiness callback blocked the accepted runner")
	}
}

type cancelBeforeReadyRunner struct {
	runStore *mockAgentRunStore
	runID    uint64
	accepted bool
}

func (r *cancelBeforeReadyRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if err := r.runStore.SetCancellationRequested(ctx, r.runID, nil); err != nil {
		return nil, err
	}
	if _, _, err := req.ExternalContinuationGate.BeginCall(ctx); err != nil {
		return nil, err
	}
	r.accepted = true
	return &RunResult{AgentRunID: r.runID, TerminalReason: TerminalCompleted}, nil
}
func (r *cancelBeforeReadyRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *cancelBeforeReadyRunner) Cancel(uint64) bool { return false }

func TestExternalToolResume_CancelAfterClaimIsNeverAcceptedByRunner(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &cancelBeforeReadyRunner{runStore: runStore, runID: run.ID}
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	resumer := newTestAgentRunResumer(t, resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))

	err := resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`),
	})
	require.Error(t, err)
	assert.False(t, runner.accepted, "a cancelled run must stop before the LLM continuation")
	resumeStore.mu.Lock()
	assert.Equal(t, 0, resumeStore.completes)
	assert.Equal(t, 1, resumeStore.releases)
	resumeStore.mu.Unlock()
}

func TestExternalToolResume_RealRunnerMissingRegistryReleasesLease(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	runner := NewAgentRunner(runStore, nil)
	resumer := newTestAgentRunResumer(t, resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))
	result := ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`)}

	require.Error(t, resumer.Resume(context.Background(), result))
	got, err := runStore.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "external_resume_ready", got.StateReason)
	assert.NotEmpty(t, got.PendingExternalActionJSON)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.releases)
	assert.Equal(t, 0, resumeStore.completes)
	resumeStore.mu.Unlock()
}

type panicRegistry struct{}

func (*panicRegistry) RegisterFactory(ToolFactory) error               { return nil }
func (*panicRegistry) LoadAll(context.Context) error                   { return nil }
func (*panicRegistry) GetTool(string) (FullTool, bool)                 { return nil, false }
func (*panicRegistry) ListEnabled(context.Context) ([]FullTool, error) { return nil, nil }
func (*panicRegistry) ListAllTools() []FullTool                        { panic("registry preflight panic") }

func TestExternalToolResume_RealRunnerRecoveredPreflightPanicStillReleasesLease(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	runner := NewAgentRunner(runStore, &panicRegistry{})
	resumer := newTestAgentRunResumer(t, resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))

	err := resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registry preflight panic")
	got, getErr := runStore.Get(context.Background(), run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "external_resume_ready", got.StateReason)
	assert.NotEmpty(t, got.PendingExternalActionJSON)
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.releases)
	assert.False(t, resumeStore.claimed)
	resumeStore.mu.Unlock()
}
