package credit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// fakeAdminConsumer implements credit.AdminTestConsumer for tests.
type fakeAdminConsumer struct {
	consumeFn func(ctx context.Context, parentUserID uint, amount int64) (uint64, error)
	refundFn  func(ctx context.Context, parentUserID uint, txID uint64, refund int64) error
	statusFn  func(ctx context.Context, parentUserID uint, now time.Time) (*AdminTestStatus, error)
}

func (f *fakeAdminConsumer) Consume(ctx context.Context, parentUserID uint, amount int64) (uint64, error) {
	if f.consumeFn != nil {
		return f.consumeFn(ctx, parentUserID, amount)
	}
	return 100, nil
}

func (f *fakeAdminConsumer) Refund(ctx context.Context, parentUserID uint, txID uint64, refund int64) error {
	if f.refundFn != nil {
		return f.refundFn(ctx, parentUserID, txID, refund)
	}
	return nil
}

func (f *fakeAdminConsumer) Status(ctx context.Context, parentUserID uint, now time.Time) (*AdminTestStatus, error) {
	if f.statusFn != nil {
		return f.statusFn(ctx, parentUserID, now)
	}
	return nil, nil
}

func newAdminTestServiceDB(t *testing.T) store.IStore {
	// Hand-roll credit_reservation DDL (model uses MySQL ENUM types that SQLite
	// AutoMigrate cannot parse; matches the pattern in credit_service_reserve_test.go).
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    reference_type TEXT NOT NULL,
    reference_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    reserved_credits INTEGER NOT NULL,
    coefficient_id INTEGER,
    status TEXT NOT NULL DEFAULT 'reserved',
    actual_cost_cents INTEGER,
    delta INTEGER,
    finalize_reason TEXT,
    idempotency_key TEXT,
    reconciled_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    estimation_source TEXT NOT NULL DEFAULT 'credit_coefficient',
    token_profile_id INTEGER,
    estimated_prompt_tokens INTEGER NOT NULL DEFAULT 0,
    estimated_completion_tokens INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    context_budget_event_id INTEGER,
    user_type_multiplier REAL NOT NULL DEFAULT 1.0
);`).Error)
	return store.NewTestStore(db)
}

func TestReserveAgentTest_HappyPath(t *testing.T) {
	s := newAdminTestServiceDB(t)
	svc := &creditService{store: s}
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		consumeFn: func(ctx context.Context, pid uint, amount int64) (uint64, error) {
			assert.Equal(t, uint(42), pid)
			assert.Equal(t, int64(100), amount)
			return 999, nil
		},
	})

	user := &model.User{Model: gorm.Model{ID: 42}}
	rsv, err := svc.ReserveAgentTest(context.Background(), user, 100, nil)
	require.NoError(t, err)
	require.NotNil(t, rsv)
	assert.Equal(t, Operation("agent_test"), rsv.Operation)
	assert.Equal(t, int64(100), rsv.ReservedCredits)
	assert.Equal(t, StatusReserved, rsv.Status)
	assert.Contains(t, rsv.ReferenceID, "admin_test_tx:999")
}

func TestReserveAgentTest_Exhausted(t *testing.T) {
	s := newAdminTestServiceDB(t)
	svc := &creditService{store: s}
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		consumeFn: func(ctx context.Context, pid uint, amount int64) (uint64, error) {
			return 0, ErrAdminTestExhausted
		},
	})

	user := &model.User{Model: gorm.Model{ID: 42}}
	_, err := svc.ReserveAgentTest(context.Background(), user, 100, nil)
	require.Error(t, err)
	// Must be exactly errno.ErrAdminTestExhausted (bridged from sentinel)
	assert.True(t, errors.Is(err, errno.ErrAdminTestExhausted))
}

func TestReserveAgentTest_NoAdminConsumer(t *testing.T) {
	s := newAdminTestServiceDB(t)
	svc := &creditService{store: s}
	user := &model.User{Model: gorm.Model{ID: 42}}
	_, err := svc.ReserveAgentTest(context.Background(), user, 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin consumer not wired")
}

func TestReserveAgentTest_NilUser(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)}
	svc.SetAdminTestConsumer(&fakeAdminConsumer{})
	_, err := svc.ReserveAgentTest(context.Background(), nil, 100, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parent user is nil")
}

func TestReserveAgentTest_ZeroEstimated(t *testing.T) {
	svc := &creditService{store: newAdminTestServiceDB(t)}
	svc.SetAdminTestConsumer(&fakeAdminConsumer{})
	user := &model.User{Model: gorm.Model{ID: 42}}
	_, err := svc.ReserveAgentTest(context.Background(), user, 0, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "estimated must be > 0")
}

func TestReconcileAgentTest_RefundPath(t *testing.T) {
	s := newAdminTestServiceDB(t)
	svc := &creditService{store: s}
	refundCalls := 0
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		refundFn: func(ctx context.Context, pid uint, txID uint64, refund int64) error {
			refundCalls++
			assert.Equal(t, int64(30), refund) // reserved 100 - actual 70 = 30
			return nil
		},
	})

	rsv := &model.CreditReservation{
		UserID:          42,
		Operation:       "agent_test",
		ReferenceType:   "agent_test",
		ReferenceID:     "admin_test_tx:999",
		ReservedCredits: 100,
		Status:          "reserved",
	}
	require.NoError(t, s.DB().Create(rsv).Error)

	require.NoError(t, svc.ReconcileAgentTest(context.Background(), rsv.ID, 70))
	assert.Equal(t, 1, refundCalls)

	var updated model.CreditReservation
	require.NoError(t, s.DB().First(&updated, rsv.ID).Error)
	assert.Equal(t, "reconciled", updated.Status)
	require.NotNil(t, updated.ActualCostCents)
	assert.Equal(t, int64(70), *updated.ActualCostCents)
	require.NotNil(t, updated.Delta)
	assert.Equal(t, int64(-30), *updated.Delta)
}

func TestReconcileAgentTest_TopupPath(t *testing.T) {
	s := newAdminTestServiceDB(t)
	svc := &creditService{store: s}
	topupCalls := 0
	svc.SetAdminTestConsumer(&fakeAdminConsumer{
		consumeFn: func(ctx context.Context, pid uint, amount int64) (uint64, error) {
			topupCalls++
			assert.Equal(t, int64(20), amount)
			return 1001, nil
		},
	})

	rsv := &model.CreditReservation{
		UserID:          42,
		Operation:       "agent_test",
		ReferenceType:   "agent_test",
		ReferenceID:     "admin_test_tx:999",
		ReservedCredits: 100,
		Status:          "reserved",
	}
	require.NoError(t, s.DB().Create(rsv).Error)

	require.NoError(t, svc.ReconcileAgentTest(context.Background(), rsv.ID, 120))
	assert.Equal(t, 1, topupCalls)

	var updated model.CreditReservation
	require.NoError(t, s.DB().First(&updated, rsv.ID).Error)
	assert.Equal(t, "reconciled", updated.Status)
	require.NotNil(t, updated.Delta)
	assert.Equal(t, int64(20), *updated.Delta)
}

func TestReconcileAgentTest_DoubleReconcileError(t *testing.T) {
	s := newAdminTestServiceDB(t)
	svc := &creditService{store: s}
	svc.SetAdminTestConsumer(&fakeAdminConsumer{})

	rsv := &model.CreditReservation{
		UserID: 42, Operation: "agent_test", ReferenceType: "agent_test",
		ReferenceID: "admin_test_tx:999", ReservedCredits: 100,
		Status: "reconciled", // already finalized
	}
	require.NoError(t, s.DB().Create(rsv).Error)

	err := svc.ReconcileAgentTest(context.Background(), rsv.ID, 50)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already reconciled")
}

func TestReconcileAgentTest_NotAgentTestError(t *testing.T) {
	s := newAdminTestServiceDB(t)
	svc := &creditService{store: s}
	svc.SetAdminTestConsumer(&fakeAdminConsumer{})

	rsv := &model.CreditReservation{
		UserID: 42, Operation: "sop_run", ReferenceType: "sop_run",
		ReferenceID: "sop_run:abc", ReservedCredits: 100,
		Status: "reserved",
	}
	require.NoError(t, s.DB().Create(rsv).Error)

	err := svc.ReconcileAgentTest(context.Background(), rsv.ID, 50)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not agent_test")
}

func TestReconcileAgentTest_NoAdminConsumer(t *testing.T) {
	s := newAdminTestServiceDB(t)
	svc := &creditService{store: s}
	rsv := &model.CreditReservation{
		UserID: 42, Operation: "agent_test", ReferenceType: "agent_test",
		ReferenceID: "admin_test_tx:1", ReservedCredits: 100, Status: "reserved",
	}
	require.NoError(t, s.DB().Create(rsv).Error)

	err := svc.ReconcileAgentTest(context.Background(), rsv.ID, 50)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "admin consumer not wired")
}
