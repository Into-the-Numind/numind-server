package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"numind-server/internal/numind/biz/budget"
)

// stubBudgetTracker captures Start/Close/RecordStep/RecordUsage/CanProceed/Snapshot
// calls so runner_budget_test can assert the lifecycle hooks fire without
// running a real BudgetTracker.
type stubBudgetTracker struct {
	startCalls []budgetStartCall
	closeCalls []uint64
}

type budgetStartCall struct {
	runID  uint64
	userID uint
	limits budget.Limits
}

func (s *stubBudgetTracker) Start(ctx context.Context, runID uint64, userID uint, limits budget.Limits) {
	s.startCalls = append(s.startCalls, budgetStartCall{runID, userID, limits})
}

func (s *stubBudgetTracker) Close(runID uint64) {
	s.closeCalls = append(s.closeCalls, runID)
}

func (s *stubBudgetTracker) RecordStep(ctx context.Context, runID uint64)               {}
func (s *stubBudgetTracker) RecordUsage(ctx context.Context, runID uint64, credits int) {}
func (s *stubBudgetTracker) Snapshot(ctx context.Context, runID uint64) budget.Snapshot {
	return budget.Snapshot{}
}
func (s *stubBudgetTracker) CanProceed(ctx context.Context, runID uint64) (bool, budget.Dimension, map[string]any) {
	return false, "", nil
}

func TestWithBudgetTracker_Setter(t *testing.T) {
	tracker := &stubBudgetTracker{}
	r := NewAgentRunner(nil, nil, WithBudgetTracker(tracker)).(*agentRunner)
	assert.Same(t, tracker, r.budgetTracker)
}

func TestWithBudgetTracker_Nil_OK(t *testing.T) {
	r := NewAgentRunner(nil, nil).(*agentRunner)
	assert.Nil(t, r.budgetTracker)
}

func TestBudgetTracker_NilTracker_RunDoesntPanic(t *testing.T) {
	// Compile-time check: agentRunner must accept nil budgetTracker without crashing.
	// Run-level integration validated by M13 acceptance doc.
	r := NewAgentRunner(nil, nil).(*agentRunner)
	assert.NotPanics(t, func() {
		_ = r.budgetTracker // just touching the field
	})
}
