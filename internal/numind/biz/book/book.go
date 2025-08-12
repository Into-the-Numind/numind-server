package book

import (
	"context"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

type BookBiz interface {
	Create(ctx context.Context, book *model.BookM) error
	GetByID(ctx context.Context, id uint) (*model.BookM, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.BookM, error)
	ListByCategory(ctx context.Context, categoryID uint, offset, limit int) (int64, []*model.BookM, error)
	Update(ctx context.Context, book *model.BookM) error
	Delete(ctx context.Context, id uint) error
	DeleteBatch(ctx context.Context, ids []uint) error
	SetCategory(ctx context.Context, bookID, userID uint, categoryID *uint) error
	UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error
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

func (b *bookBiz) ListByCategory(ctx context.Context, categoryID uint, offset, limit int) (int64, []*model.BookM, error) {
	return b.ds.Books().ListByCategory(ctx, categoryID, offset, limit)
}

func (b *bookBiz) Update(ctx context.Context, book *model.BookM) error {
	return b.ds.Books().Update(ctx, book)
}

func (b *bookBiz) Delete(ctx context.Context, id uint) error {
	// 先获取book信息，用于更新用户统计
	book, err := b.ds.Books().GetByID(ctx, id)
	if err != nil {
		return err
	}

	// 删除book
	if err := b.ds.Books().Delete(ctx, id); err != nil {
		return err
	}

	// 更新用户统计
	if err := b.ds.Books().UpdateUserBookStatsOnDelete(ctx, book.UserID, book.Status); err != nil {
		// 记录错误但不影响删除操作
		// 这里可以考虑记录日志
	}

	return nil
}

func (b *bookBiz) DeleteBatch(ctx context.Context, ids []uint) error {
	return b.ds.Books().DeleteBatch(ctx, ids)
}

func (b *bookBiz) UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error {
	return b.ds.Books().UpdateUserBookStatsOnStatusChange(ctx, userID, oldStatus, newStatus)
}

func (b *bookBiz) SetCategory(ctx context.Context, bookID, userID uint, categoryID *uint) error {
	// 获取book
	book, err := b.ds.Books().GetByID(ctx, bookID)
	if err != nil {
		return err
	}

	// 检查book是否属于当前用户
	if book.UserID != userID {
		return errno.ErrUnauthorized
	}

	// 如果指定了分类ID，验证分类是否存在
	if categoryID != nil {
		category, err := b.ds.Categories().GetByID(ctx, *categoryID)
		if err != nil {
			return errno.ErrInvalidParameter.SetMessage("分类不存在")
		}
		// 检查分类是否属于当前用户
		if category.UserID != userID {
			return errno.ErrUnauthorized
		}
		book.CategoryName = category.Name
	} else {
		book.CategoryName = ""
	}

	// 更新分类ID
	book.CategoryID = categoryID

	return b.ds.Books().Update(ctx, book)
}
