package admin_b2b

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/b2b_billing"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// AdminB2BController serves the admin-side B2B monthly billing report.
//   GET /v1/admin/b2b-billing-report?month=YYYY-MM
type AdminB2BController struct {
	biz b2b_billing.IB2BBillingBiz
}

// New constructs an AdminB2BController wired to the given biz.
func New(biz b2b_billing.IB2BBillingBiz) *AdminB2BController {
	return &AdminB2BController{biz: biz}
}

// GetBillingReport handles GET /v1/admin/b2b-billing-report.
//
// Query param:
//   month — required, format YYYY-MM (e.g. "2026-04"). Validated by biz.
func (ctrl *AdminB2BController) GetBillingReport(c *gin.Context) {
	month := c.Query("month")
	if month == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("month 参数必填，格式 YYYY-MM"), nil)
		return
	}

	report, err := ctrl.biz.GetBillingReport(c, month)
	if err != nil {
		log.C(c).Errorw("Failed to get B2B billing report", "month", month, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, report)
}
