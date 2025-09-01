package store

import (
	"context"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

type AccountRecordStore interface {
	Create(ctx context.Context, record *model.AccountRecord) error
	GetByID(ctx context.Context, id uint) (*model.AccountRecord, error)
	GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.AccountRecord, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.AccountRecord, error)
	ListByUserAndType(ctx context.Context, userID uint, recordType string, offset, limit int) ([]*model.AccountRecord, error)
	GetUserTotalAmount(ctx context.Context, userID uint, recordType string) (int64, error)
	Update(ctx context.Context, record *model.AccountRecord) error
}

type accountRecordStore struct {
	db *gorm.DB
}

func NewAccountRecordStore(db *gorm.DB) AccountRecordStore {
	return &accountRecordStore{db: db}
}

func (s *accountRecordStore) Create(ctx context.Context, record *model.AccountRecord) error {
	return s.db.WithContext(ctx).Create(record).Error
}

func (s *accountRecordStore) GetByID(ctx context.Context, id uint) (*model.AccountRecord, error) {
	var record model.AccountRecord
	if err := s.db.WithContext(ctx).First(&record, id).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *accountRecordStore) GetByOutTradeNo(ctx context.Context, outTradeNo string) (*model.AccountRecord, error) {
	var record model.AccountRecord
	if err := s.db.WithContext(ctx).Where("out_trade_no = ?", outTradeNo).First(&record).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

func (s *accountRecordStore) ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.AccountRecord, error) {
	var records []*model.AccountRecord
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).
		Order("payment_at desc").Offset(offset).Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

func (s *accountRecordStore) ListByUserAndType(ctx context.Context, userID uint, recordType string, offset, limit int) ([]*model.AccountRecord, error) {
	var records []*model.AccountRecord
	query := s.db.WithContext(ctx).Where("user_id = ?", userID)
	if recordType != "" {
		query = query.Where("type = ?", recordType)
	}
	if err := query.Order("payment_at desc").Offset(offset).Limit(limit).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

func (s *accountRecordStore) GetUserTotalAmount(ctx context.Context, userID uint, recordType string) (int64, error) {
	var total int64
	query := s.db.WithContext(ctx).Model(&model.AccountRecord{}).Where("user_id = ? AND status = ?", userID, "success")
	if recordType != "" {
		query = query.Where("type = ?", recordType)
	}
	if err := query.Select("COALESCE(SUM(amount), 0)").Scan(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (s *accountRecordStore) Update(ctx context.Context, record *model.AccountRecord) error {
	return s.db.WithContext(ctx).Save(record).Error
}
