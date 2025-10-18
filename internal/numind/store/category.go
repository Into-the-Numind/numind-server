package store

import (
	"context"
	"errors"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type CategoryStore interface {
	Create(ctx context.Context, category *model.CategoryM) error
	GetByID(ctx context.Context, id uint) (*model.CategoryM, error)
	GetByUserID(ctx context.Context, userID uint, offset, limit int) (int64, []*model.CategoryM, error)
	List(ctx context.Context, offset, limit int) (int64, []*model.CategoryM, error)
	Update(ctx context.Context, category *model.CategoryM) error
	Delete(ctx context.Context, id uint) error
	GetByName(ctx context.Context, name string) (*model.CategoryM, error)
	GetByUserIDAndName(ctx context.Context, userID uint, name string) (*model.CategoryM, error)
}

type categories struct {
	db *gorm.DB
}

var _ CategoryStore = (*categories)(nil)

func NewCategoryStore(db *gorm.DB) CategoryStore {
	return &categories{db}
}

func (s *categories) Create(ctx context.Context, category *model.CategoryM) error {
	return s.db.WithContext(ctx).Create(category).Error
}

func (s *categories) GetByID(ctx context.Context, id uint) (*model.CategoryM, error) {
	var category model.CategoryM
	err := s.db.WithContext(ctx).First(&category, id).Error
	if err != nil {
		return nil, err
	}
	return &category, nil
}

func (s *categories) GetByUserID(ctx context.Context, userID uint, offset, limit int) (count int64, ret []*model.CategoryM, err error) {
	query := s.db.WithContext(ctx).Model(&model.CategoryM{}).Where("user_id = ?", userID)

	// 获取总数
	if err := query.Count(&count).Error; err != nil {
		return 0, nil, err
	}

	// 分页查询
	err = query.Offset(offset).Limit(defaultLimit(limit)).Order("sort ASC, created_at DESC").Find(&ret).Error
	return
}

func (s *categories) List(ctx context.Context, offset, limit int) (count int64, ret []*model.CategoryM, err error) {
	err = s.db.WithContext(ctx).
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *categories) Update(ctx context.Context, category *model.CategoryM) error {
	return s.db.WithContext(ctx).Save(category).Error
}

func (s *categories) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Delete(&model.CategoryM{}, id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func (s *categories) GetByName(ctx context.Context, name string) (*model.CategoryM, error) {
	var category model.CategoryM
	err := s.db.WithContext(ctx).Where("name = ?", name).First(&category).Error
	if err != nil {
		return nil, err
	}
	return &category, nil
}

func (s *categories) GetByUserIDAndName(ctx context.Context, userID uint, name string) (*model.CategoryM, error) {
	var category model.CategoryM
	err := s.db.WithContext(ctx).Where("user_id = ? AND name = ?", userID, name).First(&category).Error
	if err != nil {
		return nil, err
	}
	return &category, nil
}
