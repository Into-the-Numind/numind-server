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
