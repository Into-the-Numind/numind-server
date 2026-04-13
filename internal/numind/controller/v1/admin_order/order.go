package admin_order

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
)

type AdminOrderController struct {
	ds store.IStore
}

func New(ds store.IStore) *AdminOrderController {
	return &AdminOrderController{ds: ds}
}

// ListOrders GET /v1/admin/orders
func (ctrl *AdminOrderController) ListOrders(c *gin.Context) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	orders, total, err := ctrl.ds.Orders().ListAll(c, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{"items": orders, "total": total})
}

// GetOrder GET /v1/admin/orders/:id
func (ctrl *AdminOrderController) GetOrder(c *gin.Context) {
	orderID, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	order, err := ctrl.ds.Orders().GetByID(c, orderID)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("订单不存在"), nil)
		return
	}
	core.WriteResponse(c, nil, order)
}
