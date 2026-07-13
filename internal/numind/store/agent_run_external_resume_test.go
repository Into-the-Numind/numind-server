package store

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

	"numind-server/internal/pkg/model"
)

const externalResumePayload = `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`

func seedExternalResumeRun(t *testing.T, s IAgentRunStore, mutate func(*model.AgentRun)) uint64 {
	t.Helper()
	endedAt := time.Now().Add(-time.Minute)
	run := &model.AgentRun{
		UserID:                    7,
		SessionID:                 "session-7",
		Status:                    "terminated",
		StateReason:               "waiting_for_user_choice",
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写入飞书"}]`),
		StartedAt:                 time.Now().Add(-time.Minute),
		EndedAt:                   &endedAt,
		PendingExternalActionJSON: datatypes.JSON(externalResumePayload),
	}
	if mutate != nil {
		mutate(run)
	}
	require.NoError(t, s.Create(context.Background(), run))
	return run.ID
}

func externalToolResumer(t *testing.T, s IAgentRunStore) IExternalToolResumer {
	t.Helper()
	r, ok := s.(IExternalToolResumer)
	require.True(t, ok, "production agentRunStore must expose only the narrow external resume capability")
	return r
}

func decodeStoredSchemaMessages(t *testing.T, raw []byte) []*schema.Message {
	t.Helper()
	var turns []json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &turns))
	out := make([]*schema.Message, 0, len(turns))
	for _, turn := range turns {
		var msg schema.Message
		require.NoError(t, json.Unmarshal(turn, &msg))
		out = append(out, &msg)
	}
	return out
}

func TestExternalToolResume_AppendsOriginalToolResultAndClaimsOnce(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	r := externalToolResumer(t, s)
	runID := seedExternalResumeRun(t, s, nil)
	result := json.RawMessage(`{"ok":true}`)

	claimed, err := r.ResumeExternalTool(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	require.True(t, claimed)

	got, err := s.Get(context.Background(), runID)
	require.NoError(t, err)
	msgs := decodeStoredSchemaMessages(t, got.Messages)
	require.Len(t, msgs, 2)
	assert.Equal(t, schema.Tool, msgs[1].Role)
	assert.Equal(t, "tc-9", msgs[1].ToolCallID)
	assert.JSONEq(t, string(result), msgs[1].Content)
	assert.Empty(t, got.PendingExternalActionJSON)
	assert.Nil(t, got.PendingExternalActionAt)
	assert.Equal(t, "running", got.Status)
	assert.Equal(t, "running", got.StateReason)
	assert.Nil(t, got.EndedAt)

	claimed, err = r.ResumeExternalTool(context.Background(), runID, "op-1", "tc-9", result)
	require.NoError(t, err)
	assert.False(t, claimed)
	got, err = s.Get(context.Background(), runID)
	require.NoError(t, err)
	assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 2, "idempotent callback must not append twice")

	_, err = r.ResumeExternalTool(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":false}`))
	require.Error(t, err, "a different result for the same already-resumed tool call is an integrity error")
}

func TestExternalToolResume_ConcurrentCallbacksHaveOneWinner(t *testing.T) {
	s, _ := newExternalActionAgentRunStore(t)
	r := externalToolResumer(t, s)
	runID := seedExternalResumeRun(t, s, nil)
	const workers = 12

	start := make(chan struct{})
	var wg sync.WaitGroup
	claimed := make(chan bool, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := r.ResumeExternalTool(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
			claimed <- ok
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(claimed)
	close(errs)

	winners := 0
	for ok := range claimed {
		if ok {
			winners++
		}
	}
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, winners)
	got, err := s.Get(context.Background(), runID)
	require.NoError(t, err)
	assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 2)
}

func TestExternalToolResume_FailsClosedWithoutClearingWait(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		toolCall  string
		result    json.RawMessage
		mutate    func(*model.AgentRun)
	}{
		{name: "wrong operation", operation: "op-other", toolCall: "tc-9", result: json.RawMessage(`{"ok":true}`)},
		{name: "wrong tool call", operation: "op-1", toolCall: "tc-other", result: json.RawMessage(`{"ok":true}`)},
		{name: "corrupt pending", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true}`), mutate: func(r *model.AgentRun) { r.PendingExternalActionJSON = datatypes.JSON(`{"operation_id":`) }},
		{name: "corrupt transcript", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true}`), mutate: func(r *model.AgentRun) { r.Messages = datatypes.JSON(`[{`) }},
		{name: "invalid result", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":`)},
		{name: "non object result", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`true`)},
		{name: "trailing result", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true} {}`)},
		{name: "duplicate result key", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true,"ok":false}`)},
		{name: "nested duplicate result key", operation: "op-1", toolCall: "tc-9", result: json.RawMessage(`{"ok":true,"data":{"id":1,"id":2}}`)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newExternalActionAgentRunStore(t)
			r := externalToolResumer(t, s)
			runID := seedExternalResumeRun(t, s, tc.mutate)
			before, err := s.Get(context.Background(), runID)
			require.NoError(t, err)
			beforeWait := append([]byte(nil), before.PendingExternalActionJSON...)
			beforeMessages := append([]byte(nil), before.Messages...)

			claimed, err := r.ResumeExternalTool(context.Background(), runID, tc.operation, tc.toolCall, tc.result)
			require.Error(t, err)
			assert.False(t, claimed)
			after, getErr := s.Get(context.Background(), runID)
			require.NoError(t, getErr)
			assert.Equal(t, string(beforeWait), string(after.PendingExternalActionJSON), "wait identity must remain for diagnosis/retry")
			assert.Equal(t, string(beforeMessages), string(after.Messages), "corrupt transcript must never be reset")
			assert.Equal(t, "terminated", after.Status)
			assert.Equal(t, "waiting_for_user_choice", after.StateReason)
		})
	}
}

func TestExternalToolResume_DoesNotReviveCancelledOrDeletedRuns(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name   string
		mutate func(*model.AgentRun)
	}{
		{name: "cancel requested", mutate: func(r *model.AgentRun) { r.CancellationRequestedAt = &now }},
		{name: "soft deleted", mutate: func(r *model.AgentRun) { r.IsDeleted = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newExternalActionAgentRunStore(t)
			r := externalToolResumer(t, s)
			runID := seedExternalResumeRun(t, s, tc.mutate)

			claimed, err := r.ResumeExternalTool(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
			require.NoError(t, err)
			assert.False(t, claimed)
			got, getErr := s.Get(context.Background(), runID)
			require.NoError(t, getErr)
			assert.Equal(t, externalResumePayload, string(got.PendingExternalActionJSON))
			assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 1)
		})
	}
}

func TestExternalToolResume_UnexpectedRunStateOrMissingWaitFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.AgentRun)
	}{
		{name: "wrong state reason", mutate: func(r *model.AgentRun) { r.StateReason = "completed" }},
		{name: "active run", mutate: func(r *model.AgentRun) { r.Status = "running" }},
		{name: "missing wait without prior result", mutate: func(r *model.AgentRun) { r.PendingExternalActionJSON = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newExternalActionAgentRunStore(t)
			r := externalToolResumer(t, s)
			runID := seedExternalResumeRun(t, s, tc.mutate)

			claimed, err := r.ResumeExternalTool(context.Background(), runID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
			require.Error(t, err)
			assert.False(t, claimed)
			got, getErr := s.Get(context.Background(), runID)
			require.NoError(t, getErr)
			assert.Len(t, decodeStoredSchemaMessages(t, got.Messages), 1)
		})
	}
}
