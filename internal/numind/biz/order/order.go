package order

import (
	"context"
	userbiz "numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"time"
)

type OrderBiz interface {
	Create(ctx context.Context, order *model.Order) error
	GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.Order, error)
	Update(ctx context.Context, order *model.Order) error
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.Order, error)

	// 处理支付成功
	HandlePaymentSuccess(ctx context.Context, outTradeNo string) error
}

type orderBiz struct {
	ds      store.IStore
	userBiz userbiz.UserBiz
}

func NewOrderBiz(ds store.IStore, userBiz userbiz.UserBiz) OrderBiz {
	return &orderBiz{ds: ds, userBiz: userBiz}
}

func (b *orderBiz) Create(ctx context.Context, order *model.Order) error {
	return b.ds.Orders().Create(ctx, order)
}

func (b *orderBiz) GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.Order, error) {
	return b.ds.Orders().GetByOutTradeNo(ctx, outTradeNo)
}

func (b *orderBiz) Update(ctx context.Context, order *model.Order) error {
	return b.ds.Orders().Update(ctx, order)
}

func (b *orderBiz) ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.Order, error) {
	return b.ds.Orders().ListByUser(ctx, userID, offset, limit)
}

// HandlePaymentSuccess 处理支付成功，更新订单状态和用户权限
func (b *orderBiz) HandlePaymentSuccess(ctx context.Context, outTradeNo string) error {
	// 获取订单信息
	order, err := b.ds.Orders().GetByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		return err
	}

	// 更新订单状态
	order.Status = "paid"
	order.PaidAt = time.Now()
	if err := b.ds.Orders().Update(ctx, order); err != nil {
		return err
	}

	// 支付成功后，将用户设置为付费用户
	if err := b.userBiz.SetUserPro(ctx, order.UserID, true); err != nil {
		log.C(ctx).Errorw("Failed to set user pro status", "user_id", order.UserID, "error", err.Error())
		// 返回错误，因为这是关键操作
		return err
	}

	return nil
}
