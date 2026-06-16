package middleware

// cost_holder_zero_test.go — TDD tests for F-7 fix.
//
// F-7 bug: finalizeReservationIfNeeded used `holder.CostCents > 0` as the
// "is set" proxy. When a pricing rule has 0/0 prices (or any legitimate
// zero-cost call), publishCostToHolder set CostCents=0 (correct) but the
// check treated it as "unset" and fell back to fi.EstimatedCredits, causing
// over-charge (e.g. reservation #50: actual_cost_cents=8192, delta=+8189).
//
// Fix mirrors F-5 (commit b498a99): explicit set bool guarded by sync.Mutex,
// Get() returns (cost, ok). publishCostToHolder calls holder.Set(c);
// finalize uses ok flag from Get().
//
// Tests in this file:
//  1. Unit tests for finalCostHolder itself (Set/Get/concurrent safety).
//  2. Chain-integration tests:
//     a. cost=0 IS used when holder was Set to 0 (not EstimatedCredits fallback).
//     b. EstimatedCredits IS used when holder exists but Set was never called.

import (
	"context"
	"sync"
	"testing"
	"time"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/contextbudget"
)

// ---------------------------------------------------------------------------
// 1. Unit tests for finalCostHolder
// ---------------------------------------------------------------------------

// TestFinalCostHolder_SetGet verifies that Set then Get returns the value and ok=true.
func TestFinalCostHolder_SetGet(t *testing.T) {
	h := &finalCostHolder{}

	// Before Set: Get must return ok=false.
	if _, ok := h.Get(); ok {
		t.Fatal("empty holder should return ok=false")
	}

	h.Set(42)
	got, ok := h.Get()
	if !ok {
		t.Fatal("after Set, Get should return ok=true")
	}
	if got != 42 {
		t.Errorf("Get() = %d, want 42", got)
	}
}

// TestFinalCostHolder_ZeroCostIsSet verifies that Set(0) marks the holder as
// set. This is the core F-7 regression guard: a legitimately zero-cost call
// must NOT be confused with "holder was never populated".
func TestFinalCostHolder_ZeroCostIsSet(t *testing.T) {
	h := &finalCostHolder{}
	h.Set(0)

	got, ok := h.Get()
	if !ok {
		t.Fatal("holder.Get() ok=false after Set(0); zero cost must be treated as set")
	}
	if got != 0 {
		t.Errorf("Get() = %d, want 0", got)
	}
}

// TestFinalCostHolder_EmptyReturnsNotOK verifies Get on a brand-new holder.
func TestFinalCostHolder_EmptyReturnsNotOK(t *testing.T) {
	h := &finalCostHolder{}
	_, ok := h.Get()
	if ok {
		t.Error("newly created holder should return ok=false")
	}
}

// TestFinalCostHolder_SetOverwrites verifies a second Set replaces first.
func TestFinalCostHolder_SetOverwrites(t *testing.T) {
	h := &finalCostHolder{}
	h.Set(10)
	h.Set(0) // explicitly overwrite with zero
	got, ok := h.Get()
	if !ok {
		t.Fatal("after Set(0), ok should be true")
	}
	if got != 0 {
		t.Errorf("Get() = %d, want 0", got)
	}
}

// TestFinalCostHolder_ConcurrentSafe verifies that concurrent Set/Get does not
// race (run with -race).
func TestFinalCostHolder_ConcurrentSafe(t *testing.T) {
	h := &finalCostHolder{}
	var wg sync.WaitGroup
	for i := int64(0); i < 50; i++ {
		wg.Add(2)
		go func(v int64) {
			defer wg.Done()
			h.Set(v)
		}(i)
		go func() {
			defer wg.Done()
			_, _ = h.Get()
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// 2a. Chain-integration: cost=0 from holder is used, NOT EstimatedCredits
// ---------------------------------------------------------------------------

// TestFinalize_UsesZeroCostFromHolder verifies that when the inner Billing
// middleware calls holder.Set(0) (e.g. 0/0 pricing rule), FinalizeReservation
// is called with actualCredits=0, NOT with the EstimatedCredits placeholder.
//
// This is the exact scenario from F-7: reservation #50 was over-charged 8189
// cents because cost=0 was misread as "unset" and fell back to EstimatedCredits.
func TestFinalize_UsesZeroCostFromHolder(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "zero cost test"}},
	}

	const estimatedCredits int64 = 8192 // the placeholder that must NOT be used

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResultWithEstimated(renderedMsgs, true, estimatedCredits),
	}
	capturingSvc := &capturingCreditService{
		mockCreditService: mockCreditService{
			checkResult: &credit.PreCheckResult{
				SkipDeduction:    false,
				Sufficient:       true,
				EstimatedCredits: estimatedCredits,
			},
			reserveResult: &credit.Reservation{
				ID:              50,
				ReservedCredits: estimatedCredits,
				Status:          credit.StatusReserved,
			},
		},
	}
	var capturedActualCredits int64 = -1 // sentinel: -1 means FinalizeReservation not called
	capturingSvc.onFinalize = func(credits int64) { capturedActualCredits = credits }

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: capturingSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Spy adapter: simulates Billing calling holder.Set(0) after a 0/0 pricing rule.
	adapter := Handler(func(ctx context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		if h := finalCostHolderFromCtx(ctx); h != nil {
			h.Set(0) // cost=0 from 0/0 pricing rule
		}
		return &aiservice.ChatResponse{
			Content: "answer",
			Usage:   aiservice.TokenUsage{PromptTokens: 100, CompletionTokens: 50},
		}, nil
	})

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	ctx := billing.WithBillingMeta(context.Background(), 42, "sop_run", nil)
	ctx = WithUserID(ctx, 42)

	_, err := handler(ctx, budgetRoute(), chatReqWithFragments(simpleFragment("f1", "input")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// FinalizeReservation must be called exactly once.
	if capturingSvc.finalizeCalls != 1 {
		t.Errorf("FinalizeReservation calls: got %d, want 1", capturingSvc.finalizeCalls)
	}

	// F-7: FinalizeReservation must be called with actualCredits=0 (from holder),
	// NOT with EstimatedCredits=8192. Before the fix, cost=0 was treated as "unset"
	// and fell back to EstimatedCredits.
	if capturedActualCredits != 0 {
		t.Errorf("FinalizeReservation actualCredits = %d, want 0 (zero-cost from holder, not EstimatedCredits=%d)",
			capturedActualCredits, estimatedCredits)
	}
}

// ---------------------------------------------------------------------------
// 2b. Chain-integration: holder unset with usage → REFUND (fix ③)
// ---------------------------------------------------------------------------

// TestFinalize_RefundsWhenHolderUnsetWithUsage verifies fix ③: when a
// *finalCostHolder exists (reservation created) but Set was never called AND
// token usage is present, the call genuinely could not be priced (pricing_rule
// miss). The reconcile must REFUND the reservation, NOT charge a fabricated
// fallback. The pre-fix behaviour — FinalizeReservation(fi.EstimatedCredits),
// where EstimatedCredits carried ReservedOutputTokens (a token count) — is
// exactly what billed 64000 credits on the customer's image request.
func TestFinalize_RefundsWhenHolderUnsetWithUsage(t *testing.T) {
	renderedMsgs := []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "unset holder test"}},
	}

	const estimatedCredits int64 = 512 // expected fallback value

	budgetSvc := &mockContextBudgetService{
		prepareResult: makePrepareResultWithEstimated(renderedMsgs, true, estimatedCredits),
	}
	capturingSvc := &capturingCreditService{
		mockCreditService: mockCreditService{
			checkResult: &credit.PreCheckResult{
				SkipDeduction:    false,
				Sufficient:       true,
				EstimatedCredits: estimatedCredits,
			},
			reserveResult: &credit.Reservation{
				ID:              99,
				ReservedCredits: estimatedCredits,
				Status:          credit.StatusReserved,
			},
		},
	}
	var capturedActualCredits int64 = -1
	capturingSvc.onFinalize = func(credits int64) { capturedActualCredits = credits }

	deps := Deps{
		ContextBudget: budgetSvc,
		CreditService: capturingSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	// Adapter that does NOT call holder.Set (simulates pricing-rule miss / Billing error).
	adapter := Handler(func(ctx context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		// Intentionally do NOT call finalCostHolderFromCtx(ctx).Set(...)
		return &aiservice.ChatResponse{
			Content: "answer",
			Usage:   aiservice.TokenUsage{PromptTokens: 100, CompletionTokens: 50},
		}, nil
	})

	mw := ContextBudgetCredits(deps)
	handler := mw(adapter)

	ctx := billing.WithBillingMeta(context.Background(), 43, "sop_run", nil)
	ctx = WithUserID(ctx, 43)

	_, err := handler(ctx, budgetRoute(), chatReqWithFragments(simpleFragment("f2", "input")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fix ③: holder unset + usage present → refund, never charge a fallback.
	if capturingSvc.finalizeCalls != 0 {
		t.Errorf("FinalizeReservation calls: got %d, want 0 (must refund, not charge fallback)", capturingSvc.finalizeCalls)
	}
	if capturingSvc.refundCalls != 1 {
		t.Errorf("Refund calls: got %d, want 1 (pricing unavailable → refund)", capturingSvc.refundCalls)
	}
	// onFinalize must not have fired (no charge happened).
	if capturedActualCredits != -1 {
		t.Errorf("FinalizeReservation should not have been called, but captured actualCredits = %d", capturedActualCredits)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makePrepareResultWithEstimated creates a PrepareResult with a specific
// ReservedOutputTokens value that becomes EstimatedCredits in FinalizeInput.
func makePrepareResultWithEstimated(renderedMessages []aiservice.ChatMessage, chargeUser bool, estimatedCredits int64) *PrepareResult {
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
			ReservedOutputTokens: int(estimatedCredits), // EstimatedCredits = ReservedOutputTokens
			SafeRatio:            0.80,
			ChargeUser:           chargeUser,
		},
		TokenProfileID: 1,
		EventID:        42,
		NormalizedOp:   "sop_run",
		SkipBudget:     false,
	}
}
