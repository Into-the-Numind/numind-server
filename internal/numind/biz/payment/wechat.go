package payment

import (
	"context"
	"crypto/rsa"
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
	client       *core.Client
	appID        string
	mchID        string
	apiV3Key     string
	notifyURL    string
	pubKeyID     string
	wechatPubKey *rsa.PublicKey
}

// NewWechatPayClient reads WeChat Pay config from viper and initialises the client.
// Supports public key mode (recommended) via wechat.wechatpay_public_key_id + wechat.wechatpay_cert_path.
func NewWechatPayClient() (*WechatPayClient, error) {
	appID := viper.GetString("wechat.app_id")
	mchID := viper.GetString("wechat.mch_id")
	certSerialNo := viper.GetString("wechat.mch_cert_serial_no")
	apiV3Key := viper.GetString("wechat.mch_api_v3_key")
	privateKeyPath := viper.GetString("wechat.mch_private_key_path")
	notifyURL := viper.GetString("wechat.notify_url")
	pubKeyID := viper.GetString("wechat.wechatpay_public_key_id")
	pubKeyPath := viper.GetString("wechat.wechatpay_cert_path")

	privateKey, err := utils.LoadPrivateKeyWithPath(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("wechat: load private key: %w", err)
	}

	var wechatPubKey *rsa.PublicKey
	var client *core.Client
	ctx := context.Background()

	// 公钥模式（推荐）
	if pubKeyID != "" && pubKeyPath != "" {
		wechatPubKey, err = utils.LoadPublicKeyWithPath(pubKeyPath)
		if err != nil {
			return nil, fmt.Errorf("wechat: load public key: %w", err)
		}
		client, err = core.NewClient(ctx, option.WithWechatPayPublicKeyAuthCipher(
			mchID, certSerialNo, privateKey, pubKeyID, wechatPubKey,
		))
		if err != nil {
			return nil, fmt.Errorf("wechat: create client (pubkey mode): %w", err)
		}
		log.Infow("WechatPayClient initialised (public key mode)", "mch_id", mchID, "pub_key_id", pubKeyID)
	} else {
		// 证书模式（兼容）
		client, err = core.NewClient(ctx, option.WithWechatPayAutoAuthCipher(mchID, certSerialNo, privateKey, apiV3Key))
		if err != nil {
			return nil, fmt.Errorf("wechat: create client (cert mode): %w", err)
		}
		log.Infow("WechatPayClient initialised (cert mode)", "mch_id", mchID)
	}

	return &WechatPayClient{
		client:       client,
		appID:        appID,
		mchID:        mchID,
		apiV3Key:     apiV3Key,
		notifyURL:    notifyURL,
		pubKeyID:     pubKeyID,
		wechatPubKey: wechatPubKey,
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
func (w *WechatPayClient) ParseNotifyRequest(ctx context.Context, request *http.Request) (outTradeNo string, transactionID string, err error) {
	var handler *notify.Handler

	if w.wechatPubKey == nil || w.pubKeyID == "" {
		return "", "", fmt.Errorf("wechat: public key not configured, cannot verify callback signature")
	}

	// 公钥模式验签
	handler, err = notify.NewRSANotifyHandler(
		w.apiV3Key,
		verifiers.NewSHA256WithRSAPubkeyVerifier(w.pubKeyID, *w.wechatPubKey),
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
