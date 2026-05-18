package order

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	paymentbiz "numind-server/internal/numind/biz/payment"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// OrderController B 客户订单控制器
type OrderController struct {
	paymentBiz paymentbiz.IPaymentBiz
	ds         store.IStore
}

// New 创建订单控制器实例
func New(paymentBiz paymentbiz.IPaymentBiz, ds store.IStore) *OrderController {
	return &OrderController{
		paymentBiz: paymentBiz,
		ds:         ds,
	}
}

// createOrderRequest 创建订单请求体
// Spec §5.2: Only product_type=booster is accepted; trial/monthly/yearly go via grant path.
// Quantity specifies the number of booster units to purchase (1–10000).
type createOrderRequest struct {
	UserID      uint   `json:"user_id" binding:"required"`
	ProductType string `json:"product_type" binding:"required"`
	Quantity    int    `json:"quantity" binding:"required,min=1,max=10000"`
	PayChannel  string `json:"pay_channel" binding:"required"`
}

// CreateOrder POST /v1/orders — B 客户为子用户创建支付订单
func (ctrl *OrderController) CreateOrder(c *gin.Context) {
	payer := middleware.GetCurrentUser(c)
	if payer == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req createOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: %s", err.Error()), nil)
		return
	}

	// 校验归属：两种合法身份
	//   1. 自购（C 端）：payer.ID == req.UserID（加量包可自购，biz 层 Q1 会拒掉 trial/monthly/yearly）
	//   2. 代付（B 端 for C）：subUser.ParentUserID == payer.ID
	subUser, err := ctrl.ds.Users().GetUserByID(c, req.UserID)
	if err != nil {
		log.C(c).Errorw("Failed to get sub user", "user_id", req.UserID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("用户不存在"), nil)
		return
	}

	if payer.ID != subUser.ID {
		// 非自购：必须是 B 为 C 代付，校验 parent-child 关系
		if subUser.ParentUserID == nil || *subUser.ParentUserID != payer.ID {
			core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权为该用户创建订单"), nil)
			return
		}
	}

	// Extract idempotency key injected by RequireIdempotencyKey middleware.
	// Empty string means no header was sent (middleware enforces presence for
	// POST, so this is defensive — internal callers may bypass the middleware).
	idempotencyKey := c.GetString("idempotency_key")

	order, err := ctrl.paymentBiz.CreateOrder(c, payer.ID, req.UserID, req.ProductType, req.Quantity, req.PayChannel, idempotencyKey)
	if err != nil {
		log.C(c).Errorw("Failed to create order", "payer_id", payer.ID, "user_id", req.UserID, "err", err)
		// 如果是已定义的 errno（如 Membership.Required / Trial.AlreadyPurchased /
		// Membership.SelfPurchaseDisabled 等），直接透传保留其原始 HTTP 状态码
		// 和 errno code；否则才包成 InternalServerError 500。
		var e *errno.Errno
		if errors.As(err, &e) {
			core.WriteResponse(c, e, nil)
			return
		}
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, order)
}

// ListOrders GET /v1/orders — 查询当前付款人的订单列表
func (ctrl *OrderController) ListOrders(c *gin.Context) {
	payer := middleware.GetCurrentUser(c)
	if payer == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}

	orders, total, err := ctrl.paymentBiz.ListOrdersByPayer(c, payer.ID, offset, limit)
	if err != nil {
		log.C(c).Errorw("Failed to list orders", "payer_id", payer.ID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"items":  orders,
		"total":  total,
		"offset": offset,
		"limit":  limit,
	})
}

// GetOrder GET /v1/orders/:id — 查询单笔订单详情
func (ctrl *OrderController) GetOrder(c *gin.Context) {
	payer := middleware.GetCurrentUser(c)
	if payer == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	orderID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的订单ID"), nil)
		return
	}

	order, err := ctrl.paymentBiz.GetOrder(c, orderID)
	if err != nil {
		log.C(c).Errorw("Failed to get order", "order_id", orderID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 校验订单归属：只能查看自己作为付款人的订单
	if order.PayerID != payer.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权查看该订单"), nil)
		return
	}

	core.WriteResponse(c, nil, order)
}
