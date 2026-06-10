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

// test(qa): reproduce dev run #113 — BudgetTracker counted raw LLM tokens as
// credits. A single ~6k-token LLM call blew through the 800-credit MaxCredits
// session cap and terminated every substantive agent run at its first tool
// call (terminal_metadata: {"budget_dimension":"max_credits","limit":800,
// "used":6829} while the authoritative reservation path had charged ~5 credits).
//
// Correct behavior: PostToolCall must feed the tracker CREDITS (pricing-converted
// or a conservative token→credit ratio), never raw token counts.
func TestPostToolCall_DoesNotCountRawTokensAsCredits(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 113, 1, budget.DefaultLimits()) // MaxCredits=800
	defer tr.Close(113)

	// Real shape from dev run #113's main ReAct call.
	fake := &fakeUsageLookup{
		usage: agent.Usage{PromptTokens: 4061, CompletionTokens: 1998, Model: "deepseek-v4-pro", Provider: "dmxapi"},
		found: true,
	}

	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(nil, WithUsageLookup(fake))
	require.NotNil(t, wrapped.PostToolCall)

	ctx := agent.WithRunID(context.Background(), 113)
	ctx = callctx.WithCallID(ctx, callctx.NewCallID())

	_, err := wrapped.PostToolCall(ctx, &mockTool{name: "web_search"}, `{}`, nil)
	require.NoError(t, err)

	exceeded, dim, detail := tr.CanProceed(ctx, 113)
	assert.False(t, exceeded,
		"one ~6k-token LLM call must NOT exhaust the 800-credit session cap (dim=%s detail=%v)", dim, detail)

	snap := tr.Snapshot(context.Background(), 113)
	assert.Less(t, snap.Credits, int64(100),
		"tracker counter must be credit-scale, not token-scale; got %d", snap.Credits)
}
