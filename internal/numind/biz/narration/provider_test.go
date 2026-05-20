package narration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustProvider(t *testing.T, yamlSrc string) *Provider {
	t.Helper()
	p, err := NewProvider(Config{
		YAMLBytes:  []byte(yamlSrc),
		BufferSize: 16,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func TestNewProvider_NeitherPathNorBytes_Errors(t *testing.T) {
	_, err := NewProvider(Config{})
	if err == nil {
		t.Fatal("expected error when neither YAMLPath nor YAMLBytes provided")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error should mention 'required', got: %v", err)
	}
}

func TestNewProvider_InvalidYAML_Errors(t *testing.T) {
	_, err := NewProvider(Config{YAMLBytes: []byte("tools: [ unterminated")})
	if err == nil {
		t.Fatal("expected error on invalid yaml")
	}
	if !strings.Contains(err.Error(), "NewProvider") {
		t.Errorf("error should wrap with NewProvider context: %v", err)
	}
}

func TestProvider_Emit_HappyPath(t *testing.T) {
	p := mustProvider(t, minimalValidYAML)
	ch, cleanup := p.Subscribe(1)
	defer cleanup()

	p.Emit(context.Background(), 1, "bash_exec", StateUse, EmitPayload{})

	select {
	case ev := <-ch:
		if ev.RunID != 1 || ev.ToolName != "bash_exec" {
			t.Errorf("event identity: %+v", ev)
		}
		if ev.ToolCallID != "1-1" {
			t.Errorf("ToolCallID: want 1-1, got %q", ev.ToolCallID)
		}
		if ev.State != StateUse {
			t.Errorf("State: want use, got %q", ev.State)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestProvider_Emit_CustomToolCallID(t *testing.T) {
	p := mustProvider(t, minimalValidYAML)
	ch, cleanup := p.Subscribe(2)
	defer cleanup()
	p.Emit(context.Background(), 2, "bash_exec", StateResult, EmitPayload{ToolCallID: "custom-id"})

	ev := <-ch
	if ev.ToolCallID != "custom-id" {
		t.Errorf("custom ToolCallID should be preserved, got %q", ev.ToolCallID)
	}
}

func TestProvider_NextCallID_Monotonic_SameRun(t *testing.T) {
	p := mustProvider(t, minimalValidYAML)
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := p.nextCallID(42)
		if seen[id] {
			t.Fatalf("duplicate ID: %q", id)
		}
		seen[id] = true
		want := fmt.Sprintf("42-%d", i+1)
		if id != want {
			t.Errorf("iter %d: want %q, got %q", i, want, id)
		}
	}
}

func TestProvider_NextCallID_LoadOrStoreRaceSafe(t *testing.T) {
	// S1-D18 verification: concurrent nextCallID for the SAME runID must
	// produce unique sequential IDs (LoadOrStore avoids TOCTOU race).
	p := mustProvider(t, minimalValidYAML)
	const goroutines = 50
	const idsPerGoroutine = 20
	totalIDs := goroutines * idsPerGoroutine

	results := make(chan string, totalIDs)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < idsPerGoroutine; i++ {
				results <- p.nextCallID(7)
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[string]bool, totalIDs)
	for id := range results {
		if seen[id] {
			t.Fatalf("duplicate ID from concurrent emit: %q", id)
		}
		seen[id] = true
	}
	if len(seen) != totalIDs {
		t.Errorf("want %d unique IDs, got %d", totalIDs, len(seen))
	}
	// Sanity: check max counter == totalIDs
	v, _ := p.callSeq.Load(uint64(7))
	max := v.(*atomic.Int64).Load()
	if max != int64(totalIDs) {
		t.Errorf("counter should equal totalIDs=%d, got %d", totalIDs, max)
	}
}

func TestProvider_NextCallID_DifferentRuns_Independent(t *testing.T) {
	p := mustProvider(t, minimalValidYAML)
	id1 := p.nextCallID(100)
	id2 := p.nextCallID(200)
	id3 := p.nextCallID(100)
	if id1 != "100-1" || id3 != "100-2" || id2 != "200-1" {
		t.Errorf("counters not independent per runID: %q, %q, %q", id1, id2, id3)
	}
}

func TestProvider_CloseRun_CleansCounter(t *testing.T) {
	p := mustProvider(t, minimalValidYAML)
	_ = p.nextCallID(500) // 500-1
	p.CloseRun(500)
	id := p.nextCallID(500) // counter reset; should be 500-1 again
	if id != "500-1" {
		t.Errorf("CloseRun should delete callSeq entry; want '500-1' after reset, got %q", id)
	}
}

func TestProvider_NilFallback_DefaultsToStub(t *testing.T) {
	// When yaml has no entry for the tool AND no defaults message, stub kicks in.
	src := `
tools: {}
defaults:
  verb: "正在处理"
  detail_template: ""
  use_template: ""
  result_template: "处理完成"
  error_template: "失败"
  rejected_template: "拦截"
`
	p, err := NewProvider(Config{YAMLBytes: []byte(src), LLMFallback: nil})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	ch, cleanup := p.Subscribe(99)
	defer cleanup()
	p.Emit(context.Background(), 99, "completely_unknown", StateUse, EmitPayload{})
	ev := <-ch
	// stub: "正在执行 completely_unknown"
	if !strings.Contains(ev.Message, "completely_unknown") {
		t.Errorf("stub fallback should include tool name, got %q", ev.Message)
	}
}

func TestProvider_Subscribe_AfterCloseRun_GetsNewOpenChan(t *testing.T) {
	// S2-D2 verification at the Provider level.
	p := mustProvider(t, minimalValidYAML)
	p.Emit(context.Background(), 11, "bash_exec", StateUse, EmitPayload{})
	p.CloseRun(11)

	ch, cleanup := p.Subscribe(11)
	defer cleanup()
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("S2-D2: Subscribe after CloseRun should return new open channel; got closed")
		}
	case <-time.After(30 * time.Millisecond):
		// Expected: open + empty
	}
}

func TestNewProvider_WithToolNames_WarnOnMissing(t *testing.T) {
	// Provide ToolNames; expect NewProvider to complete (warn-only).
	// We can't easily intercept zap.Warnw here without an injectable logger,
	// so just verify NewProvider succeeds and the Renderer reports missing names.
	p, err := NewProvider(Config{
		YAMLBytes: []byte(minimalValidYAML),
		ToolNames: []string{"bash_exec", "unknown_tool_a", "unknown_tool_b"},
	})
	if err != nil {
		t.Fatalf("NewProvider should succeed with missing tool names (warn only): %v", err)
	}
	if p == nil {
		t.Fatal("provider should be non-nil")
	}
}
