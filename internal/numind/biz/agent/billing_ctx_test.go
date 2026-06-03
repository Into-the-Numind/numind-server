package agent

import (
	"context"
	"encoding/json"
	"testing"

	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/billing"
)

// T7 (agent-mode-billing): CreateRunRequest.IsTest binds from the "is_test"
// JSON field (Builder 试聊 → admin_test pool). Guards the wire-contract tag.
func TestCreateRunRequest_IsTestJSONBinding(t *testing.T) {
	var req CreateRunRequest
	body := `{"agent_skill_id":1,"input_text":"hi","is_test":true}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !req.IsTest {
		t.Error("is_test=true not bound to CreateRunRequest.IsTest")
	}
	// default (absent) must be false (three-pool).
	var req2 CreateRunRequest
	_ = json.Unmarshal([]byte(`{"agent_skill_id":1,"input_text":"hi"}`), &req2)
	if req2.IsTest {
		t.Error("is_test absent should default false")
	}
}

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
