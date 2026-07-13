package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/model"
)

type externalResumeStoreStub struct {
	mu        sync.Mutex
	claimed   bool
	leaseSeq  int
	lease     string
	calls     int
	runStore  *mockAgentRunStore
	result    json.RawMessage
	returnOK  bool
	err       error
	releases  int
	completes int
}

func (s *externalResumeStoreStub) ResumeExternalTool(_ context.Context, runID uint64, operationID, toolCallID string, result json.RawMessage) (bool, error) {
	_, claimed, err := s.ClaimExternalToolResume(context.Background(), runID, operationID, toolCallID, result)
	return claimed, err
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
	run, getErr := s.runStore.Get(context.Background(), runID)
	if getErr != nil {
		return "", false, getErr
	}
	var turns []json.RawMessage
	if err := json.Unmarshal(run.Messages, &turns); err != nil {
		return "", false, err
	}
	toolMessage := schema.ToolMessage(string(result), toolCallID)
	toolMessage.Extra = map[string]any{externalOperationIDExtraKey: operationID}
	turn, err := json.Marshal(toolMessage)
	if err != nil {
		return "", false, err
	}
	turns = append(turns, turn)
	messages, err := json.Marshal(turns)
	if err != nil {
		return "", false, err
	}
	run.Messages = datatypes.JSON(messages)
	run.Status = "running"
	run.StateReason = "ext_resume:" + s.lease
	run.EndedAt = nil
	return s.lease, true, nil
}

func (s *externalResumeStoreStub) CompleteExternalToolResume(_ context.Context, runID uint64, operationID, toolCallID, leaseToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completes++
	if !s.claimed || leaseToken != s.lease {
		return errors.New("external resume lease changed")
	}
	run, err := s.runStore.Get(context.Background(), runID)
	if err != nil {
		return err
	}
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
	_, _ = operationID, toolCallID
	return nil
}

func (s *externalResumeStoreStub) ReleaseExternalToolResume(_ context.Context, runID uint64, operationID, toolCallID, leaseToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases++
	run, err := s.runStore.Get(context.Background(), runID)
	if err != nil {
		return err
	}
	if run.CancellationRequestedAt != nil || run.IsDeleted {
		return nil
	}
	if !s.claimed || leaseToken != s.lease {
		return errors.New("external resume lease changed")
	}
	if run.Status != "running" || run.StateReason != "ext_resume:"+leaseToken {
		return errors.New("external resume lease state changed")
	}
	s.claimed = false
	if err == nil {
		run.Status = "terminated"
		run.StateReason = "external_resume_ready"
	}
	_, _ = operationID, toolCallID
	return nil
}

type externalResumeRunner struct {
	mu       sync.Mutex
	calls    int
	req      RunRequest
	messages []*schema.Message
	done     chan struct{}
}

func (r *externalResumeRunner) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	if req.ExternalContinuationReady != nil {
		if err := req.ExternalContinuationReady(context.Background()); err != nil {
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
	resumer := NewAgentRunResumer(resumeStore, studentRuns)

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
	time.Sleep(20 * time.Millisecond)
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
	resumer := NewAgentRunResumer(resumeStore, studentRuns)

	require.NoError(t, resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, ToolCallID: "tc-9", OperationID: "op-1", Result: json.RawMessage(`{"ok":true}`),
	}))
	time.Sleep(20 * time.Millisecond)
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
	history, err := turnsToExternalResumeHistoryMessages(turns)
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
	history, err := turnsToExternalResumeHistoryMessages(turns)
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
	resumer := NewAgentRunResumer(resumeStore, studentRuns)

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
	resumer := NewAgentRunResumer(resumeStore, studentRuns)
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
	resumer := NewAgentRunResumer(resumeStore, NewStudentRunService(&panicPreflightRunner{}, runStore, nil, nil, nil, nil))
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
	if err := req.ExternalContinuationReady(ctx); err != nil {
		return nil, err
	}
	err := req.ExternalContinuationReady(ctx)
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
	resumer := NewAgentRunResumer(resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))

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
	if err := req.ExternalContinuationReady(ctx); err != nil {
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
	resumer := NewAgentRunResumer(resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))

	err := resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`),
	})
	require.Error(t, err)
	assert.False(t, runner.accepted, "a cancelled run must stop before the LLM continuation")
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.completes)
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
	resumer := NewAgentRunResumer(resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))
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
	resumer := NewAgentRunResumer(resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))

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
