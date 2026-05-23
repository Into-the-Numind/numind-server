package agent

// runner_e2e_loop_test.go — M-A1: 7 tests covering the new ReAct loop path.
//
// Design principle: these tests exercise the *real* einoAgent.Generate → chatFn path
// (len(einoTools) > 0 branch). They use:
//   1. A staticRegistry stub that returns a single no-op testTool for a known name.
//   2. The chatFn package-level seam (adapter.go:17) to inject controlled LLM responses.
//   3. A mockMemoryProvider for SyncTurn assertion tests.
//
// None of the tests require a live aiservice gateway.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/memory"
	"numind-server/internal/pkg/aiservice"
)

// ── staticRegistry ────────────────────────────────────────────────────────────
// Minimal AgentToolRegistry that holds a fixed map of FullTool by name.

type staticRegistry struct {
	tools map[string]FullTool
}

func newStaticRegistry(tools ...FullTool) *staticRegistry {
	m := make(map[string]FullTool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return &staticRegistry{tools: m}
}

func (r *staticRegistry) RegisterFactory(_ ToolFactory) error { return nil }
func (r *staticRegistry) LoadAll(_ context.Context) error     { return nil }
func (r *staticRegistry) GetTool(name string) (FullTool, bool) {
	t, ok := r.tools[name]
	return t, ok
}
func (r *staticRegistry) ListEnabled(_ context.Context) ([]FullTool, error) {
	out := make([]FullTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out, nil
}
func (r *staticRegistry) ListAllTools() []FullTool {
	out := make([]FullTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// ── loopTestTool ─────────────────────────────────────────────────────────────
// A minimal no-op FullTool used to satisfy react.NewAgent's ≥1 tool requirement.

type loopTestTool struct {
	BaseTool
}

func (t *loopTestTool) Name() string           { return "loop_test_noop" }
func (t *loopTestTool) Description() string    { return "no-op tool for ReAct loop unit tests" }
func (t *loopTestTool) UserFacingName() string { return "noop" }
func (t *loopTestTool) NarrationVerb() string  { return "执行" }
func (t *loopTestTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	return ToolResult(`"ok"`), nil
}

// ── e2eLoopMemoryProvider ─────────────────────────────────────────────────────
// Separate from the mockMemoryProvider in runner_memory_test.go; this variant
// records SyncTurn calls for assertion in the e2e loop tests.

type e2eLoopMemoryProvider struct {
	mu        sync.Mutex
	syncCalls []e2eSyncTurnArgs
	syncErr   error
}

type e2eSyncTurnArgs struct {
	userID     uint
	agentDefID uint64
	sessionID  string
	userMsg    memory.Message
	asstMsg    memory.Message
}

func (m *e2eLoopMemoryProvider) SystemPromptBlock(_ context.Context, _ uint, _ uint64, _ string) (string, error) {
	return "", nil
}
func (m *e2eLoopMemoryProvider) Prefetch(_ context.Context, _ uint, _ uint64, _ string) ([]memory.MemoryItem, error) {
	return nil, nil
}
func (m *e2eLoopMemoryProvider) SyncTurn(_ context.Context, userID uint, agentDefID uint64, sessionID string, userMsg, asstMsg memory.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncCalls = append(m.syncCalls, e2eSyncTurnArgs{userID, agentDefID, sessionID, userMsg, asstMsg})
	return m.syncErr
}
func (m *e2eLoopMemoryProvider) OnPreCompress(_ context.Context, _ uint, _ uint64, _ []memory.Message) error {
	return nil
}
func (m *e2eLoopMemoryProvider) Clear(_ context.Context, _ uint) error { return nil }

func (m *e2eLoopMemoryProvider) syncCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.syncCalls)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// withMockChatFn replaces chatFn for the duration of t and restores it on cleanup.
func withMockChatFn(t *testing.T, fn func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) {
	t.Helper()
	orig := chatFn
	t.Cleanup(func() { chatFn = orig })
	chatFn = fn
}

// successChatFn returns a chatFn mock that always returns content as a final answer (no tool calls).
func successChatFn(content string) func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content:      content,
			Model:        "test-model",
			Provider:     "test",
			FinishReason: "stop",
			Usage:        aiservice.TokenUsage{PromptTokens: 10, CompletionTokens: 5},
		}, nil
	}
}

// newReActRunner builds an AgentRunner with a staticRegistry containing a single loopTestTool.
// callers must also mock chatFn (via withMockChatFn) before calling runner.Run.
func newReActRunner(store *mockAgentRunStore, opts ...RunnerOption) (AgentRunner, string) {
	tool := &loopTestTool{}
	reg := newStaticRegistry(tool)
	runner := NewAgentRunner(store, reg, opts...)
	return runner, tool.Name()
}

// newReActRequest returns a RunRequest that will resolve the loopTestTool from the registry.
func newReActRequest(toolName, input string) RunRequest {
	return RunRequest{
		UserID:    1,
		SessionID: "test-session",
		Input:     input,
		ToolNames: []string{toolName},
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestRunner_RealReAct_HappyPath verifies that the real ReAct loop produces
// TerminalCompleted with the LLM's content as FinalOutput when chatFn returns
// a final answer (no tool calls).
func TestRunner_RealReAct_HappyPath(t *testing.T) {
	withMockChatFn(t, successChatFn("final answer"))

	store := newMockStore()
	runner, toolName := newReActRunner(store)

	result, err := runner.Run(context.Background(), newReActRequest(toolName, "hello"))
	require.NoError(t, err)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	assert.Equal(t, "final answer", result.FinalOutput)
	assert.NotZero(t, result.AgentRunID)
	assert.Greater(t, result.Duration, time.Duration(0))

	// Verify DB was written correctly.
	got, dbErr := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, dbErr)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, string(TerminalCompleted), got.StateReason)
	require.NotNil(t, got.EndedAt)
}

// TestRunner_PTL_TerminatesFast verifies the V1.5 compact-v1-removal behavior:
// a prompt_too_long error from the LLM terminates the run with
// TerminalPromptTooLong (no retry). V1's collapse+reactive_compact recovery
// chain was removed because compactv2's L3 autocompact at 85% threshold +
// L4 hard limit at 95% prevent this case in practice; if PTL still fires it
// means token estimation is severely off and retrying with the same
// estimation won't help.
func TestRunner_PTL_TerminatesFast(t *testing.T) {
	callCount := 0
	orig := chatFn
	t.Cleanup(func() { chatFn = orig })
	chatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callCount++
		return nil, errors.New("prompt_too_long: context window exceeded")
	}

	store := newMockStore()
	runner, toolName := newReActRunner(store)

	result, err := runner.Run(context.Background(), newReActRequest(toolName, "long input"))
	require.NoError(t, err)
	assert.Equal(t, TerminalPromptTooLong, result.TerminalReason,
		"PTL error should terminate run directly (no V1 retry chain)")
	assert.Equal(t, 1, callCount, "chatFn should be called exactly once (no retry)")
}

// TestRunner_MaxOutput_TerminatesFast verifies that a max_output error
// terminates with TerminalErrorMaxBudget. V1's escalation chain was removed
// — profile config should set adequate max_tokens upstream in DB Registry.
func TestRunner_MaxOutput_TerminatesFast(t *testing.T) {
	callCount := 0
	orig := chatFn
	t.Cleanup(func() { chatFn = orig })
	chatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callCount++
		return nil, errors.New("max_output_tokens: response was truncated")
	}

	store := newMockStore()
	runner, toolName := newReActRunner(store)

	result, err := runner.Run(context.Background(), newReActRequest(toolName, "generate something long"))
	require.NoError(t, err)
	assert.Equal(t, TerminalErrorMaxBudget, result.TerminalReason,
		"max_output error should terminate run directly (no V1 escalation chain)")
	assert.Equal(t, 1, callCount, "chatFn should be called exactly once (no retry)")
}

// TestRunner_UnrecoverableError verifies that a context.DeadlineExceeded error
// (non-PTL, non-max-output) terminates the run with a non-Completed terminal reason.
func TestRunner_UnrecoverableError(t *testing.T) {
	withMockChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, context.DeadlineExceeded
	})

	store := newMockStore()
	runner, toolName := newReActRunner(store)

	result, err := runner.Run(context.Background(), newReActRequest(toolName, "will timeout"))
	// Run must not return a Go-level error for unrecoverable LLM errors; the terminal
	// reason communicates the outcome instead.
	require.NoError(t, err)
	assert.NotEqual(t, TerminalCompleted, result.TerminalReason,
		"context.DeadlineExceeded should produce a terminal reason other than Completed")
	assert.NotEmpty(t, string(result.TerminalReason))

	// Verify DB state is "terminated" with a non-empty reason.
	got, dbErr := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, dbErr)
	assert.Equal(t, "terminated", got.Status)
	assert.NotEmpty(t, got.StateReason)
}

// TestRunner_HookActionStop_Propagates verifies that a HookActionStop pre-seeded in
// the registry before Run overrides the LLM-derived terminal reason.
func TestRunner_HookActionStop_Propagates(t *testing.T) {
	// chatFn returns success — we want to confirm the hook override wins.
	withMockChatFn(t, successChatFn("would be Completed"))

	reg := NewHookActionRegistry()
	reg.Record(HookActionStop)
	hooks := &RunHooks{Registry: reg}

	store := newMockStore()
	runner, toolName := newReActRunner(store)

	result, err := runner.Run(context.Background(), RunRequest{
		UserID:    1,
		SessionID: "test-session",
		Input:     "test hook stop",
		ToolNames: []string{toolName},
		Hooks:     hooks,
	})
	require.NoError(t, err)
	assert.Equal(t, TerminalHookStopped, result.TerminalReason,
		"pre-seeded HookActionStop should override LLM TerminalCompleted")

	got, dbErr := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, dbErr)
	assert.Equal(t, string(TerminalHookStopped), got.StateReason)
}

// TestRunner_SyncTurn_AsyncCalled_OnSuccess verifies that memoryProvider.SyncTurn
// is called (asynchronously) after a successful run.
func TestRunner_SyncTurn_AsyncCalled_OnSuccess(t *testing.T) {
	withMockChatFn(t, successChatFn("memory answer"))

	memProv := &e2eLoopMemoryProvider{}

	store := newMockStore()
	runner, toolName := newReActRunner(store, WithMemoryProvider(memProv))

	result, err := runner.Run(context.Background(), newReActRequest(toolName, "remember this"))
	require.NoError(t, err)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)

	// SyncTurn is called in a goroutine; poll briefly to let it complete.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if memProv.syncCallCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	assert.Equal(t, 1, memProv.syncCallCount(),
		"SyncTurn should have been called exactly once after successful completion")

	// Verify the call carried the correct messages.
	memProv.mu.Lock()
	defer memProv.mu.Unlock()
	require.Len(t, memProv.syncCalls, 1)
	call := memProv.syncCalls[0]
	assert.Equal(t, "remember this", call.userMsg.Content)
	assert.Equal(t, "memory answer", call.asstMsg.Content)
}

// TestRunner_SyncTurn_NotCalled_OnFailure verifies that memoryProvider.SyncTurn
// is NOT called when the run terminates with a non-Completed reason.
func TestRunner_SyncTurn_NotCalled_OnFailure(t *testing.T) {
	withMockChatFn(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, errors.New("model_error: internal server error")
	})

	memProv := &e2eLoopMemoryProvider{}

	store := newMockStore()
	runner, toolName := newReActRunner(store, WithMemoryProvider(memProv))

	result, err := runner.Run(context.Background(), newReActRequest(toolName, "will fail"))
	require.NoError(t, err)
	assert.NotEqual(t, TerminalCompleted, result.TerminalReason,
		"model error should produce non-Completed terminal reason")

	// Give the goroutine time to fire if it incorrectly runs.
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 0, memProv.syncCallCount(),
		"SyncTurn must NOT be called when run is not TerminalCompleted")
}
