package template

import (
	"context"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type TemplateBiz interface {
	Create(ctx context.Context, template *model.Template) error
	GetByID(ctx context.Context, id uint) (*model.Template, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.Template, error)
	Update(ctx context.Context, template *model.Template) error
	Delete(ctx context.Context, id uint) error
	GetByName(ctx context.Context, name string) (*model.Template, error)
}

type templateBiz struct {
	ds store.IStore
}

var _ TemplateBiz = (*templateBiz)(nil)

func New(ds store.IStore) *templateBiz {
	return &templateBiz{ds: ds}
}

func (b *templateBiz) Create(ctx context.Context, template *model.Template) error {
	return b.ds.Templates().Create(ctx, template)
}

func (b *templateBiz) GetByID(ctx context.Context, id uint) (*model.Template, error) {
	return b.ds.Templates().GetByID(ctx, id)
}

func (b *templateBiz) List(ctx context.Context, offset, limit int) (int64, []*model.Template, error) {
	return b.ds.Templates().List(ctx, offset, limit)
}

func (b *templateBiz) Update(ctx context.Context, template *model.Template) error {
	return b.ds.Templates().Update(ctx, template)
}

func (b *templateBiz) Delete(ctx context.Context, id uint) error {
	return b.ds.Templates().Delete(ctx, id)
}

func (b *templateBiz) GetByName(ctx context.Context, name string) (*model.Template, error) {
	return b.ds.Templates().GetByName(ctx, name)
}
