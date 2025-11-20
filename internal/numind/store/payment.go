package store

import (
	"context"
	"numind-server/internal/pkg/model"
	"time"

	"gorm.io/gorm"
)

// AdminPaymentListRequest 管理员支付列表查询请求
type AdminPaymentListRequest struct {
	Offset    int        `form:"offset"`
	Limit     int        `form:"limit"`
	UserID    *uint      `form:"user_id"`
	Status    *string    `form:"status"`
	Channel   *string    `form:"channel"`
	StartDate *time.Time `form:"start_date"`
	EndDate   *time.Time `form:"end_date"`
	Keyword   string     `form:"keyword"` // 支持搜索订单号、交易号
}

// PaymentStore 支付存储接口
type PaymentStore interface {
	Create(ctx context.Context, payment *model.PaymentM) error
	GetByID(ctx context.Context, id uint) (*model.PaymentM, error)
	GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.PaymentM, error)
	GetByTransactionID(ctx context.Context, transactionID string) (*model.PaymentM, error)
	Update(ctx context.Context, payment *model.PaymentM) error
	UpdateStatus(ctx context.Context, outTradeNo, status string, transactionID string, paidAt *time.Time) error
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.PaymentM, error)
	ListByStatus(ctx context.Context, status string, offset, limit int) ([]*model.PaymentM, error)
	ListByDateRange(ctx context.Context, startDate, endDate time.Time, offset, limit int) ([]*model.PaymentM, error)
	List(ctx context.Context, req *AdminPaymentListRequest) ([]*model.PaymentM, int64, error)
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

// GetByID 根据ID获取支付记录
func (s *paymentStore) GetByID(ctx context.Context, id uint) (*model.PaymentM, error) {
	var payment model.PaymentM
	err := s.db.WithContext(ctx).First(&payment, id).Error
	if err != nil {
		return nil, err
	}
	return &payment, nil
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

// List 获取支付记录列表（管理员，支持多条件筛选）
func (s *paymentStore) List(ctx context.Context, req *AdminPaymentListRequest) ([]*model.PaymentM, int64, error) {
	query := s.db.WithContext(ctx).Model(&model.PaymentM{})

	// 应用过滤条件
	if req.UserID != nil {
		query = query.Where("user_id = ?", *req.UserID)
	}
	if req.Status != nil && *req.Status != "" {
		query = query.Where("status = ?", *req.Status)
	}
	if req.Channel != nil && *req.Channel != "" {
		query = query.Where("channel = ?", *req.Channel)
	}
	if req.StartDate != nil && req.EndDate != nil {
		query = query.Where("created_at BETWEEN ? AND ?", *req.StartDate, *req.EndDate)
	} else if req.StartDate != nil {
		query = query.Where("created_at >= ?", *req.StartDate)
	} else if req.EndDate != nil {
		query = query.Where("created_at <= ?", *req.EndDate)
	}
	if req.Keyword != "" {
		query = query.Where("out_trade_no LIKE ? OR transaction_id LIKE ?", "%"+req.Keyword+"%", "%"+req.Keyword+"%")
	}

	// 获取总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	var payments []*model.PaymentM
	if err := query.Order("created_at DESC").
		Offset(req.Offset).
		Limit(req.Limit).
		Find(&payments).Error; err != nil {
		return nil, 0, err
	}

	return payments, total, nil
}

// Delete 删除支付记录
func (s *paymentStore) Delete(ctx context.Context, id uint) error {
	return s.db.WithContext(ctx).Delete(&model.PaymentM{}, id).Error
}
