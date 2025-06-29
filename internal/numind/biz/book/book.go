package book

import (
	"context"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type BookBiz interface {
	Create(ctx context.Context, book *model.BookM) error
	GetByID(ctx context.Context, id uint) (*model.BookM, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.BookM, error)
	Update(ctx context.Context, book *model.BookM) error
	Delete(ctx context.Context, id uint) error
}

type bookBiz struct {
	ds store.IStore
}

var _ BookBiz = (*bookBiz)(nil)

func New(ds store.IStore) *bookBiz {
	return &bookBiz{ds: ds}
}

func (b *bookBiz) Create(ctx context.Context, book *model.BookM) error {
	return b.ds.Books().Create(ctx, book)
}

func (b *bookBiz) GetByID(ctx context.Context, id uint) (*model.BookM, error) {
	return b.ds.Books().GetByID(ctx, id)
}

func (b *bookBiz) ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.BookM, error) {
	return b.ds.Books().ListByUser(ctx, userID, offset, limit)
}

func (b *bookBiz) Update(ctx context.Context, book *model.BookM) error {
	return b.ds.Books().Update(ctx, book)
}

func (b *bookBiz) Delete(ctx context.Context, id uint) error {
	return b.ds.Books().Delete(ctx, id)
}
