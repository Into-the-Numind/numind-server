package card

import (
	"context"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type CardBiz interface {
	Create(ctx context.Context, card *model.CardM) error
	GetByID(ctx context.Context, id uint) (*model.CardM, error)
	ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.CardM, error)
	Update(ctx context.Context, card *model.CardM) error
	Delete(ctx context.Context, id uint) error
}

type cardBiz struct {
	ds store.IStore
}

var _ CardBiz = (*cardBiz)(nil)

func New(ds store.IStore) *cardBiz {
	return &cardBiz{ds: ds}
}

func (b *cardBiz) Create(ctx context.Context, card *model.CardM) error {
	return b.ds.Cards().Create(ctx, card)
}

func (b *cardBiz) GetByID(ctx context.Context, id uint) (*model.CardM, error) {
	return b.ds.Cards().GetByID(ctx, id)
}

func (b *cardBiz) ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.CardM, error) {
	return b.ds.Cards().ListByBook(ctx, bookID, offset, limit)
}

func (b *cardBiz) ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.CardM, error) {
	return b.ds.Cards().ListByUser(ctx, userID, offset, limit)
}

func (b *cardBiz) Update(ctx context.Context, card *model.CardM) error {
	return b.ds.Cards().Update(ctx, card)
}

func (b *cardBiz) Delete(ctx context.Context, id uint) error {
	return b.ds.Cards().Delete(ctx, id)
}
