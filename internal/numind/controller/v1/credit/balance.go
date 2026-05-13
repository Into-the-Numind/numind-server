package credit

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// ChildBalanceView is the response shape for the parent-scoped balance query.
// Per spec §5.4: booster_total / booster_usable are omitted for privacy;
// only the subscription / trial / membership_state fields are exposed.
type ChildBalanceView struct {
	MembershipState string     `json:"membership_state"`
	TrialRemaining  int64      `json:"trial_remaining"`
	CycleRemaining  int64      `json:"cycle_remaining"`
	CycleEnd        *time.Time `json:"cycle_end,omitempty"`
	SubExpiresAt    *time.Time `json:"sub_expires_at,omitempty"`
	TrialExpiresAt  *time.Time `json:"trial_expires_at,omitempty"`
}

// GetChildBalance handles GET /v1/users/children/:child_id/balance.
//
// Auth: parent user token.
// Validates parent-child relationship via Customers().GetSubUser.
// Returns ChildBalanceView (no booster fields — privacy).
func (c *CreditController) GetChildBalance(ctx *gin.Context) {
	parent := middleware.GetCurrentUser(ctx)
	if parent == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	childIDStr := ctx.Param("child_id")
	childID64, err := strconv.ParseUint(childIDStr, 10, 64)
	if err != nil || childID64 == 0 {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("无效的子账户 ID: %s", childIDStr), nil)
		return
	}
	childID := uint(childID64)

	// Validate parent-child relationship.
	_, err = c.ds.Customers().GetSubUser(ctx, parent.ID, childID)
	if err != nil {
		log.C(ctx).Warnw("GetChildBalance: parent-child validation failed",
			"parent_id", parent.ID, "child_id", childID, "err", err)
		core.WriteResponse(ctx, errno.ErrForbidden.SetMessage("无权查看该子账户余额"), nil)
		return
	}

	view, err := c.membershipSvc.GetBalance(ctx, uint64(childID), time.Now())
	if err != nil {
		log.C(ctx).Errorw("GetChildBalance: GetBalance failed", "child_id", childID, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(ctx, nil, ChildBalanceView{
		MembershipState: view.MembershipState,
		TrialRemaining:  view.TrialRemaining,
		CycleRemaining:  view.CycleRemaining,
		CycleEnd:        view.CycleEnd,
		SubExpiresAt:    view.SubExpiresAt,
		TrialExpiresAt:  view.TrialExpiresAt,
	})
}
