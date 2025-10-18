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
	ListByUser(ctx context.Context, userID uint, offset, limit int) (count int64, ret []*model.CardM, err error)
	Update(ctx context.Context, card *model.CardM) error
	Delete(ctx context.Context, id uint) error
	DeleteByBookID(ctx context.Context, bookID uint) (int64, error) // 根据bookID删除所有相关card，返回删除数量
	CountByBookID(ctx context.Context, bookID uint) (int64, error)  // 统计指定book的card数量
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
		Offset(offset).Limit(defaultLimit(limit)).Order("sort_order ASC").Find(&ret).
		Offset(-1).Limit(-1).Count(&count).Error
	return
}

func (s *cards) ListByUser(ctx context.Context, userID uint, offset, limit int) (count int64, ret []*model.CardM, err error) {
	err = s.db.WithContext(ctx).Where("user_id = ?", userID).
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

// DeleteByBookID 根据bookID删除所有相关card，返回删除数量
func (s *cards) DeleteByBookID(ctx context.Context, bookID uint) (int64, error) {
	result := s.db.WithContext(ctx).Where("book_id = ?", bookID).Delete(&model.CardM{})
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// CountByBookID 统计指定book的card数量
func (s *cards) CountByBookID(ctx context.Context, bookID uint) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.CardM{}).Where("book_id = ?", bookID).Count(&count).Error
	return count, err
}
