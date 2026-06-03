package middleware

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/billing"
)

// Guard the cross-package string contract: middleware.BillingPoolAdminTest and
// credit.PoolAdminTest MUST stay equal (they can't share a const — credit cannot
// import middleware: import cycle). A drift would silently route admin_test
// reservations through the three-pool, charging real user credits.
func TestBillingPoolAdminTest_MatchesCreditConst(t *testing.T) {
	if BillingPoolAdminTest != credit.PoolAdminTest {
		t.Fatalf("pool const drift: middleware %q != credit %q", BillingPoolAdminTest, credit.PoolAdminTest)
	}
}

// T2 (agent-mode-billing): WithBillingPool/BillingPoolFromCtx —— 专用 ctx key，
// 选择计费池（""=三池, "admin_test"=父账户 Builder 试聊池）。须与 billing.WithBilling
// 互不覆盖（不同包 → 不同 key identity，继承 credit-log-task-names S2-D4 教训）。

func TestWithBillingPool_RoundTrip(t *testing.T) {
	ctx := WithBillingPool(context.Background(), "admin_test")
	if got := BillingPoolFromCtx(ctx); got != "admin_test" {
		t.Fatalf("BillingPoolFromCtx = %q, want %q", got, "admin_test")
	}
}

func TestBillingPool_EmptyByDefault(t *testing.T) {
	if got := BillingPoolFromCtx(context.Background()); got != "" {
		t.Fatalf("BillingPoolFromCtx(bare) = %q, want empty", got)
	}
}

func TestWithBillingPool_EmptyIsNoop(t *testing.T) {
	base := context.Background()
	ctx := WithBillingPool(base, "")
	// No-op contract: identical ctx returned (no spurious value added).
	if ctx != base {
		t.Fatalf("WithBillingPool(\"\") must return the same ctx (no-op)")
	}
	if got := BillingPoolFromCtx(ctx); got != "" {
		t.Fatalf("WithBillingPool(\"\") should be no-op, got %q", got)
	}
}

// Three-way coexistence (plan T2 acceptance): WithReservationRef + WithBilling +
// WithBillingPool 混合后三者各自值不互相覆盖（不同包 → 不同 key identity）。
func TestBillingPool_NotOverwrittenByWithBilling(t *testing.T) {
	ctx := WithBillingPool(context.Background(), "admin_test")
	ctx = WithReservationRef(ctx, "agent_run:99")
	ctx = billing.WithBilling(ctx, 42, "agent_run")

	if got := BillingPoolFromCtx(ctx); got != "admin_test" {
		t.Fatalf("pool clobbered: got %q, want %q", got, "admin_test")
	}
	if got := ReservationRefFromCtx(ctx); got != "agent_run:99" {
		t.Fatalf("reservation ref clobbered: got %q, want %q", got, "agent_run:99")
	}
	if bc := billing.FromContext(ctx); bc == nil || bc.UserID != 42 || bc.Operation != "agent_run" {
		t.Fatalf("billing ctx wrong: %+v", bc)
	}
}
