package parent_billing

import (
	"errors"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/b2b_billing"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// ParentBillingController serves the user-side (parent) self-service billing report.
//
//	GET /v1/users/me/billing-report?month=YYYY-MM   (user_token, parent accounts only)
type ParentBillingController struct {
	biz b2b_billing.IB2BBillingBiz
}

// New constructs a ParentBillingController wired to the given biz.
func New(biz b2b_billing.IB2BBillingBiz) *ParentBillingController {
	return &ParentBillingController{biz: biz}
}

// GetMyBillingReport handles GET /v1/users/me/billing-report.
//
// Parent id is taken from the auth context only (never a client param) to
// prevent cross-parent access. Non-parent callers get 403.
func (ctrl *ParentBillingController) GetMyBillingReport(c *gin.Context) {
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	month := c.Query("month")
	if month == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("month 参数必填，格式 YYYY-MM"), nil)
		return
	}
	if !b2b_billing.IsValidMonth(month) {
		core.WriteResponse(c, errno.ErrBind.SetMessage("month 格式错误，应为 YYYY-MM"), nil)
		return
	}

	report, err := ctrl.biz.GetBillingReportForParent(c, month, currentUser.ID)
	if err != nil {
		if errors.Is(err, b2b_billing.ErrNotParentAccount) {
			core.WriteResponse(c, errno.ErrForbidden.SetMessage("仅父账户可查看费用对账"), nil)
			return
		}
		// 不向 C 端泄露内部错误详情（SQL/表名/userID）。err 仅入日志。
		log.C(c).Errorw("Failed to get parent billing report", "month", month, "userID", currentUser.ID, "err", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("内部错误，请稍后重试"), nil)
		return
	}

	core.WriteResponse(c, nil, report)
}
