package externalaction

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validPayloadJSON = `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`

func TestParseRejectsNonCanonicalObjectShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"exact_duplicate": `{"provider":"feishu","provider":"lark","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`,
		"case_variant":    `{"Provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`,
		"mixed_duplicate": `{"provider":"feishu","Provider":"lark","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`,
		"unknown":         `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z","future":"x"}`,
		"url":             `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z","url":"https://sensitive.example"}`,
		"device_code":     `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z","device_code":"ABC"}`,
		"secret":          `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z","secret":"shh"}`,
		"missing_field":   `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`,
		"empty_field":     `{"provider":"","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`,
		"null_field":      `{"provider":null,"operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`,
		"invalid_time":    `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"not-a-time"}`,
		"wrong_time_type": `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":123}`,
		"zero_time":       `{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"call-1","phase":"user_auth","expires_at":"0001-01-01T00:00:00Z"}`,
		"null":            `null`,
		"non_object":      `[]`,
		"trailing":        validPayloadJSON + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(raw))
			require.Error(t, err)
		})
	}
}

func TestParseAcceptsWhitespaceAndCanonicalJSONNormalizesOrder(t *testing.T) {
	raw := []byte(" \n {\n  \"expires_at\": \"2026-07-13T09:30:00Z\",\n  \"phase\": \"user_auth\",\n  \"tool_call_id\": \"call-1\",\n  \"session_id\": \"auth-1\",\n  \"operation_id\": \"op-1\",\n  \"provider\": \"feishu\"\n } \t")

	payload, err := Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, Payload{
		Provider:    "feishu",
		OperationID: "op-1",
		SessionID:   "auth-1",
		ToolCallID:  "call-1",
		Phase:       "user_auth",
		ExpiresAt:   time.Date(2026, 7, 13, 9, 30, 0, 0, time.UTC),
	}, payload)

	canonical, err := CanonicalJSON(raw)
	require.NoError(t, err)
	assert.Equal(t, validPayloadJSON, string(canonical))
}
