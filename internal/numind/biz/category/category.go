package category

import (
	"context"
	"errors"

	"github.com/jinzhu/copier"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

type CategoryBiz interface {
	Create(ctx context.Context, userID uint, category *model.CategoryM) error
	GetByID(ctx context.Context, id uint) (*model.CategoryM, error)
	GetByUserID(ctx context.Context, userID uint, offset, limit int) (*v1.ListCategoryResponse, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.CategoryM, error)
	Update(ctx context.Context, userID uint, category *model.CategoryM) error
	Delete(ctx context.Context, userID uint, id uint) error
	GetByName(ctx context.Context, name string) (*model.CategoryM, error)
	GetByUserIDAndName(ctx context.Context, userID uint, name string) (*model.CategoryM, error)
}

type categoryBiz struct {
	ds store.IStore
}

var _ CategoryBiz = (*categoryBiz)(nil)

func New(ds store.IStore) *categoryBiz {
	return &categoryBiz{ds: ds}
}

func (b *categoryBiz) Create(ctx context.Context, userID uint, category *model.CategoryM) error {
	// 设置用户ID
	category.UserID = userID

	// 检查同一用户下是否已存在同名分类
	existingCategory, err := b.ds.Categories().GetByUserIDAndName(ctx, userID, category.Name)
	if err == nil && existingCategory != nil {
		return errno.ErrInvalidParameter.SetMessage("分类名称已存在")
	}

	return b.ds.Categories().Create(ctx, category)
}

func (b *categoryBiz) GetByID(ctx context.Context, id uint) (*model.CategoryM, error) {
	return b.ds.Categories().GetByID(ctx, id)
}

func (b *categoryBiz) GetByUserID(ctx context.Context, userID uint, offset, limit int) (*v1.ListCategoryResponse, error) {
	count, list, err := b.ds.Categories().GetByUserID(ctx, userID, offset, limit)
	if err != nil {
		log.C(ctx).Errorw("Failed to list categories from storage", "err", err)
		return nil, err
	}

	categories := make([]*v1.CategoryResponse, 0, len(list))
	for _, item := range list {
		category := item
		var resp v1.CategoryResponse
		_ = copier.Copy(&resp, category)

		// 格式化时间
		resp.CreatedAt = category.CreatedAt.Format("2006-01-02 15:04:05")
		resp.UpdatedAt = category.UpdatedAt.Format("2006-01-02 15:04:05")

		categories = append(categories, &resp)
	}

	log.C(ctx).Debugw("Get categories from backend storage", "count", len(categories))

	return &v1.ListCategoryResponse{TotalCount: count, Categories: categories}, nil
}

func (b *categoryBiz) List(ctx context.Context, offset, limit int) (int64, []*model.CategoryM, error) {
	return b.ds.Categories().List(ctx, offset, limit)
}

func (b *categoryBiz) Update(ctx context.Context, userID uint, category *model.CategoryM) error {
	// 检查分类是否存在且属于当前用户
	existingCategory, err := b.ds.Categories().GetByID(ctx, category.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrPageNotFound
		}
		return err
	}

	// 检查分类是否属于当前用户
	if existingCategory.UserID != userID {
		return errno.ErrUnauthorized
	}

	// 如果更新了名称，检查是否与其他分类重名
	if category.Name != existingCategory.Name {
		duplicateCategory, err := b.ds.Categories().GetByUserIDAndName(ctx, userID, category.Name)
		if err == nil && duplicateCategory != nil && duplicateCategory.ID != category.ID {
			return errno.ErrInvalidParameter.SetMessage("分类名称已存在")
		}
	}

	return b.ds.Categories().Update(ctx, category)
}

func (b *categoryBiz) Delete(ctx context.Context, userID uint, id uint) error {
	// 检查分类是否存在且属于当前用户
	existingCategory, err := b.ds.Categories().GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrPageNotFound
		}
		return err
	}

	// 检查分类是否属于当前用户
	if existingCategory.UserID != userID {
		return errno.ErrUnauthorized
	}

	return b.ds.Categories().Delete(ctx, id)
}

func (b *categoryBiz) GetByName(ctx context.Context, name string) (*model.CategoryM, error) {
	return b.ds.Categories().GetByName(ctx, name)
}

func (b *categoryBiz) GetByUserIDAndName(ctx context.Context, userID uint, name string) (*model.CategoryM, error) {
	return b.ds.Categories().GetByUserIDAndName(ctx, userID, name)
}
