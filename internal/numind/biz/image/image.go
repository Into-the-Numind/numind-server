package image

import (
	"context"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type ImageBiz interface {
	Create(ctx context.Context, image *model.ImageM) error
	BatchCreate(ctx context.Context, images []*model.ImageM) error
	GetByID(ctx context.Context, id uint) (*model.ImageM, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.ImageM, error)
	ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.ImageM, error)
	ListAll(ctx context.Context, req *store.AdminImageListRequest) (int64, []*model.ImageM, error)
	Update(ctx context.Context, image *model.ImageM) error
	Delete(ctx context.Context, id uint) error
}

type imageBiz struct {
	ds store.IStore
}

var _ ImageBiz = (*imageBiz)(nil)

func New(ds store.IStore) *imageBiz {
	return &imageBiz{ds: ds}
}

func (b *imageBiz) Create(ctx context.Context, image *model.ImageM) error {
	return b.ds.Images().Create(ctx, image)
}

func (b *imageBiz) BatchCreate(ctx context.Context, images []*model.ImageM) error {
	return b.ds.Images().BatchCreate(ctx, images)
}

func (b *imageBiz) GetByID(ctx context.Context, id uint) (*model.ImageM, error) {
	return b.ds.Images().GetByID(ctx, id)
}

func (b *imageBiz) ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.ImageM, error) {
	return b.ds.Images().ListByUser(ctx, userID, offset, limit)
}

func (b *imageBiz) ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.ImageM, error) {
	return b.ds.Images().ListByBook(ctx, bookID, offset, limit)
}

func (b *imageBiz) ListAll(ctx context.Context, req *store.AdminImageListRequest) (int64, []*model.ImageM, error) {
	return b.ds.Images().ListAll(ctx, req)
}

func (b *imageBiz) Update(ctx context.Context, image *model.ImageM) error {
	return b.ds.Images().Update(ctx, image)
}

func (b *imageBiz) Delete(ctx context.Context, id uint) error {
	return b.ds.Images().Delete(ctx, id)
}
