package category

import (
	"context"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type CategoryBiz interface {
	Create(ctx context.Context, category *model.CategoryM) error
	GetByID(ctx context.Context, id uint) (*model.CategoryM, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.CategoryM, error)
	Update(ctx context.Context, category *model.CategoryM) error
	Delete(ctx context.Context, id uint) error
	GetByName(ctx context.Context, name string) (*model.CategoryM, error)
}

type categoryBiz struct {
	ds store.IStore
}

var _ CategoryBiz = (*categoryBiz)(nil)

func New(ds store.IStore) *categoryBiz {
	return &categoryBiz{ds: ds}
}

func (b *categoryBiz) Create(ctx context.Context, category *model.CategoryM) error {
	return b.ds.Categories().Create(ctx, category)
}

func (b *categoryBiz) GetByID(ctx context.Context, id uint) (*model.CategoryM, error) {
	return b.ds.Categories().GetByID(ctx, id)
}

func (b *categoryBiz) List(ctx context.Context, offset, limit int) (int64, []*model.CategoryM, error) {
	return b.ds.Categories().List(ctx, offset, limit)
}

func (b *categoryBiz) Update(ctx context.Context, category *model.CategoryM) error {
	return b.ds.Categories().Update(ctx, category)
}

func (b *categoryBiz) Delete(ctx context.Context, id uint) error {
	return b.ds.Categories().Delete(ctx, id)
}

func (b *categoryBiz) GetByName(ctx context.Context, name string) (*model.CategoryM, error) {
	return b.ds.Categories().GetByName(ctx, name)
}
