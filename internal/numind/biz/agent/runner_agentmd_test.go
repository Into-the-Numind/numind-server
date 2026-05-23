package agent

// runner_agentmd_test.go — V1.5 板块 3 task 3.1 review fix P1.2:
// integration test that asserts AGENT.md content reaches the system prompt
// segment 3 (Memories) when LoadAgentMd returns non-empty content.
//
// Approach: replace the package-level chatFn seam (adapter.go:17) with a
// capture mock that records the SystemPrompt seen by the LLM call. We write
// a real AGENT.md to a tmpdir, point viper at it, then run the agent and
// verify the captured prompt contains:
//   1. The "## Memories" section header (P1.1 review fix)
//   2. The "## Agent Rules (developer-defined)" sub-header
//   3. The deployment-level path label
//   4. Verbatim content from the on-disk file

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
)

// captureChatFn intercepts adapter.chatFn and stores the SystemPrompt seen by
// the LLM. Returns a benign success response so the ReAct loop completes.
type capturedPrompt struct {
	mu    sync.Mutex
	value string
}

func (c *capturedPrompt) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *capturedPrompt) set(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = v
}

// withCaptureChatFn installs a chatFn that captures the first system message
// it sees and returns a stub success response, then restores the original
// chatFn on test cleanup.
func withCaptureChatFn(t *testing.T) *capturedPrompt {
	t.Helper()
	captured := &capturedPrompt{}
	orig := chatFn
	t.Cleanup(func() { chatFn = orig })
	chatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		for _, m := range req.Messages {
			if m.Role == aiservice.MessageRoleSystem {
				captured.set(m.Content.Text)
				break
			}
		}
		return &aiservice.ChatResponse{
			Content:      "final answer",
			Model:        "test-model",
			Provider:     "test",
			FinishReason: "stop",
			Usage:        aiservice.TokenUsage{PromptTokens: 1, CompletionTokens: 1},
		}, nil
	}
	return captured
}

// withAgentMdViperConfig points viper at tmp dirs for the AGENT.md loader and
// restores prior values on cleanup. Mirrors loader_test.go withTestConfig but
// lives in the agent package so it can be used from runner integration tests.
func withAgentMdViperConfig(t *testing.T, overrides map[string]any) {
	t.Helper()
	keys := []string{
		"agent.memory.agent_md.enabled",
		"agent.memory.agent_md.user_data_dir",
		"agent.memory.agent_md.etc_dir",
		"agent.memory.agent_md.max_per_file_chars",
		"agent.memory.agent_md.max_total_chars",
	}
	prev := make(map[string]any, len(keys))
	for _, k := range keys {
		prev[k] = viper.Get(k)
	}
	for k, v := range overrides {
		viper.Set(k, v)
	}
	t.Cleanup(func() {
		for k, v := range prev {
			viper.Set(k, v)
		}
	})
}

// TestRunner_AgentMdInjection: an on-disk AGENT.md is loaded by runner.Run
// and reaches the LLM via the system prompt.
//
// Verifies:
//   - The "## Memories" section header is present (P1.1 review fix)
//   - The "## Agent Rules (developer-defined)" sub-header is present
//   - The deployment-level path label "[Deployment-level]" is present
//   - The verbatim file content reaches the prompt
func TestRunner_AgentMdInjection(t *testing.T) {
	// Set up tmp etc dir + AGENT.md file
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	const agentMdMarker = "ABCDE_AGENT_MD_TOKEN_FGHIJ"
	deployContent := "# Deployment-level rules\n- Always respond in Chinese\n- " + agentMdMarker + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpEtc, "AGENT.md"), []byte(deployContent), 0o644))

	withAgentMdViperConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":            true,
		"agent.memory.agent_md.etc_dir":            tmpEtc,
		"agent.memory.agent_md.user_data_dir":      tmpUserData,
		"agent.memory.agent_md.max_per_file_chars": 12288,
		"agent.memory.agent_md.max_total_chars":    51200,
	})

	captured := withCaptureChatFn(t)

	store := newMockStore()
	runner, toolName := newReActRunner(store)
	result, err := runner.Run(context.Background(), RunRequest{
		UserID:    77, // arbitrary non-zero
		SessionID: "test-agentmd",
		Input:     "hello with AGENT.md",
		ToolNames: []string{toolName},
	})
	require.NoError(t, err)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)

	prompt := captured.get()
	require.NotEmpty(t, prompt, "chatFn must have been called and captured a system prompt")

	// P1.1 review fix: explicit Memories section header
	assert.Contains(t, prompt, "## Memories", "system prompt must contain '## Memories' segment header")

	// AGENT.md sub-header
	assert.Contains(t, prompt, "## Agent Rules (developer-defined)",
		"system prompt must contain the AGENT.md cascade sub-header")

	// Deployment-level label injected by loader
	assert.Contains(t, prompt, "[Deployment-level]",
		"system prompt must contain the cascade source label")

	// Verbatim file content reaches the prompt
	assert.Contains(t, prompt, agentMdMarker,
		"system prompt must contain the AGENT.md file content marker")

	// Ordering: ## Memories header appears before the Agent Rules sub-header,
	// which appears before the content marker.
	idxMemories := indexOfFirst(prompt, "## Memories")
	idxRules := indexOfFirst(prompt, "## Agent Rules (developer-defined)")
	idxMarker := indexOfFirst(prompt, agentMdMarker)
	assert.Less(t, idxMemories, idxRules, "Memories header must precede Agent Rules sub-header")
	assert.Less(t, idxRules, idxMarker, "Agent Rules sub-header must precede the content marker")
}

// TestRunner_NoAgentMd_NoMemoriesHeader: when no AGENT.md exists and memory is
// disabled, the Memories section header is omitted entirely (no empty section).
func TestRunner_NoAgentMd_NoMemoriesHeader(t *testing.T) {
	tmpEtc := t.TempDir()
	tmpUserData := t.TempDir()
	withAgentMdViperConfig(t, map[string]any{
		"agent.memory.agent_md.enabled":       true,
		"agent.memory.agent_md.etc_dir":       tmpEtc,
		"agent.memory.agent_md.user_data_dir": tmpUserData,
	})

	captured := withCaptureChatFn(t)
	store := newMockStore()
	runner, toolName := newReActRunner(store)

	result, err := runner.Run(context.Background(), RunRequest{
		UserID:       100,
		SessionID:    "test-empty",
		Input:        "no rules",
		ToolNames:    []string{toolName},
		EnableMemory: false, // memorySystemBlock will be empty too
	})
	require.NoError(t, err)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)

	prompt := captured.get()
	require.NotEmpty(t, prompt)
	assert.NotContains(t, prompt, "## Memories",
		"no AGENT.md + no memory provider → Memories header should be omitted")
}

// indexOfFirst returns the index of the first occurrence of substr in s, or -1.
// Equivalent to strings.Index; tiny helper to keep assertion lines compact.
func indexOfFirst(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
