package credit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// TestCanPerformAIOperation_MemberZeroBalance_Allowed reproduces the dev-acceptance
// bug (free-model-member-only): an active member (trial, in-period) whose credits are
// exhausted (0 balance) is wrongly blocked by the controller-level CanPerformAIOperation
// coarse balance gate — so they cannot run a SOP node even with a 0-priced model. Members
// must be exempt here; the per-call reserve is the authoritative gate.
func TestCanPerformAIOperation_MemberZeroBalance_Allowed(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	uid := uint(9001)
	// Trial member, in-period, 0 remaining credits → IsActiveMember true, balance 0.
	seedPackagesAndAccount(t, db, uid, []seedPackage{{
		Type: model.CreditTypeTrial, RemainCredits: 0, ExpiresAt: time.Now().AddDate(0, 0, 3),
	}})
	// GORM skips zero-value int fields tagged `default:200`, so db.Create (in
	// seedPackagesAndAccount) leaves trial_grant.credits_remaining=200 even though we
	// passed RemainCredits=0. Force the genuine 0 so the member truly has an exhausted
	// balance. (Repo-wide gotcha, documented in .claude/rules/database.md §6.)
	require.NoError(t, db.Exec("UPDATE trial_grant SET credits_remaining = 0 WHERE user_id = ?", uid).Error)

	b := credit.NewCreditBiz(ds)
	credit.InjectCreditBizMembershipSvc(b, membership.NewMembershipService(db))

	user := &model.User{}
	user.ID = uid
	ok, reason := b.CanPerformAIOperation(context.Background(), user, "sop_run")
	require.True(t, ok,
		"active member with 0 balance must pass CanPerformAIOperation (per-call reserve is authoritative); got reason=%q", reason)
}

// TestCanPerformAIOperation_NonMemberZeroBalance_Blocked is the regression guard: a
// non-member with 0 balance must STILL be blocked (the membership exemption must not
// leak to non-members).
func TestCanPerformAIOperation_NonMemberZeroBalance_Blocked(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	uid := uint(9002)
	seedPackagesAndAccount(t, db, uid, nil) // account only → non-member, 0 balance

	b := credit.NewCreditBiz(ds)
	credit.InjectCreditBizMembershipSvc(b, membership.NewMembershipService(db))

	user := &model.User{}
	user.ID = uid
	ok, reason := b.CanPerformAIOperation(context.Background(), user, "sop_run")
	require.False(t, ok, "non-member with 0 balance must still be blocked")
	require.Contains(t, reason, "积分不足")
}
