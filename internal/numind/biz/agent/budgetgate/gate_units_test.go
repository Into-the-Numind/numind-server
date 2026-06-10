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

// fakePricing implements pricing.ICalculator with a canned per-call cost.
type fakePricing struct {
	costCents                int64
	err                      error
	gotPrompt, gotCompletion int
	gotProvider, gotModel    string
}

func (f *fakePricing) CalculateCost(_ context.Context, _, provider, model string, promptTokens, completionTokens int) (int64, error) {
	f.gotProvider, f.gotModel = provider, model
	f.gotPrompt, f.gotCompletion = promptTokens, completionTokens
	return f.costCents, f.err
}

func (f *fakePricing) CalculateCostWithCache(ctx context.Context, st, provider, model string, p, c, _ int) (int64, error) {
	return f.CalculateCost(ctx, st, provider, model, p, c)
}

// TestPostToolCall_ConvertsTokensViaPricing pins the primary path: with a
// pricing calculator wired, the tracker accumulates the calculator's credit
// figure (cost cents == credits, same convention as the billing gateway).
func TestPostToolCall_ConvertsTokensViaPricing(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 51, 1, budget.DefaultLimits())
	defer tr.Close(51)

	fake := &fakeUsageLookup{
		usage: agent.Usage{PromptTokens: 4061, CompletionTokens: 1998, Model: "deepseek-v4-pro", Provider: "dmxapi"},
		found: true,
	}
	pc := &fakePricing{costCents: 3}

	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(nil, WithUsageLookup(fake), WithPricingCalculator(pc))

	ctx := agent.WithRunID(context.Background(), 51)
	ctx = callctx.WithCallID(ctx, callctx.NewCallID())
	_, err := wrapped.PostToolCall(ctx, &mockTool{name: "web_search"}, `{}`, nil)
	require.NoError(t, err)

	snap := tr.Snapshot(context.Background(), 51)
	assert.Equal(t, int64(3), snap.Credits, "tracker must accumulate pricing-converted credits")
	assert.Equal(t, 4061, pc.gotPrompt)
	assert.Equal(t, 1998, pc.gotCompletion)
	assert.Equal(t, "dmxapi", pc.gotProvider)
	assert.Equal(t, "deepseek-v4-pro", pc.gotModel)
}

// TestPostToolCall_PricingErrorFallsBackToRatio pins the degraded path: pricing
// lookup failure (e.g. no rule for the model) falls back to the conservative
// fixed ratio instead of recording raw tokens or recording nothing.
func TestPostToolCall_PricingErrorFallsBackToRatio(t *testing.T) {
	tr := budget.NewTracker(nil)
	tr.Start(context.Background(), 52, 1, budget.DefaultLimits())
	defer tr.Close(52)

	fake := &fakeUsageLookup{
		usage: agent.Usage{PromptTokens: 4061, CompletionTokens: 1998, Model: "deepseek-v4-pro", Provider: "dmxapi"},
		found: true,
	}
	pc := &fakePricing{err: assert.AnError}

	g := NewBudgetGate(tr, nil, nil)
	wrapped := g.WrapHooks(nil, WithUsageLookup(fake), WithPricingCalculator(pc))

	ctx := agent.WithRunID(context.Background(), 52)
	ctx = callctx.WithCallID(ctx, callctx.NewCallID())
	_, err := wrapped.PostToolCall(ctx, &mockTool{name: "web_search"}, `{}`, nil)
	require.NoError(t, err)

	snap := tr.Snapshot(context.Background(), 52)
	// ceil((4061+1998)/500) = 13
	assert.Equal(t, int64(13), snap.Credits, "pricing error must degrade to the fixed token→credit ratio")
}

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
