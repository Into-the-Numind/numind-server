package admin_credit

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// AdminCreditWithMembership extends AdminCreditController with a MembershipService
// for the admin balance query endpoint.
//
// The embedding approach avoids changing the existing New() constructor signature.
type AdminCreditWithMembership struct {
	*AdminCreditController
	membershipSvc *membership.MembershipService
}

// NewWithMembership constructs an AdminCreditController that also exposes
// GetUserBalance (GET /v1/admin/users/:user_id/balance).
func NewWithMembership(ctrl *AdminCreditController, svc *membership.MembershipService) *AdminCreditWithMembership {
	return &AdminCreditWithMembership{
		AdminCreditController: ctrl,
		membershipSvc:         svc,
	}
}

// FullBalanceView is the response shape for admin-scoped balance queries.
// Includes booster fields (admin has full visibility).
type FullBalanceView struct {
	MembershipState string     `json:"membership_state"`
	TrialRemaining  int64      `json:"trial_remaining"`
	CycleRemaining  int64      `json:"cycle_remaining"`
	CycleEnd        *time.Time `json:"cycle_end,omitempty"`
	// BoosterTotal: field name is "Total" but value is credits_remaining
	// (raw aggregate balance — see BalanceView.BoosterTotal in biz/membership/state.go).
	// Not a cumulative purchased total; render as single number, not as denominator.
	BoosterTotal   int64      `json:"booster_total"`
	BoosterUsable  int64      `json:"booster_usable"`
	SubExpiresAt   *time.Time `json:"sub_expires_at,omitempty"`
	TrialExpiresAt *time.Time `json:"trial_expires_at,omitempty"`
}

// GetUserBalance handles GET /v1/admin/users/:id/balance.
//
// Admin token required (enforced by AdminAuthMiddleware in admin_router.go).
// Returns FullBalanceView including booster fields.
//
// Note: path param is :id (not :user_id) to match the existing sibling routes
// under /users/ (gin disallows mixing :id and :user_id at the same prefix).
func (ctrl *AdminCreditWithMembership) GetUserBalance(c *gin.Context) {
	idStr := c.Param("id")
	uid, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || uid == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的用户ID: %s", idStr), nil)
		return
	}

	view, err := ctrl.membershipSvc.GetBalance(c, uid, time.Now())
	if err != nil {
		log.C(c).Errorw("AdminGetUserBalance: GetBalance failed", "user_id", uid, "err", err)
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, FullBalanceView{
		MembershipState: view.MembershipState,
		TrialRemaining:  view.TrialRemaining,
		CycleRemaining:  view.CycleRemaining,
		CycleEnd:        view.CycleEnd,
		BoosterTotal:    view.BoosterTotal,
		BoosterUsable:   view.BoosterUsable,
		SubExpiresAt:    view.SubExpiresAt,
		TrialExpiresAt:  view.TrialExpiresAt,
	})
}
