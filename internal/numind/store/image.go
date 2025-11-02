package store

import (
	"context"
	"errors"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type ImageStore interface {
	Create(ctx context.Context, image *model.ImageM) error
	BatchCreate(ctx context.Context, images []*model.ImageM) error
	GetByID(ctx context.Context, id uint) (*model.ImageM, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) (int64, []*model.ImageM, error)
	ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.ImageM, error)
	Update(ctx context.Context, image *model.ImageM) error
	Delete(ctx context.Context, id uint) error
}

type images struct {
	db *gorm.DB
}

var _ ImageStore = (*images)(nil)

func NewImageStore(db *gorm.DB) ImageStore {
	return &images{db}
}

func (s *images) Create(ctx context.Context, image *model.ImageM) error {
	return s.db.WithContext(ctx).Create(image).Error
}

func (s *images) BatchCreate(ctx context.Context, images []*model.ImageM) error {
	return s.db.WithContext(ctx).Create(&images).Error
}

func (s *images) GetByID(ctx context.Context, id uint) (*model.ImageM, error) {
	var image model.ImageM
	err := s.db.WithContext(ctx).First(&image, id).Error
	if err != nil {
		return nil, err
	}
	return &image, nil
}

func (s *images) ListByUser(ctx context.Context, userID uint, offset, limit int) (count int64, ret []*model.ImageM, err error) {
	err = s.db.WithContext(ctx).Where("user_id = ?", userID).
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *images) ListByBook(ctx context.Context, bookID uint, offset, limit int) (count int64, ret []*model.ImageM, err error) {
	err = s.db.WithContext(ctx).Where("book_id = ?", bookID).
		Offset(offset).Limit(defaultLimit(limit)).Order("id asc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *images) Update(ctx context.Context, image *model.ImageM) error {
	return s.db.WithContext(ctx).Save(image).Error
}

func (s *images) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Delete(&model.ImageM{}, id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}
