package membership_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	biz "numind-server/internal/numind/biz/membership"
	"numind-server/internal/pkg/errno"
	model "numind-server/internal/pkg/model/membership"
)

// ────────────────────────────────────────────────────────────
// helpers
// ────────────────────────────────────────────────────────────

func ts(year, month, day int) time.Time {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }

func newGrantSubReq(parentID, childID uint64, months int, now time.Time) biz.GrantSubscriptionRequest {
	return biz.GrantSubscriptionRequest{
		ParentUserID:  parentID,
		UserID:        childID,
		ProductType:   "monthly",
		Months:        months,
		GranterUserID: ptr(parentID),
		Now:           now,
	}
}

// seedStaleCycles inserts a past subscription and stale credit_cycle rows for userID.
// Directly inserts DB rows so the reopen scenario has stale cycles to clean up.
func seedStaleCycles(t *testing.T, db *gorm.DB, svc *biz.MembershipService, parentID, userID uint64, now time.Time) {
	t.Helper()
	// Create the past subscription via biz (4 months ago, 1 month duration → already expired).
	pastNow := now.AddDate(0, -4, 0)
	req := biz.GrantSubscriptionRequest{
		ParentUserID:  parentID,
		UserID:        userID,
		ProductType:   "monthly",
		Months:        1,
		GranterUserID: ptr(parentID),
		Now:           pastNow,
	}
	res, err := svc.GrantOrRenewSubscription(t.Context(), req)
	require.NoError(t, err)

	// Directly insert a stale credit_cycle row pointing at the past subscription.
	cycleStart := pastNow
	cycleEnd := pastNow.AddDate(0, 1, 0)
	cycle := model.CreditCycle{
		UserID:           userID,
		SubscriptionID:   res.SubscriptionID,
		CycleStart:       cycleStart,
		CycleEnd:         cycleEnd,
		CreditsGranted:   2000,
		CreditsRemaining: 500,
		CreatedAt:        pastNow,
		UpdatedAt:        pastNow,
	}
	require.NoError(t, db.Create(&cycle).Error)
}

// ────────────────────────────────────────────────────────────
// TestGrantSub_New — new subscription from scratch
// ────────────────────────────────────────────────────────────

func TestGrantSub_New(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)

	now := ts(2026, 1, 15)
	res, err := svc.GrantOrRenewSubscription(t.Context(), newGrantSubReq(1, 101, 3, now))
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, "new", res.Scenario)
	assert.False(t, res.Replayed)
	assert.Equal(t, now, res.FirstStartedAt)
	assert.Equal(t, now, res.CurrentStartedAt)
	// 1/15 + 3 months = 4/15
	assert.Equal(t, ts(2026, 4, 15), res.ExpiresAt)
	assert.Equal(t, 3, res.TotalMonthsPurchased)

	// Verify subscription row persisted.
	var sub model.Subscription
	require.NoError(t, db.Where("user_id = ?", uint64(101)).Take(&sub).Error)
	assert.Equal(t, now, sub.FirstStartedAt)
	assert.Equal(t, now, sub.CurrentStartedAt)
	assert.Equal(t, ts(2026, 4, 15), sub.ExpiresAt)
	assert.Equal(t, 3, sub.TotalMonthsPurchased)

	// Verify event row.
	var evt model.MembershipEvent
	require.NoError(t, db.Where("user_id = ?", uint64(101)).Take(&evt).Error)
	assert.Equal(t, model.EventTypeSubGranted, evt.EventType)
	assert.Equal(t, model.ProductTypeMonthly, evt.ProductType)
	require.NotNil(t, evt.Months)
	assert.Equal(t, uint8(3), *evt.Months)
}

// ────────────────────────────────────────────────────────────
// TestGrantSub_RenewAnchorPreserved — 1/31 + 3 mo = 4/30, then +1 mo = 5/31
// ────────────────────────────────────────────────────────────

func TestGrantSub_RenewAnchorPreserved(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)

	// First grant: 2026-01-31, 3 months → expires 2026-04-30
	now1 := ts(2026, 1, 31)
	res1, err := svc.GrantOrRenewSubscription(t.Context(), newGrantSubReq(1, 101, 3, now1))
	require.NoError(t, err)
	assert.Equal(t, "new", res1.Scenario)
	// 1/31 + 3 mo: April has 30 days → 4/30
	assert.Equal(t, ts(2026, 4, 30), res1.ExpiresAt)
	assert.Equal(t, 3, res1.TotalMonthsPurchased)

	// Renew while still active: +1 month
	now2 := ts(2026, 3, 1) // still active (expires 4/30)
	res2, err := svc.GrantOrRenewSubscription(t.Context(), newGrantSubReq(1, 101, 1, now2))
	require.NoError(t, err)
	assert.Equal(t, "renew", res2.Scenario)
	assert.Equal(t, 4, res2.TotalMonthsPurchased)
	// INV-4: expires_at = AnchorAddMonths(current_started_at=1/31, total=4) = 5/31
	assert.Equal(t, ts(2026, 5, 31), res2.ExpiresAt)
	// INV-5: first_started_at preserved
	assert.Equal(t, ts(2026, 1, 31), res2.FirstStartedAt)

	// Renew event type is sub_renewed
	var evt model.MembershipEvent
	require.NoError(t, db.Where("user_id = ? AND event_type = ?", uint64(101), model.EventTypeSubRenewed).Take(&evt).Error)
	assert.Equal(t, uint8(1), *evt.Months)
}

// ────────────────────────────────────────────────────────────
// TestGrantSub_ReopenCleansStaleCycles
// ────────────────────────────────────────────────────────────

func TestGrantSub_ReopenCleansStaleCycles(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)

	parentID := uint64(1)
	childID := uint64(102)

	// Seed a past subscription + stale cycle rows.
	nowReal := ts(2026, 4, 1)
	seedStaleCycles(t, db, svc, parentID, childID, nowReal)

	// Verify there's a stale cycle in DB.
	var cycleCount int64
	require.NoError(t, db.Model(&model.CreditCycle{}).Where("user_id = ?", childID).Count(&cycleCount).Error)
	// There should be at least 1 cycle from the past grant.
	assert.GreaterOrEqual(t, cycleCount, int64(1))

	// Now reopen (expired sub).
	nowReopen := ts(2026, 4, 1) // sub expired months ago
	res, err := svc.GrantOrRenewSubscription(t.Context(), newGrantSubReq(parentID, childID, 2, nowReopen))
	require.NoError(t, err)
	assert.Equal(t, "reopen", res.Scenario)

	// Stale cycles should be deleted.
	var afterCount int64
	require.NoError(t, db.Model(&model.CreditCycle{}).Where("user_id = ?", childID).Count(&afterCount).Error)
	assert.Equal(t, int64(0), afterCount)

	// first_started_at preserved from original grant.
	assert.True(t, res.FirstStartedAt.Before(nowReopen))
	// current_started_at = reopen time.
	assert.Equal(t, nowReopen, res.CurrentStartedAt)
	// total_months_purchased reset to 2.
	assert.Equal(t, 2, res.TotalMonthsPurchased)
	// expires_at = AnchorAddMonths(nowReopen, 2) = 2026-06-01
	assert.Equal(t, ts(2026, 6, 1), res.ExpiresAt)

	// reopen event uses sub_granted event_type.
	var evt model.MembershipEvent
	require.NoError(t, db.Where("user_id = ? AND event_type = ?", childID, model.EventTypeSubGranted).
		Order("id DESC").Take(&evt).Error)
	assert.Equal(t, model.EventTypeSubGranted, evt.EventType)
}

// ────────────────────────────────────────────────────────────
// TestGrantSub_SelfPurchaseDisabled
// ────────────────────────────────────────────────────────────

func TestGrantSub_SelfPurchaseDisabled(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)

	req := biz.GrantSubscriptionRequest{
		ParentUserID:  101, // same as UserID → self-purchase
		UserID:        101,
		ProductType:   "monthly",
		Months:        1,
		GranterUserID: ptr(uint64(101)),
		Now:           ts(2026, 1, 1),
	}
	_, err := svc.GrantOrRenewSubscription(t.Context(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrMembershipSelfPurchaseDisabled)
}

// ────────────────────────────────────────────────────────────
// TestGrantSub_InvalidMonths
// ────────────────────────────────────────────────────────────

func TestGrantSub_InvalidMonths(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)

	cases := []struct {
		name   string
		months int
	}{
		{"zero", 0},
		{"thirteen", 13},
		{"negative", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := biz.GrantSubscriptionRequest{
				ParentUserID:  1,
				UserID:        200,
				ProductType:   "monthly",
				Months:        tc.months,
				GranterUserID: ptr(uint64(1)),
				Now:           ts(2026, 1, 1),
			}
			_, err := svc.GrantOrRenewSubscription(t.Context(), req)
			require.Error(t, err)
			assert.ErrorIs(t, err, errno.ErrInvalidParameter)
		})
	}
}

// ────────────────────────────────────────────────────────────
// TestGrantSub_IdempotencyReplay
// ────────────────────────────────────────────────────────────

func TestGrantSub_IdempotencyReplay(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)

	key := "idm-replay-001"
	req := biz.GrantSubscriptionRequest{
		ParentUserID:   1,
		UserID:         301,
		ProductType:    "monthly",
		Months:         2,
		GranterUserID:  ptr(uint64(1)),
		IdempotencyKey: ptr(key),
		Now:            ts(2026, 2, 1),
	}

	res1, err := svc.GrantOrRenewSubscription(t.Context(), req)
	require.NoError(t, err)
	assert.False(t, res1.Replayed)

	// Replay the same request.
	res2, err := svc.GrantOrRenewSubscription(t.Context(), req)
	require.NoError(t, err)
	assert.True(t, res2.Replayed)

	// Results should be identical.
	assert.Equal(t, res1.SubscriptionID, res2.SubscriptionID)
	assert.Equal(t, res1.ExpiresAt, res2.ExpiresAt)
	assert.Equal(t, res1.TotalMonthsPurchased, res2.TotalMonthsPurchased)
}

// ────────────────────────────────────────────────────────────
// TestGrantSub_IdempotencyConflict
// ────────────────────────────────────────────────────────────

func TestGrantSub_IdempotencyConflict(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)

	key := "idm-conflict-001"
	req1 := biz.GrantSubscriptionRequest{
		ParentUserID:   1,
		UserID:         401,
		ProductType:    "monthly",
		Months:         2,
		GranterUserID:  ptr(uint64(1)),
		IdempotencyKey: ptr(key),
		Now:            ts(2026, 2, 1),
	}
	_, err := svc.GrantOrRenewSubscription(t.Context(), req1)
	require.NoError(t, err)

	// Same key, different UserID.
	req2 := req1
	req2.UserID = 402
	_, err = svc.GrantOrRenewSubscription(t.Context(), req2)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrIdempotencyKeyConflict)
}
