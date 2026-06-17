package budgetgate

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/budget"
)

// mockTool implements einotool.BaseTool for tests.
type mockTool struct {
	name string
}

func (m *mockTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: m.name}, nil
}

// mockRunStore captures UpdateTerminalMetadata calls.
type mockRunStore struct {
	mu    sync.Mutex
	calls []mockRunStoreCall
}

type mockRunStoreCall struct {
	ID       uint64
	Metadata datatypes.JSON
}

func (m *mockRunStore) UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mockRunStoreCall{ID: id, Metadata: metadata})
	return nil
}

func TestWrapHooks_PreToolCall_AllowForwardsToBase(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 1, 1, budget.DefaultLimits())
	defer tr.Close(1)

	var baseCalled atomic.Bool
	base := &agent.RunHooks{
		PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
			baseCalled.Store(true)
			return agent.HookActionContinue, nil
		},
	}
	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(base)

	ctx := agent.WithRunID(context.Background(), 1)
	action, err := wrapped.PreToolCall(ctx, &mockTool{name: "t"}, "{}")
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)
	assert.True(t, baseCalled.Load(), "base should be called when budget allows")
}

func TestWrapHooks_PreToolCall_ExceededShortCircuits(t *testing.T) {
	tr := budget.NewTracker(nil)
	// Limits with MaxTurns=1 → CanProceed exceeded after 1 RecordStep
	tr.Start(context.Background(), 1, 1, budget.Limits{MaxTurns: 1, MaxWallTime: time.Hour, MaxDailyCredits: 10000})
	defer tr.Close(1)
	tr.RecordStep(context.Background(), 1)

	var baseCalled atomic.Bool
	reg := agent.NewHookActionRegistry()
	base := &agent.RunHooks{
		PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
			baseCalled.Store(true)
			return agent.HookActionContinue, nil
		},
		Registry: reg,
	}
	rs := &mockRunStore{}
	g := NewBudgetGate(tr, nil, rs)
	wrapped := g.WrapHooks(base)

	ctx := agent.WithRunID(context.Background(), 1)
	action, err := wrapped.PreToolCall(ctx, &mockTool{name: "t"}, "{}")
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionBudgetExceeded, action)
	assert.False(t, baseCalled.Load(), "base should NOT be called when budget exceeded")
	assert.Equal(t, agent.HookActionBudgetExceeded, reg.LastAction())

	// Wait for async writeTerminalMetadata
	time.Sleep(50 * time.Millisecond)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	require.Len(t, rs.calls, 1)
	assert.Equal(t, uint64(1), rs.calls[0].ID)
	assert.Contains(t, string(rs.calls[0].Metadata), "max_turns")
}

func TestWrapHooks_PreToolCall_RunIDZeroFailOpen(t *testing.T) {
	tr := budget.NewTracker(nil)
	var baseCalled atomic.Bool
	base := &agent.RunHooks{
		PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
			baseCalled.Store(true)
			return agent.HookActionContinue, nil
		},
	}
	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(base)

	// No runID in ctx → fall through to base
	action, err := wrapped.PreToolCall(context.Background(), &mockTool{name: "t"}, "{}")
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)
	assert.True(t, baseCalled.Load())
}

func TestWrapHooks_PostToolCall_ForwardsToBaseFirst(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 1, 1, budget.DefaultLimits())
	defer tr.Close(1)

	callOrder := []string{}
	var orderMu sync.Mutex
	base := &agent.RunHooks{
		PostToolCall: func(ctx context.Context, t einotool.BaseTool, output string, err error) (agent.HookAction, error) {
			orderMu.Lock()
			callOrder = append(callOrder, "base")
			orderMu.Unlock()
			return agent.HookActionContinue, nil
		},
	}
	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(base)

	ctx := agent.WithRunID(context.Background(), 1)
	action, err := wrapped.PostToolCall(ctx, &mockTool{name: "t"}, `{"usage":{"total_tokens":42}}`, nil)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)
	assert.Equal(t, []string{"base"}, callOrder)

	s := tr.Snapshot(context.Background(), 1)
	// units fix: 42 tokens → ceil(42/500) = 1 credit.
	assert.Equal(t, int64(1), s.Credits, "output usage tokens are ratio-converted to credits before recording")
}

func TestWrapHooks_PostToolCall_NilBase(t *testing.T) {
	g := NewBudgetGate(budget.NewTracker(nil), nil, nil)
	wrapped := g.WrapHooks(nil)
	action, err := wrapped.PostToolCall(context.Background(), &mockTool{name: "t"}, "", nil)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)
}

func TestWrapHooks_PreservesRegistry(t *testing.T) {
	// T3 (#5): NarrationRunID field removed; the per-run id now flows through
	// ctx (RunIDFromContext), so it is not copied through the wrapper. Registry
	// is still copied and must survive the wrap.
	reg := agent.NewHookActionRegistry()
	base := &agent.RunHooks{
		Registry: reg,
	}
	g := NewBudgetGate(budget.NewTracker(nil), nil, nil)
	wrapped := g.WrapHooks(base)
	assert.Same(t, reg, wrapped.Registry, "Registry preserved")
}

func TestWrapHooks_PostToolCall_RunStoreNil_NoCrash(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 1, 1, budget.Limits{MaxTurns: 1, MaxWallTime: time.Hour, MaxDailyCredits: 10000})
	defer tr.Close(1)
	tr.RecordStep(context.Background(), 1)

	g := NewBudgetGate(tr, nil, nil) // runStore == nil
	wrapped := g.WrapHooks(&agent.RunHooks{Registry: agent.NewHookActionRegistry()})
	ctx := agent.WithRunID(context.Background(), 1)
	action, err := wrapped.PreToolCall(ctx, &mockTool{name: "t"}, "{}")
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionBudgetExceeded, action)
	// Async goroutine should not panic with nil store
	time.Sleep(20 * time.Millisecond)
}

func TestWrapHooks_ConcurrentPre(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 1, 1, budget.DefaultLimits())
	defer tr.Close(1)
	base := &agent.RunHooks{Registry: agent.NewHookActionRegistry()}
	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(base)

	var wg sync.WaitGroup
	ctx := agent.WithRunID(context.Background(), 1)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := wrapped.PreToolCall(ctx, &mockTool{name: fmt.Sprintf("t-%d", time.Now().UnixNano())}, "{}")
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}
