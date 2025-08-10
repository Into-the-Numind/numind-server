package account_record

import (
	"context"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type AccountRecordBiz interface {
	Create(ctx context.Context, record *model.AccountRecord) error
	CreatePaymentRecord(ctx context.Context, order *model.Order, channel string) error
	GetByID(ctx context.Context, id uint) (*model.AccountRecord, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.AccountRecord, error)
	ListByUserAndType(ctx context.Context, userID uint, recordType string, offset, limit int) ([]*model.AccountRecord, error)
	GetUserTotalAmount(ctx context.Context, userID uint, recordType string) (int64, error)
	GetUserPaymentHistory(ctx context.Context, userID uint, offset, limit int) ([]*model.AccountRecord, error)
}

type accountRecordBiz struct {
	ds store.IStore
}

func NewAccountRecordBiz(ds store.IStore) AccountRecordBiz {
	return &accountRecordBiz{ds: ds}
}

func (b *accountRecordBiz) Create(ctx context.Context, record *model.AccountRecord) error {
	return b.ds.AccountRecords().Create(ctx, record)
}

// CreatePaymentRecord 创建支付记录
func (b *accountRecordBiz) CreatePaymentRecord(ctx context.Context, order *model.Order, channel string) error {
	// 将分转换为元
	amountYuan := float64(order.Amount) / 100.0

	record := &model.AccountRecord{
		UserID:      order.UserID,
		OrderID:     order.ID,
		OutTradeNo:  order.OutTradeNo,
		Amount:      order.Amount,
		AmountYuan:  amountYuan,
		Type:        "payment",
		Status:      "success",
		Description: order.Description,
		PaymentAt:   order.PaidAt,
		Channel:     channel,
		Remark:      "微信支付成功",
	}

	return b.ds.AccountRecords().Create(ctx, record)
}

func (b *accountRecordBiz) GetByID(ctx context.Context, id uint) (*model.AccountRecord, error) {
	return b.ds.AccountRecords().GetByID(ctx, id)
}

func (b *accountRecordBiz) ListByUser(ctx context.Context, userID uint, offset, limit int) ([]*model.AccountRecord, error) {
	return b.ds.AccountRecords().ListByUser(ctx, userID, offset, limit)
}

func (b *accountRecordBiz) ListByUserAndType(ctx context.Context, userID uint, recordType string, offset, limit int) ([]*model.AccountRecord, error) {
	return b.ds.AccountRecords().ListByUserAndType(ctx, userID, recordType, offset, limit)
}

func (b *accountRecordBiz) GetUserTotalAmount(ctx context.Context, userID uint, recordType string) (int64, error) {
	return b.ds.AccountRecords().GetUserTotalAmount(ctx, userID, recordType)
}

// GetUserPaymentHistory 获取用户支付历史
func (b *accountRecordBiz) GetUserPaymentHistory(ctx context.Context, userID uint, offset, limit int) ([]*model.AccountRecord, error) {
	return b.ds.AccountRecords().ListByUserAndType(ctx, userID, "payment", offset, limit)
}
