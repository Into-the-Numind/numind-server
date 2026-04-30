package payment

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/viper"
	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"

	"numind-server/internal/pkg/log"
)

// WechatPayClient wraps the WeChat Pay SDK client for Native payment.
type WechatPayClient struct {
	client         *core.Client
	appID          string
	mchID          string
	apiV3Key       string
	notifyURL      string
	pubKeyID       string
	wechatPubKey   *rsa.PublicKey
	certDownloader *downloader.CertificateDownloader // 用于旧证书模式验签（灰度期兼容）
}

// NewWechatPayClient reads WeChat Pay config from viper and initialises the client.
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

	wc := &WechatPayClient{
		appID:     appID,
		mchID:     mchID,
		apiV3Key:  apiV3Key,
		notifyURL: notifyURL,
		pubKeyID:  pubKeyID,
	}

	ctx := context.Background()

	// 加载公钥（如果配置了）
	if pubKeyID != "" && pubKeyPath != "" {
		wc.wechatPubKey, err = utils.LoadPublicKeyWithPath(pubKeyPath)
		if err != nil {
			return nil, fmt.Errorf("wechat: load public key: %w", err)
		}
	}

	// 创建 client（公钥模式优先）
	if wc.wechatPubKey != nil {
		wc.client, err = core.NewClient(ctx, option.WithWechatPayPublicKeyAuthCipher(
			mchID, certSerialNo, privateKey, pubKeyID, wc.wechatPubKey,
		))
		if err != nil {
			return nil, fmt.Errorf("wechat: create client (pubkey mode): %w", err)
		}
		log.Infow("WechatPayClient initialised (public key mode)", "mch_id", mchID, "pub_key_id", pubKeyID)
	} else {
		wc.client, err = core.NewClient(ctx, option.WithWechatPayAutoAuthCipher(mchID, certSerialNo, privateKey, apiV3Key))
		if err != nil {
			return nil, fmt.Errorf("wechat: create client (cert mode): %w", err)
		}
		log.Infow("WechatPayClient initialised (cert mode)", "mch_id", mchID)
	}

	// 下载平台证书用于灰度期旧证书回调验签
	certDownloader, err := downloader.NewCertificateDownloaderWithClient(ctx, wc.client, apiV3Key)
	if err != nil {
		log.Warnw("Failed to init certificate downloader (old cert callbacks will fail)", "error", err)
	} else {
		wc.certDownloader = certDownloader
		log.Infow("Platform certificate downloader initialised for hybrid verify")
	}

	return wc, nil
}

// NativePrepay creates a Native payment order and returns the QR code URL.
func (w *WechatPayClient) NativePrepay(ctx context.Context, orderNo string, amountCents int64, description string) (string, error) {
	svc := native.NativeApiService{Client: w.client}

	resp, _, err := svc.Prepay(ctx, native.PrepayRequest{
		Appid:       core.String(w.appID),
		Mchid:       core.String(w.mchID),
		Description: core.String(description),
		OutTradeNo:  core.String(orderNo),
		NotifyUrl:   core.String(w.notifyURL),
		Amount: &native.Amount{
			Total:    core.Int64(amountCents),
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
// Supports both public key mode and certificate mode (for gray-release compatibility).
// E2E bypass via NUMIND_E2E_BYPASS_PAY_SIG=1 in dev/qa environments (prod immune due to env check).
func (w *WechatPayClient) ParseNotifyRequest(ctx context.Context, request *http.Request) (outTradeNo string, transactionID string, err error) {
	// E2E bypass check: dev/qa only
	if os.Getenv("NUMIND_E2E_BYPASS_PAY_SIG") == "1" && viper.GetString("runmode") != "release" {
		log.Warnw("Wechat signature verification BYPASSED via NUMIND_E2E_BYPASS_PAY_SIG=1; this MUST never happen in prod")
		return w.parseNotifyRequestWithoutVerify(ctx, request)
	}

	// 根据回调头部的 Wechatpay-Serial 判断用哪种验签
	serial := request.Header.Get("Wechatpay-Serial")
	isPubKeyMode := strings.HasPrefix(serial, "PUB_KEY_ID_")

	var handler *notify.Handler

	if isPubKeyMode && w.wechatPubKey != nil {
		// 公钥模式
		handler, err = notify.NewRSANotifyHandler(
			w.apiV3Key,
			verifiers.NewSHA256WithRSAPubkeyVerifier(w.pubKeyID, *w.wechatPubKey),
		)
	} else if w.certDownloader != nil {
		// 旧证书模式（灰度期兼容）
		handler, err = notify.NewRSANotifyHandler(w.apiV3Key, verifiers.NewSHA256WithRSAVerifier(w.certDownloader))
	} else {
		return "", "", fmt.Errorf("wechat: no verifier available for serial=%s", serial)
	}

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

// parseNotifyRequestWithoutVerify extracts outTradeNo and transactionID from request body without signature verification.
// Used only for E2E testing when NUMIND_E2E_BYPASS_PAY_SIG=1 in dev/qa environments.
func (w *WechatPayClient) parseNotifyRequestWithoutVerify(ctx context.Context, request *http.Request) (outTradeNo string, transactionID string, err error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return "", "", fmt.Errorf("wechat: read request body: %w", err)
	}

	var notification struct {
		Resource struct {
			OriginalType string `json:"original_type"`
			OriginalData struct {
				OutTradeNo    string `json:"out_trade_no"`
				TransactionID string `json:"transaction_id"`
				TradeState    string `json:"trade_state"`
			} `json:"original_data"`
		} `json:"resource"`
	}

	if err = json.Unmarshal(body, &notification); err != nil {
		return "", "", fmt.Errorf("wechat: unmarshal notification: %w", err)
	}

	if notification.Resource.OriginalData.TradeState != "SUCCESS" {
		return "", "", fmt.Errorf("wechat: payment not successful, trade_state: %s", notification.Resource.OriginalData.TradeState)
	}

	return notification.Resource.OriginalData.OutTradeNo, notification.Resource.OriginalData.TransactionID, nil
}
