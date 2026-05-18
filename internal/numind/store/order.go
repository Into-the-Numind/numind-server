package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// OrderStore 定义了订单存储层需要实现的方法.
type OrderStore interface {
	Create(ctx context.Context, order *model.Order) error
	GetByID(ctx context.Context, id uint64) (*model.Order, error)
	GetByOrderNo(ctx context.Context, orderNo string) (*model.Order, error)
	// FindByIdempotencyKey returns the order with the given Idempotency-Key,
	// or (nil, nil) when no row matches. An empty key short-circuits to
	// (nil, nil) so callers can pass through the middleware-set value without
	// a separate nil check. Used by CreateOrder to dedup double-submit retries.
	FindByIdempotencyKey(ctx context.Context, key string) (*model.Order, error)
	UpdateStatus(ctx context.Context, id uint64, status string, updates map[string]interface{}) error
	ListByPayer(ctx context.Context, payerID uint, offset, limit int) ([]model.Order, int64, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]model.Order, int64, error)
	ListAll(ctx context.Context, offset, limit int) ([]model.Order, int64, error)
	CloseExpiredOrders(ctx context.Context) (int64, error)
}

type orderStore struct {
	db *gorm.DB
}

func newOrderStore(db *gorm.DB) OrderStore {
	return &orderStore{db: db}
}

// Create 创建新订单.
func (s *orderStore) Create(ctx context.Context, order *model.Order) error {
	return s.db.WithContext(ctx).Create(order).Error
}

// GetByID 根据 ID 查询订单.
func (s *orderStore) GetByID(ctx context.Context, id uint64) (*model.Order, error) {
	var order model.Order
	if err := s.db.WithContext(ctx).First(&order, id).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// GetByOrderNo 根据订单号查询订单.
func (s *orderStore) GetByOrderNo(ctx context.Context, orderNo string) (*model.Order, error) {
	var order model.Order
	if err := s.db.WithContext(ctx).Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// FindByIdempotencyKey returns the order with the given Idempotency-Key,
// or (nil, nil) when no row matches. Empty key short-circuits to (nil, nil)
// so callers can pass through the middleware-set value without a separate
// nil check. Used by CreateOrder to dedup double-submit retries.
func (s *orderStore) FindByIdempotencyKey(ctx context.Context, key string) (*model.Order, error) {
	if key == "" {
		return nil, nil
	}
	var order model.Order
	err := s.db.WithContext(ctx).Where("idempotency_key = ?", key).First(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("FindByIdempotencyKey: %w", err)
	}
	return &order, nil
}

// UpdateStatus 更新订单状态及附加字段.
func (s *orderStore) UpdateStatus(ctx context.Context, id uint64, status string, updates map[string]interface{}) error {
	if updates == nil {
		updates = make(map[string]interface{})
	}
	updates["pay_status"] = status
	updates["updated_at"] = time.Now()
	return s.db.WithContext(ctx).Model(&model.Order{}).Where("id = ?", id).Updates(updates).Error
}

// ListByPayer 查询付款人的订单列表.
func (s *orderStore) ListByPayer(ctx context.Context, payerID uint, offset, limit int) ([]model.Order, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Order{}).Where("payer_id = ?", payerID).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var orders []model.Order
	if err := s.db.WithContext(ctx).Where("payer_id = ?", payerID).
		Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		return nil, 0, err
	}
	return orders, total, nil
}

// ListByUser 查询用户的订单列表（被购买方）.
func (s *orderStore) ListByUser(ctx context.Context, userID uint, offset, limit int) ([]model.Order, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Order{}).Where("user_id = ?", userID).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var orders []model.Order
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		return nil, 0, err
	}
	return orders, total, nil
}

// ListAll 查询所有订单列表（管理端）.
func (s *orderStore) ListAll(ctx context.Context, offset, limit int) ([]model.Order, int64, error) {
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Order{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var orders []model.Order
	if err := s.db.WithContext(ctx).Order("created_at DESC").Offset(offset).Limit(limit).Find(&orders).Error; err != nil {
		return nil, 0, err
	}
	return orders, total, nil
}

// CloseExpiredOrders 关闭所有超时未支付的订单.
func (s *orderStore) CloseExpiredOrders(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).Model(&model.Order{}).
		Where("pay_status = ? AND expired_at < NOW()", model.OrderStatusPending).
		Update("pay_status", model.OrderStatusClosed)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
