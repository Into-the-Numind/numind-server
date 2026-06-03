package middleware

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/model"
)

// poolCapturingCreditService implements ContextBudgetCreditService and records the
// pool threaded through the middleware + per-call Reserve/Finalize counts.
type poolCapturingCreditService struct {
	reserveCalls  int
	finalizeCalls int
	pools         []string // BudgetReservationInput.Pool seen per reserve
}

func (c *poolCapturingCreditService) LoadUser(context.Context, uint) (*model.User, error) {
	return &model.User{}, nil
}
func (c *poolCapturingCreditService) CheckAndEstimateBudget(_ context.Context, _ *model.User, in credit.BudgetPrecheckInput) (*credit.PreCheckResult, error) {
	return &credit.PreCheckResult{Sufficient: true, EstimatedCredits: 5}, nil
}
func (c *poolCapturingCreditService) ReserveBudget(_ context.Context, _ *model.User, in credit.BudgetReservationInput) (*credit.Reservation, error) {
	c.reserveCalls++
	c.pools = append(c.pools, in.Pool) // assert ctx pool → credit input threading
	return &credit.Reservation{ID: uint64(c.reserveCalls), ReservedCredits: 5, Status: credit.StatusReserved}, nil
}
func (c *poolCapturingCreditService) FinalizeReservation(context.Context, uint64, int64, string) error {
	c.finalizeCalls++
	return nil
}
func (c *poolCapturingCreditService) Refund(context.Context, uint64, string) error { return nil }

// ctxCheckingCreditService records the ctx error seen by Refund — to prove the
// settle write runs on a non-cancelled ctx (agent-mode-billing T11).
type ctxCheckingCreditService struct {
	poolCapturingCreditService
	refundCalled bool
	refundCtxErr error
}

func (c *ctxCheckingCreditService) Refund(ctx context.Context, _ uint64, _ string) error {
	c.refundCalled = true
	c.refundCtxErr = ctx.Err()
	return nil
}

// T11 fix: finalizeReservationIfNeeded settles on a non-cancelled ctx so that a
// refund triggered by a cancelled request still PERSISTS promptly (not stuck
// until the 1h sweeper).
func TestFinalizeReservation_RefundPersistsOnCancelledCtx(t *testing.T) {
	cs := &ctxCheckingCreditService{}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel() // request ctx already cancelled (user/admin cancel / disconnect)

	finalizeReservationIfNeeded(cancelled, Deps{CreditService: cs, Logger: &mockLogger{}},
		FinalizeInput{ReservationID: 7, Refund: true, ErrorCode: "user_cancelled"})

	if !cs.refundCalled {
		t.Fatal("Refund should be called on cancel")
	}
	if cs.refundCtxErr != nil {
		t.Errorf("Refund must run on a non-cancelled ctx, got ctx.Err()=%v", cs.refundCtxErr)
	}
}

// T12 (agent-mode-billing): integration of T2+T3-input+T4+T5 — a bill-only agent
// request with pool=admin_test in ctx threads the pool all the way to
// ReserveBudget, preserves tool-structured messages, and bills PER CALL.
func TestBillOnly_Integration_PoolThreadingAndPerCallBilling(t *testing.T) {
	cs := &poolCapturingCreditService{}
	// prepareResult is never consulted in bill-only mode (synthBillOnlyResult is
	// used instead); the mock only needs to be non-nil so ContextBudget != nil
	// avoids the passthrough short-circuit, and so Finalize is counted.
	budgetSvc := &mockContextBudgetService{prepareResult: makePrepareResult(nil, true)}
	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: cs,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, req interface{}) (interface{}, error) {
		return &aiservice.ChatResponse{
			Content: "ok",
			Usage:   aiservice.TokenUsage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60},
		}, nil
	})
	handler := ContextBudgetCredits(deps)(adapter)

	// Agent ctx: bill-only + admin_test pool (parent Builder 试聊) + userID.
	ctx := billing.WithBillingMeta(context.Background(), 9, "agent_run", nil)
	ctx = WithUserID(ctx, 9)
	ctx = WithBillingPool(ctx, BillingPoolAdminTest)
	ctx = WithGatewayBillingOnly(ctx)

	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleAssistant, ToolCalls: []aiservice.ToolCall{
				{ID: "c1", Type: "function", Function: aiservice.ToolCallFunction{Name: "kb_search", Arguments: "{}"}},
			}},
			{Role: aiservice.MessageRoleTool, ToolCallID: "c1", Content: aiservice.MessageContent{Text: "r"}},
		},
	}

	// Two LLM calls in the run → two independent Reserve/Reconcile (per-call billing).
	for i := 0; i < 2; i++ {
		if _, err := handler(ctx, budgetRoute(), req); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if cs.reserveCalls != 2 {
		t.Errorf("per-call Reserve: got %d, want 2", cs.reserveCalls)
	}
	if cs.finalizeCalls != 2 {
		t.Errorf("per-call credit Finalize: got %d, want 2", cs.finalizeCalls)
	}
	if got := budgetSvc.finalizeCallCount(); got != 2 {
		t.Errorf("per-call ContextBudget.Finalize: got %d, want 2", got)
	}
	for i, p := range cs.pools {
		if p != BillingPoolAdminTest {
			t.Errorf("reserve %d pool: got %q, want admin_test (ctx pool not threaded to credit input)", i, p)
		}
	}
}

// Regression: a NON-agent request (no bill-only flag, no fragments) still
// short-circuits — zero billing, zero behaviour change.
func TestBillOnly_Integration_NonAgentPassthroughUnchanged(t *testing.T) {
	cs := &poolCapturingCreditService{}
	deps := Deps{
		ContextBudget: &mockContextBudgetService{prepareResult: makePrepareResult(nil, true)},
		CreditService: cs,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}
	var called bool
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		called = true
		return &aiservice.ChatResponse{Content: "ok"}, nil
	})
	handler := ContextBudgetCredits(deps)(adapter)

	// No bill-only flag, no ContextFragments → Step 1 short-circuit.
	req := aiservice.ChatRequest{Messages: []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
	}}
	ctx := billing.WithBillingMeta(context.Background(), 1, "sop_run", nil)
	if _, err := handler(ctx, budgetRoute(), req); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !called {
		t.Fatal("provider should be called via passthrough")
	}
	if cs.reserveCalls != 0 {
		t.Errorf("passthrough must not Reserve: got %d", cs.reserveCalls)
	}
}
