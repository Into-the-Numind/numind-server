package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/model"
)

// mockAgentRunStore 是 IAgentRunStore 的 in-memory mock。
type mockAgentRunStore struct {
	mu   sync.Mutex
	runs map[uint64]*model.AgentRun
	seq  uint64
}

func newMockStore() *mockAgentRunStore {
	return &mockAgentRunStore{runs: make(map[uint64]*model.AgentRun)}
}

func (m *mockAgentRunStore) Create(_ context.Context, run *model.AgentRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	run.ID = m.seq
	m.runs[run.ID] = run
	return nil
}

func (m *mockAgentRunStore) Get(_ context.Context, id uint64) (*model.AgentRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[id]; ok {
		return r, nil
	}
	return nil, errors.New("not found")
}

func (m *mockAgentRunStore) UpdateState(_ context.Context, id uint64, status, reason string, endedAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[id]; ok {
		r.Status = status
		r.StateReason = reason
		r.EndedAt = endedAt
		return nil
	}
	return errors.New("not found")
}

func (m *mockAgentRunStore) WriteTurn(_ context.Context, id uint64, messages json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[id]; ok {
		r.Messages = []byte(messages)
		return nil
	}
	return errors.New("not found")
}

func (m *mockAgentRunStore) ListBySession(_ context.Context, _ string, _, _ int) ([]model.AgentRun, int64, error) {
	return nil, 0, nil
}

// UpdateTerminalMetadata — #12 agent-mode-billing-integration mock impl
func (m *mockAgentRunStore) UpdateTerminalMetadata(_ context.Context, id uint64, metadata datatypes.JSON) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[id]; ok {
		r.TerminalMetadata = metadata
		return nil
	}
	return errors.New("not found")
}

// SetCancellationRequested — M-C3b admin cancel mock impl
func (m *mockAgentRunStore) SetCancellationRequested(_ context.Context, id uint64, metadata datatypes.JSON) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return errors.New("not found")
	}
	now := time.Now()
	r.CancellationRequestedAt = &now
	r.TerminalMetadata = metadata
	return nil
}

// ListByParentUserIDAndStatus — M-C4a admin listing mock impl
func (m *mockAgentRunStore) ListByParentUserIDAndStatus(_ context.Context, _ uint, _ string, _, _ int) ([]model.AgentRun, int64, error) {
	return nil, 0, nil
}

// ---

func TestAgentRunner_Run_Basic(t *testing.T) {
	store := newMockStore()
	runner := NewAgentRunner(store, nil)
	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1,
		Input:  "hello",
	})
	require.NoError(t, err)
	assert.NotZero(t, result.AgentRunID)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	assert.Contains(t, result.FinalOutput, "hello")

	// 验证 DB 行已写
	got, err := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, "terminated", got.Status)
	assert.Equal(t, string(TerminalCompleted), got.StateReason)
	require.NotNil(t, got.EndedAt)
}

func TestAgentRunner_Cancel_NotFound(t *testing.T) {
	runner := NewAgentRunner(newMockStore(), nil)
	if runner.Cancel(99999) {
		t.Error("Cancel for non-existent runID should return false")
	}
}

func TestAgentRunner_Cancel_AfterRun(t *testing.T) {
	runner := NewAgentRunner(newMockStore(), nil)
	result, err := runner.Run(context.Background(), RunRequest{UserID: 1, Input: "x"})
	require.NoError(t, err)
	// Run 完成后 unregisterCancel 会清空 registry，Cancel 应返回 false
	if runner.Cancel(result.AgentRunID) {
		t.Error("Cancel after Run finished should return false")
	}
}

// 并发 Cancel 测试 race detector 干净。
func TestAgentRunner_ConcurrentCancel(t *testing.T) {
	r := NewAgentRunner(newMockStore(), nil).(*agentRunner)
	var wg sync.WaitGroup
	for i := uint64(1); i <= 50; i++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			_, cancel := context.WithCancel(context.Background())
			r.registerCancel(id, cancel)
			r.Cancel(id)
		}(i)
	}
	wg.Wait()
}

func TestAgentRunner_Run_SetsSessionID(t *testing.T) {
	store := newMockStore()
	runner := NewAgentRunner(store, nil)
	result, err := runner.Run(context.Background(), RunRequest{
		UserID:    7,
		SessionID: "sess-123",
		Input:     "ping",
	})
	require.NoError(t, err)
	got, err := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, "sess-123", got.SessionID)
	assert.EqualValues(t, 7, got.UserID)
}

// ============================================================================
// #4 sandbox-integration: ctx WithRunID injection + WithDefaultHooks option
// ============================================================================

func TestNewAgentRunner_NoOptions_DefaultHooksNil(t *testing.T) {
	r := NewAgentRunner(newMockStore(), nil).(*agentRunner)
	if r.defaultHooks != nil {
		t.Errorf("default runner should have nil defaultHooks")
	}
}

func TestNewAgentRunner_WithDefaultHooks(t *testing.T) {
	hooks := &RunHooks{}
	r := NewAgentRunner(newMockStore(), nil, WithDefaultHooks(hooks)).(*agentRunner)
	if r.defaultHooks != hooks {
		t.Errorf("WithDefaultHooks should store the supplied hooks")
	}
}

func TestNewAgentRunner_MultipleOptionsLastWins(t *testing.T) {
	h1 := &RunHooks{}
	h2 := &RunHooks{}
	r := NewAgentRunner(newMockStore(), nil,
		WithDefaultHooks(h1),
		WithDefaultHooks(h2),
	).(*agentRunner)
	if r.defaultHooks != h2 {
		t.Errorf("multiple WithDefaultHooks: last value should win")
	}
}

// ── M10: Registry auto-inject + TerminalReason propagation tests ──────────────

func TestRunner_Run_autoInjectsRegistry(t *testing.T) {
	// Supply hooks without a Registry; Run() must auto-inject one.
	hooks := &RunHooks{} // Registry intentionally nil before Run
	assert.Nil(t, hooks.Registry, "before Run, Registry should be nil")

	runner := NewAgentRunner(newMockStore(), nil, WithDefaultHooks(hooks))
	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1,
		Input:  "test",
	})
	require.NoError(t, err)
	assert.NotZero(t, result.AgentRunID)
	// Run() must have auto-injected a non-nil Registry into effectiveHooks.
	// Because effectiveHooks == hooks (req.Hooks is nil, so defaultHooks is used),
	// the injection is visible through the same pointer.
	assert.NotNil(t, hooks.Registry, "Run() should auto-inject Registry when nil")
}

func TestRunner_Run_preservesProvidedRegistry(t *testing.T) {
	// Caller provides their own Registry — Run() must not overwrite it.
	providedReg := NewHookActionRegistry()
	providedReg.Record(HookActionStop) // mark it so we can detect identity

	hooks := &RunHooks{
		Registry: providedReg,
	}
	runner := NewAgentRunner(newMockStore(), nil)
	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1,
		Input:  "test",
		Hooks:  hooks,
	})
	require.NoError(t, err)
	assert.NotZero(t, result.AgentRunID)
	// The provided registry must still be the same object (not replaced).
	assert.Same(t, providedReg, hooks.Registry, "caller-provided Registry must not be overwritten")
}

func TestRunner_Run_RegistryStopPropagatesToTerminalReason(t *testing.T) {
	// Pre-record HookActionStop before Run executes; Run() should read it and
	// produce TerminalHookStopped instead of TerminalCompleted.
	reg := NewHookActionRegistry()
	hooks := &RunHooks{
		Registry: reg,
		// PreToolCall fires during InvokableRun (tool execution), which doesn't happen
		// in the #2 skeleton. Instead we pre-seed the registry before Run().
	}
	// Pre-seed: simulate that a hook recorded Stop during a prior tool call.
	reg.Record(HookActionStop)

	store := newMockStore()
	runner := NewAgentRunner(store, nil)
	result, err := runner.Run(context.Background(), RunRequest{
		UserID: 1,
		Input:  "test",
		Hooks:  hooks,
	})
	require.NoError(t, err)
	assert.Equal(t, TerminalHookStopped, result.TerminalReason,
		"Registry.Record(HookActionStop) before Run should produce TerminalHookStopped")

	// Verify DB was written with the correct reason.
	got, err := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, string(TerminalHookStopped), got.StateReason)
}

// ---------------------------------------------------------------------------
// M11 (#5 skill-system) — Runner Skill 注入测试
// ---------------------------------------------------------------------------

// mockSkillStore is an inline mock of store.IAgentDefinitionStore for M11 tests.
// Only GetByIDIncludeInactive is used in runner.Run; other methods stub to nil.
type mockSkillStore struct {
	fixed *model.AgentDefinition
	err   error
}

func (m *mockSkillStore) Create(_ context.Context, _ *model.AgentDefinition) error { return nil }
func (m *mockSkillStore) CreateTx(_ context.Context, _ interface{}, _ *model.AgentDefinition) error {
	return nil
}
func (m *mockSkillStore) GetByID(_ context.Context, _ uint64) (*model.AgentDefinition, error) {
	return nil, nil
}
func (m *mockSkillStore) GetByIDIncludeInactive(_ context.Context, id uint64) (*model.AgentDefinition, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.fixed == nil {
		return nil, errors.New("not found")
	}
	return m.fixed, nil
}
func (m *mockSkillStore) ListByParent(_ context.Context, _ uint, _ bool, _, _ int) ([]model.AgentDefinition, int64, error) {
	return nil, 0, nil
}
func (m *mockSkillStore) Update(_ context.Context, _ *model.AgentDefinition) error { return nil }
func (m *mockSkillStore) SoftDelete(_ context.Context, _ uint64) error             { return nil }
func (m *mockSkillStore) WriteHistory(_ context.Context, _ *model.AgentDefinitionHistory) error {
	return nil
}
func (m *mockSkillStore) ListHistory(_ context.Context, _ uint64) ([]model.AgentDefinitionHistory, error) {
	return nil, nil
}
func (m *mockSkillStore) GetHistoryByVersion(_ context.Context, _ uint64, _ uint) (*model.AgentDefinitionHistory, error) {
	return nil, nil
}
func (m *mockSkillStore) MaxVersion(_ context.Context, _ uint64) (uint, error) { return 0, nil }

func TestRunner_AgentDefinitionID0_fallThroughMock(t *testing.T) {
	runner := NewAgentRunner(newMockStore(), nil)
	result, err := runner.Run(context.Background(), RunRequest{
		UserID:            1,
		Input:             "test",
		AgentDefinitionID: 0, // explicit fall through
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.SkillVersion, "fall through should yield SkillVersion=0")
}

func TestRunner_AgentDefinitionID_skillStoreNil_fallsThrough(t *testing.T) {
	// Even with AgentDefinitionID > 0, if WithSkillStore was not wired,
	// runner.Run must not panic and fall through to mock behaviour.
	runner := NewAgentRunner(newMockStore(), nil) // no WithSkillStore
	result, err := runner.Run(context.Background(), RunRequest{
		UserID:            1,
		Input:             "test",
		AgentDefinitionID: 42, // ignored because skillStore is nil
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.SkillVersion)
}

func TestRunner_RunResult_SkillVersion_zeroWhenFallThrough(t *testing.T) {
	runner := NewAgentRunner(newMockStore(), nil)
	result, err := runner.Run(context.Background(), RunRequest{UserID: 1, Input: "x"})
	require.NoError(t, err)
	assert.Equal(t, 0, result.SkillVersion)
}

// ── #8 narration-layer integration ──────────────────────────────────────────

// runnerNarrationYAML — minimal yaml for runner-level narration fixture.
const runnerNarrationYAML = `
tools: {}
defaults:
  verb: "正在处理"
  use_template: "{{ .verb }}"
  result_template: "处理完成"
  error_template: "失败"
  rejected_template: "拦截"
`

func TestRunner_WithNarrationProvider_AttachesAndDefersCloseRun(t *testing.T) {
	// Spec §10.6: real Provider; verify channel-close after Run completes
	// (proof CloseRun defer fired). Do NOT replace with a mock Provider that
	// captures CloseRun separately — the real-channel-close path is the
	// integration contract being verified.
	prov, err := narration.NewProvider(narration.Config{
		YAMLBytes:  []byte(runnerNarrationYAML),
		BufferSize: 8,
	})
	require.NoError(t, err)

	runner := NewAgentRunner(newMockStore(), nil, WithNarrationProvider(prov))

	// Subscribe BEFORE Run to avoid racing the lazy channel creation.
	// mockAgentRunStore assigns IDs starting at 1.
	ch, cleanup := prov.Subscribe(1)
	defer cleanup()

	_, runErr := runner.Run(context.Background(), RunRequest{UserID: 1, SessionID: "test", Input: "hello"})
	// runErr is acceptable here (mock runner with no tools / no LLM); we care
	// only about the CloseRun side-effect.
	_ = runErr

	// After Run completes, the per-runID channel MUST be closed (CloseRun
	// defer fired). recv on closed channel returns immediately with ok=false.
	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel should be closed after Run completes (CloseRun defer fired)")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel did not close within 100ms — CloseRun defer not fired")
	}
}

func TestRunner_WithoutNarrationProvider_NoOp(t *testing.T) {
	// Sanity: when narration is not wired, Run completes normally
	// (no panic from defer, no nil-deref).
	runner := NewAgentRunner(newMockStore(), nil)
	_, runErr := runner.Run(context.Background(), RunRequest{UserID: 1, SessionID: "test", Input: "hello"})
	_ = runErr
}
