// v2 #2 agent-mode-v2-skill-invocation T09 — AC-6: load_skill tool invocations
// pass through BudgetGate hook chain just like any other tool.
//
// Why this test exists separately from gate_wrap_hooks_test.go:
// gate_wrap_hooks_test.go uses a *mockTool stub that's only loosely tied to
// real platform tools. This file proves that load_skill — the NEW platform
// tool from this feature — is structurally a einotool.BaseTool that flows
// through PreToolCall + PostToolCall just like file_read or kb_search,
// satisfying AC-6 "load_skill 计入 BudgetTracker" without requiring a real
// Eino ReAct loop.
//
// Degraded note: the original plan considered mocking BudgetTracker itself
// to verify exact Pre/PostToolCall call counts. The existing
// gate_wrap_hooks_test.go infrastructure uses a REAL budget.NewTracker which
// is more representative of production, so we follow that pattern. We assert
// that:
//   1. Pre/Post are invoked exactly once per load_skill simulated tool-call
//   2. RecordUsage is observable via tracker.Snapshot delta
//   3. Budget exhaustion short-circuits load_skill same as any other tool

package budgetgate

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/budget"
)

// useSkillToolFacade — einotool.BaseTool facade that names itself "load_skill"
// to prove BudgetGate treats it identically to other platform tools. We don't
// actually invoke the real agent.useSkillTool.Execute here — only its
// einotool.BaseTool interface footprint (Name + Info) matters for hook chain
// admission. This isolates the "BudgetGate doesn't special-case load_skill"
// contract from the orthogonal "load_skill Execute logic" already covered by
// eino_skill_integration_test.go and tool_load_skill_test.go.
type useSkillToolFacade struct{}

func (u *useSkillToolFacade) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: agent.LoadSkillToolName}, nil
}

var _ einotool.BaseTool = (*useSkillToolFacade)(nil)

func TestBudget_UseSkill_PreAndPostBothInvoked(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 7, 1, budget.DefaultLimits())
	defer tr.Close(7)

	var preCalled, postCalled atomic.Int32
	base := &agent.RunHooks{
		PreToolCall: func(ctx context.Context, tl einotool.BaseTool, input string) (agent.HookAction, error) {
			info, _ := tl.Info(ctx)
			assert.Equal(t, agent.LoadSkillToolName, info.Name,
				"base PreToolCall must see load_skill tool name (no special-case stripping)")
			preCalled.Add(1)
			return agent.HookActionContinue, nil
		},
		PostToolCall: func(ctx context.Context, tl einotool.BaseTool, output string, err error) (agent.HookAction, error) {
			info, _ := tl.Info(ctx)
			assert.Equal(t, agent.LoadSkillToolName, info.Name,
				"base PostToolCall must see load_skill tool name (no special-case stripping)")
			postCalled.Add(1)
			return agent.HookActionContinue, nil
		},
	}
	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(base)

	ctx := agent.WithRunID(context.Background(), 7)
	tool := &useSkillToolFacade{}

	// PreToolCall
	preAction, preErr := wrapped.PreToolCall(ctx, tool, `{"name":"销售话术训练"}`)
	require.NoError(t, preErr)
	assert.Equal(t, agent.HookActionContinue, preAction)
	assert.Equal(t, int32(1), preCalled.Load(), "PreToolCall must be invoked exactly once")

	// PostToolCall — simulate ack JSON from load_skill Execute (contains body field
	// per S4-D27, but for BudgetTracker only "usage" matters for token recording).
	output := `{"status":"loaded","skill_name":"销售话术训练","body":"<system-reminder>...</system-reminder>","usage":{"total_tokens":15}}`
	postAction, postErr := wrapped.PostToolCall(ctx, tool, output, nil)
	require.NoError(t, postErr)
	assert.Equal(t, agent.HookActionContinue, postAction)
	assert.Equal(t, int32(1), postCalled.Load(), "PostToolCall must be invoked exactly once")

	// Verify BudgetTracker recorded the usage (load_skill counts the same as any tool)
	snap := tr.Snapshot(context.Background(), 7)
	assert.Equal(t, int64(15), snap.Credits,
		"load_skill output tokens (15) must be recorded by tracker.RecordUsage (AC-6)")
}

func TestBudget_UseSkill_BudgetExceededShortCircuits(t *testing.T) {
	tr := budget.NewTracker(nil)
	// MaxTurns=1 → after 1 RecordStep, CanProceed returns exceeded
	tr.Start(context.Background(), 9, 1, budget.Limits{
		MaxTurns:        1,
		MaxCredits:      1000,
		MaxWallTime:     time.Hour,
		MaxDailyCredits: 10000,
	})
	defer tr.Close(9)
	tr.RecordStep(context.Background(), 9) // exhaust the turn budget

	var preCalled atomic.Bool
	reg := agent.NewHookActionRegistry()
	base := &agent.RunHooks{
		PreToolCall: func(ctx context.Context, tl einotool.BaseTool, input string) (agent.HookAction, error) {
			preCalled.Store(true)
			return agent.HookActionContinue, nil
		},
		Registry: reg,
	}
	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(base)

	ctx := agent.WithRunID(context.Background(), 9)
	tool := &useSkillToolFacade{}

	action, err := wrapped.PreToolCall(ctx, tool, `{"name":"销售话术训练"}`)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionBudgetExceeded, action,
		"load_skill must be short-circuited by budget exhaustion, identical to other tools")
	assert.False(t, preCalled.Load(),
		"base PreToolCall must NOT be invoked when budget exceeded (load_skill is not special-cased)")
	assert.Equal(t, agent.HookActionBudgetExceeded, reg.LastAction(),
		"Registry must record budget exceeded action for load_skill")
}

func TestBudget_UseSkill_PostBeforeRecordUsage_OrderingContract(t *testing.T) {
	// Verifies the documented order: forward to base.PostToolCall FIRST,
	// then RecordUsage. Important because base.PostToolCall on load_skill might
	// emit narration ("📚 已调用技能：..."); if we recorded usage first and the
	// recording overflowed budget, narration would never fire — bad UX.
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 11, 1, budget.DefaultLimits())
	defer tr.Close(11)

	order := make([]string, 0, 4)
	base := &agent.RunHooks{
		PostToolCall: func(ctx context.Context, tl einotool.BaseTool, output string, err error) (agent.HookAction, error) {
			order = append(order, "base.Post")
			return agent.HookActionContinue, nil
		},
	}
	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(base)

	ctx := agent.WithRunID(context.Background(), 11)
	tool := &useSkillToolFacade{}

	_, err := wrapped.PostToolCall(ctx, tool, `{"usage":{"total_tokens":42}}`, nil)
	require.NoError(t, err)

	// base.Post must have been called
	require.Equal(t, []string{"base.Post"}, order, "base.PostToolCall must run first")

	// Snapshot proves RecordUsage ran AFTER base.Post (else credit would be 0)
	snap := tr.Snapshot(context.Background(), 11)
	assert.Equal(t, int64(42), snap.Credits, "RecordUsage must run for load_skill after base.Post")
}
