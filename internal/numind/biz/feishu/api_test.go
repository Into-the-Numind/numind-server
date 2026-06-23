package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// TestAPIFor_NotConnected_PropagatesSentinel verifies APIFor surfaces
// ErrLarkNotConnected unchanged (so the tool layer maps it to the "please
// connect" soft error) rather than wrapping it into a generic build failure.
func TestAPIFor_NotConnected_PropagatesSentinel(t *testing.T) {
	store := &fakeAccountStore{acc: nil}
	c := newTestClient(t, store, &fakeRefresher{}, nil)

	api, err := c.APIFor(context.Background(), 7)
	if api != nil {
		t.Fatalf("APIFor should return nil LarkAPI on not-connected; got %v", api)
	}
	if !errors.Is(err, errno.ErrLarkNotConnected) {
		t.Fatalf("want ErrLarkNotConnected, got %v", err)
	}
}

// TestAPIFor_ReauthRequired_PropagatesSentinel verifies an expired token with no
// refresh_token propagates ErrLarkReauthRequired through APIFor.
func TestAPIFor_ReauthRequired_PropagatesSentinel(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID:         7,
		Provider:       ProviderLark,
		AppID:          "cli_app",
		AppSecretEnc:   mustEnc(t, "app-secret"),
		AccessTokenEnc: mustEnc(t, "expired"),
		TokenExpiresAt: pastTime(time.Hour),
		// no RefreshTokenEnc → cannot refresh → reauth required
	}}
	c := newTestClient(t, store, &fakeRefresher{}, nil)

	api, err := c.APIFor(context.Background(), 7)
	if api != nil {
		t.Fatalf("APIFor should return nil LarkAPI on reauth-required; got %v", api)
	}
	if !errors.Is(err, errno.ErrLarkReauthRequired) {
		t.Fatalf("want ErrLarkReauthRequired, got %v", err)
	}
}

// TestAPIFor_ValidToken_BuildsAPI verifies the happy path: a valid token yields a
// non-nil sdkLarkAPI bound to the decrypted user access token. (The SDK calls
// themselves require a live 飞书 server and are covered by S5 end-to-end, not here.)
func TestAPIFor_ValidToken_BuildsAPI(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID:         7,
		Provider:       ProviderLark,
		AppID:          "cli_app",
		AppSecretEnc:   mustEnc(t, "app-secret"),
		AccessTokenEnc: mustEnc(t, "u-token-valid"),
		TokenExpiresAt: futureTime(time.Hour),
	}}
	c := newTestClient(t, store, &fakeRefresher{}, nil)

	api, err := c.APIFor(context.Background(), 7)
	if err != nil {
		t.Fatalf("APIFor: %v", err)
	}
	sdk, ok := api.(*sdkLarkAPI)
	if !ok {
		t.Fatalf("APIFor should return *sdkLarkAPI; got %T", api)
	}
	if sdk.lc == nil || sdk.lc.API == nil {
		t.Fatal("sdkLarkAPI must hold a built *LarkClient with a non-nil SDK client")
	}
	if sdk.lc.UserAccessToken != "u-token-valid" {
		t.Fatalf("bound token: want u-token-valid, got %q", sdk.lc.UserAccessToken)
	}
	if got := sdk.opt(); got == nil {
		t.Fatal("opt() must return a non-nil request option binding the user token")
	}
}

// TestClient_SatisfiesLarkAPIProvider is a compile-time-style guard executed at
// runtime: *Client must be usable wherever a LarkAPIProvider is expected (the
// tool layer holds the interface, not the concrete type).
func TestClient_SatisfiesLarkAPIProvider(t *testing.T) {
	var _ LarkAPIProvider = (*Client)(nil)
}
