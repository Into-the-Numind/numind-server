package membership_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store/membership"
	model "numind-server/internal/pkg/model/membership"
)

// ============================================================
// ISubscriptionStore tests
// ============================================================

func TestSubscriptionStore_GetNotFound(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewSubscriptionStore(db)

	got, err := s.Get(context.Background(), 9999)
	require.NoError(t, err)
	assert.Nil(t, got, "Get on missing record should return (nil, nil)")
}

func TestSubscriptionStore_CreateAndGet(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewSubscriptionStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	grantID := uint64(1)
	sub := &model.Subscription{
		UserID:               42,
		FirstStartedAt:       now,
		CurrentStartedAt:     now,
		ExpiresAt:            now.AddDate(0, 1, 0),
		TotalMonthsPurchased: 1,
		Source:               model.SourceB2BGrant,
		GranterUserID:        &grantID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	require.NoError(t, s.Create(context.Background(), db, sub))
	assert.NotZero(t, sub.ID)

	got, err := s.Get(context.Background(), 42)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, uint64(42), got.UserID)
	assert.Equal(t, 1, got.TotalMonthsPurchased)
}

func TestSubscriptionStore_HasActive_InPeriod(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewSubscriptionStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	sub := &model.Subscription{
		UserID:               10,
		FirstStartedAt:       now.AddDate(0, -1, 0),
		CurrentStartedAt:     now.AddDate(0, -1, 0),
		ExpiresAt:            now.AddDate(0, 1, 0), // active
		TotalMonthsPurchased: 2,
		Source:               model.SourceB2BGrant,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	require.NoError(t, s.Create(context.Background(), db, sub))

	active, err := s.HasActive(context.Background(), 10, now)
	require.NoError(t, err)
	assert.True(t, active)
}

func TestSubscriptionStore_HasActive_Expired(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewSubscriptionStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	sub := &model.Subscription{
		UserID:               11,
		FirstStartedAt:       now.AddDate(0, -2, 0),
		CurrentStartedAt:     now.AddDate(0, -2, 0),
		ExpiresAt:            now.AddDate(0, -1, 0), // expired
		TotalMonthsPurchased: 1,
		Source:               model.SourceB2BGrant,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	require.NoError(t, s.Create(context.Background(), db, sub))

	active, err := s.HasActive(context.Background(), 11, now)
	require.NoError(t, err)
	assert.False(t, active)
}

// ============================================================
// ITrialGrantStore tests
// ============================================================

func TestTrialGrantStore_GetNotFound(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewTrialGrantStore(db)

	got, err := s.Get(context.Background(), 8888)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestTrialGrantStore_CreateAndGet(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewTrialGrantStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	tg := &model.TrialGrant{
		UserID:           7,
		GrantedAt:        now,
		ExpiresAt:        now.AddDate(0, 0, 3),
		CreditsRemaining: 200,
		Source:           model.SourceB2BGrant,
		CreatedAt:        now,
	}
	require.NoError(t, s.Create(context.Background(), db, tg))
	assert.NotZero(t, tg.ID)

	got, err := s.Get(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 200, got.CreditsRemaining)
}

func TestTrialGrantStore_UniqueConstraintOnDuplicate(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewTrialGrantStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	tg := &model.TrialGrant{
		UserID:           20,
		GrantedAt:        now,
		ExpiresAt:        now.AddDate(0, 0, 3),
		CreditsRemaining: 200,
		Source:           model.SourceB2BGrant,
		CreatedAt:        now,
	}
	require.NoError(t, s.Create(context.Background(), db, tg))

	// Second insert with same user_id should fail (UNIQUE constraint)
	tg2 := &model.TrialGrant{
		UserID:           20,
		GrantedAt:        now,
		ExpiresAt:        now.AddDate(0, 0, 3),
		CreditsRemaining: 200,
		Source:           model.SourceB2BGrant,
		CreatedAt:        now,
	}
	err := s.Create(context.Background(), db, tg2)
	assert.Error(t, err, "duplicate user_id should fail with UNIQUE constraint")
}

// ============================================================
// ICreditCycleStore tests
// ============================================================

func TestCreditCycleStore_GetByUserAndStart_NotFound(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewCreditCycleStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	got, err := s.GetByUserAndStart(context.Background(), db, 999, now)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestCreditCycleStore_InsertOrIgnore_DuplicateSilentNoOp(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewCreditCycleStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	cycle := &model.CreditCycle{
		UserID:           1,
		SubscriptionID:   1,
		CycleStart:       now,
		CycleEnd:         now.AddDate(0, 1, 0),
		CreditsGranted:   2000,
		CreditsRemaining: 2000,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, s.InsertOrIgnore(context.Background(), db, cycle))
	assert.NotZero(t, cycle.ID)

	// Duplicate insert (same user_id + cycle_start) should be silently ignored
	cycle2 := &model.CreditCycle{
		UserID:           1,
		SubscriptionID:   1,
		CycleStart:       now,
		CycleEnd:         now.AddDate(0, 1, 0),
		CreditsGranted:   2000,
		CreditsRemaining: 2000,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	err := s.InsertOrIgnore(context.Background(), db, cycle2)
	assert.NoError(t, err, "InsertOrIgnore on duplicate (user_id, cycle_start) should be silent no-op")

	// Verify only one row exists
	var count int64
	require.NoError(t, db.Table("credit_cycle").Where("user_id = ?", 1).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// ============================================================
// IUserBoosterBalanceStore tests
// ============================================================

func TestUserBoosterBalanceStore_GetNotFound(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewUserBoosterBalanceStore(db)

	got, err := s.Get(context.Background(), 7777)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestUserBoosterBalanceStore_IncrementCreatesAndAccumulates(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewUserBoosterBalanceStore(db)

	// First increment creates the row
	require.NoError(t, s.Increment(context.Background(), db, 5, 600))

	got, err := s.Get(context.Background(), 5)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(600), got.CreditsRemaining)

	// Second increment accumulates
	require.NoError(t, s.Increment(context.Background(), db, 5, 300))

	got, err = s.Get(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, int64(900), got.CreditsRemaining)
}

func TestUserBoosterBalanceStore_DecrementReducesBalance(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewUserBoosterBalanceStore(db)

	require.NoError(t, s.Increment(context.Background(), db, 6, 600))
	require.NoError(t, s.Decrement(context.Background(), db, 6, 100))

	got, err := s.Get(context.Background(), 6)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(500), got.CreditsRemaining)
}

// ============================================================
// IMembershipEventStore tests
// ============================================================

func TestMembershipEventStore_GetByIdempotencyKey_NotFound(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewMembershipEventStore(db)

	got, err := s.GetByIdempotencyKey(context.Background(), "nonexistent-key")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestMembershipEventStore_CreateAndGetByIdempotencyKey(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewMembershipEventStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	key := "idem-key-001"
	event := &model.MembershipEvent{
		UserID:         9,
		EventType:      model.EventTypeTrialGranted,
		ProductType:    model.ProductTypeTrial,
		AmountCents:    990,
		Source:         model.SourceB2BGrant,
		IdempotencyKey: &key,
		OccurredAt:     now,
	}
	require.NoError(t, s.Create(context.Background(), db, event))
	assert.NotZero(t, event.ID)

	got, err := s.GetByIdempotencyKey(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, uint64(9), got.UserID)
}

func TestMembershipEventStore_UniqueIdempotencyKey(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewMembershipEventStore(db)

	now := time.Now().UTC().Truncate(time.Second)
	key := "idem-key-002"
	event := &model.MembershipEvent{
		UserID:         9,
		EventType:      model.EventTypeSubGranted,
		ProductType:    model.ProductTypeMonthly,
		AmountCents:    0,
		Source:         model.SourceB2BGrant,
		IdempotencyKey: &key,
		OccurredAt:     now,
	}
	require.NoError(t, s.Create(context.Background(), db, event))

	// Same idempotency key should fail
	event2 := &model.MembershipEvent{
		UserID:         9,
		EventType:      model.EventTypeSubGranted,
		ProductType:    model.ProductTypeMonthly,
		AmountCents:    0,
		Source:         model.SourceB2BGrant,
		IdempotencyKey: &key,
		OccurredAt:     now,
	}
	err := s.Create(context.Background(), db, event2)
	assert.Error(t, err, "duplicate idempotency_key should fail")
}

func TestMembershipEventStore_QueryByGranterAndMonth(t *testing.T) {
	db := newTestDB(t)
	s := membership.NewMembershipEventStore(db)

	granterID := uint64(100)
	monthStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	// Event within range
	key1 := "key-within"
	event1 := &model.MembershipEvent{
		UserID:         1,
		EventType:      model.EventTypeSubGranted,
		ProductType:    model.ProductTypeMonthly,
		AmountCents:    0,
		Source:         model.SourceB2BGrant,
		GranterUserID:  &granterID,
		IdempotencyKey: &key1,
		OccurredAt:     time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, s.Create(context.Background(), db, event1))

	// Event outside range (month before)
	key2 := "key-outside"
	event2 := &model.MembershipEvent{
		UserID:         2,
		EventType:      model.EventTypeTrialGranted,
		ProductType:    model.ProductTypeTrial,
		AmountCents:    0,
		Source:         model.SourceB2BGrant,
		GranterUserID:  &granterID,
		IdempotencyKey: &key2,
		OccurredAt:     time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, s.Create(context.Background(), db, event2))

	results, err := s.QueryByGranterAndMonth(context.Background(), granterID, monthStart, monthEnd)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, *event1.IdempotencyKey, *results[0].IdempotencyKey)
}
