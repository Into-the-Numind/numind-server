package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/errno"
)

// TestRunner_LLMError_PersistsErrorToTerminalMetadata reproduces the dev incident
// where agent_run 41/40/38 terminated with state_reason=model_error but
// terminal_metadata was NULL — the real provider error (dmxapi timeout) only lived
// in server logs.
//
// Repro contract: when chatFn returns an ErrAIProviderTimeout (which is what the
// dmxapi adapter wraps net/http header-timeout into), the runner MUST persist the
// error into agent_run.terminal_metadata — a USER-FACING friendly message in
// error_message and the raw provider cause in error_detail — so frontend/admin
// can see it without log access AND learners never see engineer text.
func TestRunner_LLMError_PersistsErrorToTerminalMetadata(t *testing.T) {
	// Inject the exact error shape produced by the dmxapi adapter
	// (wrapHTTPClientErr → errno.ErrAIProviderTimeout.SetMessage(...)).
	injectedErr := errno.ErrAIProviderTimeout.SetMessage(
		"doPost /chat/completions: request failed after 1 attempts: " +
			"Post \"https://www.dmxapi.cn/v1/chat/completions\": " +
			"net/http: timeout awaiting response headers")
	withMockChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, injectedErr
	})

	store := newMockStore()
	runner, toolName := newReActRunner(store)

	result, err := runner.Run(context.Background(), newReActRequest(toolName, "trigger model error"))
	require.NoError(t, err)
	require.NotZero(t, result.AgentRunID)
	assert.Equal(t, TerminalModelError, result.TerminalReason,
		"provider timeout must map to TerminalModelError")

	got, dbErr := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, dbErr)
	require.NotEmpty(t, got.TerminalMetadata,
		"terminal_metadata MUST be populated on model_error so frontend/admin can see "+
			"the real provider error without grepping server logs")

	var meta map[string]any
	require.NoError(t, json.Unmarshal(got.TerminalMetadata, &meta))

	errMsg, ok := meta["error_message"].(string)
	require.True(t, ok, "terminal_metadata.error_message must be a string, got %T", meta["error_message"])
	// error_message is USER-FACING (friendly Chinese) — it must NOT leak the raw
	// provider string. The raw cause is preserved under error_detail for ops.
	assert.NotContains(t, errMsg, "dmxapi.cn", "error_message must not leak the provider host to users")
	assert.NotContains(t, errMsg, "net/http", "error_message must not leak raw engineer text")
	assert.Contains(t, errMsg, "超时", "provider timeout should map to a friendly 超时 message")

	errDetail, _ := meta["error_detail"].(string)
	assert.Contains(t, errDetail, "dmxapi.cn",
		"error_detail must preserve the raw provider error for ops debugging")
	assert.Contains(t, errDetail, "timeout awaiting response headers",
		"error_detail must carry the root cause from net/http")

	errClass, _ := meta["error_class"].(string)
	assert.Equal(t, "model_error", errClass,
		"error_class should match terminal reason for grep-ability")
}
