package compact

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCompactStateV1_JSONRoundTrip(t *testing.T) {
	when := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	original := CompactStateV1{
		LastCompactAt:         &when,
		LastBoundaryMessageID: "msg_089abc",
		TotalCompactAttempts:  2,
		ConsecutiveFailures:   0,
		SummaryTokenCount:     3840,
		StrategyUsed:          "reactive_compact",
	}
	bs, err := json.Marshal(original)
	assert.NoError(t, err)

	var got CompactStateV1
	assert.NoError(t, json.Unmarshal(bs, &got))
	assert.Equal(t, original.LastBoundaryMessageID, got.LastBoundaryMessageID)
	assert.Equal(t, original.TotalCompactAttempts, got.TotalCompactAttempts)
	assert.Equal(t, original.SummaryTokenCount, got.SummaryTokenCount)
	assert.Equal(t, original.StrategyUsed, got.StrategyUsed)
	assert.NotNil(t, got.LastCompactAt)
	assert.True(t, original.LastCompactAt.Equal(*got.LastCompactAt))
}

func TestCompactStateV1_PartialFieldsZeroValue(t *testing.T) {
	// Only a subset of fields present in JSON.
	raw := `{"strategy_used":"reactive_compact","total_compact_attempts":1}`
	var got CompactStateV1
	assert.NoError(t, json.Unmarshal([]byte(raw), &got))
	assert.Equal(t, "reactive_compact", got.StrategyUsed)
	assert.Equal(t, 1, got.TotalCompactAttempts)
	// Other fields → Go zero value, no error
	assert.Empty(t, got.LastBoundaryMessageID)
	assert.Equal(t, 0, got.SummaryTokenCount)
	assert.Nil(t, got.LastCompactAt)
}

func TestCompactStateV1_OmitemptyAllFields(t *testing.T) {
	empty := CompactStateV1{}
	bs, err := json.Marshal(empty)
	assert.NoError(t, err)
	// Every field is omitempty so empty struct marshals to {}.
	assert.JSONEq(t, `{}`, string(bs))
}

func TestMessage_OmitemptyFields(t *testing.T) {
	m := Message{Role: "user", Content: "hello"}
	bs, err := json.Marshal(m)
	assert.NoError(t, err)
	// HasFileRef / IsCompactMark / ToolCalls / ToolCallID all omitempty
	assert.JSONEq(t, `{"role":"user","content":"hello"}`, string(bs))
}

func TestMessage_FullFields(t *testing.T) {
	m := Message{
		Role:          "tool",
		Content:       "result",
		ToolCallID:    "call_001",
		HasFileRef:    true,
		IsCompactMark: false,
	}
	bs, err := json.Marshal(m)
	assert.NoError(t, err)
	// IsCompactMark=false is omitted; HasFileRef=true persists
	assert.JSONEq(t, `{"role":"tool","content":"result","tool_call_id":"call_001","has_file_ref":true}`, string(bs))
}
