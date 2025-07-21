package order

import (
	"context"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type OrderBiz interface {
	Create(ctx context.Context, order *model.Order) error
	GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.Order, error)
	Update(ctx context.Context, order *model.Order) error
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.Order, error)
}

type orderBiz struct {
	ds store.IStore
}

func NewOrderBiz(ds store.IStore) OrderBiz {
	return &orderBiz{ds: ds}
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
