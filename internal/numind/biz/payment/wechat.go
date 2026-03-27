package payment

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spf13/viper"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"

	"numind-server/internal/pkg/log"
)

// WechatPayClient wraps the WeChat Pay SDK client for Native payment.
type WechatPayClient struct {
	client    *core.Client
	appID     string
	mchID     string
	apiV3Key  string
	notifyURL string
}

// NewWechatPayClient reads WeChat Pay config from viper and initialises the client.
func NewWechatPayClient() (*WechatPayClient, error) {
	appID := viper.GetString("wechat.app_id")
	mchID := viper.GetString("wechat.mch_id")
	certSerialNo := viper.GetString("wechat.mch_cert_serial_no")
	apiV3Key := viper.GetString("wechat.mch_api_v3_key")
	privateKeyPath := viper.GetString("wechat.mch_private_key_path")
	notifyURL := viper.GetString("wechat.notify_url")

	privateKey, err := utils.LoadPrivateKeyWithPath(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("wechat: load private key: %w", err)
	}

	ctx := context.Background()
	client, err := core.NewClient(ctx, option.WithWechatPayAutoAuthCipher(mchID, certSerialNo, privateKey, apiV3Key))
	if err != nil {
		return nil, fmt.Errorf("wechat: create client: %w", err)
	}

	log.Infow("WechatPayClient initialised", "mch_id", mchID, "app_id", appID)

	return &WechatPayClient{
		client:    client,
		appID:     appID,
		mchID:     mchID,
		apiV3Key:  apiV3Key,
		notifyURL: notifyURL,
	}, nil
}

// NativePrepay creates a Native payment order and returns the QR code URL.
// amountCents is the payment amount in fen (RMB cents).
func (w *WechatPayClient) NativePrepay(ctx context.Context, orderNo string, amountCents int64, description string) (string, error) {
	svc := native.NativeApiService{Client: w.client}

	total := int64(amountCents)
	resp, _, err := svc.Prepay(ctx, native.PrepayRequest{
		Appid:       core.String(w.appID),
		Mchid:       core.String(w.mchID),
		Description: core.String(description),
		OutTradeNo:  core.String(orderNo),
		NotifyUrl:   core.String(w.notifyURL),
		Amount: &native.Amount{
			Total:    core.Int64(total),
			Currency: core.String("CNY"),
		},
	})
	if err != nil {
		return "", fmt.Errorf("wechat: native prepay: %w", err)
	}
	if resp.CodeUrl == nil {
		return "", fmt.Errorf("wechat: native prepay: empty code_url")
	}

	return *resp.CodeUrl, nil
}

// ParseNotifyRequest verifies the WeChat Pay signature and parses the payment notification.
// Returns outTradeNo and transactionID on success.
//
// TODO: Replace core.NewCertificateMapWithList(nil) with actual platform certificate loading
// for production. Currently signature verification will fail without valid certs.
// Consider using the downloader package or loading the cert from wechat.wechatpay_cert_path.
func (w *WechatPayClient) ParseNotifyRequest(ctx context.Context, request *http.Request) (outTradeNo string, transactionID string, err error) {
	handler, err := notify.NewRSANotifyHandler(
		w.apiV3Key,
		verifiers.NewSHA256WithRSAVerifier(core.NewCertificateMapWithList(nil)),
	)
	if err != nil {
		return "", "", fmt.Errorf("wechat: create notify handler: %w", err)
	}

	var transaction payments.Transaction
	_, err = handler.ParseNotifyRequest(ctx, request, &transaction)
	if err != nil {
		return "", "", fmt.Errorf("wechat: parse notify request: %w", err)
	}

	if transaction.TradeState == nil || *transaction.TradeState != "SUCCESS" {
		state := ""
		if transaction.TradeState != nil {
			state = *transaction.TradeState
		}
		return "", "", fmt.Errorf("wechat: payment not successful, trade_state: %s", state)
	}

	if transaction.OutTradeNo != nil {
		outTradeNo = *transaction.OutTradeNo
	}
	if transaction.TransactionId != nil {
		transactionID = *transaction.TransactionId
	}

	return outTradeNo, transactionID, nil
}
