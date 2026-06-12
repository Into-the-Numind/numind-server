package membership_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	biz "numind-server/internal/numind/biz/membership"
	model "numind-server/internal/pkg/model/membership"
)

// ─────────────────────────────────────────────────────────────────────────────
// Seed helpers
// ─────────────────────────────────────────────────────────────────────────────

// seedActiveTrial inserts an active trial_grant that expires at expiresAt with
// the given creditsRemaining.
func seedActiveTrial(t *testing.T, db *gorm.DB, userID uint64, now, expiresAt time.Time, creditsRemaining int) {
	t.Helper()
	tg := &model.TrialGrant{
		UserID:           userID,
		GrantedAt:        now,
		ExpiresAt:        expiresAt,
		CreditsRemaining: creditsRemaining,
		Source:           model.SourceB2BGrant,
		CreatedAt:        now,
	}
	require.NoError(t, db.Create(tg).Error)
}

// seedActiveSub inserts an active subscription starting at currentStartedAt and
// expiring at expiresAt.
func seedActiveSub(t *testing.T, db *gorm.DB, userID uint64, currentStartedAt, expiresAt time.Time, months int) *model.Subscription {
	t.Helper()
	sub := &model.Subscription{
		UserID:               userID,
		FirstStartedAt:       currentStartedAt,
		CurrentStartedAt:     currentStartedAt,
		ExpiresAt:            expiresAt,
		TotalMonthsPurchased: months,
		Source:               model.SourceB2BGrant,
		CreatedAt:            currentStartedAt,
		UpdatedAt:            currentStartedAt,
	}
	require.NoError(t, db.Create(sub).Error)
	return sub
}

// seedBooster inserts a user_booster_balance row for userID.
func seedBooster(t *testing.T, db *gorm.DB, userID uint64, creditsRemaining int64) {
	t.Helper()
	bal := &model.UserBoosterBalance{
		UserID:           userID,
		CreditsRemaining: creditsRemaining,
		UpdatedAt:        time.Now().UTC(),
	}
	require.NoError(t, db.Create(bal).Error)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetMembershipState tests
// ─────────────────────────────────────────────────────────────────────────────

// TestGetMembershipState_Free verifies that a user with no trial and no
// subscription gets DisplayState="free" and all active flags false.
func TestGetMembershipState_Free(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	state, err := svc.GetMembershipState(ctx, 9001, now)
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.Equal(t, "free", state.DisplayState)
	assert.False(t, state.TrialActive)
	assert.False(t, state.SubActive)
	assert.True(t, state.BoosterFrozen, "booster must be frozen when no trial and no sub")
	assert.Nil(t, state.TrialExpiresAt)
	assert.Nil(t, state.SubExpiresAt)
	assert.Nil(t, state.SubFirstStartedAt)
}

// TestGetMembershipState_TrialOnly verifies that a user with only an active
// trial (no sub) gets DisplayState="trial", TrialActive=true, SubActive=false.
func TestGetMembershipState_TrialOnly(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	expiresAt := now.AddDate(0, 0, 3)
	seedActiveTrial(t, db, 9002, now, expiresAt, 200)

	state, err := svc.GetMembershipState(ctx, 9002, now)
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.Equal(t, "trial", state.DisplayState)
	assert.True(t, state.TrialActive)
	assert.False(t, state.SubActive)
	assert.False(t, state.BoosterFrozen, "booster must NOT be frozen when trial is active")
	require.NotNil(t, state.TrialExpiresAt)
	assert.Equal(t, expiresAt, *state.TrialExpiresAt)
	assert.Nil(t, state.SubExpiresAt)
}

// TestGetMembershipState_ProOnly verifies that a user with only an active
// subscription (no trial) gets DisplayState="pro", SubActive=true, TrialActive=false.
func TestGetMembershipState_ProOnly(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	subStart := now.AddDate(0, -1, 0)
	subExpires := now.AddDate(0, 1, 0)
	seedActiveSub(t, db, 9003, subStart, subExpires, 2)

	state, err := svc.GetMembershipState(ctx, 9003, now)
	require.NoError(t, err)
	require.NotNil(t, state)

	assert.Equal(t, "pro", state.DisplayState)
	assert.False(t, state.TrialActive)
	assert.True(t, state.SubActive)
	assert.False(t, state.BoosterFrozen, "booster must NOT be frozen when sub is active")
	require.NotNil(t, state.SubExpiresAt)
	assert.Equal(t, subExpires, *state.SubExpiresAt)
	require.NotNil(t, state.SubFirstStartedAt)
	assert.Equal(t, subStart, *state.SubFirstStartedAt)
	assert.Nil(t, state.TrialExpiresAt)
}

// TestGetMembershipStateBatch_IncludesBoosterTotal 验证批量接口把
// user_booster_balance 的剩余积分填到 BoosterTotal（客户列表「加量包」列数据源）。
// 缺 booster 行的用户应为 0。
func TestGetMembershipStateBatch_IncludesBoosterTotal(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	seedBooster(t, db, 9101, 600)
	seedBooster(t, db, 9102, 1200)
	// 9103 无 booster 行 → 期望 0

	out, err := svc.GetMembershipStateBatch(ctx, []uint64{9101, 9102, 9103}, now)
	require.NoError(t, err)

	require.NotNil(t, out[9101])
	assert.Equal(t, int64(600), out[9101].BoosterTotal)
	require.NotNil(t, out[9102])
	assert.Equal(t, int64(1200), out[9102].BoosterTotal)
	require.NotNil(t, out[9103])
	assert.Equal(t, int64(0), out[9103].BoosterTotal, "无 booster 行应为 0")
}

// TestGetMembershipState_TrialOverlapsPro is the key US-2 test:
// when BOTH trial and subscription are active, DisplayState must be "trial".
// (BoosterFrozen=false because at least one membership is active.)
func TestGetMembershipState_TrialOverlapsPro(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	// Active trial
	trialExpires := now.AddDate(0, 0, 2)
	seedActiveTrial(t, db, 9004, now, trialExpires, 50)
	// Active subscription overlapping with trial
	subStart := now.AddDate(0, 0, -1)
	subExpires := now.AddDate(0, 2, 0)
	seedActiveSub(t, db, 9004, subStart, subExpires, 3)

	state, err := svc.GetMembershipState(ctx, 9004, now)
	require.NoError(t, err)
	require.NotNil(t, state)

	// US-2: trial takes priority over sub in display state.
	assert.Equal(t, "trial", state.DisplayState, "US-2: trial must take display priority over sub")
	assert.True(t, state.TrialActive)
	assert.True(t, state.SubActive)
	assert.False(t, state.BoosterFrozen)
	require.NotNil(t, state.TrialExpiresAt)
	assert.Equal(t, trialExpires, *state.TrialExpiresAt)
	require.NotNil(t, state.SubExpiresAt)
	assert.Equal(t, subExpires, *state.SubExpiresAt)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetBalance tests
// ─────────────────────────────────────────────────────────────────────────────

// TestGetBalance_NoCycleRowYet tests INV-20: when sub is active but the cycle
// row has not been lazily created yet, CycleRemaining defaults to 2000.
func TestGetBalance_NoCycleRowYet(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	subStart := now.AddDate(0, 0, -5) // started 5 days ago
	subExpires := now.AddDate(0, 1, 0)
	seedActiveSub(t, db, 9010, subStart, subExpires, 2)
	// No cycle row inserted — simulates first visit before any deduction

	bal, err := svc.GetBalance(ctx, 9010, now)
	require.NoError(t, err)
	require.NotNil(t, bal)

	// INV-20: cycle row not yet created → default to cycleCredits (2000)
	assert.Equal(t, int64(2000), bal.CycleRemaining, "INV-20: no cycle row → default 2000")
	assert.Equal(t, "pro", bal.MembershipState)
	assert.Equal(t, int64(0), bal.TrialRemaining)
	assert.Equal(t, int64(0), bal.BoosterTotal)
	assert.Equal(t, int64(0), bal.BoosterUsable)
}

// TestGetBalance_BoosterFrozen tests INV-19: when neither trial nor sub is active,
// booster is frozen: BoosterUsable=0 but BoosterTotal still reflects the balance.
func TestGetBalance_BoosterFrozen(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	// No active trial, no active sub → booster frozen
	seedBooster(t, db, 9011, 600)

	bal, err := svc.GetBalance(ctx, 9011, now)
	require.NoError(t, err)
	require.NotNil(t, bal)

	// INV-19: booster frozen
	assert.Equal(t, "free", bal.MembershipState)
	assert.Equal(t, int64(600), bal.BoosterTotal, "BoosterTotal reflects actual balance even when frozen")
	assert.Equal(t, int64(0), bal.BoosterUsable, "INV-19: BoosterUsable=0 when frozen")
	assert.Equal(t, int64(0), bal.CycleRemaining)
	assert.Equal(t, int64(0), bal.TrialRemaining)
}

// TestGetBalance_TrialUserNoBooster verifies a trial user with no sub:
// TrialRemaining reflects the trial credits_remaining; CycleRemaining=0 (no sub);
// BoosterUsable=balance when booster exists (trial is active → not frozen).
func TestGetBalance_TrialUserNoBooster(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	trialExpires := now.AddDate(0, 0, 2)
	seedActiveTrial(t, db, 9012, now, trialExpires, 150)
	seedBooster(t, db, 9012, 600)

	bal, err := svc.GetBalance(ctx, 9012, now)
	require.NoError(t, err)
	require.NotNil(t, bal)

	assert.Equal(t, "trial", bal.MembershipState)
	assert.Equal(t, int64(150), bal.TrialRemaining)
	assert.Equal(t, int64(0), bal.CycleRemaining, "no sub → CycleRemaining=0")
	assert.Equal(t, int64(600), bal.BoosterTotal)
	assert.Equal(t, int64(600), bal.BoosterUsable, "trial active → booster usable")
}

// TestGetBalance_DTOFieldCompleteness verifies that when a sub is active and
// a cycle row exists, all DTO fields are populated with non-zero values.
//
// Timeline (all times are midnight UTC for determinism):
//
//	subStart  = 2026-03-01 → month 0 cycle: [2026-03-01, 2026-04-01)
//	now       = 2026-03-15 → inside month 0 cycle
//	subExpires = 2026-06-01 (3 months from subStart)
//	cycleStart = 2026-03-01, cycleEnd = 2026-04-01
func TestGetBalance_DTOFieldCompleteness(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()

	// Use midnight times to avoid AnchorAddMonths time-of-day drift issues.
	subStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC) // mid-way through month 0
	subExpires := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	sub := seedActiveSub(t, db, 9013, subStart, subExpires, 3)

	// The cycle for month 0: cycleStart=subStart, cycleEnd=AnchorAddMonths(subStart,1)=2026-04-01
	cycleStart := subStart // 2026-03-01 00:00:00
	cycleEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	cycle := &model.CreditCycle{
		UserID:           9013,
		SubscriptionID:   sub.ID,
		CycleStart:       cycleStart,
		CycleEnd:         cycleEnd,
		CreditsGranted:   2000,
		CreditsRemaining: 1800,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, db.Create(cycle).Error)
	seedBooster(t, db, 9013, 300)

	bal, err := svc.GetBalance(ctx, 9013, now)
	require.NoError(t, err)
	require.NotNil(t, bal)

	assert.Equal(t, "pro", bal.MembershipState)
	assert.Equal(t, int64(1800), bal.CycleRemaining, "existing cycle row → actual remaining")
	assert.Equal(t, int64(300), bal.BoosterTotal)
	assert.Equal(t, int64(300), bal.BoosterUsable, "sub active → booster usable")
	assert.Equal(t, int64(0), bal.TrialRemaining)
	require.NotNil(t, bal.SubExpiresAt)
	assert.Equal(t, subExpires, *bal.SubExpiresAt)
	require.NotNil(t, bal.CycleEnd)
	assert.Equal(t, cycleEnd, *bal.CycleEnd)
}
