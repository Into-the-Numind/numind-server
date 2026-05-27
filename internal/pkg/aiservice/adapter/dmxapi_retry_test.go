package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// TestDMXAPI_PostMaxRetries_ConstValue is a regression lock: if someone
// re-sets dmxapiPostMaxRetries back to 0 (the pre-fix value that caused
// dev incident agent_run 41/40/38 to fail on first transient blip), this
// test fails immediately without needing a flaky network repro.
func TestDMXAPI_PostMaxRetries_ConstValue(t *testing.T) {
	assert.Equal(t, 3, dmxapiPostMaxRetries,
		"doPost must retry transient dmxapi.cn blips; do not regress to 0")
}

// TestDMXAPI_Chat_RetriesOn5xx_ThenSucceeds proves the retry wiring actually
// works end-to-end through the adapter: httptest server returns 500 on the
// first attempt, 200 on the second; Chat must surface success.
//
// Pre-fix (MaxRetries=0) this test would FAIL with "doPost: HTTP 500" because
// the adapter gave up immediately.
func TestDMXAPI_Chat_RetriesOn5xx_ThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			// First attempt: transient 5xx that the retry policy should swallow.
			http.Error(w, `{"error":{"message":"upstream blip"}}`, http.StatusBadGateway)
			return
		}
		// Subsequent attempts: real success.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-retry-test",
			"model": "deepseek-v4-pro",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "recovered",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{
				"prompt_tokens":     5,
				"completion_tokens": 1,
				"total_tokens":      6,
			},
		})
	}))
	t.Cleanup(srv.Close)

	route := &registry.ResolvedRoute{
		TaskID:          "test.task",
		ServiceID:       1,
		ServiceKey:      "deepseek-v4-pro",
		ServiceType:     "llm",
		ProviderModelID: "deepseek-v4-pro",
		Provider: registry.ProviderInfo{
			Name:    "dmxapi",
			BaseURL: srv.URL,
			APIKey:  "sk-test",
		},
	}

	d := NewDMXAPIAdapter()
	resp, err := d.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
		},
	})
	require.NoError(t, err, "doPost must retry past the first 502 blip and succeed")
	require.NotNil(t, resp)
	assert.Equal(t, "recovered", resp.Content)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hits),
		"expected exactly 2 upstream hits (1 fail + 1 retry success)")
}
