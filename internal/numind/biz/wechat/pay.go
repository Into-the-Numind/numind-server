package wechat

import (
	"context"
	"crypto/x509"
	"net/http"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

type PayClient struct {
	Client *core.Client
}

// NewPayClientFromMap 支持从map[string]string初始化PayClient
func NewPayClientFromMap(cfg map[string]string) (*PayClient, error) {
	mchPrivateKey, err := utils.LoadPrivateKeyWithPath(cfg["mch_private_key_path"])
	if err != nil {
		return nil, err
	}
	wechatPayCert, err := utils.LoadCertificateWithPath(cfg["wechatpay_cert_path"])
	if err != nil {
		return nil, err
	}
	client, err := core.NewClient(
		context.Background(),
		option.WithWechatPayAutoAuthCipher(
			cfg["mch_id"],
			cfg["mch_cert_serial_no"],
			mchPrivateKey,
			cfg["mch_api_v3_key"],
		),
		option.WithWechatPayCertificate([]*x509.Certificate{wechatPayCert}),
	)
	if err != nil {
		return nil, err
	}
	return &PayClient{Client: client}, nil
}

// Native下单业务
func CreateNativeOrder(cfg map[string]string, outTradeNo, description string, amount int64) (interface{}, error) {
	payClient, err := NewPayClientFromMap(cfg)
	if err != nil {
		return nil, err
	}
	svc := native.NativeApiService{Client: payClient.Client}
	resp, _, err := svc.Prepay(context.Background(),
		native.PrepayRequest{
			Appid:       core.String(cfg["app_id"]),
			Mchid:       core.String(cfg["mch_id"]),
			Description: core.String(description),
			OutTradeNo:  core.String(outTradeNo),
			NotifyUrl:   core.String(cfg["notify_url"]),
			Amount: &native.Amount{
				Total: core.Int64(amount),
			},
		},
	)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// 支付回调业务
func ParsePayNotify(cfg map[string]string, ctx context.Context, req *http.Request) (*payments.Transaction, error) {
	handler := notify.NewNotifyHandler(cfg["mch_api_v3_key"], nil)
	transaction := &payments.Transaction{}
	_, err := handler.ParseNotifyRequest(ctx, req, transaction)
	if err != nil {
		return nil, err
	}
	return transaction, nil
}
