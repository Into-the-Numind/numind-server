package order

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// OrderStatusResponse is the response shape for GET /v1/orders/:id/status.
// Designed for polling use (e.g. booster purchase flow in Task 19 Frontend).
type OrderStatusResponse struct {
	OrderID     uint64     `json:"order_id"`
	Status      string     `json:"status"`
	PaidAt      *time.Time `json:"paid_at,omitempty"`
	AmountCents int64      `json:"amount_cents"`
	ProductType string     `json:"product_type"`
	Quantity    int        `json:"quantity"`
}

// GetOrderStatus handles GET /v1/orders/:id/status.
//
// Auth: user or parent-account token.
// Access rules:
//   - order.UserID == token user (self / beneficiary) → allow
//   - order.PayerID == token user (payer viewing own order) → allow
//   - token user is parent of order.UserID → allow (parent view)
//   - otherwise → 403
//
// Returns a lightweight status payload suitable for polling.
func (ctrl *OrderController) GetOrderStatus(c *gin.Context) {
	requester := middleware.GetCurrentUser(c)
	if requester == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	orderIDStr := c.Param("id")
	orderID, err := strconv.ParseUint(orderIDStr, 10, 64)
	if err != nil || orderID == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的订单ID: %s", orderIDStr), nil)
		return
	}

	order, err := ctrl.ds.Orders().GetByID(c, orderID)
	if err != nil {
		log.C(c).Warnw("GetOrderStatus: order not found", "order_id", orderID, "err", err)
		core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("订单不存在"), nil)
		return
	}

	// Access control: token user must be payer, beneficiary, or parent of beneficiary.
	requesterID := requester.ID
	isAuthorized := requesterID == order.PayerID || requesterID == order.UserID

	if !isAuthorized {
		// Check if requester is the parent of the beneficiary (B2B2C).
		child, err := ctrl.ds.Users().GetByID(c, order.UserID)
		if err == nil && child.ParentUserID != nil && *child.ParentUserID == requesterID {
			isAuthorized = true
		}
	}

	if !isAuthorized {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权查看该订单"), nil)
		return
	}

	// quantity is stored in the Months field for booster orders (see payment.go comment).
	core.WriteResponse(c, nil, OrderStatusResponse{
		OrderID:     order.ID,
		Status:      order.PayStatus,
		PaidAt:      order.PaidAt,
		AmountCents: order.Amount,
		ProductType: order.ProductType,
		Quantity:    order.Months,
	})
}
