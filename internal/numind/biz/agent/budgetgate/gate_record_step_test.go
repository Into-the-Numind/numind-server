package budgetgate

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/budget"
)

// T6 (agent-mode-billing): PreToolCall now calls RecordStep so the MaxTurns
// dimension actually advances (previously RecordStep had no caller → MaxTurns
// never tripped). Recorded before CanProceed, so the Nth turn trips the limit.
func TestPreToolCall_RecordsStep_TripsMaxTurns(t *testing.T) {
	tr := budget.NewTracker(nil)
	limits := budget.Limits{
		MaxTurns:        2,
		MaxCredits:      800,
		MaxWallTime:     time.Hour,
		MaxDailyCredits: 2000,
	}
	tr.Start(context.Background(), 50, 1, limits)
	defer tr.Close(50)

	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(nil)
	require.NotNil(t, wrapped.PreToolCall)

	ctx := agent.WithRunID(context.Background(), 50)

	// Turn 1: RecordStep → turns=1; CanProceed(1>=2? no) → Continue.
	a1, err := wrapped.PreToolCall(ctx, &mockTool{name: "t"}, "")
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, a1)

	// Turn 2: RecordStep → turns=2; CanProceed(2>=2? yes) → BudgetExceeded.
	a2, err := wrapped.PreToolCall(ctx, &mockTool{name: "t"}, "")
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionBudgetExceeded, a2)
}
