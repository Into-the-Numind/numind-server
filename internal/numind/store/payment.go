package store

import (
	"context"
	"numind-server/internal/pkg/model"
	"time"

	"gorm.io/gorm"
)

// PaymentStore 支付存储接口
type PaymentStore interface {
	Create(ctx context.Context, payment *model.PaymentM) error
	GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.PaymentM, error)
	GetByTransactionID(ctx context.Context, transactionID string) (*model.PaymentM, error)
	Update(ctx context.Context, payment *model.PaymentM) error
	UpdateStatus(ctx context.Context, outTradeNo, status string, transactionID string, paidAt *time.Time) error
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.PaymentM, error)
	ListByStatus(ctx context.Context, status string, offset, limit int) ([]*model.PaymentM, error)
	ListByDateRange(ctx context.Context, startDate, endDate time.Time, offset, limit int) ([]*model.PaymentM, error)
	CountByUser(ctx context.Context, userID uint) (int64, error)
	CountByStatus(ctx context.Context, status string) (int64, error)
	Delete(ctx context.Context, id uint) error
}

// paymentStore 支付存储实现
type paymentStore struct {
	db *gorm.DB
}

// NewPaymentStore 创建支付存储实例
func NewPaymentStore(db *gorm.DB) PaymentStore {
	return &paymentStore{db: db}
}

// Create 创建支付记录
func (s *paymentStore) Create(ctx context.Context, payment *model.PaymentM) error {
	return s.db.WithContext(ctx).Create(payment).Error
}

// GetByOutTradeNo 根据商户订单号获取支付记录
func (s *paymentStore) GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.PaymentM, error) {
	var payment model.PaymentM
	err := s.db.WithContext(ctx).Where("out_trade_no = ?", outTradeNo).First(&payment).Error
	if err != nil {
		return nil, err
	}
	return &payment, nil
}

// GetByTransactionID 根据微信支付订单号获取支付记录
func (s *paymentStore) GetByTransactionID(ctx context.Context, transactionID string) (*model.PaymentM, error) {
	var payment model.PaymentM
	err := s.db.WithContext(ctx).Where("transaction_id = ?", transactionID).First(&payment).Error
	if err != nil {
		return nil, err
	}
	return &payment, nil
}

// Update 更新支付记录
func (s *paymentStore) Update(ctx context.Context, payment *model.PaymentM) error {
	return s.db.WithContext(ctx).Save(payment).Error
}

// UpdateStatus 更新支付状态
func (s *paymentStore) UpdateStatus(ctx context.Context, outTradeNo, status string, transactionID string, paidAt *time.Time) error {
	updates := map[string]interface{}{
		"status": status,
	}
	
	if transactionID != "" {
		updates["transaction_id"] = transactionID
	}
	
	if paidAt != nil {
		updates["paid_at"] = paidAt
	}
	
	return s.db.WithContext(ctx).Model(&model.PaymentM{}).
		Where("out_trade_no = ?", outTradeNo).
		Updates(updates).Error
}

// ListByUser 获取用户的支付记录列表
func (s *paymentStore) ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.PaymentM, error) {
	var payments []*model.PaymentM
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&payments).Error
	return payments, err
}

// ListByStatus 根据状态获取支付记录列表
func (s *paymentStore) ListByStatus(ctx context.Context, status string, offset, limit int) ([]*model.PaymentM, error) {
	var payments []*model.PaymentM
	err := s.db.WithContext(ctx).
		Where("status = ?", status).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&payments).Error
	return payments, err
}

// ListByDateRange 根据日期范围获取支付记录列表
func (s *paymentStore) ListByDateRange(ctx context.Context, startDate, endDate time.Time, offset, limit int) ([]*model.PaymentM, error) {
	var payments []*model.PaymentM
	err := s.db.WithContext(ctx).
		Where("created_at BETWEEN ? AND ?", startDate, endDate).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&payments).Error
	return payments, err
}

// CountByUser 统计用户的支付记录数量
func (s *paymentStore) CountByUser(ctx context.Context, userID uint) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.PaymentM{}).
		Where("user_id = ?", userID).
		Count(&count).Error
	return count, err
}

// CountByStatus 根据状态统计支付记录数量
func (s *paymentStore) CountByStatus(ctx context.Context, status string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.PaymentM{}).
		Where("status = ?", status).
		Count(&count).Error
	return count, err
}

// Delete 删除支付记录
func (s *paymentStore) Delete(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.PaymentM{}, id).Error
}
