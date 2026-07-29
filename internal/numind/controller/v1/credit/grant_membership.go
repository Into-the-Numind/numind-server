package credit

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	membershipmodel "numind-server/internal/pkg/model/membership"
)

// GrantMembershipRequest is the JSON body for POST /v1/users/children/:child_id/grant-membership.
type GrantMembershipRequest struct {
	ProductType string `json:"product_type" binding:"required,oneof=trial weekly monthly"`
	Months      int    `json:"months"       binding:"omitempty,min=0,max=12"`
}

// GrantMembership handles POST /v1/users/children/:child_id/grant-membership.
//
// Auth context: current user = parent (B2B2C account).
// Path param:   :child_id = target child user ID.
// Header:       Idempotency-Key (enforced by RequireIdempotencyKey middleware).
// Body:         { product_type: "trial"|"weekly"|"monthly", months: 0-12 }.
//
// Dispatches to MembershipService.GrantTrial (trial) or
// MembershipService.GrantWeeklySubscription / GrantOrRenewSubscription.
func (c *CreditController) GrantMembership(ctx *gin.Context) {
	log.C(ctx).Infow("GrantMembership called")

	// 1. Extract authenticated parent user.
	parent := middleware.GetCurrentUser(ctx)
	if parent == nil {
		core.WriteResponse(ctx, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}

	// 2. Parse :child_id path param.
	childIDRaw := ctx.Param("child_id")
	childID64, err := strconv.ParseUint(childIDRaw, 10, 64)
	if err != nil || childID64 == 0 {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("无效的子账户 ID: %s", childIDRaw), nil)
		return
	}

	// 3. Bind JSON body.
	var req GrantMembershipRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 4. Validate product-type-specific rules.
	if req.ProductType == "trial" && req.Months > 0 {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("trial 类型不接受 months 参数"), nil)
		return
	}
	if req.ProductType == "weekly" && req.Months > 0 {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("weekly 类型不接受 months 参数"), nil)
		return
	}
	if req.ProductType == "monthly" && (req.Months < 1 || req.Months > 12) {
		core.WriteResponse(ctx, errno.ErrBind.SetMessage("monthly 类型 months 必须在 1-12 之间"), nil)
		return
	}

	// 5. Extract idempotency key injected by RequireIdempotencyKey middleware.
	idemKeyRaw := ctx.GetString("idempotency_key")
	var idemKey *string
	if idemKeyRaw != "" {
		idemKey = &idemKeyRaw
	}

	parentID := uint64(parent.ID)

	// 5.5. P0 audit fix: verify caller is parent of child_id.
	// child_id == caller.ID is allowed (self-grant by parent account).
	// Without this check, any authenticated user could grant trial/subscription
	// to any other user by guessing IDs.
	if uint(childID64) != parent.ID {
		// Caller is granting to another user; verify parent-child relationship.
		childUser, err := c.ds.Users().GetByID(ctx, uint(childID64))
		if err != nil {
			log.C(ctx).Warnw("Grant membership: child user lookup failed",
				"caller_id", parent.ID, "child_id", childID64, "err", err)
			core.WriteResponse(ctx, errno.ErrUserNotFound, nil)
			return
		}
		if childUser.ParentUserID == nil || *childUser.ParentUserID != parent.ID {
			log.C(ctx).Warnw("Grant membership: caller is not parent of child",
				"caller_id", parent.ID, "child_id", childID64,
				"child_parent_id", childUser.ParentUserID)
			core.WriteResponse(ctx, errno.ErrForbidden.SetMessage("无权为该用户开通会员"), nil)
			return
		}
	}

	// 6. Dispatch by product type.
	switch req.ProductType {
	case "trial":
		res, err := c.membershipSvc.GrantTrial(ctx, membership.GrantTrialRequest{
			UserID:         childID64,
			GranterUserID:  &parentID,
			IdempotencyKey: idemKey,
		})
		if err != nil {
			log.C(ctx).Errorw("GrantTrial failed",
				"parent_id", parentID,
				"child_id", childID64,
				"err", err)
			core.WriteResponse(ctx, mapMembershipError(err), nil)
			return
		}
		core.WriteResponse(ctx, nil, gin.H{
			"child_user_id": childID64,
			"product_type":  "trial",
			"event_type":    "trial_granted",
			"expires_at":    res.TrialGrant.ExpiresAt,
		})

	case "weekly":
		finalParentID := parentID
		if parentID == childID64 && parent.ParentUserID == nil {
			finalParentID = 0
		}
		res, err := c.membershipSvc.GrantWeeklySubscription(ctx, membership.GrantWeeklySubscriptionRequest{
			ParentUserID:   finalParentID,
			UserID:         childID64,
			GranterUserID:  &parentID,
			IdempotencyKey: idemKey,
		})
		if err != nil {
			log.C(ctx).Errorw("GrantWeeklySubscription failed",
				"parent_id", parentID,
				"child_id", childID64,
				"err", err)
			core.WriteResponse(ctx, mapMembershipError(err), nil)
			return
		}
		eventType := scenarioToEventType(res.Scenario)
		core.WriteResponse(ctx, nil, gin.H{
			"child_user_id": childID64,
			"product_type":  "weekly",
			"event_id":      res.EventID,
			"event_type":    eventType,
			"expires_at":    res.ExpiresAt,
			"scenario":      res.Scenario,
			"days":          membershipmodel.WeeklyDurationDays,
		})

	case "monthly":
		finalParentID := parentID
		if parentID == childID64 && parent.ParentUserID == nil {
			finalParentID = 0
		}
		res, err := c.membershipSvc.GrantOrRenewSubscription(ctx, membership.GrantSubscriptionRequest{
			ParentUserID:   finalParentID,
			UserID:         childID64,
			ProductType:    "monthly",
			Months:         req.Months,
			GranterUserID:  &parentID,
			IdempotencyKey: idemKey,
		})
		if err != nil {
			log.C(ctx).Errorw("GrantOrRenewSubscription failed",
				"parent_id", parentID,
				"child_id", childID64,
				"months", req.Months,
				"err", err)
			core.WriteResponse(ctx, mapMembershipError(err), nil)
			return
		}
		eventType := scenarioToEventType(res.Scenario)
		core.WriteResponse(ctx, nil, gin.H{
			"child_user_id": childID64,
			"product_type":  "monthly",
			"event_id":      res.EventID,
			"event_type":    eventType,
			"expires_at":    res.ExpiresAt,
			"scenario":      res.Scenario,
		})
	}
}

// scenarioToEventType maps a GrantResult.Scenario to a wire-level event_type string.
func scenarioToEventType(scenario string) string {
	switch scenario {
	case "renew":
		return "sub_renewed"
	default:
		// "new" and "reopen" both map to sub_granted (no sub_reopened enum).
		return "sub_granted"
	}
}

// mapMembershipError translates membership biz sentinel errors to HTTP-aware errno values.
func mapMembershipError(err error) *errno.Errno {
	switch {
	case errors.Is(err, errno.ErrTrialAlreadyGranted):
		return errno.ErrTrialAlreadyGranted
	case errors.Is(err, errno.ErrTrialNotAllowedForActivePro):
		return errno.ErrTrialNotAllowedForActivePro
	case errors.Is(err, errno.ErrIdempotencyKeyConflict):
		return errno.ErrIdempotencyKeyConflict
	case errors.Is(err, errno.ErrMembershipSelfPurchaseDisabled):
		return errno.ErrMembershipSelfPurchaseDisabled
	case errors.Is(err, errno.ErrInvalidParameter):
		return errno.ErrBind.SetMessage("%s", err.Error())
	default:
		return errno.ErrInternalServer.SetMessage("%s", err.Error())
	}
}
