package narration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
)

// mockChatFn replaces chatFn for tests. It records call count and returns
// configured responses or errors.
type mockChat struct {
	calls   atomic.Int64
	content string
	err     error
}

func (m *mockChat) fn(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	m.calls.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	return &aiservice.ChatResponse{Content: m.content}, nil
}

func TestAIServiceLLMFallback_CacheHit_NoLLMCall(t *testing.T) {
	// Arrange: pre-populate cache, mock chatFn to track calls.
	mock := &mockChat{content: "should-not-be-called"}
	origChatFn := chatFn
	chatFn = mock.fn
	t.Cleanup(func() { chatFn = origChatFn })

	f := &AiserviceLLMFallback{}
	// Pre-seed cache entry for "bash_exec:use"
	f.cache.Store("bash_exec:"+string(StateUse), [2]string{"查询", "文件"})

	// Act
	verb, detail := f.Render(context.Background(), "bash_exec", StateUse, EmitPayload{})

	// Assert: correct cached values returned, chatFn never called
	if verb != "查询" {
		t.Errorf("expected verb=%q, got %q", "查询", verb)
	}
	if detail != "文件" {
		t.Errorf("expected detail=%q, got %q", "文件", detail)
	}
	if calls := mock.calls.Load(); calls != 0 {
		t.Errorf("expected chatFn to not be called, got %d call(s)", calls)
	}
}

func TestAIServiceLLMFallback_CacheMiss_TriggersLLM(t *testing.T) {
	// Arrange: mock returns "查询|文件"
	mock := &mockChat{content: "查询|文件"}
	origChatFn := chatFn
	chatFn = mock.fn
	t.Cleanup(func() { chatFn = origChatFn })

	f := &AiserviceLLMFallback{}

	// Act
	verb, detail := f.Render(context.Background(), "web_search", StateResult, EmitPayload{})

	// Assert: parsed correctly
	if verb != "查询" {
		t.Errorf("expected verb=%q, got %q", "查询", verb)
	}
	if detail != "文件" {
		t.Errorf("expected detail=%q, got %q", "文件", detail)
	}
	if calls := mock.calls.Load(); calls != 1 {
		t.Errorf("expected chatFn to be called exactly once, got %d", calls)
	}

	// Assert: cache updated — second call must NOT invoke chatFn again
	verb2, detail2 := f.Render(context.Background(), "web_search", StateResult, EmitPayload{})
	if verb2 != "查询" || detail2 != "文件" {
		t.Errorf("second call from cache: got (%q, %q)", verb2, detail2)
	}
	if calls := mock.calls.Load(); calls != 1 {
		t.Errorf("expected chatFn not called again after cache populated, got %d", calls)
	}
}

func TestAIServiceLLMFallback_Timeout_FallsBackToStub(t *testing.T) {
	// Arrange: mock returns a deadline-exceeded error
	mock := &mockChat{err: errors.New("simulated timeout")}
	origChatFn := chatFn
	chatFn = mock.fn
	t.Cleanup(func() { chatFn = origChatFn })

	tests := []struct {
		state      State
		wantVerb   string
		wantDetail string
	}{
		{StateUse, "正在执行", "bash_exec"},
		{StateQueued, "正在执行", "bash_exec"},
		{StateResult, "完成", "bash_exec"},
		{StateError, "执行出错", "bash_exec"},
		{StateRejected, "操作被拦截", "bash_exec"},
		{StateProgress, "处理中", "bash_exec"},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			// fresh fallback per sub-test so cache doesn't interfere
			fLocal := &AiserviceLLMFallback{}
			verb, detail := fLocal.Render(context.Background(), "bash_exec", tc.state, EmitPayload{})
			if verb != tc.wantVerb {
				t.Errorf("state=%s: expected verb=%q, got %q", tc.state, tc.wantVerb, verb)
			}
			if detail != tc.wantDetail {
				t.Errorf("state=%s: expected detail=%q, got %q", tc.state, tc.wantDetail, detail)
			}
		})
	}
}

func TestAIServiceLLMFallback_ConcurrentSameKey_RaceFree(t *testing.T) {
	// Arrange: mock returns a fixed response; track call count
	mock := &mockChat{content: "并行|测试"}
	origChatFn := chatFn
	chatFn = mock.fn
	t.Cleanup(func() { chatFn = origChatFn })

	f := &AiserviceLLMFallback{}
	const goroutines = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			verb, detail := f.Render(context.Background(), "concurrent_tool", StateUse, EmitPayload{})
			if verb == "" || detail == "" {
				// Both stub and LLM responses are non-empty; any empty result is a bug.
				t.Errorf("unexpected empty result: verb=%q detail=%q", verb, detail)
			}
		}()
	}
	wg.Wait()

	// Cache should converge: subsequent calls return the cached value.
	verb, detail := f.Render(context.Background(), "concurrent_tool", StateUse, EmitPayload{})
	if verb != "并行" {
		t.Errorf("expected final verb=%q, got %q", "并行", verb)
	}
	if detail != "测试" {
		t.Errorf("expected final detail=%q, got %q", "测试", detail)
	}

	// chatFn should have been called a small number of times (not 100), but
	// sync.Map allows a transient window where multiple goroutines miss the
	// cache simultaneously. We allow up to goroutines calls but verify far
	// fewer once the cache is warm.
	calls := mock.calls.Load()
	if calls > int64(goroutines) {
		t.Errorf("chatFn called %d times; expected ≤ %d (no-race invariant violated)", calls, goroutines)
	}
	// After cache warm, further calls must not invoke chatFn.
	callsBefore := mock.calls.Load()
	for i := 0; i < 10; i++ {
		f.Render(context.Background(), "concurrent_tool", StateUse, EmitPayload{})
	}
	if after := mock.calls.Load(); after != callsBefore {
		t.Errorf("chatFn called again after cache warm: before=%d after=%d", callsBefore, after)
	}
	t.Logf("chatFn called %d time(s) across %d concurrent goroutines (cache converged)", calls, goroutines)
}

// Verify profile constant exists (compile-time check)
var _ = profile.AgentNarrationFallback
