package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	storepkg "numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

func TestLarkExternalResumeIntegration_ReusesOriginalToolCallOnceAfterRestart(t *testing.T) {
	// The pending action is committed by the pre-restart process. The resumed
	// process must reopen the durable store and build a fresh resumer; an
	// in-memory mock would only prove duplicate callbacks within one process.
	db := newSQTestDB(t)
	ds := storepkg.NewTestStore(db)
	runStore := ds.AgentRuns()
	lease, ok := runStore.(storepkg.IExternalToolResumeLease)
	require.True(t, ok)
	run := &model.AgentRun{
		UserID: 77, SessionID: "lark-integration-session", AgentDefinitionID: 99, IsTest: true,
		Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-lark-integration","session_id":"auth-lark-integration","tool_call_id":"tool-lark-integration","phase":"user_auth","expires_at":"2026-07-15T09:30:00Z"}`),
		Messages: datatypes.JSON(`[
			{"role":"user","content":"把分析写成飞书文档"},
			{"role":"assistant","content":"我来创建文档"}
		]`),
		StartedAt: time.Now().UTC(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &externalResumeRunner{done: make(chan struct{})}
	skillStore := newLifecycleSkillStore()
	skillStore.defs[99] = &model.AgentDefinition{ID: 99, ToolFlags: datatypes.JSON(`{"web_search":false,"lark_execute":true}`)}
	completed := ExternalToolResult{
		RunID: run.ID, OperationID: "op-lark-integration", ToolCallID: "tool-lark-integration",
		Result: json.RawMessage(`{"ok":true,"state":"succeeded","operation_id":"op-lark-integration","data":{"document_id":"docx-result"}}`),
	}

	// Process 1 has already persisted the wait. Process 2 reopens the same
	// SQLite-backed AgentRun store and receives the completion callback.
	firstProcess := newTestAgentRunResumer(t, lease, NewStudentRunService(runner, runStore, skillStore, nil, nil, nil))
	_ = firstProcess
	resumedProcess := newTestAgentRunResumer(t, lease, NewStudentRunService(runner, runStore, skillStore, nil, nil, nil))
	require.NoError(t, resumedProcess.Resume(context.Background(), completed))
	select {
	case <-runner.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the completed Feishu operation did not resume the original run")
	}
	// Process 3 receives a duplicate callback/recovery scan after process 2 has
	// completed. The persisted lease/result fence must make it a no-op.
	duplicateProcess := newTestAgentRunResumer(t, lease, NewStudentRunService(runner, runStore, skillStore, nil, nil, nil))
	require.NoError(t, duplicateProcess.Resume(context.Background(), completed))

	runner.mu.Lock()
	calls := runner.calls
	request := runner.req
	messages := append([]*schema.Message(nil), runner.messages...)
	runner.mu.Unlock()
	require.Equal(t, 1, calls)
	require.Equal(t, run.ID, request.ExistingRunID)
	require.True(t, request.ContinueWithoutUserInput)
	require.Empty(t, request.Input)
	require.Len(t, messages, 4)

	assistantCall := messages[len(messages)-2]
	toolResult := messages[len(messages)-1]
	require.Equal(t, schema.Assistant, assistantCall.Role)
	require.Len(t, assistantCall.ToolCalls, 1, "the original lark tool call must be reconstructed exactly once")
	require.Equal(t, "tool-lark-integration", assistantCall.ToolCalls[0].ID)
	require.Equal(t, "lark_execute", assistantCall.ToolCalls[0].Function.Name)
	require.Equal(t, schema.Tool, toolResult.Role)
	require.Equal(t, "tool-lark-integration", toolResult.ToolCallID)
	require.JSONEq(t, string(completed.Result), toolResult.Content)
	for _, message := range messages {
		require.False(t, message.Role == schema.User && message.Content == "", "recovery must not synthesize a user message")
	}
}
