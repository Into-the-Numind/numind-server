package budgetgate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/budget"
)

func TestNewBudgetGate(t *testing.T) {
	tr := budget.NewTracker(nil)
	g := NewBudgetGate(tr, nil, nil)
	assert.NotNil(t, g)
	assert.Equal(t, tr, g.Tracker())
	assert.Nil(t, g.AdminConsumer())
}

func TestBudgetGate_WrapHooks_NilBase(t *testing.T) {
	g := NewBudgetGate(budget.NewTracker(nil), nil, nil)
	wrapped := g.WrapHooks(nil)
	require.NotNil(t, wrapped)
	require.NotNil(t, wrapped.PreToolCall)
	require.NotNil(t, wrapped.PostToolCall)
}

func TestBudgetGate_WrapHooks_NilGate(t *testing.T) {
	var g *BudgetGate
	base := &agent.RunHooks{}
	wrapped := g.WrapHooks(base)
	assert.Same(t, base, wrapped, "nil gate returns base unchanged")
}

func TestTokensFromOutput(t *testing.T) {
	assert.Equal(t, 0, tokensFromOutput(""))
	assert.Equal(t, 0, tokensFromOutput("not json"))
	assert.Equal(t, 0, tokensFromOutput(`{}`))
	assert.Equal(t, 150, tokensFromOutput(`{"usage":{"total_tokens":150}}`))
	assert.Equal(t, 0, tokensFromOutput(`{"usage":{"prompt_tokens":50}}`))
}
