package compactv2

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompactStateV2_MarshalUnmarshal verifies that CompactStateV2 round-trips
// through JSON without losing any field value.
func TestCompactStateV2_MarshalUnmarshal(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	original := CompactStateV2{
		CurrentPhase:                   "L3_summarized",
		EstimatedTokens:                42_000,
		ConsecutiveAutocompactFailures: 2,
		SummaryMessageUUID:             "uuid-summary-001",
		LastCompactionAt:               now,
		TotalAutocompactRuns:           5,
	}

	b, err := json.Marshal(original)
	require.NoError(t, err)

	var restored CompactStateV2
	require.NoError(t, json.Unmarshal(b, &restored))

	assert.Equal(t, original.CurrentPhase, restored.CurrentPhase)
	assert.Equal(t, original.EstimatedTokens, restored.EstimatedTokens)
	assert.Equal(t, original.ConsecutiveAutocompactFailures, restored.ConsecutiveAutocompactFailures)
	assert.Equal(t, original.SummaryMessageUUID, restored.SummaryMessageUUID)
	assert.True(t, original.LastCompactionAt.Equal(restored.LastCompactionAt))
	assert.Equal(t, original.TotalAutocompactRuns, restored.TotalAutocompactRuns)
}

// TestCompactStateV2_EmptyOmitempty ensures CompactStateV2 with no string/int
// content (current_phase / estimated_tokens / etc all zero) marshals without
// those scalar keys. The `last_compaction_at` field is a `time.Time` (not
// pointer, per spec), and the well-known Go encoding/json gotcha means its
// zero value (`0001-01-01T00:00:00Z`) is NOT treated as empty by `omitempty`.
// We document the behavior so future readers don't assume otherwise.
func TestCompactStateV2_EmptyOmitempty(t *testing.T) {
	b, err := json.Marshal(CompactStateV2{})
	require.NoError(t, err)
	// Only the time.Time zero value is emitted; other omitempty fields are stripped.
	assert.Equal(t, `{"last_compaction_at":"0001-01-01T00:00:00Z"}`, string(b))
}

// TestNewMessageFromJSON_MetaNilDefaults verifies:
//   - meta absent → MessageMetaV2 nil (caller treats nil as active per spec §设计要点边界 ①)
//   - uuid absent → transient uuid generated, non-empty
//   - existing fields parsed correctly
func TestNewMessageFromJSON_MetaNilDefaults(t *testing.T) {
	raw := []byte(`{"role":"user","content":"hello"}`)
	msg, err := NewMessageFromJSON(raw)
	require.NoError(t, err)

	// uuid missing → transient generated (non-empty)
	assert.NotEmpty(t, msg.UUID, "transient uuid should be generated when missing")
	// meta missing → nil (caller treats as active)
	assert.Nil(t, msg.Meta, "meta should remain nil when missing in JSON")
	assert.Equal(t, "user", msg.Role)
	assert.Equal(t, "hello", msg.Content)
}

// TestNewMessageFromJSON_TransientUUIDsAreUnique ensures repeated calls on
// raw without uuid produce different uuids (i.e. they are truly transient
// and not constant placeholders).
func TestNewMessageFromJSON_TransientUUIDsAreUnique(t *testing.T) {
	raw := []byte(`{"role":"user","content":"hello"}`)
	m1, err := NewMessageFromJSON(raw)
	require.NoError(t, err)
	m2, err := NewMessageFromJSON(raw)
	require.NoError(t, err)
	assert.NotEqual(t, m1.UUID, m2.UUID, "transient uuids should be unique per call")
}

// TestNewMessageFromJSON_KeepsExistingUUID verifies that if uuid is present
// in the JSON it is preserved (not overwritten by a transient uuid).
func TestNewMessageFromJSON_KeepsExistingUUID(t *testing.T) {
	raw := []byte(`{"uuid":"existing-uuid-123","role":"user","content":"hi"}`)
	msg, err := NewMessageFromJSON(raw)
	require.NoError(t, err)
	assert.Equal(t, "existing-uuid-123", msg.UUID)
}

// TestNewMessageFromJSON_WithMeta verifies a full V2 entry with meta is parsed
// correctly.
func TestNewMessageFromJSON_WithMeta(t *testing.T) {
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{
		"uuid": "msg-001",
		"role": "tool",
		"content": "ok",
		"tool_call_id": "call-1",
		"meta": {
			"is_compacted": true,
			"compaction_phase": "L0",
			"original_size_bytes": 32768,
			"artifact_ref": "art-uuid-abc",
			"preview": "abc...",
			"compacted_at": "2026-05-23T12:00:00Z",
			"tool_name": "file_read",
			"turn_index": 3
		}
	}`)
	msg, err := NewMessageFromJSON(raw)
	require.NoError(t, err)
	assert.Equal(t, "msg-001", msg.UUID)
	assert.Equal(t, "tool", msg.Role)
	require.NotNil(t, msg.Meta)
	assert.True(t, msg.Meta.IsCompacted)
	assert.Equal(t, "L0", msg.Meta.CompactionPhase)
	assert.Equal(t, int64(32768), msg.Meta.OriginalSizeBytes)
	assert.Equal(t, "art-uuid-abc", msg.Meta.ArtifactRef)
	assert.Equal(t, "file_read", msg.Meta.ToolName)
	assert.Equal(t, 3, msg.Meta.TurnIndex)
	assert.True(t, msg.Meta.CompactedAt.Equal(now))
}

// TestNewMessageFromJSON_BadJSON verifies invalid JSON returns an error.
func TestNewMessageFromJSON_BadJSON(t *testing.T) {
	_, err := NewMessageFromJSON([]byte(`{not valid json`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NewMessageFromJSON")
}

// TestThresholdConstants smoke-checks that the task 2.1 constants stayed
// at the expected values (board README §D2 / spec §设计要点).
func TestThresholdConstants(t *testing.T) {
	assert.Equal(t, 16*1024, ToolArtifactSizeLimit)
	assert.Equal(t, 1024, ArtifactPreviewBytes)
	assert.Equal(t, 30, ArtifactDefaultTTLDays)
}
