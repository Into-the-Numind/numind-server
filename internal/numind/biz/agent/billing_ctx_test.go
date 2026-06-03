package agent

import (
	"context"
	"testing"

	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/billing"
)

// T5 (agent-mode-billing): injectAgentBillingCtx wires billing for an agent run
// so every aiservice.Chat call bills via ContextBudgetCredits bill-only mode,
// charging the run initiator (req.UserID); IsTest routes to the admin_test pool.
func TestInjectAgentBillingCtx_StudentRun(t *testing.T) {
	ctx := injectAgentBillingCtx(context.Background(), RunRequest{UserID: 7, IsTest: false}, 42)

	bc := billing.FromContext(ctx)
	if bc == nil || bc.UserID != 7 || bc.Operation != "agent_run" {
		t.Fatalf("billing ctx: %+v", bc)
	}
	if got := bc.Meta["run_id"]; got != "42" {
		t.Errorf("meta run_id: got %q, want 42", got)
	}
	if got := aismw.ReservationRefFromCtx(ctx); got != "agent_run:42" {
		t.Errorf("reservation ref: got %q, want agent_run:42", got)
	}
	if got := aismw.BillingPoolFromCtx(ctx); got != "" {
		t.Errorf("pool: got %q, want '' (three-pool)", got)
	}
	if !aismw.GatewayBillingOnlyFromCtx(ctx) {
		t.Error("bill-only flag must be set for agent runs")
	}
}

func TestInjectAgentBillingCtx_TestRunUsesAdminTestPool(t *testing.T) {
	ctx := injectAgentBillingCtx(context.Background(), RunRequest{UserID: 8, IsTest: true}, 43)
	if got := aismw.BillingPoolFromCtx(ctx); got != aismw.BillingPoolAdminTest {
		t.Errorf("pool: got %q, want admin_test", got)
	}
	if got := aismw.ReservationRefFromCtx(ctx); got != "agent_run:43" {
		t.Errorf("reservation ref: got %q, want agent_run:43", got)
	}
	if !aismw.GatewayBillingOnlyFromCtx(ctx) {
		t.Error("bill-only flag must be set for test runs too")
	}
}
