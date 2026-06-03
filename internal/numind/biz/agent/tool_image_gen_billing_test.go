package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/model"
)

// stubCreditService implements credit.ICreditService for image_gen billing tests.
// Only ReserveBudget/Reconcile/Refund are exercised; the rest are no-ops.
type stubCreditService struct {
	reserveBudgetCalls int
	reconcileCalls     int
	refundCalls        int
	reserveErr         error
	reserveID          uint64
}

func (s *stubCreditService) ReserveBudget(_ context.Context, _ *model.User, _ credit.BudgetReservationInput) (*credit.Reservation, error) {
	s.reserveBudgetCalls++
	if s.reserveErr != nil {
		return nil, s.reserveErr
	}
	return &credit.Reservation{ID: s.reserveID}, nil
}
func (s *stubCreditService) Reconcile(_ context.Context, _ uint64, _ int64) error {
	s.reconcileCalls++
	return nil
}
func (s *stubCreditService) Refund(_ context.Context, _ uint64, _ string) error {
	s.refundCalls++
	return nil
}

// --- remaining ICreditService methods: no-op stubs ---
func (s *stubCreditService) CheckAndEstimate(context.Context, *model.User, credit.Operation, credit.EstimationInput) (*credit.PreCheckResult, error) {
	return &credit.PreCheckResult{}, nil
}
func (s *stubCreditService) Reserve(context.Context, *model.User, credit.Operation, int64, uint64, *string) (*credit.Reservation, error) {
	return &credit.Reservation{}, nil
}
func (s *stubCreditService) FinalizeReservation(context.Context, *credit.Reservation, *int64, *error) error {
	return nil
}
func (s *stubCreditService) GetBalance(context.Context, *model.User) (*credit.BalanceBreakdown, error) {
	return &credit.BalanceBreakdown{}, nil
}
func (s *stubCreditService) CheckAndEstimateBudget(context.Context, *model.User, credit.BudgetPrecheckInput) (*credit.PreCheckResult, error) {
	return &credit.PreCheckResult{}, nil
}
func (s *stubCreditService) ReserveAgentTest(context.Context, *model.User, int64, *string) (*credit.Reservation, error) {
	return &credit.Reservation{}, nil
}
func (s *stubCreditService) ReconcileAgentTest(context.Context, uint64, int64) error { return nil }
func (s *stubCreditService) ListConsumptionLog(context.Context, uint, int, int) ([]credit.ConsumptionLogItem, int64, error) {
	return nil, 0, nil
}

func isSoftError(t *testing.T, res ToolResult) bool {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(res, &m); err != nil {
		return false
	}
	return strings.HasPrefix(m["error"], "ERROR:")
}

// T9: when billing is unwired (nil creditService), Execute proceeds without
// billing (no panic) — preserves pre-T9 behaviour for tests.
func TestImageGen_NoBilling_WhenCreditServiceNil(t *testing.T) {
	tool := &imageGenTool{ds: nil, creditService: nil} // nil ds → generateImage fails softly
	res, err := tool.Execute(context.Background(), ToolInput(`{"prompt":"a cat"}`))
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if !isSoftError(t, res) {
		t.Errorf("expected soft error (nil db), got %s", res)
	}
}

// T9: with billing wired but no billing user in ctx, reserve fails closed and
// Execute returns a soft '积分不足' error WITHOUT generating or reconciling.
func TestImageGen_Reserve_MissingBillingUser_SoftError(t *testing.T) {
	stub := &stubCreditService{reserveID: 55}
	tool := &imageGenTool{ds: nil, creditService: stub}
	// ctx has NO billing context → reserve() returns "missing billing user".
	res, err := tool.Execute(context.Background(), ToolInput(`{"prompt":"a cat"}`))
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if !isSoftError(t, res) {
		t.Errorf("expected soft '积分不足' error, got %s", res)
	}
	// ReserveBudget not reached (billing-user guard fails first); nothing settled.
	if stub.reserveBudgetCalls != 0 || stub.reconcileCalls != 0 || stub.refundCalls != 0 {
		t.Errorf("no settlement expected: reserve=%d reconcile=%d refund=%d",
			stub.reserveBudgetCalls, stub.reconcileCalls, stub.refundCalls)
	}
}
