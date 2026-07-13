package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/model"
)

type panicAfterExternalCompleteStore struct {
	*mockAgentRunStore
}

func (s *panicAfterExternalCompleteStore) WriteTurn(context.Context, uint64, json.RawMessage) error {
	panic("finalize after external completion")
}

func newCompletedThenPanicRun(t *testing.T) (*mockAgentRunStore, *model.AgentRun, *externalResumeStoreStub, *externalContinuationGate) {
	t.Helper()
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "s", Status: "running", StateReason: "ext_resume:lease-1",
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, claimed: true, lease: "lease-1"}
	gate := newExternalContinuationGate(resumeStore, ExternalToolResult{RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9"}, "lease-1", make(chan error, 1))
	return runStore, run, resumeStore, gate
}

func TestExternalContinuation_CompleteThenRunPanicPersistsModelError(t *testing.T) {
	withMockChatFn(t, successChatFn("model response"))
	runStore, run, _, gate := newCompletedThenPanicRun(t)
	tool := &loopTestTool{}
	runner := NewAgentRunner(&panicAfterExternalCompleteStore{runStore}, newStaticRegistry(tool))
	_, err := runner.Run(context.Background(), RunRequest{
		UserID: 7, SessionID: "s", ExistingRunID: run.ID,
		ContinueWithoutUserInput: true, History: []*schema.Message{schema.UserMessage("写飞书")},
		ExternalContinuationGate: gate,
	})
	require.Error(t, err)
	assert.True(t, gate.CompletedSuccessfully())
	got, getErr := runStore.Get(context.Background(), run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, string(TerminalModelError), got.StateReason)
	assert.Empty(t, got.PendingExternalActionJSON)
}

func TestExternalContinuation_CompleteThenRunStreamPanicPersistsModelError(t *testing.T) {
	withMockChatStreamFn(t, successStreamFn("model response"))
	runStore, run, _, gate := newCompletedThenPanicRun(t)
	tool := &loopTestTool{}
	runner := NewAgentRunner(&panicAfterExternalCompleteStore{runStore}, newStaticRegistry(tool))
	events := make(chan stream.Event, 256)
	_, err := runner.RunStream(context.Background(), RunRequest{
		UserID: 7, SessionID: "s", ExistingRunID: run.ID,
		ContinueWithoutUserInput: true, History: []*schema.Message{schema.UserMessage("写飞书")},
		ExternalContinuationGate: gate,
	}, run.ID, events)
	require.Error(t, err)
	assert.True(t, gate.CompletedSuccessfully())
	got, getErr := runStore.Get(context.Background(), run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, string(TerminalModelError), got.StateReason)
	assert.Empty(t, got.PendingExternalActionJSON)
}
