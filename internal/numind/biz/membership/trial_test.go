package membership_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/pkg/errno"
	model "numind-server/internal/pkg/model/membership"
)

// ptr helpers
func strPtr(s string) *string { return &s }
func u64Ptr(v uint64) *uint64 { return &v }

// baseTime is a fixed point in time used across test cases.
var baseTime = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

// TestGrantTrial_HappyPath verifies that a fresh trial grant is created with
// the correct credits, expiry (+3 days), and event log entry.
func TestGrantTrial_HappyPath(t *testing.T) {
	db := newTestDB(t)
	svc := membership.NewMembershipService(db)
	ctx := context.Background()

	idemKey := "hp-idem-001"
	req := membership.GrantTrialRequest{
		UserID:         101,
		GranterUserID:  u64Ptr(1),
		IdempotencyKey: strPtr(idemKey),
		Now:            baseTime,
	}

	res, err := svc.GrantTrial(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Replayed, "first call must not be a replay")
	require.NotNil(t, res.TrialGrant)
	assert.Equal(t, uint64(101), res.TrialGrant.UserID)
	assert.Equal(t, 200, res.TrialGrant.CreditsRemaining)
	assert.Equal(t, model.SourceB2BGrant, res.TrialGrant.Source)
	assert.Equal(t, baseTime.AddDate(0, 0, 3), res.TrialGrant.ExpiresAt)

	// Verify event was written.
	var count int64
	require.NoError(t, db.Table("membership_event").
		Where("user_id = ? AND event_type = ?", 101, model.EventTypeTrialGranted).
		Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestGrantTrial_AlreadyGranted verifies that a second call for the same user
// (even with a different idempotency key) returns ErrTrialAlreadyGranted.
func TestGrantTrial_AlreadyGranted(t *testing.T) {
	db := newTestDB(t)
	svc := membership.NewMembershipService(db)
	ctx := context.Background()

	// First grant succeeds.
	_, err := svc.GrantTrial(ctx, membership.GrantTrialRequest{
		UserID:         202,
		GranterUserID:  u64Ptr(1),
		IdempotencyKey: strPtr("ag-idem-first"),
		Now:            baseTime,
	})
	require.NoError(t, err)

	// Second attempt with a different idempotency key must be rejected.
	_, err = svc.GrantTrial(ctx, membership.GrantTrialRequest{
		UserID:         202,
		GranterUserID:  u64Ptr(1),
		IdempotencyKey: strPtr("ag-idem-second"),
		Now:            baseTime.Add(time.Minute),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrTrialAlreadyGranted,
		"second grant for same user must return ErrTrialAlreadyGranted")
}

// TestGrantTrial_BlockedByActivePro verifies that a user with an active
// subscription cannot receive a trial grant.
func TestGrantTrial_BlockedByActivePro(t *testing.T) {
	db := newTestDB(t)
	svc := membership.NewMembershipService(db)
	ctx := context.Background()

	// Seed an active subscription for user 303.
	sub := &model.Subscription{
		UserID:               303,
		FirstStartedAt:       baseTime.AddDate(0, -1, 0),
		CurrentStartedAt:     baseTime.AddDate(0, -1, 0),
		ExpiresAt:            baseTime.AddDate(0, 1, 0), // expires in the future
		TotalMonthsPurchased: 2,
		Source:               model.SourceB2BGrant,
		GranterUserID:        u64Ptr(1),
		CreatedAt:            baseTime.AddDate(0, -1, 0),
		UpdatedAt:            baseTime.AddDate(0, -1, 0),
	}
	require.NoError(t, db.Create(sub).Error)

	_, err := svc.GrantTrial(ctx, membership.GrantTrialRequest{
		UserID:         303,
		GranterUserID:  u64Ptr(1),
		IdempotencyKey: strPtr("pro-blocked-idem-001"),
		Now:            baseTime,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrTrialNotAllowedForActivePro,
		"active Pro user must not be granted a trial")
}

// TestGrantTrial_IdempotencyReplay_SameBody verifies that replaying the same
// idempotency key with the same UserID returns Replayed=true and the original
// TrialGrant without writing a second row.
func TestGrantTrial_IdempotencyReplay_SameBody(t *testing.T) {
	db := newTestDB(t)
	svc := membership.NewMembershipService(db)
	ctx := context.Background()

	idemKey := "replay-same-001"
	req := membership.GrantTrialRequest{
		UserID:         404,
		GranterUserID:  u64Ptr(1),
		IdempotencyKey: strPtr(idemKey),
		Now:            baseTime,
	}

	res1, err := svc.GrantTrial(ctx, req)
	require.NoError(t, err)
	assert.False(t, res1.Replayed)

	// Replay with identical body.
	res2, err := svc.GrantTrial(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res2)
	assert.True(t, res2.Replayed, "second call with same key+user must be a replay")

	// Exactly one trial_grant row must exist.
	var count int64
	require.NoError(t, db.Table("trial_grant").Where("user_id = ?", 404).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// TestGrantTrial_IdempotencyConflict_DifferentBody verifies that reusing the
// same idempotency key for a different UserID returns ErrIdempotencyKeyConflict.
func TestGrantTrial_IdempotencyConflict_DifferentBody(t *testing.T) {
	db := newTestDB(t)
	svc := membership.NewMembershipService(db)
	ctx := context.Background()

	idemKey := "conflict-key-001"

	// First call: user 501.
	_, err := svc.GrantTrial(ctx, membership.GrantTrialRequest{
		UserID:         501,
		GranterUserID:  u64Ptr(1),
		IdempotencyKey: strPtr(idemKey),
		Now:            baseTime,
	})
	require.NoError(t, err)

	// Second call: same key, different user (502) → conflict.
	_, err = svc.GrantTrial(ctx, membership.GrantTrialRequest{
		UserID:         502,
		GranterUserID:  u64Ptr(1),
		IdempotencyKey: strPtr(idemKey),
		Now:            baseTime.Add(time.Minute),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrIdempotencyKeyConflict,
		"same idem key with different user must return ErrIdempotencyKeyConflict")
}
