package feishu

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// --- test doubles for the client -------------------------------------------

// fakeAccountStore is an in-memory IThirdPartyAccountStore for unit tests.
// It records UpdateTokens calls so the refresh path can be asserted.
type fakeAccountStore struct {
	mu      sync.Mutex
	acc     *model.UserThirdPartyAccount // current row (nil = not connected)
	getErr  error
	updErr  error
	updates int32 // how many times UpdateTokens was invoked
}

func (f *fakeAccountStore) Get(_ context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.acc == nil || f.acc.UserID != userID || f.acc.Provider != provider {
		return nil, gormNotFound
	}
	// return a copy so callers can't mutate stored state by reference
	cp := *f.acc
	return &cp, nil
}

func (f *fakeAccountStore) Upsert(_ context.Context, acc *model.UserThirdPartyAccount) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *acc
	f.acc = &cp
	return nil
}

func (f *fakeAccountStore) Delete(_ context.Context, _ uint, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acc = nil
	return nil
}

func (f *fakeAccountStore) UpdateTokens(_ context.Context, userID uint, provider string, accessEnc, refreshEnc []byte, exp *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	atomic.AddInt32(&f.updates, 1)
	if f.updErr != nil {
		return f.updErr
	}
	if f.acc == nil {
		return gormNotFound
	}
	f.acc.AccessTokenEnc = accessEnc
	f.acc.RefreshTokenEnc = refreshEnc
	f.acc.TokenExpiresAt = exp
	return nil
}

func (f *fakeAccountStore) updateCount() int32 { return atomic.LoadInt32(&f.updates) }

// fakeRefresher is a scripted TokenRefresher.
type fakeRefresher struct {
	resp  *oauthTokenResp
	err   error
	calls int32
	// gate, if non-nil, is closed after the refresher is entered; the call then
	// blocks until release is closed — used to exercise the concurrency lock.
	gate    chan struct{}
	release chan struct{}
}

func (f *fakeRefresher) Refresh(_ context.Context, _, _, _ string) (*oauthTokenResp, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.gate != nil {
		close(f.gate)
		<-f.release
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeRefresher) callCount() int32 { return atomic.LoadInt32(&f.calls) }

// fakeLocker is an in-process refreshLocker that serialises holders of the same
// key (mirrors a Redis blocking lock for single-process tests).
type fakeLocker struct {
	mu   sync.Mutex
	held map[string]*sync.Mutex
}

func newFakeLocker() *fakeLocker { return &fakeLocker{held: map[string]*sync.Mutex{}} }

func (l *fakeLocker) Acquire(_ context.Context, key string) (release func(), err error) {
	l.mu.Lock()
	m, ok := l.held[key]
	if !ok {
		m = &sync.Mutex{}
		l.held[key] = m
	}
	l.mu.Unlock()
	m.Lock()
	return func() { m.Unlock() }, nil
}

// gormNotFound mirrors gorm.ErrRecordNotFound for the fake store without
// importing gorm into the test (the client classifies it via errors.Is).
var gormNotFound = errors.New("record not found")

// --- helpers ----------------------------------------------------------------

func newTestClient(t *testing.T, store *fakeAccountStore, refresher *fakeRefresher, locker refreshLocker) *Client {
	t.Helper()
	cipher, err := crypto.NewCipher(testKey)
	if err != nil {
		t.Fatalf("crypto.NewCipher: %v", err)
	}
	if locker == nil {
		locker = newFakeLocker()
	}
	c, err := NewClient(store, cipher, refresher, locker)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// classify our fake "record not found" as the not-connected sentinel.
	c.isNotFound = func(err error) bool { return errors.Is(err, gormNotFound) }
	return c
}

func mustEnc(t *testing.T, plain string) []byte {
	t.Helper()
	c, err := crypto.NewCipher(testKey)
	if err != nil {
		t.Fatalf("crypto.NewCipher: %v", err)
	}
	blob, err := c.Encrypt([]byte(plain))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return blob
}

func futureTime(d time.Duration) *time.Time { t := time.Now().Add(d); return &t }
func pastTime(d time.Duration) *time.Time   { t := time.Now().Add(-d); return &t }

// --- tests ------------------------------------------------------------------

func TestFor_NotConnected_ReturnsErrLarkNotConnected(t *testing.T) {
	store := &fakeAccountStore{acc: nil}
	c := newTestClient(t, store, &fakeRefresher{}, nil)

	_, err := c.For(context.Background(), 7)
	if !errors.Is(err, errno.ErrLarkNotConnected) {
		t.Fatalf("want ErrLarkNotConnected, got %v", err)
	}
}

func TestFor_ValidToken_NoRefresh(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID:         7,
		Provider:       ProviderLark,
		AppID:          "cli_app",
		AppSecretEnc:   mustEnc(t, "app-secret"),
		AccessTokenEnc: mustEnc(t, "u-token-valid"),
		TokenExpiresAt: futureTime(time.Hour),
	}}
	refresher := &fakeRefresher{}
	c := newTestClient(t, store, refresher, nil)

	lc, err := c.For(context.Background(), 7)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if lc.UserAccessToken != "u-token-valid" {
		t.Fatalf("token: want u-token-valid, got %q", lc.UserAccessToken)
	}
	if lc.API == nil {
		t.Fatal("API client must be non-nil")
	}
	if lc.AppID != "cli_app" {
		t.Fatalf("AppID: want cli_app, got %q", lc.AppID)
	}
	if refresher.callCount() != 0 {
		t.Fatalf("refresh should not be called for a valid token; got %d", refresher.callCount())
	}
}

func TestFor_NilExpiry_TreatedAsValid_NoRefresh(t *testing.T) {
	// 飞书 may omit expires_in → TokenExpiresAt nil. design.md §3: nil must NOT be
	// treated as already-expired.
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID:         7,
		Provider:       ProviderLark,
		AppSecretEnc:   mustEnc(t, "app-secret"),
		AccessTokenEnc: mustEnc(t, "u-token-noexp"),
		TokenExpiresAt: nil,
	}}
	refresher := &fakeRefresher{}
	c := newTestClient(t, store, refresher, nil)

	lc, err := c.For(context.Background(), 7)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if lc.UserAccessToken != "u-token-noexp" {
		t.Fatalf("token mismatch: %q", lc.UserAccessToken)
	}
	if refresher.callCount() != 0 {
		t.Fatalf("nil expiry must not trigger refresh; got %d", refresher.callCount())
	}
}

func TestFor_ExpiredWithRefresh_RefreshesAndUpdates(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID:          7,
		Provider:        ProviderLark,
		AppID:           "cli_app",
		AppSecretEnc:    mustEnc(t, "app-secret"),
		AccessTokenEnc:  mustEnc(t, "old-token"),
		RefreshTokenEnc: mustEnc(t, "refresh-tok"),
		TokenExpiresAt:  pastTime(time.Minute),
	}}
	refresher := &fakeRefresher{resp: &oauthTokenResp{
		AccessToken:  "new-token",
		RefreshToken: "new-refresh",
		ExpiresIn:    7200,
	}}
	c := newTestClient(t, store, refresher, nil)

	lc, err := c.For(context.Background(), 7)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if lc.UserAccessToken != "new-token" {
		t.Fatalf("want refreshed token new-token, got %q", lc.UserAccessToken)
	}
	if refresher.callCount() != 1 {
		t.Fatalf("want exactly 1 refresh, got %d", refresher.callCount())
	}
	if store.updateCount() != 1 {
		t.Fatalf("want exactly 1 UpdateTokens, got %d", store.updateCount())
	}
	// stored access token must be the new one (decryptable round-trip).
	got, derr := c.cipher.Decrypt(store.acc.AccessTokenEnc)
	if derr != nil {
		t.Fatalf("decrypt stored token: %v", derr)
	}
	if string(got) != "new-token" {
		t.Fatalf("stored token: want new-token, got %q", got)
	}
}

func TestFor_ExpiredNoRefreshToken_ReturnsErrLarkReauthRequired(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID:          7,
		Provider:        ProviderLark,
		AccessTokenEnc:  mustEnc(t, "old-token"),
		RefreshTokenEnc: nil, // no refresh token
		TokenExpiresAt:  pastTime(time.Minute),
	}}
	refresher := &fakeRefresher{}
	c := newTestClient(t, store, refresher, nil)

	_, err := c.For(context.Background(), 7)
	if !errors.Is(err, errno.ErrLarkReauthRequired) {
		t.Fatalf("want ErrLarkReauthRequired, got %v", err)
	}
	if refresher.callCount() != 0 {
		t.Fatalf("refresher must not be called without a refresh token; got %d", refresher.callCount())
	}
}

func TestFor_RefreshFails_ReturnsErrLarkReauthRequired(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID:          7,
		Provider:        ProviderLark,
		AppSecretEnc:    mustEnc(t, "app-secret"),
		AccessTokenEnc:  mustEnc(t, "old-token"),
		RefreshTokenEnc: mustEnc(t, "refresh-tok"),
		TokenExpiresAt:  pastTime(time.Minute),
	}}
	refresher := &fakeRefresher{err: errors.New("飞书 refresh rejected")}
	c := newTestClient(t, store, refresher, nil)

	_, err := c.For(context.Background(), 7)
	if !errors.Is(err, errno.ErrLarkReauthRequired) {
		t.Fatalf("want ErrLarkReauthRequired, got %v", err)
	}
}

// TestFor_ConcurrentExpired_RefreshesOnce verifies the per-user lock: when N
// goroutines all see an expired token, only ONE refresh occurs; the rest pick up
// the freshly-written token (double-check after acquiring the lock).
func TestFor_ConcurrentExpired_RefreshesOnce(t *testing.T) {
	store := &fakeAccountStore{acc: &model.UserThirdPartyAccount{
		UserID:          7,
		Provider:        ProviderLark,
		AppSecretEnc:    mustEnc(t, "app-secret"),
		AccessTokenEnc:  mustEnc(t, "old-token"),
		RefreshTokenEnc: mustEnc(t, "refresh-tok"),
		TokenExpiresAt:  pastTime(time.Minute),
	}}
	refresher := &fakeRefresher{resp: &oauthTokenResp{
		AccessToken:  "new-token",
		RefreshToken: "new-refresh",
		ExpiresIn:    7200,
	}}
	c := newTestClient(t, store, refresher, newFakeLocker())

	const n = 8
	var wg sync.WaitGroup
	tokens := make([]string, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			lc, err := c.For(context.Background(), 7)
			if err != nil {
				errs[i] = err
				return
			}
			tokens[i] = lc.UserAccessToken
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := refresher.callCount(); got != 1 {
		t.Fatalf("concurrent expired refresh: want exactly 1 refresh, got %d", got)
	}
	for i, tok := range tokens {
		if tok != "new-token" {
			t.Fatalf("goroutine %d got token %q, want new-token", i, tok)
		}
	}
}

func TestRefreshLockKey_PerUser(t *testing.T) {
	// Lock key must include the userID so refreshes never cross users.
	if got := refreshLockKey(7); got != "feishu:refresh:7" {
		t.Fatalf("refreshLockKey(7) = %q, want feishu:refresh:7", got)
	}
	if got := refreshLockKey(42); got != "feishu:refresh:42" {
		t.Fatalf("refreshLockKey(42) = %q, want feishu:refresh:42", got)
	}
	if refreshLockKey(7) == refreshLockKey(8) {
		t.Fatal("lock keys for different users must differ")
	}
}

func TestNewClient_NilDeps(t *testing.T) {
	cipher, _ := crypto.NewCipher(testKey)
	store := &fakeAccountStore{}
	locker := newFakeLocker()
	refresher := &fakeRefresher{}

	if _, err := NewClient(nil, cipher, refresher, locker); err == nil {
		t.Fatal("nil store must error")
	}
	if _, err := NewClient(store, nil, refresher, locker); err == nil {
		t.Fatal("nil cipher must error")
	}
	if _, err := NewClient(store, cipher, nil, locker); err == nil {
		t.Fatal("nil refresher must error")
	}
	if _, err := NewClient(store, cipher, refresher, nil); err == nil {
		t.Fatal("nil locker must error")
	}
}
