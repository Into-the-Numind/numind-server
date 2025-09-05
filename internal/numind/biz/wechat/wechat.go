package wechat

import (
	"context"
	"net/http"
	"numind-server/internal/numind/store"

	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
)

type WechatBiz interface {
	CreateNativeOrder(cfg map[string]string, outTradeNo, description string, amount int64) (interface{}, error)
	ParsePayNotify(cfg map[string]string, ctx context.Context, req *http.Request) (*payments.Transaction, error)
}

type wechatBiz struct {
	ds store.IStore
}

func New(ds store.IStore) WechatBiz {
	return &wechatBiz{ds: ds}
}

func (b *wechatBiz) CreateNativeOrder(cfg map[string]string, outTradeNo, description string, amount int64) (interface{}, error) {
	return CreateNativeOrder(cfg, outTradeNo, description, amount)
}

func (b *wechatBiz) ParsePayNotify(cfg map[string]string, ctx context.Context, req *http.Request) (*payments.Transaction, error) {
	return ParsePayNotify(cfg, ctx, req)
}
