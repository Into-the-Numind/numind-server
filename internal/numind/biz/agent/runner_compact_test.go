package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/compact"
)

// newCompactTestRunner returns an *agentRunner with no store/registry — fine
// because the #9 helpers are pure and never touch DB / Eino.
func newCompactTestRunner(provider compact.CompactProvider, cfg compact.Config) *agentRunner {
	return &agentRunner{
		cancels:         make(map[uint64]context.CancelFunc),
		compactProvider: provider,
		compactConfig:   cfg,
	}
}

func TestRunner_NewAgentRunner_DefaultCompactConfig(t *testing.T) {
	r := NewAgentRunner(nil, nil)
	impl := r.(*agentRunner)
	assert.Equal(t, compact.DefaultConfig(), impl.compactConfig)
	assert.Nil(t, impl.compactProvider, "no provider wired by default")
}

func TestRunner_WithCompactProvider_Wires(t *testing.T) {
	m := &compact.MockCompactProvider{PlaceholderSummary: "x"}
	r := NewAgentRunner(nil, nil, WithCompactProvider(m))
	impl := r.(*agentRunner)
	assert.NotNil(t, impl.compactProvider)
}

func TestRunner_WithCompactConfig_Overrides(t *testing.T) {
	custom := compact.Config{ContextWindow: 200_000, AutoCompactThreshold: 150_000}
	r := NewAgentRunner(nil, nil, WithCompactConfig(custom))
	impl := r.(*agentRunner)
	assert.Equal(t, 200_000, impl.compactConfig.ContextWindow)
	assert.Equal(t, 150_000, impl.compactConfig.AutoCompactThreshold)
}

func TestRunner_TryPreLLMCompact_Skip_BelowThreshold(t *testing.T) {
	r := newCompactTestRunner(&compact.MockCompactProvider{PlaceholderSummary: "summary"}, compact.DefaultConfig())
	msgs := []compact.Message{{Role: "user", Content: "hi"}}
	out, did, err := r.tryPreLLMCompact(context.Background(), msgs)
	require.NoError(t, err)
	assert.False(t, did)
	assert.Equal(t, msgs, out)
}

func TestRunner_TryPreLLMCompact_Trigger_AboveThreshold(t *testing.T) {
	// Construct messages whose estimated tokens > AutoCompactThreshold (107_000).
	// ASCII char ≈ 0.25 token → need > 428_000 ASCII chars. Use a small threshold instead.
	cfg := compact.DefaultConfig()
	cfg.AutoCompactThreshold = 5 // tiny threshold to force trigger
	cfg.PTLCollapseKeepTurns = 1
	r := newCompactTestRunner(&compact.MockCompactProvider{PlaceholderSummary: "summary text"}, cfg)
	msgs := []compact.Message{{Role: "user", Content: strings.Repeat("一二三四五", 20)}}
	out, did, err := r.tryPreLLMCompact(context.Background(), msgs)
	require.NoError(t, err)
	assert.True(t, did)
	require.Greater(t, len(out), 0)
	assert.True(t, out[0].IsCompactMark)
	assert.Equal(t, "system", out[0].Role)
}

func TestRunner_TryPreLLMCompact_NilProvider(t *testing.T) {
	r := newCompactTestRunner(nil, compact.DefaultConfig())
	msgs := []compact.Message{{Role: "user", Content: "hi"}}
	out, did, err := r.tryPreLLMCompact(context.Background(), msgs)
	require.NoError(t, err)
	assert.False(t, did)
	assert.Equal(t, msgs, out)
}

func TestRunner_TryPreLLMCompact_ProviderErrorPropagates(t *testing.T) {
	wantErr := errors.New("compact failed")
	cfg := compact.DefaultConfig()
	cfg.AutoCompactThreshold = 1 // always trigger
	provider := &compact.MockCompactProvider{
		PlaceholderSummary: "x",
		FailureSequence:    []error{wantErr, wantErr, wantErr, wantErr},
	}
	r := newCompactTestRunner(provider, cfg)
	msgs := []compact.Message{{Role: "user", Content: "hello world"}}
	out, did, err := r.tryPreLLMCompact(context.Background(), msgs)
	require.Error(t, err)
	assert.False(t, did)
	assert.Equal(t, msgs, out, "on error return original messages")
}

func TestRunner_HandlePTLError_Step1Collapse(t *testing.T) {
	r := newCompactTestRunner(&compact.MockCompactProvider{PlaceholderSummary: "s"}, compact.DefaultConfig())
	st := &LoopState{} // PTLRetries=0 → Transition makes it 1 → ContinueCollapseDrainRetry
	msgs := make([]compact.Message, 0, 18)
	for i := 0; i < 6; i++ {
		msgs = append(msgs, compact.Message{Role: "user", Content: "u"})
		msgs = append(msgs, compact.Message{Role: "assistant", Content: "a"})
		msgs = append(msgs, compact.Message{Role: "tool", ToolCallID: "t"})
	}
	cont, newMsgs, isTerm, term, err := r.handlePTLError(context.Background(), st, msgs)
	require.NoError(t, err)
	assert.False(t, isTerm)
	assert.Empty(t, term)
	assert.Equal(t, ContinueCollapseDrainRetry, cont)
	assert.NotNil(t, newMsgs)
	assert.Equal(t, 1, st.PTLRetries)
}

func TestRunner_HandlePTLError_Step2Reactive(t *testing.T) {
	r := newCompactTestRunner(&compact.MockCompactProvider{PlaceholderSummary: "summary x"}, compact.DefaultConfig())
	st := &LoopState{PTLRetries: 1}
	msgs := []compact.Message{{Role: "user", Content: "hi"}}
	cont, newMsgs, isTerm, term, err := r.handlePTLError(context.Background(), st, msgs)
	require.NoError(t, err)
	assert.False(t, isTerm)
	assert.Empty(t, term)
	assert.Equal(t, ContinueReactiveCompactRetry, cont)
	require.Greater(t, len(newMsgs), 0)
	assert.True(t, newMsgs[0].IsCompactMark)
	assert.Equal(t, 2, st.PTLRetries)
}

func TestRunner_HandlePTLError_Step2NilProvider(t *testing.T) {
	r := newCompactTestRunner(nil, compact.DefaultConfig())
	st := &LoopState{PTLRetries: 1}
	msgs := []compact.Message{{Role: "user", Content: "hi"}}
	_, _, isTerm, term, err := r.handlePTLError(context.Background(), st, msgs)
	require.Error(t, err)
	assert.True(t, isTerm)
	assert.Equal(t, TerminalPromptTooLong, term)
}

func TestRunner_HandlePTLError_Step2ProviderError(t *testing.T) {
	wantErr := errors.New("compact failed")
	provider := &compact.MockCompactProvider{
		PlaceholderSummary: "x",
		FailureSequence:    []error{wantErr, wantErr, wantErr, wantErr},
	}
	r := newCompactTestRunner(provider, compact.DefaultConfig())
	st := &LoopState{PTLRetries: 1}
	msgs := []compact.Message{{Role: "user", Content: "hi"}}
	_, _, isTerm, term, err := r.handlePTLError(context.Background(), st, msgs)
	require.Error(t, err)
	assert.True(t, isTerm)
	assert.Equal(t, TerminalPromptTooLong, term)
}

func TestRunner_HandlePTLError_Terminal_RetriesExhausted(t *testing.T) {
	r := newCompactTestRunner(&compact.MockCompactProvider{PlaceholderSummary: "s"}, compact.DefaultConfig())
	st := &LoopState{PTLRetries: 2} // next Transition → PTLRetries=3 > MaxPTLRetries=2 → terminal
	_, _, isTerm, term, err := r.handlePTLError(context.Background(), st, nil)
	require.NoError(t, err)
	assert.True(t, isTerm)
	assert.Equal(t, TerminalPromptTooLong, term)
}

func TestRunner_HandlePTLError_NoDoubleCounting(t *testing.T) {
	// Calling helper once must advance PTLRetries by exactly 1, not 2.
	r := newCompactTestRunner(&compact.MockCompactProvider{PlaceholderSummary: "s"}, compact.DefaultConfig())
	st := &LoopState{}
	cont, _, _, _, err := r.handlePTLError(context.Background(), st, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, st.PTLRetries, "exactly one Transition consumed per helper call")
	assert.Equal(t, ContinueCollapseDrainRetry, cont, "Step 1 → ContinueCollapseDrainRetry")
}

func TestRunner_HandleMaxOutputError_Escalate(t *testing.T) {
	r := newCompactTestRunner(nil, compact.DefaultConfig())
	st := &LoopState{} // → MaxOutputRetries=1 → ContinueMaxOutputEscalate
	cont, newMax, isTerm, term := r.handleMaxOutputError(context.Background(), st, compact.DefaultMaxTokens)
	assert.False(t, isTerm)
	assert.Empty(t, term)
	assert.Equal(t, ContinueMaxOutputEscalate, cont)
	assert.Equal(t, compact.EscalatedMaxTokens, newMax)
	assert.Equal(t, 1, st.MaxOutputRetries)
}

func TestRunner_HandleMaxOutputError_Recovery(t *testing.T) {
	r := newCompactTestRunner(nil, compact.DefaultConfig())
	st := &LoopState{MaxOutputRetries: 1} // → 2 → ContinueMaxOutputRecovery
	cont, newMax, isTerm, term := r.handleMaxOutputError(context.Background(), st, compact.EscalatedMaxTokens)
	assert.False(t, isTerm)
	assert.Empty(t, term)
	assert.Equal(t, ContinueMaxOutputRecovery, cont)
	assert.Equal(t, compact.EscalatedMaxTokens, newMax, "recovery preserves currentMaxTokens")
	assert.Equal(t, 2, st.MaxOutputRetries)
}

func TestRunner_HandleMaxOutputError_Terminal(t *testing.T) {
	r := newCompactTestRunner(nil, compact.DefaultConfig())
	st := &LoopState{MaxOutputRetries: 2} // → 3 > MaxOutputRetriesLimit=2 → terminal
	_, _, isTerm, term := r.handleMaxOutputError(context.Background(), st, compact.EscalatedMaxTokens)
	assert.True(t, isTerm)
	assert.Equal(t, TerminalErrorMaxBudget, term)
}

func TestRunner_HandleMaxOutputError_NoDoubleCounting(t *testing.T) {
	r := newCompactTestRunner(nil, compact.DefaultConfig())
	st := &LoopState{}
	_, _, _, _ = r.handleMaxOutputError(context.Background(), st, compact.DefaultMaxTokens)
	assert.Equal(t, 1, st.MaxOutputRetries)
}

// TestRunner_HelpersRaceSafe exercises the 3 helpers concurrently.
// Each goroutine uses ITS OWN LoopState — handlePTLError / handleMaxOutputError
// receive *LoopState and the caller (each Run() invocation) owns its own
// state instance. The race detector flags shared-state mutation; this test
// must not share state across goroutines (S3 reviewer P2 fix — race test
// simulates independent runs, not concurrent access to one run's state).
func TestRunner_HelpersRaceSafe(t *testing.T) {
	cfg := compact.DefaultConfig()
	// Each goroutine has its own provider too (MockCompactProvider is documented
	// as not-safe-for-concurrent-use; provider.go P2 fix).
	const goroutines = 8
	const iters = 25

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := newCompactTestRunner(&compact.MockCompactProvider{PlaceholderSummary: "s"}, cfg)
			for j := 0; j < iters; j++ {
				st := &LoopState{}
				_, _, _, _, _ = r.handlePTLError(context.Background(), st, nil)
				st2 := &LoopState{}
				_, _, _, _ = r.handleMaxOutputError(context.Background(), st2, compact.DefaultMaxTokens)
			}
		}()
	}
	wg.Wait()
}
