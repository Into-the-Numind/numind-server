package credit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// T3 (agent-mode-billing): pool-selector 分支 —— Pool="admin_test" 时
// CheckAndEstimateBudget/ReserveBudget/Reconcile/Refund 走 admin_test 池
// (adminConsumer)，绝不碰三池 (s.credits 故意留 nil → 误碰即 panic)。
// Pool="" 默认三池路径由既有 reserve/reconcile 测试套保证不变。

func parentUser(id uint) *model.User {
	return &model.User{Model: gorm.Model{ID: id}} // ParentUserID == nil → parent account
}

func TestCheckAndEstimateBudget_AdminTest_Sufficient(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)}
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		statusFn: func(_ context.Context, pid uint, _ time.Time) (*AdminTestStatus, error) {
			assert.Equal(t, uint(42), pid)
			return &AdminTestStatus{Remaining: 1000}, nil
		},
	})
	pre, err := svc.CheckAndEstimateBudget(context.Background(), parentUser(42), BudgetPrecheckInput{
		UserID: 42, Operation: "agent_run", Pool: PoolAdminTest,
	})
	require.NoError(t, err)
	assert.True(t, pre.Sufficient)
	assert.Equal(t, int64(6), pre.EstimatedCredits) // fallback estimate for agent_run
}

func TestCheckAndEstimateBudget_AdminTest_Exhausted(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)}
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		statusFn: func(_ context.Context, _ uint, _ time.Time) (*AdminTestStatus, error) {
			return &AdminTestStatus{Remaining: 2}, nil // < estimate 6
		},
	})
	pre, err := svc.CheckAndEstimateBudget(context.Background(), parentUser(42), BudgetPrecheckInput{
		UserID: 42, Operation: "agent_run", Pool: PoolAdminTest,
	})
	require.Error(t, err)
	assert.False(t, pre.Sufficient)
}

func TestReserveBudget_AdminTestPool_RoutesToAgentTest(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)} // s.credits intentionally nil
	consumed := false
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		statusFn: func(_ context.Context, _ uint, _ time.Time) (*AdminTestStatus, error) {
			return &AdminTestStatus{Remaining: 1000}, nil
		},
		consumeFn: func(_ context.Context, pid uint, amount int64) (uint64, error) {
			consumed = true
			assert.Equal(t, uint(42), pid)
			return 777, nil
		},
	})
	rsv, err := svc.ReserveBudget(context.Background(), parentUser(42), BudgetReservationInput{
		BudgetPrecheckInput: BudgetPrecheckInput{UserID: 42, Operation: "agent_run", Pool: PoolAdminTest},
	})
	require.NoError(t, err)
	require.NotNil(t, rsv)
	assert.True(t, consumed, "admin_test Consume must be called")
	assert.Equal(t, Operation("agent_test"), rsv.Operation)
	assert.Contains(t, rsv.ReferenceID, "admin_test_tx:777")
}

func TestReconcile_RoutesAgentTestReservation(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)} // s.credits nil
	refundCalled := false
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		consumeFn: func(_ context.Context, _ uint, _ int64) (uint64, error) { return 555, nil },
		refundFn: func(_ context.Context, _ uint, txID uint64, refund int64) error {
			refundCalled = true
			assert.Equal(t, uint64(555), txID)
			assert.Equal(t, int64(40), refund) // reserved 100 - actual 60
			return nil
		},
	})
	rsv, err := svc.ReserveAgentTest(context.Background(), parentUser(42), 100, nil)
	require.NoError(t, err)

	// Reconcile must route to admin_test (NOT three-pool s.credits which is nil).
	require.NoError(t, svc.Reconcile(context.Background(), rsv.ID, 60))
	assert.True(t, refundCalled, "admin_test Refund must be called on over-reserve")

	var row model.CreditReservation
	require.NoError(t, svc.store.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "reconciled", row.Status)
}

func TestReconcile_AgentTest_UnderReserve_Topup(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)} // s.credits nil
	topup := false
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		consumeFn: func(_ context.Context, _ uint, amount int64) (uint64, error) {
			if amount == 20 { // second Consume = topup (actual 120 - reserved 100)
				topup = true
			}
			return 555, nil
		},
	})
	rsv, err := svc.ReserveAgentTest(context.Background(), parentUser(42), 100, nil)
	require.NoError(t, err)
	require.NoError(t, svc.Reconcile(context.Background(), rsv.ID, 120)) // actual > reserved
	assert.True(t, topup, "under-reserve must topup from admin_test pool")
}

func TestReconcile_AgentTest_Idempotent(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)} // s.credits nil
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		consumeFn: func(_ context.Context, _ uint, _ int64) (uint64, error) { return 1, nil },
	})
	rsv, err := svc.ReserveAgentTest(context.Background(), parentUser(42), 100, nil)
	require.NoError(t, err)
	require.NoError(t, svc.Reconcile(context.Background(), rsv.ID, 100)) // delta 0
	// Second call → already reconciled → sentinel (sweeper/retry safe).
	err = svc.Reconcile(context.Background(), rsv.ID, 100)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAlreadyFinalized), "double reconcile must be ErrAlreadyFinalized")
}

func TestRefund_RoutesAgentTestReservation(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)} // s.credits nil
	var refundedAmt int64
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		consumeFn: func(_ context.Context, _ uint, _ int64) (uint64, error) { return 321, nil },
		refundFn: func(_ context.Context, _ uint, txID uint64, refund int64) error {
			assert.Equal(t, uint64(321), txID)
			refundedAmt = refund
			return nil
		},
	})
	rsv, err := svc.ReserveAgentTest(context.Background(), parentUser(42), 100, nil)
	require.NoError(t, err)

	require.NoError(t, svc.Refund(context.Background(), rsv.ID, "user_cancelled"))
	assert.Equal(t, int64(100), refundedAmt, "full reserved refunded to admin_test pool")

	var row model.CreditReservation
	require.NoError(t, svc.store.DB().First(&row, rsv.ID).Error)
	assert.Equal(t, "refunded", row.Status)
}
