package budgetgate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/callctx"
	"numind-server/internal/numind/biz/budget"
)

// fakeUsageLookup implements UsageLookupable for tests.
type fakeUsageLookup struct {
	usage agent.Usage
	found bool
}

func (f *fakeUsageLookup) LookupUsage(_ string) (agent.Usage, bool) {
	return f.usage, f.found
}

// TestPostToolCall_ReadsUsageFromAdapter verifies the primary #14 A8b path:
// when a UsageLookupable is injected and returns a Usage for the ctx call-id,
// tracker.RecordUsage is called with PromptTokens+CompletionTokens.
func TestPostToolCall_ReadsUsageFromAdapter(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 42, 1, budget.DefaultLimits())
	defer tr.Close(42)

	fake := &fakeUsageLookup{
		usage: agent.Usage{PromptTokens: 100, CompletionTokens: 50, Model: "test-model"},
		found: true,
	}

	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(nil, WithUsageLookup(fake))
	require.NotNil(t, wrapped)
	require.NotNil(t, wrapped.PostToolCall)

	// Inject a call-id into ctx so PostToolCall can look up the stashed Usage.
	ctx := agent.WithRunID(context.Background(), 42)
	ctx = callctx.WithCallID(ctx, callctx.NewCallID())

	action, err := wrapped.PostToolCall(ctx, &mockTool{name: "some-tool"}, `{}`, nil)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)

	// Verify tracker recorded 150 (100+50) credits.
	snap := tr.Snapshot(context.Background(), 42)
	assert.Equal(t, int64(150), snap.Credits, "tracker should record PromptTokens+CompletionTokens from adapter")
}

// TestPostToolCall_FallbackToOutputTokens verifies the legacy fallback path:
// when the adapter is present but LookupUsage returns found=false (e.g. call-id
// mismatch), PostToolCall falls back to tokensFromOutput.
func TestPostToolCall_FallbackToOutputTokens(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 43, 1, budget.DefaultLimits())
	defer tr.Close(43)

	// Adapter present but LookupUsage always returns found=false.
	fake := &fakeUsageLookup{found: false}

	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(nil, WithUsageLookup(fake))
	require.NotNil(t, wrapped.PostToolCall)

	ctx := agent.WithRunID(context.Background(), 43)
	ctx = callctx.WithCallID(ctx, callctx.NewCallID())

	// Output carries the legacy JSON shape with total_tokens=77.
	output := `{"usage":{"total_tokens":77}}`
	action, err := wrapped.PostToolCall(ctx, &mockTool{name: "tool"}, output, nil)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)

	snap := tr.Snapshot(context.Background(), 43)
	assert.Equal(t, int64(77), snap.Credits, "should fall back to tokensFromOutput when adapter lookup misses")
}

// TestPostToolCall_NilAdapter_FallsBack verifies backward-compat: WrapHooks built
// without any WithUsageLookup option behaves identically to the pre-#14 code and
// still reads tokens from the legacy output JSON field.
func TestPostToolCall_NilAdapter_FallsBack(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 44, 1, budget.DefaultLimits())
	defer tr.Close(44)

	// No WithUsageLookup option — nil adapter.
	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(nil) // zero opts
	require.NotNil(t, wrapped.PostToolCall)

	ctx := agent.WithRunID(context.Background(), 44)

	output := `{"usage":{"total_tokens":200}}`
	action, err := wrapped.PostToolCall(ctx, &mockTool{name: "tool"}, output, nil)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)

	snap := tr.Snapshot(context.Background(), 44)
	assert.Equal(t, int64(200), snap.Credits, "nil adapter should fall back to tokensFromOutput (backward compat)")
}
