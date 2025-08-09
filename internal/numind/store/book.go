package store

import (
	"context"
	"errors"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type BookStore interface {
	Create(ctx context.Context, book *model.BookM) error
	GetByID(ctx context.Context, id uint) (*model.BookM, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.BookM, error)
	ListByCategory(ctx context.Context, categoryID uint, offset, limit int) (int64, []*model.BookM, error)
	Update(ctx context.Context, book *model.BookM) error
	Delete(ctx context.Context, id uint) error
	DeleteBatch(ctx context.Context, ids []uint) error
}

type books struct {
	db *gorm.DB
}

var _ BookStore = (*books)(nil)

func NewBookStore(db *gorm.DB) BookStore {
	return &books{db}
}

func (s *books) Create(ctx context.Context, book *model.BookM) error {
	return s.db.WithContext(ctx).Create(book).Error
}

func (s *books) GetByID(ctx context.Context, id uint) (*model.BookM, error) {
	var book model.BookM
	err := s.db.WithContext(ctx).Preload("Category").First(&book, id).Error
	if err != nil {
		return nil, err
	}
	return &book, nil
}

func (s *books) ListByUser(ctx context.Context, userID uint, offset, limit int) (count int64, ret []*model.BookM, err error) {
	err = s.db.WithContext(ctx).Where("user_id = ? AND status != ?", userID, model.BookStatusFailed).
		Preload("Category").
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *books) ListByCategory(ctx context.Context, categoryID uint, offset, limit int) (count int64, ret []*model.BookM, err error) {
	err = s.db.WithContext(ctx).Where("category_id = ? AND status != ?", categoryID, model.BookStatusFailed).
		Preload("Category").
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *books) Update(ctx context.Context, book *model.BookM) error {
	return s.db.WithContext(ctx).Save(book).Error
}

func (s *books) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Delete(&model.BookM{}, id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func (s *books) DeleteBatch(ctx context.Context, ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	err := s.db.WithContext(ctx).Where("id IN (?)", ids).Delete(&model.BookM{}).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}
