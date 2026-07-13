package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

func TestExternalToolResumeFinalization_DoesNotPersistSyntheticUserTurn(t *testing.T) {
	prior := json.RawMessage(`[
		{"role":"user","content":"把分析写入飞书"},
		{"role":"assistant","content":"我来执行"},
		{"role":"tool","content":"{\"ok\":true}","tool_call_id":"tc-9"}
	]`)
	store := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "session-7", Status: "running", StateReason: "running",
		Messages: datatypes.JSON(prior), StartedAt: time.Now(),
	}
	require.NoError(t, store.Create(context.Background(), run))
	runner := NewAgentRunner(store, nil).(*agentRunner)
	_, err := runner.finalizeRun(
		context.Background(),
		run,
		&LoopState{TerminalReason: TerminalCompleted},
		time.Now(),
		"已经写入飞书文档",
		"",
		nil,
		false,
		0,
		false,
		RunRequest{UserID: 7, SessionID: "session-7", ExistingRunID: run.ID, ContinueWithoutUserInput: true},
		nil,
		nil,
		"session-7",
		prior,
	)
	require.NoError(t, err)
	stored, getErr := store.Get(context.Background(), run.ID)
	require.NoError(t, getErr)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(stored.Messages, &got))
	require.Len(t, got, 4)
	assert.Equal(t, "tool", got[2]["role"])
	assert.Equal(t, "tc-9", got[2]["tool_call_id"])
	assert.Equal(t, "assistant", got[3]["role"])
	assert.Equal(t, "已经写入飞书文档", got[3]["content"])
	for i, turn := range got {
		if turn["role"] == "user" {
			assert.NotEmpty(t, turn["content"], "turn %d is a synthetic empty user message", i)
		}
	}
}

func TestExternalToolResume_NoRegistryFailsClosedWithoutWritingEmptyUser(t *testing.T) {
	store := newMockStore()
	prior := datatypes.JSON(`[
		{"role":"user","content":"把分析写入飞书"},
		{"role":"assistant","content":"","tool_calls":[{"id":"tc-9","type":"function","function":{"name":"lark_execute","arguments":"{}"}}]},
		{"role":"tool","content":"{\"ok\":true}","tool_call_id":"tc-9"}
	]`)
	run := &model.AgentRun{
		UserID: 7, SessionID: "session-7", Status: "running", StateReason: "running",
		Messages: prior, StartedAt: time.Now(), UseCompactV2: false,
	}
	require.NoError(t, store.Create(context.Background(), run))
	runner := NewAgentRunner(store, nil)

	_, err := runner.Run(context.Background(), RunRequest{
		UserID: 7, SessionID: "session-7", ExistingRunID: run.ID,
		History:                  nil,
		ContinueWithoutUserInput: true,
	})
	require.Error(t, err)
	got, getErr := store.Get(context.Background(), run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, string(prior), string(got.Messages), "fail-closed continuation must preserve the exact tool result transcript")
	assert.Equal(t, string(TerminalModelError), got.StateReason)
	assert.NotContains(t, string(got.Messages), `"role":"user","content":""`)
}
