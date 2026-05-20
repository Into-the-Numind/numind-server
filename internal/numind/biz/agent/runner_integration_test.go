package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunnerIntegration_FullHappyPath: Run 完整流程 — mock store — DB 写入验证。
func TestRunnerIntegration_FullHappyPath(t *testing.T) {
	store := newMockStore()
	runner := NewAgentRunner(store, nil)

	result, err := runner.Run(context.Background(), RunRequest{
		UserID:    42,
		SessionID: "integration-1",
		Input:     "What is 2 + 2?",
	})
	require.NoError(t, err)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	assert.NotZero(t, result.AgentRunID)

	// 验证 DB 流程：Create → WriteTurn → UpdateState
	run, err := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, err)
	assert.Equal(t, "terminated", run.Status)
	assert.Equal(t, string(TerminalCompleted), run.StateReason)
	assert.NotNil(t, run.EndedAt)
	assert.NotEmpty(t, run.Messages)

	// messages JSON 应含 user input
	var msgs []map[string]any
	require.NoError(t, json.Unmarshal(run.Messages, &msgs))
	assert.GreaterOrEqual(t, len(msgs), 1)
}

// TestRunnerIntegration_StateMachineFullCoverage: 遍历所有 LoopEvent，验证每个 isTerminal 结果。
func TestRunnerIntegration_StateMachineFullCoverage(t *testing.T) {
	// 根据 state.go Transition 逻辑：
	// PTL / MaxOutput 在首次 retry（retries==1）时是 Continue，不是 Terminal。
	// PTLRetries > MaxPTLRetries(2) 时才 Terminal；但该测试只触发一次，所以是 Continue。
	events := map[LoopEvent]bool{
		LoopEventLLMOKWithToolCall:   false, // → ContinueNextTurn
		LoopEventLLMOKNoToolCall:     true,  // → TerminalCompleted
		LoopEventLLMErrPTL:           false, // PTLRetries==1 → Continue
		LoopEventLLMErrMaxOutput:     false, // MaxOutputRetries==1 → Continue
		LoopEventLLMErrModel:         true,  // → TerminalModelError
		LoopEventLLMErrImage:         true,  // → TerminalImageError
		LoopEventToolErr:             true,  // → TerminalAbortedTools
		LoopEventCtxCanceled:         true,  // → TerminalAbortedStreaming
		LoopEventMaxStepsReached:     true,  // → TerminalMaxTurns
		LoopEventBlockingLimitHit:    true,  // → TerminalBlockingLimit
		LoopEventMaxRetriesReached:   true,  // → TerminalErrorMaxRetries
		LoopEventHookActionStop:      true,  // → TerminalHookStopped
		LoopEventHookActionBlockStop: true,  // → TerminalStopHookPrevented
		LoopEventHookActionBlocking:  false, // → ContinueStopHookBlocking
		LoopEventTokenBudgetContinue: false, // → ContinueTokenBudgetContinue
		LoopEventCollapseDrainRetry:  false, // → ContinueCollapseDrainRetry
		LoopEventMaxOutputEscalate:   false, // → ContinueMaxOutputEscalate
	}
	for event, expectedTerminal := range events {
		s := &LoopState{}
		_, _, isTerm := s.Transition(event)
		if isTerm != expectedTerminal {
			t.Errorf("event %v: isTerminal got %v, want %v", event, isTerm, expectedTerminal)
		}
	}
}

// TestRunnerIntegration_PTLEscalationToTerminal: PTL 重试超限后终止。
func TestRunnerIntegration_PTLEscalationToTerminal(t *testing.T) {
	s := &LoopState{}
	// 首次 PTL → continue
	_, _, isTerm := s.Transition(LoopEventLLMErrPTL)
	assert.False(t, isTerm, "first PTL should continue")
	// 第二次 PTL → continue (reactive_compact)
	_, _, isTerm = s.Transition(LoopEventLLMErrPTL)
	assert.False(t, isTerm, "second PTL should continue")
	// 第三次 PTL → terminal (PTLRetries > MaxPTLRetries=2)
	term, _, isTerm := s.Transition(LoopEventLLMErrPTL)
	assert.True(t, isTerm, "third PTL should terminate")
	assert.Equal(t, TerminalPromptTooLong, term)
}

// TestRunnerIntegration_AbortViaContext: 父 ctx cancel 透传到 runner，不 panic，2s 内返回。
func TestRunnerIntegration_AbortViaContext(t *testing.T) {
	store := newMockStore()
	runner := NewAgentRunner(store, nil)
	parentCtx, parentCancel := context.WithCancel(context.Background())

	done := make(chan *RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := runner.Run(parentCtx, RunRequest{UserID: 1, Input: "test"})
		if err != nil {
			errCh <- err
			return
		}
		done <- result
	}()

	// 立即 cancel；skeleton 完成很快，cancel 可能 race — 两种结果都接受
	parentCancel()

	select {
	case <-done:
		// ok: skeleton 完成得太快 cancel 没赶上
	case <-errCh:
		// ok: skeleton 在 ctx done 时返回 error
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after parent cancel")
	}
}

// TestRunnerIntegration_ConcurrentRuns: 20 个并发 Run，每个结果独立，race detector 干净。
func TestRunnerIntegration_ConcurrentRuns(t *testing.T) {
	store := newMockStore()
	runner := NewAgentRunner(store, nil)

	var wg sync.WaitGroup
	results := make([]*RunResult, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, err := runner.Run(context.Background(), RunRequest{
				UserID:    uint(idx + 100),
				SessionID: fmt.Sprintf("concurrent-%d", idx),
				Input:     fmt.Sprintf("input-%d", idx),
			})
			if err != nil {
				t.Errorf("run %d: %v", idx, err)
				return
			}
			results[idx] = r
		}(i)
	}
	wg.Wait()

	// 每个 run 都有独立 ID + Completed status
	seen := make(map[uint64]bool)
	for i, r := range results {
		require.NotNil(t, r, "run %d nil result", i)
		require.False(t, seen[r.AgentRunID], "duplicate AgentRunID %d", r.AgentRunID)
		seen[r.AgentRunID] = true
		assert.Equal(t, TerminalCompleted, r.TerminalReason)
	}
}

// TestRunnerIntegration_AbortControllerThreeLayer: 三层 ctx 派生 + cancel 级联验证。
func TestRunnerIntegration_AbortControllerThreeLayer(t *testing.T) {
	queryCtx, queryCancel := DeriveQueryCtx(context.Background())
	batchCtx, batchCancel := DeriveBatchCtx(queryCtx)
	defer batchCancel()
	toolCtx, toolCancel := DeriveToolCtx(batchCtx)
	defer toolCancel()

	done := make(chan struct{})
	go func() {
		<-toolCtx.Done()
		close(done)
	}()

	queryCancel()
	select {
	case <-done:
		// expected: queryCancel 级联到 toolCtx
	case <-time.After(100 * time.Millisecond):
		t.Fatal("queryCancel did not propagate three layers")
	}
}

// TestRunnerIntegration_WithholdPriority: PTL 优先级高于 MaxOutput。
func TestRunnerIntegration_WithholdPriority(t *testing.T) {
	err := errors.New("prompt_too_long: also max_tokens reached")
	event := HandleLLMError(&LoopState{}, err)
	assert.Equal(t, LoopEventLLMErrPTL, event, "PTL must win over MaxOutput")
}

// TestRunnerIntegration_HookActionsMapping: RunHooks 的 HookAction → LoopEvent → TerminalReason 映射。
func TestRunnerIntegration_HookActionsMapping(t *testing.T) {
	cases := []struct {
		action       HookAction
		wantEvent    LoopEvent
		wantTerminal TerminalReason
	}{
		{HookActionStop, LoopEventHookActionStop, TerminalHookStopped},
		{HookActionBlockingStop, LoopEventHookActionBlockStop, TerminalStopHookPrevented},
		{HookActionPermissionDeny, LoopEventPermissionDenied, TerminalPermissionDenied}, // #6
	}
	for _, c := range cases {
		event := HookActionToLoopEvent(c.action)
		assert.Equal(t, c.wantEvent, event)

		s := &LoopState{}
		term, _, isTerm := s.Transition(event)
		assert.True(t, isTerm, "action %v should terminate", c.action)
		assert.Equal(t, c.wantTerminal, term)
	}
}

// ── M11: Permission deny propagation (runner-level) ───────────────────────────

// TestRunnerIntegration_PermissionDeny_TerminalReason verifies that when a hook
// records HookActionPermissionDeny on the Registry, runner.Run propagates it to
// TerminalPermissionDenied via state.Transition (no actual wrapper / sink involved).
func TestRunnerIntegration_PermissionDeny_TerminalReason(t *testing.T) {
	reg := NewHookActionRegistry()
	reg.Record(HookActionPermissionDeny)

	hooks := &RunHooks{Registry: reg}
	runner := NewAgentRunner(newMockStore(), nil, WithDefaultHooks(hooks))

	result, err := runner.Run(context.Background(), RunRequest{UserID: 1, Input: "x"})
	require.NoError(t, err)
	assert.Equal(t, TerminalPermissionDenied, result.TerminalReason,
		"HookActionPermissionDeny in Registry should propagate to TerminalPermissionDenied")
	// PermissionDenial is nil because no wrapper invoked the sink (skeleton hooks unused).
	assert.Nil(t, result.PermissionDenial,
		"PermissionDenial expected nil when sink unused")
}

// TestRunnerIntegration_DefaultRun_NoPermissionDenial verifies that a Run with
// no hook denial keeps PermissionDenial as nil (backward compat / regression).
func TestRunnerIntegration_DefaultRun_NoPermissionDenial(t *testing.T) {
	runner := NewAgentRunner(newMockStore(), nil)
	result, err := runner.Run(context.Background(), RunRequest{UserID: 1, Input: "x"})
	require.NoError(t, err)
	assert.Equal(t, TerminalCompleted, result.TerminalReason)
	assert.Nil(t, result.PermissionDenial)
}

// TestRunnerIntegration_PermissionDenial_Field verifies that RunResult struct
// has the PermissionDenial field properly typed and nil-by-default.
//
// End-to-end "wrapper deny → sink → RunResult.PermissionDenial filled" path is
// covered by biz/permission/wrap_hooks_test.go because the #2 skeleton does
// not actually invoke PreToolCall hooks (only state.Transition based on
// Registry.LastAction). Once real ReAct loop lands (#14), this gap closes
// automatically.
func TestRunnerIntegration_PermissionDenial_Field(t *testing.T) {
	runner := NewAgentRunner(newMockStore(), nil)
	result, err := runner.Run(context.Background(), RunRequest{UserID: 1, Input: "x"})
	require.NoError(t, err)
	// PermissionDenial 默认 nil；omitempty 标签验证由 JSON 序列化测试覆盖
	assert.Nil(t, result.PermissionDenial)

	// 验证字段类型正确（compile-time check）
	var _ *PermissionDenialDetail = result.PermissionDenial
}
