package book

import (
	"context"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type BookBiz interface {
	Create(ctx context.Context, book *model.BookM) error
	GetByID(ctx context.Context, id uint) (*model.BookM, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.BookM, error)
	ListByCategory(ctx context.Context, categoryID uint, offset, limit int) (int64, []*model.BookM, error)
	ListAll(ctx context.Context, offset, limit int) (int64, []*model.BookM, error) // 获取所有书籍（用于后台管理等场景）
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

// ListAll 获取所有书籍（用于后台管理等场景，返回所有字段）
func (b *bookBiz) ListAll(ctx context.Context, offset, limit int) (int64, []*model.BookM, error) {
	return b.ds.Books().ListAll(ctx, offset, limit)
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

	// 删除book相关的所有card
	cardCount, err := b.ds.Cards().DeleteByBookID(ctx, id)
	if err != nil {
		// 记录错误但不影响删除操作
		log.C(ctx).Errorw("Failed to delete cards for book", "book_id", id, "error", err.Error())
	}

	// 删除book
	if err := b.ds.Books().Delete(ctx, id); err != nil {
		return err
	}

	// 更新用户统计
	if err := b.ds.Books().UpdateUserBookStatsOnDelete(ctx, book.UserID, book.Status); err != nil {
		// 记录错误但不影响删除操作
		log.C(ctx).Errorw("Failed to update user book stats on delete", "user_id", book.UserID, "error", err.Error())
	}

	// 更新用户card统计
	if cardCount > 0 {
		if err := b.ds.DB().Model(&model.User{}).Where("id = ?", book.UserID).
			UpdateColumn("card_num", gorm.Expr("card_num - ?", cardCount)).Error; err != nil {
			// 记录错误但不影响删除操作
			log.C(ctx).Errorw("Failed to decrement user card num", "user_id", book.UserID, "card_count", cardCount, "error", err.Error())
		}
	}

	return nil
}

func (b *bookBiz) DeleteBatch(ctx context.Context, ids []uint) error {
	// 先获取所有要删除的book信息
	var books []*model.BookM
	if err := b.ds.Books().GetByIDs(ctx, ids, &books); err != nil {
		return err
	}

	// 统计每个用户的card数量
	userCardCounts := make(map[uint]int64)

	// 删除每个book相关的card
	for _, book := range books {
		cardCount, err := b.ds.Cards().DeleteByBookID(ctx, book.ID)
		if err != nil {
			// 记录错误但不影响删除操作
			log.C(ctx).Errorw("Failed to delete cards for book", "book_id", book.ID, "error", err.Error())
		} else {
			userCardCounts[book.UserID] += cardCount
		}
	}

	// 批量删除books
	if err := b.ds.Books().DeleteBatch(ctx, ids); err != nil {
		return err
	}

	// 更新每个用户的card统计
	for userID, cardCount := range userCardCounts {
		if cardCount > 0 {
			if err := b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
				UpdateColumn("card_num", gorm.Expr("card_num - ?", cardCount)).Error; err != nil {
				// 记录错误但不影响删除操作
				log.C(ctx).Errorw("Failed to decrement user card num", "user_id", userID, "card_count", cardCount, "error", err.Error())
			}
		}
	}

	return nil
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
		book.Category = category
	} else {
		// 移除分类时，清空相关字段
		book.CategoryName = ""
		book.Category = nil
	}

	// 更新分类ID
	book.CategoryID = categoryID

	return b.ds.Books().Update(ctx, book)
}
