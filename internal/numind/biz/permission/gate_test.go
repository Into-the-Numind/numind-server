package permission

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// syncCountLogger 同步阻塞统计 — 用于测试 channel 容量/Close drain。
type syncCountLogger struct {
	mu    sync.Mutex
	count int
	sleep time.Duration // 模拟慢消费
}

func (l *syncCountLogger) Log(_ context.Context, _ AuditEntry) {
	if l.sleep > 0 {
		time.Sleep(l.sleep)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.count++
}

func (l *syncCountLogger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

// stubAllowValidator 总是 allow（用于 gate.Check 不会被 pipeline 干扰）。
type stubAllowValidator struct{}

func (s *stubAllowValidator) ID() string { return "StubAllow" }
func (s *stubAllowValidator) Validate(_ context.Context, _ PermissionRequest) PermissionResult {
	return Allow("StubAllow", DecisionReasonOther, "")
}

func TestGate_Check_AuditWritten(t *testing.T) {
	logger := &syncCountLogger{}
	g := NewPermissionGate(
		WithValidators(&stubAllowValidator{}),
		WithAuditLogger(logger),
		WithAuditChannelSize(8),
	)
	defer g.Close()

	g.Check(context.Background(), PermissionRequest{AgentRunID: 1})

	// Wait briefly for async write
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if logger.Count() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := logger.Count(); got != 1 {
		t.Errorf("audit count = %d, want 1", got)
	}
}

func TestGate_Check_DefaultAllowWhenNoValidators(t *testing.T) {
	g := NewPermissionGate()
	defer g.Close()

	got := g.Check(context.Background(), PermissionRequest{AgentRunID: 1})
	if got.Behavior != BehaviorAllow {
		t.Errorf("Behavior = %s, want allow", got.Behavior)
	}
	if got.ValidatorID != "DefaultAllow" {
		t.Errorf("ValidatorID = %s, want DefaultAllow", got.ValidatorID)
	}
}

func TestGate_Close_DrainPendingEntries(t *testing.T) {
	logger := &syncCountLogger{sleep: 10 * time.Millisecond} // 慢消费保证 channel 有残留
	g := NewPermissionGate(
		WithValidators(&stubAllowValidator{}),
		WithAuditLogger(logger),
		WithAuditChannelSize(64),
	)

	// 突发塞 50 个，超过消费速率
	for i := 0; i < 50; i++ {
		g.Check(context.Background(), PermissionRequest{AgentRunID: uint64(i)})
	}

	g.Close() // 等到 drain 完成或 5s 超时

	if got := logger.Count(); got != 50 {
		t.Errorf("after Close, count = %d, want 50 (drain should consume all)", got)
	}
}

func TestGate_Close_AfterCloseCheckGoesWarnPath(t *testing.T) {
	logger := &syncCountLogger{}
	g := NewPermissionGate(
		WithValidators(&stubAllowValidator{}),
		WithAuditLogger(logger),
	)
	g.Close() // 立即关闭

	// Close 后再调 Check：返回 allow，但 audit 不写入
	got := g.Check(context.Background(), PermissionRequest{AgentRunID: 99})
	if got.Behavior != BehaviorAllow {
		t.Errorf("post-close Check should still return decision; got %s", got.Behavior)
	}

	// 等 50ms 确认没有任何 audit 被消费
	time.Sleep(50 * time.Millisecond)
	if got := logger.Count(); got != 0 {
		t.Errorf("post-close audit count = %d, want 0", got)
	}
}

func TestGate_AuditChannelFull_DropsWarnNoBlock(t *testing.T) {
	logger := &syncCountLogger{sleep: 500 * time.Millisecond} // 极慢消费保证 channel 满
	g := NewPermissionGate(
		WithValidators(&stubAllowValidator{}),
		WithAuditLogger(logger),
		WithAuditChannelSize(2), // 小容量
	)
	defer g.Close()

	// 立即塞 10 条，远超 channel size + 慢消费 → 后面的会被 drop，但不阻塞
	start := time.Now()
	for i := 0; i < 10; i++ {
		g.Check(context.Background(), PermissionRequest{AgentRunID: uint64(i)})
	}
	elapsed := time.Since(start)
	// Check 应该都很快（每次 < 10ms）；如果阻塞了就会 > 1s
	if elapsed > 500*time.Millisecond {
		t.Errorf("Check appears to block when channel full; elapsed=%v", elapsed)
	}
}

func TestGate_CloseGoroutineExits(t *testing.T) {
	before := runtime.NumGoroutine()
	g := NewPermissionGate(
		WithValidators(&stubAllowValidator{}),
		WithAuditLogger(&syncCountLogger{}),
	)
	g.Close()

	// drain goroutine 应在 5s 内退出
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+1 { // 允许 1 个 noise
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("audit goroutine did not exit; before=%d after=%d", before, runtime.NumGoroutine())
}

func TestGate_ConcurrentCheck_RaceSafe(t *testing.T) {
	// 并发 100 goroutine 各 10 次 Check，验证 race detector 通过
	logger := &syncCountLogger{}
	g := NewPermissionGate(
		WithValidators(&stubAllowValidator{}),
		WithAuditLogger(logger),
		WithAuditChannelSize(4096),
	)
	defer g.Close()

	var wg sync.WaitGroup
	var counter atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				g.Check(context.Background(), PermissionRequest{AgentRunID: uint64(idx*10 + j)})
				counter.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if counter.Load() != 1000 {
		t.Errorf("counter = %d, want 1000", counter.Load())
	}
	// audit count 可能小于 1000（channel 限速），但应该 > 0
	time.Sleep(100 * time.Millisecond)
	if logger.Count() == 0 {
		t.Errorf("audit count = 0, expected some writes")
	}
}
