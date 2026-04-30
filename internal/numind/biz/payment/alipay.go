package payment

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/smartwalle/alipay/v3"
	"github.com/spf13/viper"

	"numind-server/internal/pkg/log"
)

// AlipayClient wraps the Alipay SDK client for PC page payment.
type AlipayClient struct {
	client    *alipay.Client
	notifyURL string
}

// NewAlipayClient reads Alipay config from viper and initialises the client.
// If alipay.app_id is empty, returns (nil, nil) — Alipay not configured, which is expected.
func NewAlipayClient() (*AlipayClient, error) {
	appID := viper.GetString("alipay.app_id")
	if appID == "" {
		log.Infow("AlipayClient not configured, skipping initialisation")
		return nil, nil
	}

	privateKey := viper.GetString("alipay.private_key")
	alipayPublicKey := viper.GetString("alipay.alipay_public_key")
	notifyURL := viper.GetString("alipay.notify_url")
	isProduction := viper.GetBool("alipay.is_production")

	client, err := alipay.New(appID, privateKey, isProduction)
	if err != nil {
		return nil, fmt.Errorf("alipay: create client: %w", err)
	}

	if err = client.LoadAliPayPublicKey(alipayPublicKey); err != nil {
		return nil, fmt.Errorf("alipay: load public key: %w", err)
	}

	log.Infow("AlipayClient initialised", "app_id", appID, "is_production", isProduction)

	return &AlipayClient{
		client:    client,
		notifyURL: notifyURL,
	}, nil
}

// PagePay creates an Alipay PC page payment order and returns the payment page URL.
// amountCents is the payment amount in fen (RMB cents); it is converted to yuan for Alipay.
func (a *AlipayClient) PagePay(ctx context.Context, orderNo string, amountCents int64, subject string) (string, error) {
	// Convert cents to yuan with two decimal places (e.g. 9900 -> "99.00")
	yuan := fmt.Sprintf("%.2f", float64(amountCents)/100.0)

	param := alipay.TradePagePay{
		Trade: alipay.Trade{
			Subject:     subject,
			OutTradeNo:  orderNo,
			TotalAmount: yuan,
			ProductCode: "FAST_INSTANT_TRADE_PAY",
			NotifyURL:   a.notifyURL,
		},
	}

	payURL, err := a.client.TradePagePay(param)
	if err != nil {
		return "", fmt.Errorf("alipay: trade page pay: %w", err)
	}

	return payURL.String(), nil
}

// VerifyNotify parses and verifies the Alipay async notification from an HTTP request.
// E2E bypass via NUMIND_E2E_BYPASS_PAY_SIG=1 in dev/qa environments (prod immune due to env check).
func (a *AlipayClient) VerifyNotify(request *http.Request) (*alipay.Notification, error) {
	// E2E bypass check: dev/qa only
	if os.Getenv("NUMIND_E2E_BYPASS_PAY_SIG") == "1" && viper.GetString("runmode") != "release" {
		log.Warnw("Alipay signature verification BYPASSED via NUMIND_E2E_BYPASS_PAY_SIG=1; this MUST never happen in prod")
		return a.verifyNotifyWithoutSignature(request)
	}

	if err := request.ParseForm(); err != nil {
		return nil, fmt.Errorf("alipay: parse form: %w", err)
	}

	notification, err := a.client.DecodeNotification(context.Background(), request.Form)
	if err != nil {
		return nil, fmt.Errorf("alipay: decode notification: %w", err)
	}

	return notification, nil
}

// verifyNotifyWithoutSignature extracts notification data from form without signature verification.
// Used only for E2E testing when NUMIND_E2E_BYPASS_PAY_SIG=1 in dev/qa environments.
func (a *AlipayClient) verifyNotifyWithoutSignature(request *http.Request) (*alipay.Notification, error) {
	if err := request.ParseForm(); err != nil {
		return nil, fmt.Errorf("alipay: parse form: %w", err)
	}

	// Extract critical fields from form. The caller (payment.go HandleAlipayNotify)
	// will check TradeStatus against "TRADE_SUCCESS" or "TRADE_FINISHED".
	// In test mode, TradeStatus is left zero; callers should set via form or mock.
	notification := &alipay.Notification{
		OutTradeNo: request.FormValue("out_trade_no"),
		TradeNo:    request.FormValue("trade_no"),
	}

	return notification, nil
}
