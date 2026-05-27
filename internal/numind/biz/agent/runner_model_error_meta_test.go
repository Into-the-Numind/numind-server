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
// dmxapi adapter wraps net/http header-timeout into), the runner MUST persist
// the actual error string into agent_run.terminal_metadata so it is visible to
// frontend / admin without needing log access.
//
// Pre-fix: this test FAILS — runner.go only writes state_reason but leaves
// terminal_metadata NULL on LLM failure.
// Post-fix: this test PASSES — runner merges {"error_message": "...", "error_class": "model_error"}.
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
	assert.Contains(t, errMsg, "dmxapi.cn",
		"error_message must carry the underlying provider error (not just the placeholder)")
	assert.Contains(t, errMsg, "timeout awaiting response headers",
		"error_message must carry the root cause from net/http")

	errClass, _ := meta["error_class"].(string)
	assert.Equal(t, "model_error", errClass,
		"error_class should match terminal reason for grep-ability")
}
