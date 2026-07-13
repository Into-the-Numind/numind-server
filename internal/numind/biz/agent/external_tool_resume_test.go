package agent

import (
	"context"
	"encoding/json"
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
	mu       sync.Mutex
	claimed  bool
	calls    int
	runStore *mockAgentRunStore
	result   json.RawMessage
	returnOK bool
	err      error
}

func (s *externalResumeStoreStub) ResumeExternalTool(_ context.Context, runID uint64, operationID, toolCallID string, result json.RawMessage) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	if !s.returnOK || s.claimed {
		return false, nil
	}
	s.claimed = true
	s.result = append(json.RawMessage(nil), result...)
	run, getErr := s.runStore.Get(context.Background(), runID)
	if getErr != nil {
		return false, getErr
	}
	var turns []json.RawMessage
	if err := json.Unmarshal(run.Messages, &turns); err != nil {
		return false, err
	}
	turn, err := json.Marshal(schema.ToolMessage(string(result), toolCallID))
	if err != nil {
		return false, err
	}
	turns = append(turns, turn)
	messages, err := json.Marshal(turns)
	if err != nil {
		return false, err
	}
	run.Messages = datatypes.JSON(messages)
	run.PendingExternalActionJSON = nil
	run.PendingExternalActionAt = nil
	run.Status = "running"
	run.StateReason = "running"
	run.EndedAt = nil
	_ = operationID
	return true, nil
}

type externalResumeRunner struct {
	mu       sync.Mutex
	calls    int
	req      RunRequest
	messages []*schema.Message
	done     chan struct{}
}

func (r *externalResumeRunner) Run(_ context.Context, req RunRequest) (*RunResult, error) {
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
		UserID:            77,
		SessionID:         "session-77",
		AgentDefinitionID: 99,
		IsTest:            true,
		Status:            "terminated",
		StateReason:       string(TerminalWaitingForUserChoice),
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
	runner := &externalResumeRunner{done: make(chan struct{})}
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: false}
	resumer := NewAgentRunResumer(resumeStore, studentRuns)

	require.NoError(t, resumer.Resume(context.Background(), ExternalToolResult{
		RunID: 44, ToolCallID: "tc-9", OperationID: "op-1", Result: json.RawMessage(`{"ok":true}`),
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
