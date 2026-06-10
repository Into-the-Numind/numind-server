package membership_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	biz "numind-server/internal/numind/biz/membership"
)

// IsActiveMember (feature free-model-member-only, C2/AC2)判定"会员"按有效期，
// 不看剩余积分：sub 未过期 OR trial 未过期即会员；store 错误必须传播。

func TestIsActiveMember_SubInPeriod(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	seedActiveSub(t, db, 7001, now.AddDate(0, -1, 0), now.AddDate(0, 1, 0), 1)

	ok, err := svc.IsActiveMember(ctx, 7001, now)
	require.NoError(t, err)
	require.True(t, ok, "unexpired sub => member")
}

// AC2 关键：trial 在期但积分用光（CreditsRemaining=0）仍算会员。
func TestIsActiveMember_TrialZeroCreditsStillMember(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	seedActiveTrial(t, db, 7002, now, now.AddDate(0, 0, 3), 0) // 0 credits remaining

	ok, err := svc.IsActiveMember(ctx, 7002, now)
	require.NoError(t, err)
	require.True(t, ok, "unexpired trial with 0 credits is still a member (AC2)")
}

func TestIsActiveMember_AllExpired(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	seedActiveSub(t, db, 7003, now.AddDate(0, -2, 0), now.AddDate(0, -1, 0), 1)     // expired sub
	seedActiveTrial(t, db, 7003, now.AddDate(0, 0, -10), now.AddDate(0, 0, -7), 50) // expired trial

	ok, err := svc.IsActiveMember(ctx, 7003, now)
	require.NoError(t, err)
	require.False(t, ok, "all expired => not a member")
}

func TestIsActiveMember_NoRecords(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	ok, err := svc.IsActiveMember(ctx, 7004, now)
	require.NoError(t, err)
	require.False(t, ok, "no sub and no trial => not a member")
}

// booster-only (sub expired, only leftover booster) => 非会员（用户定义=仅 sub/trial）。
func TestIsActiveMember_BoosterOnlyNotMember(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	seedActiveSub(t, db, 7005, now.AddDate(0, -2, 0), now.AddDate(0, -1, 0), 1) // expired sub
	seedBooster(t, db, 7005, 600)

	ok, err := svc.IsActiveMember(ctx, 7005, now)
	require.NoError(t, err)
	require.False(t, ok, "booster-only (no active sub/trial) => not a member")
}

// store 错误必须传播（P0-1）：关闭底层 DB 后查询失败 → 返回 error，不静默当非会员。
func TestIsActiveMember_StoreErrorPropagated(t *testing.T) {
	db := newTestDB(t)
	svc := biz.NewMembershipService(db)
	ctx := context.Background()
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	ok, err := svc.IsActiveMember(ctx, 7006, now)
	require.Error(t, err, "store failure must propagate, not be swallowed")
	require.False(t, ok)
}
