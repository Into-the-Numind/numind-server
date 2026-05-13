package payment

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestVerifyNotify_E2E_BypassDev(t *testing.T) {
	// Setup: set env var and runmode
	t.Setenv("NUMIND_E2E_BYPASS_PAY_SIG", "1")
	viper.Set("runmode", "debug")

	client := &AlipayClient{
		client:    nil, // Not needed for bypass
		notifyURL: "http://example.com/notify",
	}

	// Create mock request with form data (no signature verification)
	formData := "out_trade_no=alipay_order_123&trade_no=alipay_tx_456&trade_status=TRADE_SUCCESS"
	request := &http.Request{
		Method: "POST",
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewBufferString(formData)),
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Act
	notification, err := client.VerifyNotify(request)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, notification)
	require.Equal(t, "alipay_order_123", notification.OutTradeNo)
	require.Equal(t, "alipay_tx_456", notification.TradeNo)
}

func TestVerifyNotify_E2E_BypassQA(t *testing.T) {
	// Setup: set env var and runmode for QA
	t.Setenv("NUMIND_E2E_BYPASS_PAY_SIG", "1")
	viper.Set("runmode", "debug")

	client := &AlipayClient{
		client:    nil,
		notifyURL: "http://example.com/notify",
	}

	formData := "out_trade_no=qa_order_789&trade_no=qa_tx_012&trade_status=TRADE_FINISHED"
	request := &http.Request{
		Method: "POST",
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewBufferString(formData)),
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Act
	notification, err := client.VerifyNotify(request)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, notification)
	require.Equal(t, "qa_order_789", notification.OutTradeNo)
	require.Equal(t, "qa_tx_012", notification.TradeNo)
}

func TestVerifyNotify_E2E_BypassDeniedInProd(t *testing.T) {
	// Setup: set env var to bypass but runmode=release (prod)
	// This simulates someone trying to enable bypass in prod (should be blocked)
	// When bypass is disabled, the real DecodeNotification is called, which fails with nil client
	t.Setenv("NUMIND_E2E_BYPASS_PAY_SIG", "1")
	viper.Set("runmode", "release")

	client := &AlipayClient{
		client:    nil,
		notifyURL: "http://example.com/notify",
	}

	formData := "out_trade_no=prod_order&trade_no=prod_tx&trade_status=TRADE_SUCCESS"
	request := &http.Request{
		Method: "POST",
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewBufferString(formData)),
	}

	// Act: should panic or fail because bypass is blocked in prod (runmode=release)
	// and client is nil, so DecodeNotification will be called and fail
	defer func() {
		if r := recover(); r != nil {
			// Expected: nil pointer panic from client.DecodeNotification
			t.Logf("Expected panic (bypass blocked in prod): %v", r)
		}
	}()

	_, err := client.VerifyNotify(request)

	// Assert: error expected if no panic
	if err == nil {
		t.Fatal("expected error due to nil client and bypass disabled in prod")
	}
}

func TestVerifyNotify_E2E_BypassDisabledByDefault(t *testing.T) {
	// Setup: no env var set, runmode=debug (dev)
	// Should attempt normal signature verification (which will fail without proper setup)
	viper.Set("runmode", "debug")

	client := &AlipayClient{
		client:    nil,
		notifyURL: "http://example.com/notify",
	}

	formData := "out_trade_no=order&trade_no=tx&trade_status=TRADE_SUCCESS"
	request := &http.Request{
		Method: "POST",
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewBufferString(formData)),
	}

	// Act: should fail because client is nil and bypass env var not set
	defer func() {
		if r := recover(); r != nil {
			// Expected: nil pointer panic from client.DecodeNotification
			t.Logf("Expected panic (bypass not enabled): %v", r)
		}
	}()

	_, err := client.VerifyNotify(request)

	// Assert: error expected if no panic
	if err == nil {
		t.Fatal("expected error due to nil client and bypass env var not set")
	}
}

func TestVerifyNotifyWithoutSignature_ParseForm(t *testing.T) {
	// Test that unverified parsing correctly extracts form values
	client := &AlipayClient{}

	formData := "out_trade_no=test_order_xyz&trade_no=test_tx_abc&trade_status=TRADE_SUCCESS"
	request := &http.Request{
		Method: "POST",
		Header: make(http.Header),
		Body:   io.NopCloser(bytes.NewBufferString(formData)),
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Act
	notification, err := client.verifyNotifyWithoutSignature(request)

	// Assert
	require.NoError(t, err)
	require.NotNil(t, notification)
	require.Equal(t, "test_order_xyz", notification.OutTradeNo)
	require.Equal(t, "test_tx_abc", notification.TradeNo)
	// Note: verifyNotifyWithoutSignature only extracts OutTradeNo and TradeNo
	// (signature bypass only for those critical fields). Caller checks TradeStatus separately.
}
