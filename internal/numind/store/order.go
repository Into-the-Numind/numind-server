package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type OrderStore interface {
	Create(ctx context.Context, order *model.Order) error
	GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.Order, error)
	Update(ctx context.Context, order *model.Order) error
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.Order, error)
}

type orderStore struct {
	db *gorm.DB
}

func NewOrderStore(db *gorm.DB) OrderStore {
	return &orderStore{db: db}
}

func (s *orderStore) Create(ctx context.Context, order *model.Order) error {
	return s.db.WithContext(ctx).Create(order).Error
}

func (s *orderStore) GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.Order, error) {
	var order model.Order
	if err := s.db.WithContext(ctx).Where("out_trade_no = ?", outTradeNo).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func (s *orderStore) Update(ctx context.Context, order *model.Order) error {
	return s.db.WithContext(ctx).Save(order).Error
}

func (s *orderStore) ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.Order, error) {
	var orders []*model.Order
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at desc").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		return nil, err
	}
	return orders, nil
}
