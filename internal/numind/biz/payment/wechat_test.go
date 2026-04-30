package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestParseNotifyRequest_E2E_BypassDev(t *testing.T) {
	// Setup: set env var and runmode
	t.Setenv("NUMIND_E2E_BYPASS_PAY_SIG", "1")
	viper.Set("runmode", "debug")

	client := &WechatPayClient{
		appID:    "test_app",
		mchID:    "test_mch",
		apiV3Key: "test_key",
	}

	// Create mock request with unverified body
	body := map[string]interface{}{
		"id":   "test_event_id",
		"type": "TRANSACTION.SUCCESS",
		"resource": map[string]interface{}{
			"original_type": "transaction",
			"original_data": map[string]interface{}{
				"out_trade_no":   "test_order_123",
				"transaction_id": "test_tx_456",
				"trade_state":    "SUCCESS",
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	request := &http.Request{
		Body:   io.NopCloser(bytes.NewReader(bodyBytes)),
		Header: make(http.Header),
	}

	// Act
	outTradeNo, txID, err := client.ParseNotifyRequest(context.Background(), request)

	// Assert
	require.NoError(t, err)
	require.Equal(t, "test_order_123", outTradeNo)
	require.Equal(t, "test_tx_456", txID)
}

func TestParseNotifyRequest_E2E_BypassQA(t *testing.T) {
	// Setup: set env var and runmode for QA
	t.Setenv("NUMIND_E2E_BYPASS_PAY_SIG", "1")
	viper.Set("runmode", "debug")

	client := &WechatPayClient{
		appID:    "test_app",
		mchID:    "test_mch",
		apiV3Key: "test_key",
	}

	body := map[string]interface{}{
		"resource": map[string]interface{}{
			"original_type": "transaction",
			"original_data": map[string]interface{}{
				"out_trade_no":   "qa_order_789",
				"transaction_id": "qa_tx_012",
				"trade_state":    "SUCCESS",
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	request := &http.Request{
		Body:   io.NopCloser(bytes.NewReader(bodyBytes)),
		Header: make(http.Header),
	}

	// Act
	outTradeNo, txID, err := client.ParseNotifyRequest(context.Background(), request)

	// Assert
	require.NoError(t, err)
	require.Equal(t, "qa_order_789", outTradeNo)
	require.Equal(t, "qa_tx_012", txID)
}

func TestParseNotifyRequest_E2E_BypassDeniedInProd(t *testing.T) {
	// Setup: set env var to bypass but runmode=release (prod)
	// This simulates someone trying to enable bypass in prod (should fail)
	t.Setenv("NUMIND_E2E_BYPASS_PAY_SIG", "1")
	viper.Set("runmode", "release")

	client := &WechatPayClient{
		appID:          "test_app",
		mchID:          "test_mch",
		apiV3Key:       "test_key",
		wechatPubKey:   nil,
		certDownloader: nil,
	}

	body := map[string]interface{}{
		"resource": map[string]interface{}{
			"original_data": map[string]interface{}{
				"trade_state": "SUCCESS",
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	request := &http.Request{
		Body:   io.NopCloser(bytes.NewReader(bodyBytes)),
		Header: make(http.Header),
	}

	// Act: should fail because no verifier is available (bypass is blocked)
	_, _, err := client.ParseNotifyRequest(context.Background(), request)

	// Assert: error expected (not bypassed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no verifier available")
}

func TestParseNotifyRequest_E2E_BypassDisabledByDefault(t *testing.T) {
	// Setup: no env var set, runmode=debug (dev)
	// Should attempt normal signature verification (which will fail without proper setup)
	viper.Set("runmode", "debug")

	client := &WechatPayClient{
		appID:          "test_app",
		mchID:          "test_mch",
		apiV3Key:       "test_key",
		wechatPubKey:   nil,
		certDownloader: nil,
	}

	body := map[string]interface{}{
		"resource": map[string]interface{}{
			"original_data": map[string]interface{}{
				"trade_state": "SUCCESS",
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	request := &http.Request{
		Body:   io.NopCloser(bytes.NewReader(bodyBytes)),
		Header: make(http.Header),
	}

	// Act: should fail because no verifier and env var not set
	_, _, err := client.ParseNotifyRequest(context.Background(), request)

	// Assert: error expected (bypass not triggered)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no verifier available")
}

func TestParseNotifyRequestWithoutVerify_TradeStateNotSuccess(t *testing.T) {
	// Test that unverified parsing still checks trade_state
	client := &WechatPayClient{}

	body := map[string]interface{}{
		"resource": map[string]interface{}{
			"original_type": "transaction",
			"original_data": map[string]interface{}{
				"out_trade_no":   "test_order",
				"transaction_id": "test_tx",
				"trade_state":    "PENDING",
			},
		},
	}
	bodyBytes, _ := json.Marshal(body)
	request := &http.Request{
		Body:   io.NopCloser(bytes.NewReader(bodyBytes)),
		Header: make(http.Header),
	}

	// Act
	_, _, err := client.parseNotifyRequestWithoutVerify(context.Background(), request)

	// Assert
	require.Error(t, err)
	require.Contains(t, err.Error(), "payment not successful")
}
