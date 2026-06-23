// Package feishu — client.go builds a per-user 飞书 (Lark) API client, handling
// token decryption, expiry detection, and concurrency-safe token refresh. This
// is the feishu-integration T9 building block consumed by the lark_* agent tools
// (T10).
//
// Design (design.md §7):
//
//   - Tokens are stored AES-256-GCM encrypted (T3). Client.For decrypts the
//     user_access_token at THIS boundary and bundles it with a freshly built
//     *lark.Client so a tool can pass it per call via larkcore.WithUserAccessToken.
//   - Expiry: if TokenExpiresAt is in the past (minus a safety skew) AND a
//     refresh_token exists → refresh under a per-user distributed lock
//     (feishu:refresh:<userID>) so concurrent agent tool calls refresh AT MOST
//     ONCE; the losers re-read the freshly-written token. No refresh_token (or a
//     failed refresh) → the sentinel errno.ErrLarkReauthRequired, which the tool
//     layer (T10) translates into a SOFT error prompting the user to reconnect —
//     it never hard-kills the agent run.
//   - A nil TokenExpiresAt means 飞书 omitted expires_in (unknown). Per design we
//     treat it as valid (do NOT proactively refresh / reauth) — a 401 at call
//     time is then surfaced as a soft error by the tool layer.
//
// NOT routed through aiservice: 飞书 is an external business API, not an LLM
// gateway. The underlying SDK is github.com/larksuite/oapi-sdk-go/v3.
//
// Security: plaintext tokens / secrets are never logged. The returned
// LarkClient.UserAccessToken is plaintext by necessity (the SDK needs it) but
// callers must not log it.
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ProviderLark is the third-party provider key for 飞书 in
// user_third_party_account (design.md §3).
const ProviderLark = "lark"

// refreshSkew is subtracted from TokenExpiresAt when deciding whether a token is
// "still valid". Refreshing slightly early avoids a token expiring mid-call
// (clock skew + request latency between the check and the 飞书 API call).
const refreshSkew = 2 * time.Minute

// refreshLockTTL bounds how long a single refresh may hold the per-user lock.
// 飞书's token endpoint is fast; this is a safety net against a crashed holder
// wedging other callers. It MUST exceed the worst-case refresh round-trip.
const refreshLockTTL = 30 * time.Second

// refreshLockWait bounds how long a losing caller blocks waiting for the winner
// to finish refreshing before giving up (and surfacing reauth). Generous enough
// to cover a slow 飞书 round-trip, short enough not to wedge an agent tool call.
const refreshLockWait = 20 * time.Second

// Cipher is the subset of crypto.Cipher the client needs at the store boundary:
// Decrypt to open stored credentials, Encrypt to re-seal refreshed tokens before
// persisting. An interface so tests can substitute a fake; production always
// passes *crypto.Cipher.
type Cipher interface {
	Decrypt(blob []byte) ([]byte, error)
	Encrypt(plain []byte) ([]byte, error)
}

// TokenRefresher exchanges a refresh_token for a fresh user_access_token. It is
// abstracted so the HTTP call to 飞书 is mocked in tests. The real
// implementation (httpTokenRefresher) lives below and talks to the 飞书 v2 token
// endpoint with grant_type=refresh_token — NOT through aiservice.
type TokenRefresher interface {
	Refresh(ctx context.Context, appID, appSecret, refreshToken string) (*oauthTokenResp, error)
}

// refreshLocker is a per-key distributed mutual-exclusion primitive used to
// serialise token refreshes for a single user. Acquire BLOCKS until the lock is
// held (or the context/ deadline elapses) and returns a release closure. The
// production implementation is Redis-backed (RedisRefreshLocker); tests inject an
// in-process fake. A blocking (not "skip if held") lock is required so the losing
// caller waits and then re-reads the freshly refreshed token, rather than racing
// a second refresh.
type refreshLocker interface {
	Acquire(ctx context.Context, key string) (release func(), err error)
}

// LarkClient bundles a built 飞书 SDK client with the (decrypted) user access
// token a tool must pass per request via larkcore.WithUserAccessToken, plus the
// app_id for observability (langfuse span metadata, design.md §9). The token is
// plaintext and MUST NOT be logged.
type LarkClient struct {
	API             *lark.Client
	UserAccessToken string
	AppID           string
}

// Client builds per-user LarkClients. Safe for concurrent use: it holds only
// immutable dependencies; per-user mutable state (token rows) lives in the store
// and refreshes are serialised by the locker.
type Client struct {
	store     store.IThirdPartyAccountStore
	cipher    Cipher
	refresher TokenRefresher
	locker    refreshLocker

	// now is the clock, overridable in tests. Defaults to time.Now.
	now func() time.Time
	// isNotFound classifies a store Get error as "no row" (→ ErrLarkNotConnected).
	// Defaults to gorm.ErrRecordNotFound matching; tests override for fake stores.
	isNotFound func(error) bool
}

// NewClient wires the client. All dependencies are required — a nil cipher would
// mean handling plaintext-at-rest assumptions wrong, a nil locker would disable
// the single-refresh guarantee, so it fails fast rather than degrading silently.
func NewClient(s store.IThirdPartyAccountStore, cipher Cipher, refresher TokenRefresher, locker refreshLocker) (*Client, error) {
	if s == nil {
		return nil, errors.New("feishu: nil store for client")
	}
	if cipher == nil {
		return nil, errors.New("feishu: nil cipher for client")
	}
	if refresher == nil {
		return nil, errors.New("feishu: nil token refresher for client")
	}
	if locker == nil {
		return nil, errors.New("feishu: nil refresh locker for client")
	}
	return &Client{
		store:      s,
		cipher:     cipher,
		refresher:  refresher,
		locker:     locker,
		now:        time.Now,
		isNotFound: func(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) },
	}, nil
}

// For returns a LarkClient ready to act on behalf of userID's 飞书 account.
//
// Flow:
//  1. Load the account row. Missing → errno.ErrLarkNotConnected.
//  2. If the token is still valid → build the client straight away.
//  3. If expired and there's a refresh_token → refresh under the per-user lock
//     (refreshing AT MOST once across concurrent callers) → build with the new
//     token.
//  4. If expired with no refresh_token, or the refresh fails →
//     errno.ErrLarkReauthRequired.
func (c *Client) For(ctx context.Context, userID uint) (*LarkClient, error) {
	acc, err := c.store.Get(ctx, userID, ProviderLark)
	if err != nil {
		if c.isNotFound(err) {
			return nil, fmt.Errorf("%w: user %d has no 飞书 connection", errno.ErrLarkNotConnected, userID)
		}
		return nil, fmt.Errorf("feishu: load account (user %d): %w", userID, err)
	}

	if !c.expired(acc.TokenExpiresAt) {
		return c.build(acc)
	}

	// Expired (or near expiry). We need to refresh; that requires a refresh token.
	if len(acc.RefreshTokenEnc) == 0 {
		return nil, fmt.Errorf("%w: 飞书 token expired and no refresh token (user %d)", errno.ErrLarkReauthRequired, userID)
	}

	refreshed, err := c.refresh(ctx, userID)
	if err != nil {
		return nil, err
	}
	return c.build(refreshed)
}

// expired reports whether a token with the given absolute expiry should be
// refreshed. A nil expiry means "unknown" (飞书 omitted expires_in) → treated as
// NOT expired (design.md §3: must not be misjudged as already-expired). A
// non-nil expiry is expired once now+skew has passed it.
func (c *Client) expired(exp *time.Time) bool {
	if exp == nil {
		return false
	}
	return !c.now().Add(refreshSkew).Before(*exp)
}

// refresh refreshes userID's token under the per-user lock and returns the
// updated account row. Concurrency-safe single refresh: after acquiring the lock
// it RE-READS the row and re-checks expiry, so a caller that lost the race to a
// peer that already refreshed simply returns the fresh row without a second
// 飞书 round-trip.
func (c *Client) refresh(ctx context.Context, userID uint) (*model.UserThirdPartyAccount, error) {
	lockCtx, cancel := context.WithTimeout(ctx, refreshLockWait)
	defer cancel()

	release, err := c.locker.Acquire(lockCtx, refreshLockKey(userID))
	if err != nil {
		// Could not serialise the refresh → fail closed as reauth-required rather
		// than racing an uncoordinated refresh (the tool layer turns this into a
		// soft "please reconnect" message).
		return nil, fmt.Errorf("%w: could not acquire 飞书 refresh lock (user %d): %v", errno.ErrLarkReauthRequired, userID, err)
	}
	defer release()

	// Double-check: a peer may have refreshed while we waited for the lock.
	acc, err := c.store.Get(ctx, userID, ProviderLark)
	if err != nil {
		if c.isNotFound(err) {
			return nil, fmt.Errorf("%w: user %d 飞书 connection vanished during refresh", errno.ErrLarkReauthRequired, userID)
		}
		return nil, fmt.Errorf("feishu: reload account before refresh (user %d): %w", userID, err)
	}
	if !c.expired(acc.TokenExpiresAt) {
		return acc, nil // someone already refreshed; use the fresh row
	}
	if len(acc.RefreshTokenEnc) == 0 {
		return nil, fmt.Errorf("%w: 飞书 token expired and no refresh token (user %d)", errno.ErrLarkReauthRequired, userID)
	}

	appSecret, err := c.cipher.Decrypt(acc.AppSecretEnc)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypt app secret for refresh (user %d): %v", errno.ErrLarkReauthRequired, userID, err)
	}
	refreshTok, err := c.cipher.Decrypt(acc.RefreshTokenEnc)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypt refresh token (user %d): %v", errno.ErrLarkReauthRequired, userID, err)
	}

	resp, err := c.refresher.Refresh(ctx, acc.AppID, string(appSecret), string(refreshTok))
	if err != nil || resp == nil || resp.AccessToken == "" {
		// Any refresh failure means the user must re-authorize.
		return nil, fmt.Errorf("%w: 飞书 token refresh failed (user %d): %v", errno.ErrLarkReauthRequired, userID, err)
	}

	accessEnc, err := c.cipher.Encrypt([]byte(resp.AccessToken))
	if err != nil {
		return nil, fmt.Errorf("feishu: encrypt refreshed access token (user %d): %w", userID, err)
	}
	// 飞书 v2 rotates the refresh_token; keep the old one if a new one is absent.
	refreshEnc := acc.RefreshTokenEnc
	if resp.RefreshToken != "" {
		refreshEnc, err = c.cipher.Encrypt([]byte(resp.RefreshToken))
		if err != nil {
			return nil, fmt.Errorf("feishu: encrypt refreshed refresh token (user %d): %w", userID, err)
		}
	}
	var exp *time.Time
	if resp.ExpiresIn > 0 {
		t := c.now().Add(time.Duration(resp.ExpiresIn) * time.Second)
		exp = &t
	}

	if err := c.store.UpdateTokens(ctx, userID, ProviderLark, accessEnc, refreshEnc, exp); err != nil {
		return nil, fmt.Errorf("feishu: persist refreshed tokens (user %d): %w", userID, err)
	}

	// Reflect the new values onto the in-memory row we return (avoid a re-read).
	acc.AccessTokenEnc = accessEnc
	acc.RefreshTokenEnc = refreshEnc
	acc.TokenExpiresAt = exp
	if resp.Scope != "" {
		acc.Scopes = resp.Scope
	}
	return acc, nil
}

// build constructs a LarkClient from an account row with a known-good token. It
// decrypts the access token and wires the SDK client with the app credentials so
// the SDK can mint app-level tokens when needed (the user token is passed per
// call by the tool layer).
func (c *Client) build(acc *model.UserThirdPartyAccount) (*LarkClient, error) {
	accessTok, err := c.cipher.Decrypt(acc.AccessTokenEnc)
	if err != nil {
		// A corrupt/undecryptable token is unrecoverable without re-auth.
		return nil, fmt.Errorf("%w: decrypt access token (user %d): %v", errno.ErrLarkReauthRequired, acc.UserID, err)
	}

	appSecret, err := c.cipher.Decrypt(acc.AppSecretEnc)
	if err != nil {
		return nil, fmt.Errorf("%w: decrypt app secret (user %d): %v", errno.ErrLarkReauthRequired, acc.UserID, err)
	}

	// LogLevelWarn suppresses the SDK's per-construction "client ready" Info line
	// (we build a fresh client per For call) while keeping warnings/errors.
	api := lark.NewClient(acc.AppID, string(appSecret), lark.WithLogLevel(larkcore.LogLevelWarn))
	return &LarkClient{
		API:             api,
		UserAccessToken: string(accessTok),
		AppID:           acc.AppID,
	}, nil
}

// refreshLockKey returns the per-user Redis lock key. Including the userID
// guarantees one user's refresh never blocks or clobbers another's
// (design.md §7 / plan T9 acceptance: key MUST contain userID).
func refreshLockKey(userID uint) string {
	return "feishu:refresh:" + strconv.FormatUint(uint64(userID), 10)
}

// --- Redis-backed refresh locker --------------------------------------------

// RedisRefreshLocker is the production refreshLocker over go-redis. Acquire
// blocks (SETNX-poll) until it wins the lock or the context elapses; release
// deletes the key (best-effort — the TTL is the crash safety net).
type RedisRefreshLocker struct {
	rdb *redis.Client
	// pollInterval is how often Acquire retries SETNX while waiting. Small enough
	// to feel instant, large enough not to hammer Redis.
	pollInterval time.Duration
}

// NewRedisRefreshLocker wraps a go-redis client. A nil client is rejected rather
// than producing a locker that silently no-ops (which would disable the
// single-refresh guarantee and allow refresh storms).
func NewRedisRefreshLocker(rdb *redis.Client) (*RedisRefreshLocker, error) {
	if rdb == nil {
		return nil, errors.New("feishu: nil redis client for refresh locker")
	}
	return &RedisRefreshLocker{rdb: rdb, pollInterval: 50 * time.Millisecond}, nil
}

// Acquire blocks until it wins the lock (SETNX succeeds) or ctx is done. The
// returned release deletes the key. The lock carries refreshLockTTL so a crashed
// holder cannot wedge other callers forever.
func (l *RedisRefreshLocker) Acquire(ctx context.Context, key string) (func(), error) {
	for {
		ok, err := l.rdb.SetNX(ctx, key, "1", refreshLockTTL).Result()
		if err != nil {
			return nil, err
		}
		if ok {
			var once sync.Once
			return func() {
				once.Do(func() {
					// Best-effort delete; ignore errors (TTL covers the failure mode).
					_ = l.rdb.Del(context.Background(), key).Err()
				})
			}, nil
		}
		// Lock held by someone else — wait and retry, respecting ctx cancellation.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(l.pollInterval):
		}
	}
}

// compile-time guard: RedisRefreshLocker satisfies refreshLocker.
var _ refreshLocker = (*RedisRefreshLocker)(nil)

// --- 飞书 v2 OAuth token refresher (HTTP) ------------------------------------

// httpTokenRefresher refreshes a user_access_token via the 飞书 v2 token endpoint
// (grant_type=refresh_token). It is a plain *http.Client (NOT aiservice): 飞书 is
// an external business API, not an LLM gateway. It reuses oauthTokenURL and
// oauthTokenEnvelope defined in provisioner_cli.go (same package).
type httpTokenRefresher struct {
	client *http.Client
}

// NewHTTPTokenRefresher builds the production TokenRefresher.
func NewHTTPTokenRefresher() *httpTokenRefresher {
	return &httpTokenRefresher{client: &http.Client{Timeout: oauthExchangeTimeout}}
}

// Refresh POSTs the refresh_token grant and returns the fresh token fields. On a
// non-zero 飞书 business code or HTTP error it returns ErrLarkCallFailed (wrapped)
// so the caller classifies it as a refresh failure; it never logs the secret or
// the refresh token.
func (r *httpTokenRefresher) Refresh(ctx context.Context, appID, appSecret, refreshToken string) (*oauthTokenResp, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     appID,
		"client_secret": appSecret,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: marshal refresh request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("feishu: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: refresh request: %v", errno.ErrLarkCallFailed, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read refresh response: %v", errno.ErrLarkCallFailed, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: refresh endpoint HTTP %d", errno.ErrLarkCallFailed, resp.StatusCode)
	}

	var env oauthTokenEnvelope
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		return nil, fmt.Errorf("%w: parse refresh response: %v", errno.ErrLarkCallFailed, jerr)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("%w: 飞书 code %d (%s)", errno.ErrLarkCallFailed, env.Code, env.Msg)
	}

	return &env.oauthTokenResp, nil
}

// compile-time guard: httpTokenRefresher satisfies TokenRefresher.
var _ TokenRefresher = (*httpTokenRefresher)(nil)
