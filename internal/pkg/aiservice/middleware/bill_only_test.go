package middleware

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
)

// T4 (agent-mode-billing): bill-only gateway mode bills the agent's LLM call
// WITHOUT compressing or replacing Messages — so ReAct tool structure
// (tool_calls / tool_call_id) survives — and still runs Reserve/Reconcile.
func TestContextBudgetCredits_BillOnly_PreservesToolMessagesAndReserves(t *testing.T) {
	// If Prepare WERE called, it would replace Messages with this sentinel.
	rendered := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "RENDERED-SHOULD-NOT-APPEAR"}},
	}
	budgetSvc := &mockContextBudgetService{prepareResult: makePrepareResult(rendered, true)}
	creditSvc := &mockCreditService{
		checkResult:   &credit.PreCheckResult{Sufficient: true, EstimatedCredits: 10},
		reserveResult: &credit.Reservation{ID: 99, ReservedCredits: 10, Status: credit.StatusReserved},
	}
	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	var capturedReq interface{}
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, req interface{}) (interface{}, error) {
		capturedReq = req
		return &aiservice.ChatResponse{
			Content: "ok",
			Usage:   aiservice.TokenUsage{PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100},
		}, nil
	})
	handler := ContextBudgetCredits(deps)(adapter)

	// Agent ReAct request: tool-structured messages, NO fragments.
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: "你是助手"}},
			{Role: aiservice.MessageRoleAssistant, ToolCalls: []aiservice.ToolCall{
				{ID: "call_1", Type: "function", Function: aiservice.ToolCallFunction{Name: "web_search", Arguments: "{}"}},
			}},
			{Role: aiservice.MessageRoleTool, ToolCallID: "call_1", Content: aiservice.MessageContent{Text: "result"}},
		},
	}
	ctx := billing.WithBillingMeta(context.Background(), 7, "agent_run", nil)
	ctx = WithUserID(ctx, 7)
	ctx = WithGatewayBillingOnly(ctx)

	if _, err := handler(ctx, budgetRoute(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	capturedChat, ok := capturedReq.(aiservice.ChatRequest)
	if !ok {
		t.Fatalf("capturedReq type: got %T", capturedReq)
	}
	if len(capturedChat.Messages) != 3 {
		t.Fatalf("Messages length: got %d, want 3 (preserved, not rendered)", len(capturedChat.Messages))
	}
	if len(capturedChat.Messages[1].ToolCalls) == 0 || capturedChat.Messages[1].ToolCalls[0].ID != "call_1" {
		t.Error("assistant tool_calls dropped by bill-only path")
	}
	if capturedChat.Messages[2].ToolCallID != "call_1" {
		t.Error("tool message tool_call_id dropped by bill-only path")
	}
	for _, m := range capturedChat.Messages {
		if m.Content.Text == "RENDERED-SHOULD-NOT-APPEAR" {
			t.Fatal("Messages were replaced by Prepare render — bill-only must skip Prepare")
		}
	}
	if capturedChat.ContextFragments != nil {
		t.Error("ContextFragments should be cleared before provider call")
	}
	// Billing happened.
	if creditSvc.reserveCalls != 1 {
		t.Errorf("ReserveBudget calls: got %d, want 1", creditSvc.reserveCalls)
	}
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
}
