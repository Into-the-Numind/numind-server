package middleware

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Mock ContextBudgetService
// ----------------------------------------------------------------------------

type mockContextBudgetService struct {
	prepareResult *PrepareResult
	prepareErr    error
	finalizeCalls []FinalizeInput
	finalizeErr   error
	mu            sync.Mutex
}

func (m *mockContextBudgetService) Prepare(_ context.Context, _ PrepareInput) (*PrepareResult, error) {
	return m.prepareResult, m.prepareErr
}

func (m *mockContextBudgetService) Finalize(_ context.Context, input FinalizeInput) error {
	m.mu.Lock()
	m.finalizeCalls = append(m.finalizeCalls, input)
	m.mu.Unlock()
	return m.finalizeErr
}

// finalizeCallCount returns the number of times Finalize was called (thread-safe).
func (m *mockContextBudgetService) finalizeCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.finalizeCalls)
}

// lastFinalizeInput returns the most recent FinalizeInput (thread-safe).
func (m *mockContextBudgetService) lastFinalizeInput() FinalizeInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.finalizeCalls) == 0 {
		return FinalizeInput{}
	}
	return m.finalizeCalls[len(m.finalizeCalls)-1]
}

// ----------------------------------------------------------------------------
// Mock ContextBudgetCreditService
// ----------------------------------------------------------------------------

type mockCreditService struct {
	checkResult   *credit.PreCheckResult
	checkErr      error
	reserveResult *credit.Reservation
	reserveErr    error
	finalizeErr   error
	refundErr     error
	reserveCalls  int
	finalizeCalls int
	refundCalls   int
	mu            sync.Mutex
}

// LoadUser returns a non-nil placeholder *model.User. The mock doesn't care
// about its contents, but middleware code now calls LoadUser before
// CheckAndEstimateBudget per spec §6.1.2 (S5 found a panic when user was nil).
// Post legacy-deprecation (T1) every user routes through the credits path.
func (m *mockCreditService) LoadUser(_ context.Context, _ uint) (*model.User, error) {
	return &model.User{}, nil
}

func (m *mockCreditService) CheckAndEstimateBudget(_ context.Context, _ *model.User, _ credit.BudgetPrecheckInput) (*credit.PreCheckResult, error) {
	return m.checkResult, m.checkErr
}

func (m *mockCreditService) ReserveBudget(_ context.Context, _ *model.User, _ credit.BudgetReservationInput) (*credit.Reservation, error) {
	m.mu.Lock()
	m.reserveCalls++
	m.mu.Unlock()
	return m.reserveResult, m.reserveErr
}

func (m *mockCreditService) FinalizeReservation(_ context.Context, _ uint64, _ int64, _ string) error {
	m.mu.Lock()
	m.finalizeCalls++
	m.mu.Unlock()
	return m.finalizeErr
}

func (m *mockCreditService) Refund(_ context.Context, _ uint64, _ string) error {
	m.mu.Lock()
	m.refundCalls++
	m.mu.Unlock()
	return m.refundErr
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// budgetRoute builds a ResolvedRoute with a filled Capability for budget tests.
func budgetRoute() *registry.ResolvedRoute {
	route := llmRoute()
	// Add capability for budget tests.
	route.Capability = profileCapability()
	return route
}

func profileCapability() profile.ServiceCapability {
	return profile.ServiceCapability{
		ContextWindow:   128000,
		MaxOutputTokens: 4096,
	}
}

// chatReqWithFragments builds a ChatRequest that carries ContextFragments.
func chatReqWithFragments(fragments ...contextbudget.ContextFragment) aiservice.ChatRequest {
	return aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hello"}},
		},
		ContextFragments: fragments,
	}
}

// simpleFragment creates a minimal ContextFragment for testing.
func simpleFragment(id, content string) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleRecent,
		Source:          contextbudget.SourceUser,
		ContentType:     contextbudget.ContentText,
		Content:         content,
		Compressibility: contextbudget.CompressNone,
		Critical:        true,
	}
}

// makePrepareResult creates a PrepareResult indicating a successful budget run.
func makePrepareResult(renderedMessages []aiservice.ChatMessage, chargeUser bool) *PrepareResult {
	return &PrepareResult{
		Fragments: []contextbudget.ContextFragment{simpleFragment("f1", "hi")},
		Messages:  renderedMessages,
		Plan: contextbudget.Plan{
			Feasible:       true,
			EstimatedAfter: 100,
		},
		EstimatedBefore: 120,
		EstimatedAfter:  100,
		SafeInputBudget: 50000,
		Policy: contextbudget.BudgetPolicy{
			Operation:            "sop_run",
			ReservedOutputTokens: 512,
			SafeRatio:            0.80,
			ChargeUser:           chargeUser,
		},
		TokenProfileID: 1,
		EventID:        42,
		NormalizedOp:   "sop_run",
		SkipBudget:     false,
	}
}

// ----------------------------------------------------------------------------
// Test: Under-budget request reserves credits and renders fragments
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_UnderBudgetReservesAndRendersFragments verifies that
// when Prepare succeeds (under budget, ChargeUser=true), the middleware:
//   - injects the rendered Messages into ChatRequest.Messages
//   - calls ReserveBudget exactly once
//   - calls Finalize exactly once after the provider call
func TestContextBudgetCredits_UnderBudgetReservesAndRendersFragments(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: "[system context]"}},
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hello"}},
	}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult(renderedMsgs, true),
	}
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 10,
		},
		reserveResult: &credit.Reservation{
			ID:              99,
			ReservedCredits: 10,
			Status:          credit.StatusReserved,
		},
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
			Content: "answer",
			Usage: aiservice.TokenUsage{
				PromptTokens:     80,
				CompletionTokens: 20,
				TotalTokens:      100,
			},
		}, nil
	})

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "user message")
	req := chatReqWithFragments(fragment)

	// Provide billing ctx so the middleware knows UserID.
	ctx := billing.WithBillingMeta(context.Background(), 7, "sop_run", nil)
	ctx = WithUserID(ctx, 7)

	resp, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a response")
	}

	// The captured request must have the rendered messages injected.
	if capturedReq == nil {
		t.Fatal("adapter was not called")
	}
	capturedChat, ok := capturedReq.(aiservice.ChatRequest)
	if !ok {
		t.Fatalf("capturedReq type: got %T, want ChatRequest", capturedReq)
	}
	if len(capturedChat.Messages) != len(renderedMsgs) {
		t.Errorf("Messages length: got %d, want %d", len(capturedChat.Messages), len(renderedMsgs))
	}
	for i, want := range renderedMsgs {
		if capturedChat.Messages[i].Role != want.Role {
			t.Errorf("Messages[%d].Role: got %q, want %q", i, capturedChat.Messages[i].Role, want.Role)
		}
	}

	// ReserveBudget must be called exactly once.
	if creditSvc.reserveCalls != 1 {
		t.Errorf("ReserveBudget calls: got %d, want 1", creditSvc.reserveCalls)
	}

	// Finalize must be called exactly once.
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
}

// ----------------------------------------------------------------------------
// Test: Over-budget request fails before provider call when planner cannot fit
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_OverBudgetFailsBeforeProviderWhenPlannerCannotFit
// verifies that when Prepare returns ErrContextTooLarge:
//   - the error is returned to the caller
//   - the adapter (next) is never called
func TestContextBudgetCredits_OverBudgetFailsBeforeProviderWhenPlannerCannotFit(t *testing.T) {
	budgetSvc := &mockContextBudgetService{
		prepareErr: contextbudget.ErrContextTooLarge,
	}

	adapterCalled := false
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		adapterCalled = true
		return "should-not-get-here", nil
	})

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: &mockCreditService{},
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "a very long message that exceeds the context budget")
	req := chatReqWithFragments(fragment)
	ctx := billing.WithBillingMeta(context.Background(), 5, "sop_run", nil)
	ctx = WithUserID(ctx, 5)

	_, err := handler(ctx, budgetRoute(), req)

	// Error must be non-nil and must wrap ErrContextTooLarge.
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, contextbudget.ErrContextTooLarge) {
		t.Errorf("expected ErrContextTooLarge, got %v", err)
	}

	// Adapter must NOT be called.
	if adapterCalled {
		t.Error("adapter should not have been called when context is too large")
	}
}

// ----------------------------------------------------------------------------
// Test: ChargeUser=false does NOT create a user credit reservation
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_CompressionOperationDoesNotReserveUserCredits verifies
// that when the policy has ChargeUser=false (e.g. context_compression), the
// middleware calls Prepare successfully but never calls ReserveBudget.
func TestContextBudgetCredits_CompressionOperationDoesNotReserveUserCredits(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "compress this"}},
	}

	budgetSvc := &mockContextBudgetService{
		// ChargeUser=false: compression operation.
		prepareResult: makePrepareResult(renderedMsgs, false),
	}
	creditSvc := &mockCreditService{}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return &aiservice.ChatResponse{Content: "compressed"}, nil
	})

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "data to compress")
	req := chatReqWithFragments(fragment)
	ctx := context.Background()

	_, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ReserveBudget must NOT be called when ChargeUser=false.
	if creditSvc.reserveCalls != 0 {
		t.Errorf("ReserveBudget calls: got %d, want 0 (ChargeUser=false)", creditSvc.reserveCalls)
	}

	// Finalize must still be called for event tracking.
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
}

// ----------------------------------------------------------------------------
// Test: Streaming — final usage chunk triggers reconcile exactly once
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_StreamFinalUsageReconcilesOnce verifies that when a
// stream emits a final chunk with Usage, Finalize is called exactly once with
// the actual token counts.
func TestContextBudgetCredits_StreamFinalUsageReconcilesOnce(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "stream request"}},
	}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult(renderedMsgs, true),
	}
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 15,
		},
		reserveResult: &credit.Reservation{
			ID:              101,
			ReservedCredits: 15,
			Status:          credit.StatusReserved,
		},
	}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Build stream with multiple chunks + final usage.
	chunks := []aiservice.ChatChunk{
		{Delta: "chunk1", Index: 0},
		{Delta: "chunk2", Index: 1},
		{
			Delta:   "",
			Index:   2,
			IsFinal: true,
			Usage: &aiservice.TokenUsage{
				PromptTokens:     300,
				CompletionTokens: 50,
				TotalTokens:      350,
			},
		},
	}
	adapter := streamHandler(chunks)

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "stream input")
	req := chatReqWithFragments(fragment)
	ctx := billing.WithBillingMeta(context.Background(), 3, "sop_run", nil)
	ctx = WithUserID(ctx, 3)

	resp, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatalf("expected <-chan ChatChunk, got %T", resp)
	}

	// Drain the stream.
	var received []aiservice.ChatChunk
	for c := range ch {
		received = append(received, c)
	}

	// All chunks must be forwarded.
	if len(received) != len(chunks) {
		t.Errorf("forwarded %d chunks, want %d", len(received), len(chunks))
	}

	// Finalize must be called exactly once with actual usage.
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
	fi := budgetSvc.lastFinalizeInput()
	if fi.ActualPromptTokens != 300 {
		t.Errorf("FinalizeInput.ActualPromptTokens: got %d, want 300", fi.ActualPromptTokens)
	}
	if fi.ActualCompletionTokens != 50 {
		t.Errorf("FinalizeInput.ActualCompletionTokens: got %d, want 50", fi.ActualCompletionTokens)
	}
	if fi.Status != "ok" {
		t.Errorf("FinalizeInput.Status: got %q, want %q", fi.Status, "ok")
	}
	if fi.Refund {
		t.Error("FinalizeInput.Refund should be false on successful finalization")
	}
}

// ----------------------------------------------------------------------------
// Test: Streaming — channel close without usage finalizes with estimated
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_StreamCloseWithoutUsageFinalizesEstimated verifies
// that when the stream channel closes without a final IsFinal chunk, Finalize
// is called exactly once with CalibrationSkipped=true.
func TestContextBudgetCredits_StreamCloseWithoutUsageFinalizesEstimated(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "stream no-usage"}},
	}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult(renderedMsgs, true),
	}
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 10,
		},
		reserveResult: &credit.Reservation{
			ID:              102,
			ReservedCredits: 10,
			Status:          credit.StatusReserved,
		},
	}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Stream ends without IsFinal — just closes.
	chunks := []aiservice.ChatChunk{
		{Delta: "partial1", Index: 0},
		{Delta: "partial2", Index: 1},
		// No IsFinal chunk.
	}
	adapter := streamHandler(chunks)

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "stream input")
	req := chatReqWithFragments(fragment)
	ctx := billing.WithBillingMeta(context.Background(), 4, "sop_run", nil)
	ctx = WithUserID(ctx, 4)

	resp, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch := resp.(<-chan aiservice.ChatChunk)
	for range ch {
	}

	// Finalize called exactly once.
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
	fi := budgetSvc.lastFinalizeInput()
	if !fi.CalibrationSkipped {
		t.Error("FinalizeInput.CalibrationSkipped should be true when stream closed without usage")
	}
}

// ----------------------------------------------------------------------------
// Test: budgetMetadata carries reserved_output_tokens, reservation_id, and
//       compression_status (P2-A + P2-B + P2-C spec compliance fixes)
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_BudgetMetadataIncludesReservedOutputAndReservationID
// verifies that after a successful ChargeUser=true call where the planner
// produced at least one non-keep action:
//
//   - budgetMetadata.ReservedOutputTokens == Policy.ReservedOutputTokens (P2-B)
//   - budgetMetadata.ReservationID == reservation.ID (P2-C)
//   - budgetMetadata.CompressionStatus == "compressed" (P2-A)
func TestContextBudgetCredits_BudgetMetadataIncludesReservedOutputAndReservationID(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "metadata test"}},
	}

	// Build PrepareResult with:
	//   - a plan that has ActionKeep + ActionSummarize (to trigger "compressed")
	//   - Policy.ReservedOutputTokens = 8192
	prepResult := makePrepareResult(renderedMsgs, true)
	prepResult.Plan.Actions = []contextbudget.Action{
		{FragmentID: "f1", Type: contextbudget.ActionKeep},
		{FragmentID: "f2", Type: contextbudget.ActionSummarize},
	}
	prepResult.Policy.ReservedOutputTokens = 8192

	budgetSvc := &mockContextBudgetService{
		prepareResult: prepResult,
	}
	// Reserve returns reservation.ID = 42.
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 20,
		},
		reserveResult: &credit.Reservation{
			ID:              42,
			ReservedCredits: 20,
			Status:          credit.StatusReserved,
		},
	}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Spy adapter: capture the ctx so we can extract budgetMetadata after next()
	// is called (the middleware injects metadata into ctx before calling next).
	var capturedBM budgetMetadata
	var capturedBMOk bool
	adapter := Handler(func(ctx context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		capturedBM, capturedBMOk = budgetMetadataFromCtx(ctx)
		return &aiservice.ChatResponse{Content: "ok"}, nil
	})

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "metadata input")
	req := chatReqWithFragments(fragment)
	ctx := billing.WithBillingMeta(context.Background(), 9, "sop_run", nil)
	ctx = WithUserID(ctx, 9)

	_, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !capturedBMOk {
		t.Fatal("budgetMetadata not found in ctx (withBudgetMetadata not called or ctx not forwarded)")
	}

	// P2-B: reserved_output_tokens must equal Policy.ReservedOutputTokens.
	if capturedBM.ReservedOutputTokens != 8192 {
		t.Errorf("budgetMetadata.ReservedOutputTokens: got %d, want 8192", capturedBM.ReservedOutputTokens)
	}

	// P2-C: reservation_id must equal the reservation.ID returned by ReserveBudget.
	if capturedBM.ReservationID != 42 {
		t.Errorf("budgetMetadata.ReservationID: got %d, want 42", capturedBM.ReservationID)
	}

	// P2-A: compression_status must be "compressed" because plan has ActionSummarize.
	if capturedBM.CompressionStatus != "compressed" {
		t.Errorf("budgetMetadata.CompressionStatus: got %q, want %q", capturedBM.CompressionStatus, "compressed")
	}
}

// ----------------------------------------------------------------------------
// Test: Context cancellation refunds without usage
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_ContextCancelledRefundsWithoutUsage verifies that
// when the caller's context is cancelled before any final usage chunk arrives,
// Finalize is called exactly once with Refund=true.
func TestContextBudgetCredits_ContextCancelledRefundsWithoutUsage(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "cancel me"}},
	}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult(renderedMsgs, true),
	}
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 8,
		},
		reserveResult: &credit.Reservation{
			ID:              103,
			ReservedCredits: 8,
			Status:          credit.StatusReserved,
		},
	}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Use a slow inner channel that we'll close after cancellation.
	innerCh := make(chan aiservice.ChatChunk, 1)
	slowAdapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return (<-chan aiservice.ChatChunk)(innerCh), nil
	})

	mw := ContextBudgetCredits(deps)
	handler := mw(slowAdapter)

	fragment := simpleFragment("f1", "cancel input")
	req := chatReqWithFragments(fragment)

	ctx, cancel := context.WithCancel(context.Background())
	ctx = billing.WithBillingMeta(ctx, 6, "sop_run", nil)
	ctx = WithUserID(ctx, 6)

	resp, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ch := resp.(<-chan aiservice.ChatChunk)

	// Cancel context to trigger the ctx.Done() path.
	cancel()
	// Close inner channel so the wrapper goroutine's drain loop exits.
	close(innerCh)

	// Drain the wrapper.
	for range ch {
	}

	// Finalize must be called exactly once with Refund=true.
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
	fi := budgetSvc.lastFinalizeInput()
	if !fi.Refund {
		t.Error("FinalizeInput.Refund should be true on context cancellation")
	}
	if fi.ErrorCode != "user_cancelled" {
		t.Errorf("FinalizeInput.ErrorCode: got %q, want %q", fi.ErrorCode, "user_cancelled")
	}
}

// ----------------------------------------------------------------------------
// Test: Streaming — final chunk with error AND usage → reconcile with error code
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_StreamFinalErrWithUsageReconcilesWithErrorCode verifies
// that when the upstream stream emits a final chunk with both an error and usage
// data, the wrapper reconciles using the actual usage but tags the finalize
// status with the provider error code, ensuring credits are charged for tokens
// that were actually generated before the error occurred.
func TestContextBudgetCredits_StreamFinalErrWithUsageReconcilesWithErrorCode(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "stream with err+usage"}},
	}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult(renderedMsgs, true),
	}
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 12,
		},
		reserveResult: &credit.Reservation{
			ID:              201,
			ReservedCredits: 12,
			Status:          credit.StatusReserved,
		},
	}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Stream emits a single IsFinal chunk with both Err and Usage (provider
	// generated tokens but terminated with an error, e.g. a timeout mid-stream).
	providerErr := errors.New("provider timeout")
	chunks := []aiservice.ChatChunk{
		{Delta: "partial output", Index: 0},
		{
			Delta:   "",
			Index:   1,
			IsFinal: true,
			Err:     providerErr,
			Usage: &aiservice.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		},
	}
	adapter := streamHandler(chunks)

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "stream err+usage input")
	req := chatReqWithFragments(fragment)
	ctx := billing.WithBillingMeta(context.Background(), 11, "sop_run", nil)
	ctx = WithUserID(ctx, 11)

	resp, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error from handler: %v", err)
	}

	ch, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatalf("expected <-chan ChatChunk, got %T", resp)
	}

	// Drain the stream.
	for range ch {
	}

	// Finalize must be called exactly once.
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
	fi := budgetSvc.lastFinalizeInput()

	// Actual token counts must be populated (reconcile, not refund).
	if fi.ActualPromptTokens != 100 {
		t.Errorf("FinalizeInput.ActualPromptTokens: got %d, want 100", fi.ActualPromptTokens)
	}
	if fi.ActualCompletionTokens != 50 {
		t.Errorf("FinalizeInput.ActualCompletionTokens: got %d, want 50", fi.ActualCompletionTokens)
	}

	// Refund must be false — we have usage, so we reconcile.
	if fi.Refund {
		t.Error("FinalizeInput.Refund should be false when usage is present (reconcile path)")
	}

	// ErrorCode must be set to indicate the provider error.
	if fi.ErrorCode != "provider_err" {
		t.Errorf("FinalizeInput.ErrorCode: got %q, want %q", fi.ErrorCode, "provider_err")
	}

	// Status should reflect "ok" from the reconcile path (usage was captured).
	if fi.Status != "ok" {
		t.Errorf("FinalizeInput.Status: got %q, want %q", fi.Status, "ok")
	}
}

// ----------------------------------------------------------------------------
// Test: Streaming — final chunk with error and NO usage → refund
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_StreamFinalErrWithoutUsageRefunds verifies that when
// the upstream stream emits a final chunk with an error but no usage data
// (e.g., connection failed mid-stream before any tokens were accounted for),
// the wrapper refunds the reservation instead of reconciling with stale estimates.
func TestContextBudgetCredits_StreamFinalErrWithoutUsageRefunds(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "stream err no-usage"}},
	}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult(renderedMsgs, true),
	}
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 8,
		},
		reserveResult: &credit.Reservation{
			ID:              202,
			ReservedCredits: 8,
			Status:          credit.StatusReserved,
		},
	}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Stream emits a final chunk with Err but nil Usage — connection dropped
	// before the provider reported any token usage.
	chunks := []aiservice.ChatChunk{
		{
			Delta:   "",
			Index:   0,
			IsFinal: true,
			Err:     errors.New("connection reset by provider"),
			Usage:   nil,
		},
	}
	adapter := streamHandler(chunks)

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "stream err no-usage input")
	req := chatReqWithFragments(fragment)
	ctx := billing.WithBillingMeta(context.Background(), 12, "sop_run", nil)
	ctx = WithUserID(ctx, 12)

	resp, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error from handler: %v", err)
	}

	ch, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatalf("expected <-chan ChatChunk, got %T", resp)
	}

	// Drain the stream.
	for range ch {
	}

	// Finalize must be called exactly once.
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
	fi := budgetSvc.lastFinalizeInput()

	// Refund must be true — no usage data, so we can't reconcile.
	if !fi.Refund {
		t.Error("FinalizeInput.Refund should be true when IsFinal has Err and no Usage")
	}

	// ErrorCode must be set to indicate the provider error.
	if fi.ErrorCode != "provider_err" {
		t.Errorf("FinalizeInput.ErrorCode: got %q, want %q", fi.ErrorCode, "provider_err")
	}

	// Status should be "failed".
	if fi.Status != "failed" {
		t.Errorf("FinalizeInput.Status: got %q, want %q", fi.Status, "failed")
	}

	// No actual tokens should be reported.
	if fi.ActualPromptTokens != 0 || fi.ActualCompletionTokens != 0 {
		t.Errorf("FinalizeInput token counts should be 0 on refund path, got prompt=%d completion=%d",
			fi.ActualPromptTokens, fi.ActualCompletionTokens)
	}
}

// ----------------------------------------------------------------------------
// Test: Non-streaming — provider error triggers refund
// ----------------------------------------------------------------------------

// TestContextBudgetCredits_NonStreamingProviderErrorRefunds verifies that when
// the inner adapter returns an error (no stream, no response), the middleware
// refunds the reservation rather than reconciling.
func TestContextBudgetCredits_NonStreamingProviderErrorRefunds(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "non-streaming error"}},
	}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult(renderedMsgs, true),
	}
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 10,
		},
		reserveResult: &credit.Reservation{
			ID:              301,
			ReservedCredits: 10,
			Status:          credit.StatusReserved,
		},
	}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Adapter returns an error — provider unavailable.
	providerErr := errors.New("provider unavailable")
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		return nil, providerErr
	})

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "non-streaming error input")
	req := chatReqWithFragments(fragment)
	ctx := billing.WithBillingMeta(context.Background(), 13, "sop_run", nil)
	ctx = WithUserID(ctx, 13)

	resp, err := handler(ctx, budgetRoute(), req)

	// The middleware must propagate the provider error.
	if err == nil {
		t.Fatal("expected an error from provider, got nil")
	}

	// Response must be nil (no partial data to return).
	if resp != nil {
		t.Errorf("expected nil response on provider error, got %v", resp)
	}

	// Finalize must be called exactly once.
	if budgetSvc.finalizeCallCount() != 1 {
		t.Errorf("Finalize calls: got %d, want 1", budgetSvc.finalizeCallCount())
	}
	fi := budgetSvc.lastFinalizeInput()

	// Refund must be true — provider returned an error, no tokens consumed.
	if !fi.Refund {
		t.Error("FinalizeInput.Refund should be true when provider returns an error")
	}

	// ErrorCode must be set.
	if fi.ErrorCode != "provider_err" {
		t.Errorf("FinalizeInput.ErrorCode: got %q, want %q", fi.ErrorCode, "provider_err")
	}

	// Status must be "failed".
	if fi.Status != "failed" {
		t.Errorf("FinalizeInput.Status: got %q, want %q", fi.Status, "failed")
	}
}

// ============================================================================
// F-3 cost-calibration: reconciler reads actual cost from finalCostHolder
// ============================================================================

// TestContextBudgetCredits_ReconcileUsesActualCostFromBillingHolder verifies
// that when the *finalCostHolder (injected by ContextBudgetCredits after
// Reserve) is populated with a real cost by the inner Billing middleware,
// FinalizeReservation is called with that real cost rather than the pre-call
// EstimatedCredits placeholder.
//
// The test simulates the Billing middleware by using a spy adapter that locates
// the holder in ctx and sets CostCents directly (bypassing the real Billing
// middleware to keep the test focused on the ContextBudgetCredits reconciler).
// The Billing-side population is separately verified by
// TestBillingSetsFinalCostInHolderWhenPresent in billing_test.go.
func TestContextBudgetCredits_ReconcileUsesActualCostFromBillingHolder(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "cost calibration test"}},
	}

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResult(renderedMsgs, true),
	}
	// EstimatedCredits = 8192 (placeholder from ReservedOutputTokens).
	// The real cost (5 cents) should come from the holder, not EstimatedCredits.
	creditSvc := &mockCreditService{
		checkResult: &credit.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 8192, // this is the placeholder that was incorrectly used before
		},
		reserveResult: &credit.Reservation{
			ID:              500,
			ReservedCredits: 8192,
			Status:          credit.StatusReserved,
		},
	}

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Spy adapter: simulates what the inner Billing middleware does — finds the
	// finalCostHolder in ctx and populates it with the real pricing-rule cost.
	const realCostCents int64 = 5
	adapter := Handler(func(ctx context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		// Simulate Billing populating the holder after computing actual cost.
		// F-7: use Set() so the set flag is raised (including for cost=0 calls).
		if h := finalCostHolderFromCtx(ctx); h != nil {
			h.Set(realCostCents)
		}
		return &aiservice.ChatResponse{
			Content: "answer",
			Usage: aiservice.TokenUsage{
				PromptTokens:     800,
				CompletionTokens: 50,
				TotalTokens:      850,
			},
		}, nil
	})

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	fragment := simpleFragment("f1", "input")
	req := chatReqWithFragments(fragment)
	ctx := billing.WithBillingMeta(context.Background(), 20, "sop_run", nil)
	ctx = WithUserID(ctx, 20)

	_, err := handler(ctx, budgetRoute(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// FinalizeReservation must be called exactly once.
	if creditSvc.finalizeCalls != 1 {
		t.Errorf("FinalizeReservation calls: got %d, want 1", creditSvc.finalizeCalls)
	}

	// The actual credits passed to FinalizeReservation must be the real cost
	// from the holder, NOT the EstimatedCredits placeholder (8192).
	//
	// We cannot directly inspect the actualCredits argument from the mock (the
	// existing mockCreditService doesn't capture it), so we verify indirectly:
	// the holder was Set to realCostCents and the code path in
	// finalizeReservationIfNeeded prefers holder.Get() when ok=true (F-7 fix).
	// The test therefore verifies the holder mechanism by checking the holder
	// is accessible from the ctx that ContextBudgetCredits passes to finalize.
	//
	// To make the assertion direct, we add a captureActualCredits field to a
	// local spy credit service override.
	// Re-run with a capturing mock to get the actual argument.

	var capturedActualCredits int64
	capturingSvc := &capturingCreditService{
		mockCreditService: mockCreditService{
			checkResult:   creditSvc.checkResult,
			reserveResult: creditSvc.reserveResult,
		},
	}
	capturingSvc.onFinalize = func(credits int64) { capturedActualCredits = credits }

	deps2 := deps
	deps2.CreditService = capturingSvc

	mw2 := ContextBudgetCredits(deps2)
	handler2 := mw2(adapter)

	ctx2 := billing.WithBillingMeta(context.Background(), 21, "sop_run", nil)
	ctx2 = WithUserID(ctx2, 21)

	_, err = handler2(ctx2, budgetRoute(), chatReqWithFragments(simpleFragment("f2", "input2")))
	if err != nil {
		t.Fatalf("unexpected error on second run: %v", err)
	}

	if capturedActualCredits != realCostCents {
		t.Errorf("FinalizeReservation actualCredits = %d, want %d (real cost from holder, not EstimatedCredits=8192)",
			capturedActualCredits, realCostCents)
	}
}

// capturingCreditService is a local test double that extends mockCreditService
// to capture the actualCredits argument passed to FinalizeReservation.
type capturingCreditService struct {
	mockCreditService
	onFinalize func(credits int64)
}

func (c *capturingCreditService) FinalizeReservation(ctx context.Context, reservationID uint64, actualCredits int64, reason string) error {
	if c.onFinalize != nil {
		c.onFinalize(actualCredits)
	}
	return c.mockCreditService.FinalizeReservation(ctx, reservationID, actualCredits, reason)
}
