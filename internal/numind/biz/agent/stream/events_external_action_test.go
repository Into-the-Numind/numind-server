package stream

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalActionEvent_RoundTripsLiveURL(t *testing.T) {
	expiresAt := time.Date(2026, 7, 13, 9, 30, 0, 0, time.UTC)
	payload := ExternalActionPayload{
		Provider:    "feishu",
		OperationID: "op-123",
		SessionID:   "auth-456",
		ToolCallID:  "call-789",
		Phase:       "user_auth",
		URL:         "https://open.feishu.cn/authorize?state=short-lived",
		ExpiresAt:   expiresAt,
	}

	event, err := Encode(EventExternalAction, payload, 8, 42, 3)
	require.NoError(t, err)
	assert.Equal(t, EventExternalAction, event.Type)

	var decoded ExternalActionPayload
	require.NoError(t, json.Unmarshal(event.Data, &decoded))
	assert.Equal(t, payload, decoded)
}
