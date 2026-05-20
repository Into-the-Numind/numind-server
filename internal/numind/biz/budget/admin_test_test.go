package budget

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newAdminTestStore creates an isolated in-memory SQLite DB for AdminTestConsumer tests.
// Uses "file::memory:?cache=shared&mode=memory" with a unique URI per test so
// concurrent goroutines share the same connection and the schema is visible to all.
func newAdminTestStore(t *testing.T) store.IStore {
	t.Helper()
	// Use a named in-memory DB (cache=shared) so all connections within the test
	// process share the same schema. Each test gets a unique name via t.Name().
	dsn := "file::" + t.Name() + "?cache=shared&mode=memory"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	require.NoError(t, db.AutoMigrate(
		&model.CreditAdminTestGrant{},
		&model.CreditTransaction{},
	), "auto-migrate")

	// Increase max open connections so concurrent goroutines don't serialize on
	// connection acquisition (SQLite itself serializes writes).
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(10)
	t.Cleanup(func() { _ = sqlDB.Close() })

	return store.NewTestStore(db)
}

// TestConsumeFirstTime_LazyCreate verifies that the first Consume for a parent
// lazily creates the grant row and records a credit_transaction.
func TestConsumeFirstTime_LazyCreate(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	txID, err := consumer.Consume(context.Background(), 1, 100)
	require.NoError(t, err)
	assert.Greater(t, txID, uint64(0), "txID should be a valid auto-increment ID")

	// Verify grant row was created
	var grant model.CreditAdminTestGrant
	require.NoError(t, s.DB().Where("parent_user_id = ?", 1).First(&grant).Error)
	assert.Equal(t, DefaultAdminTestGrant, grant.GrantedAmount)
	assert.Equal(t, uint32(100), grant.UsedAmount)
	assert.NotNil(t, grant.LastUsedAt)

	// Verify credit_transaction was inserted
	var tx model.CreditTransaction
	require.NoError(t, s.DB().Where("id = ?", txID).First(&tx).Error)
	assert.Equal(t, uint(1), tx.UserID)
	assert.Equal(t, int64(-100), tx.Amount)
	require.NotNil(t, tx.SourceType)
	assert.Equal(t, "admin_test", *tx.SourceType)
	assert.Equal(t, "agent_test_reserve", tx.Operation)
}

// TestConsumeSecondTime_AccumulatesUsed verifies that a second Consume on the
// same parent accumulates used_amount rather than resetting it.
func TestConsumeSecondTime_AccumulatesUsed(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	_, err := consumer.Consume(context.Background(), 2, 200)
	require.NoError(t, err)

	_, err = consumer.Consume(context.Background(), 2, 300)
	require.NoError(t, err)

	var grant model.CreditAdminTestGrant
	require.NoError(t, s.DB().Where("parent_user_id = ?", 2).First(&grant).Error)
	assert.Equal(t, uint32(500), grant.UsedAmount)
}

// TestConsumeExhausted verifies that Consume returns ErrAdminTestExhausted
// when the remaining balance is less than the requested amount.
func TestConsumeExhausted(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	// Consume all but 1 credit
	_, err := consumer.Consume(context.Background(), 3, DefaultAdminTestGrantInt64-1)
	require.NoError(t, err)

	// Now try to consume 2 — should fail
	_, err = consumer.Consume(context.Background(), 3, 2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAdminTestExhausted), "expected ErrAdminTestExhausted, got: %v", err)
}

// TestConsumeZeroAmount verifies that Consume rejects amount <= 0.
func TestConsumeZeroAmount(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	_, err := consumer.Consume(context.Background(), 4, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "amount must be > 0")
}

// TestRefundCap verifies that Refund caps the refund amount to current used_amount
// so used_amount never goes below 0.
func TestRefundCap(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	// Consume 100
	txID, err := consumer.Consume(context.Background(), 5, 100)
	require.NoError(t, err)

	// Refund 9999 — should be capped to 100 (the actual used_amount)
	err = consumer.Refund(context.Background(), 5, txID, 9999)
	require.NoError(t, err)

	var grant model.CreditAdminTestGrant
	require.NoError(t, s.DB().Where("parent_user_id = ?", 5).First(&grant).Error)
	assert.Equal(t, uint32(0), grant.UsedAmount, "used_amount should be 0 after capped refund")

	// Verify refund credit_transaction was inserted
	var txCount int64
	s.DB().Model(&model.CreditTransaction{}).
		Where("user_id = ? AND operation = ?", 5, "agent_test_refund").
		Count(&txCount)
	assert.Equal(t, int64(1), txCount)
}

// TestRefundZeroAmount verifies that Refund with refundAmount <= 0 is a no-op.
func TestRefundZeroAmount(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	txID, err := consumer.Consume(context.Background(), 6, 50)
	require.NoError(t, err)

	err = consumer.Refund(context.Background(), 6, txID, 0)
	require.NoError(t, err)

	// used_amount should remain 50
	var grant model.CreditAdminTestGrant
	require.NoError(t, s.DB().Where("parent_user_id = ?", 6).First(&grant).Error)
	assert.Equal(t, uint32(50), grant.UsedAmount)
}

// TestRefundWrongSourceType verifies that Refund rejects transactions whose
// source_type is not "admin_test".
func TestRefundWrongSourceType(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	// Insert a credit_transaction with a different source_type
	sourceType := "trial"
	ct := &model.CreditTransaction{
		UserID:     7,
		Amount:     -50,
		SourceType: &sourceType,
		Operation:  "some_op",
	}
	require.NoError(t, s.DB().Create(ct).Error)

	err := consumer.Refund(context.Background(), 7, ct.ID, 50)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source_type not admin_test")
}

// TestStatusNoGrant verifies that Status returns the default 5000 grant state
// when no grant row exists for the parent user this month.
func TestStatusNoGrant(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	status, err := consumer.Status(context.Background(), 8, time.Now())
	require.NoError(t, err)
	assert.Equal(t, DefaultAdminTestGrantInt64, status.Granted)
	assert.Equal(t, int64(0), status.Used)
	assert.Equal(t, DefaultAdminTestGrantInt64, status.Remaining)
	assert.True(t, status.DaysToExpire >= 0)
}

// TestStatusWithGrant verifies that Status reflects the actual grant row values
// when a grant exists.
func TestStatusWithGrant(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	// Consume some credits to create grant row
	_, err := consumer.Consume(context.Background(), 9, 1500)
	require.NoError(t, err)

	status, err := consumer.Status(context.Background(), 9, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(5000), status.Granted)
	assert.Equal(t, int64(1500), status.Used)
	assert.Equal(t, int64(3500), status.Remaining)
}

// TestConcurrentConsume_Race runs 10 goroutines each trying to consume 500 credits
// from the same parent (total pool = 5000). This test is meant to be run with
// -race to detect data races.
//
// Note: SQLite's transaction serialization is less strict than MySQL's
// SELECT ... FOR UPDATE — all goroutines may succeed (SQLite serializes writes
// at the DB level). The key invariant is: no panic, no race detector warnings,
// and used_amount never exceeds 5000.
func TestConcurrentConsume_Race(t *testing.T) {
	s := newAdminTestStore(t)
	consumer := NewAdminTestConsumer(s)

	var wg sync.WaitGroup
	var successCount atomic.Int32
	var exhaustedCount atomic.Int32
	var otherErrCount atomic.Int32

	const goroutines = 10
	const amountEach int64 = 500 // 10 × 500 = 5000 = DefaultAdminTestGrant

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := consumer.Consume(context.Background(), 99, amountEach)
			if err == nil {
				successCount.Add(1)
			} else if errors.Is(err, ErrAdminTestExhausted) {
				exhaustedCount.Add(1)
			} else {
				// SQLite may return "database locked" under heavy concurrency — count separately
				otherErrCount.Add(1)
			}
		}()
	}
	wg.Wait()

	// No hangs, no panics — all goroutines completed
	total := successCount.Load() + exhaustedCount.Load() + otherErrCount.Load()
	assert.Equal(t, int32(goroutines), total, "all goroutines must complete (no hangs)")

	// At least one must succeed
	assert.GreaterOrEqual(t, successCount.Load(), int32(1), "at least one consume must succeed")

	// The grant's used_amount must never exceed the granted amount (no overflow)
	var grant model.CreditAdminTestGrant
	require.NoError(t, s.DB().Where("parent_user_id = ?", 99).First(&grant).Error)
	assert.LessOrEqual(t, grant.UsedAmount, DefaultAdminTestGrant,
		"used_amount must not exceed GrantedAmount=%d", DefaultAdminTestGrant)
}
