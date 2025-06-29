package store

import (
	"context"
	"errors"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type CardStore interface {
	Create(ctx context.Context, card *model.CardM) error
	GetByID(ctx context.Context, id uint) (*model.CardM, error)
	ListByBook(ctx context.Context, bookID uint, offset, limit int) (int64, []*model.CardM, error)
	Update(ctx context.Context, card *model.CardM) error
	Delete(ctx context.Context, id uint) error
}

type cards struct {
	db *gorm.DB
}

var _ CardStore = (*cards)(nil)

func NewCardStore(db *gorm.DB) CardStore {
	return &cards{db}
}

func (s *cards) Create(ctx context.Context, card *model.CardM) error {
	return s.db.WithContext(ctx).Create(card).Error
}

func (s *cards) GetByID(ctx context.Context, id uint) (*model.CardM, error) {
	var card model.CardM
	err := s.db.WithContext(ctx).First(&card, id).Error
	if err != nil {
		return nil, err
	}
	return &card, nil
}

func (s *cards) ListByBook(ctx context.Context, bookID uint, offset, limit int) (count int64, ret []*model.CardM, err error) {
	err = s.db.WithContext(ctx).Where("book_id = ?", bookID).
		Offset(offset).Limit(defaultLimit(limit)).Order("id desc").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *cards) Update(ctx context.Context, card *model.CardM) error {
	return s.db.WithContext(ctx).Save(card).Error
}

func (s *cards) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Delete(&model.CardM{}, id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}
