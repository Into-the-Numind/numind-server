package compact

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

func mustMessages(t *testing.T, msgs []Message) datatypes.JSON {
	t.Helper()
	bs, err := json.Marshal(msgs)
	require.NoError(t, err)
	return datatypes.JSON(bs)
}

func TestCleanseMessages_DropsDanglingToolUse(t *testing.T) {
	calls := json.RawMessage(`[{"id":"call_dangling","name":"foo"}]`)
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: calls}, // dangling, no content, no matching tool result
	}
	out := cleanseMessages(msgs)
	assert.Equal(t, 1, len(out))
	assert.Equal(t, "user", out[0].Role)
}

func TestCleanseMessages_KeepsContentWithDanglingTool(t *testing.T) {
	calls := json.RawMessage(`[{"id":"call_dangling","name":"foo"}]`)
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "thinking out loud", ToolCalls: calls},
	}
	out := cleanseMessages(msgs)
	// Content non-empty → retained even though tool_call is dangling (v1 known limitation)
	assert.Equal(t, 2, len(out))
}

func TestCleanseMessages_DropsEmptyAssistant(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: ""}, // empty + no tool_calls
		{Role: "user", Content: "still here"},
	}
	out := cleanseMessages(msgs)
	assert.Equal(t, 2, len(out))
	assert.Equal(t, "still here", out[1].Content)
}

func TestCleanseMessages_DropsThinking(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "thinking", Content: "internal reasoning"},
		{Role: "assistant", Content: "answer"},
	}
	out := cleanseMessages(msgs)
	assert.Equal(t, 2, len(out))
	for _, m := range out {
		assert.NotEqual(t, "thinking", m.Role)
	}
}

func TestCleanseMessages_KeepsValidToolPair(t *testing.T) {
	calls := json.RawMessage(`[{"id":"call_001","name":"foo"}]`)
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: calls},
		{Role: "tool", ToolCallID: "call_001", Content: "result_data"},
		{Role: "assistant", Content: "ok"},
	}
	out := cleanseMessages(msgs)
	// All 4 messages should be retained (tool_use has matching tool_result)
	assert.Equal(t, 4, len(out))
}

func TestRestore_NilReinjectorReturnsErr(t *testing.T) {
	run := &model.AgentRun{ID: 1}
	_, err := Restore(context.Background(), run, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil reinjector")
}

func TestRestore_NilRunReturnsErr(t *testing.T) {
	_, err := Restore(context.Background(), nil, &NullAttachmentReinjector{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil run")
}

func TestRestore_InjectsNarration(t *testing.T) {
	run := &model.AgentRun{ID: 1, Messages: datatypes.JSON(`[]`)}
	got, err := Restore(context.Background(), run, &NullAttachmentReinjector{})
	require.NoError(t, err)
	assert.Equal(t, RestorationNarration, got.SystemNarration)
}

func TestRestore_FirstTurnNoTools(t *testing.T) {
	run := &model.AgentRun{ID: 1, Messages: datatypes.JSON(`[]`)}
	got, err := Restore(context.Background(), run, &NullAttachmentReinjector{})
	require.NoError(t, err)
	assert.True(t, got.FirstTurnNoTools, "FirstTurnNoTools must default to true per §4.8.6 step 5")
}

func TestRestore_NoCompactSummary(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "hi"}}
	run := &model.AgentRun{ID: 1, Messages: mustMessages(t, msgs)}
	got, err := Restore(context.Background(), run, &NullAttachmentReinjector{})
	require.NoError(t, err)
	// No compact_summary → no IsCompactMark prefix
	assert.Equal(t, 1, len(got.Messages))
	assert.False(t, got.Messages[0].IsCompactMark)
}

func TestRestore_WithCompactSummary(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "hi"}}
	run := &model.AgentRun{
		ID:             1,
		Messages:       mustMessages(t, msgs),
		CompactSummary: "summary text",
	}
	got, err := Restore(context.Background(), run, &NullAttachmentReinjector{})
	require.NoError(t, err)
	require.Equal(t, 2, len(got.Messages))
	assert.True(t, got.Messages[0].IsCompactMark, "first message should be the compact summary mark")
	assert.Equal(t, "system", got.Messages[0].Role)
	assert.Equal(t, "summary text", got.Messages[0].Content)
}

func TestRestore_NullReinjectorPassthrough(t *testing.T) {
	run := &model.AgentRun{ID: 1, Messages: datatypes.JSON(`[]`)}
	got, err := Restore(context.Background(), run, &NullAttachmentReinjector{})
	require.NoError(t, err)
	// Null reinjector returns RestorationNarration unchanged.
	assert.Equal(t, RestorationNarration, got.SystemNarration)
}

func TestRestore_EmptyMessages(t *testing.T) {
	run := &model.AgentRun{ID: 1, Messages: nil}
	got, err := Restore(context.Background(), run, &NullAttachmentReinjector{})
	require.NoError(t, err)
	assert.Empty(t, got.Messages)
}

func TestRestore_MalformedMessagesJSON(t *testing.T) {
	run := &model.AgentRun{ID: 1, Messages: datatypes.JSON(`{not valid json`)}
	_, err := Restore(context.Background(), run, &NullAttachmentReinjector{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal messages")
}

// failingReinjector returns the configured error from Reinject for negative testing.
type failingReinjector struct{ err error }

func (f *failingReinjector) Reinject(ctx context.Context, systemPrompt string, runID uint64) (string, error) {
	return "", f.err
}

func TestRestore_ReinjectorError(t *testing.T) {
	wantErr := errors.New("boom")
	run := &model.AgentRun{ID: 1, Messages: datatypes.JSON(`[]`)}
	_, err := Restore(context.Background(), run, &failingReinjector{err: wantErr})
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
}
