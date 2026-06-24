package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"numind-server/internal/pkg/crypto"
)

// --- test doubles -----------------------------------------------------------

// newTestCipher builds a real AES-256-GCM cipher from the deterministic test key
// so encrypt/decrypt round-trips can be asserted in provisioner tests.
func newTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.NewCipher(testKey)
	if err != nil {
		t.Fatalf("crypto.NewCipher: %v", err)
	}
	return c
}

// fakeCLIRunner implements cliRunner with scripted behaviour, no os/exec.
//
// The handle-based seam is bridged with a scratch-path session ref: startScript
// returns the page URL + the scratch path that becomes handle.home; pollScript is
// keyed by that same path (resolveHandle just wraps the ref into a handle).
type fakeCLIRunner struct {
	// startScript returns the page URL + scratch path (or error) for StartAppCreate.
	startScript func(userID uint) (pageURL, scratch string, err error)
	// pollScript returns the credential snapshot for a scratch path (handle.home).
	pollScript func(home string) (appID, appSecret string, done bool, err error)
	// secretScript returns the plaintext app_secret for an appID (exchange path).
	secretScript func(appID string) (string, error)
}

func newFakeCLIRunner() *fakeCLIRunner {
	return &fakeCLIRunner{
		// default: exchange-path secret lookup succeeds (most tests don't care).
		secretScript: func(appID string) (string, error) { return "default-secret", nil },
	}
}

func (f *fakeCLIRunner) StartAppCreate(_ context.Context, userID uint) (pageURL string, handle *AppCreateHandle, err error) {
	url, scratch, serr := f.startScript(userID)
	if serr != nil {
		return "", nil, serr
	}
	return url, &AppCreateHandle{home: scratch}, nil
}

func (f *fakeCLIRunner) PollAppCreated(_ context.Context, handle *AppCreateHandle) (appID, appSecret string, done bool, err error) {
	home := ""
	if handle != nil {
		home = handle.home
	}
	return f.pollScript(home)
}

func (f *fakeCLIRunner) ReadAppSecret(_ context.Context, appID string) (string, error) {
	return f.secretScript(appID)
}

// resolveHandle mirrors the production seam: wrap a scratch-path ref into a
// path-only handle (the fake keeps no in-memory sessions).
func (f *fakeCLIRunner) resolveHandle(sessionRef string) *AppCreateHandle {
	if sessionRef == "" {
		return nil
	}
	return &AppCreateHandle{home: sessionRef}
}

// sessionRefForUser mirrors the production deterministic per-user scratch ref.
// The fake keys pollScript by this ref, so PollCredentialsForUser resolves the
// same snapshot a path-based PollCredentials would.
func (f *fakeCLIRunner) sessionRefForUser(userID uint) string {
	if userID == 0 {
		return ""
	}
	return "u" + itoa(userID)
}

// fakeExchanger implements tokenExchanger with a scripted token response.
type fakeExchanger struct {
	resp      *oauthTokenResp
	err       error
	gotApp    string
	gotSecret string
	gotCode   string
}

func (f *fakeExchanger) Exchange(_ context.Context, appID, appSecret, code string) (*oauthTokenResp, error) {
	f.gotApp = appID
	f.gotSecret = appSecret
	f.gotCode = code
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func newTestProvisioner(t *testing.T, cli cliRunner, ex tokenExchanger) *Provisioner {
	t.Helper()
	p, err := NewProvisioner(newTestCipher(t), cli, ex)
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}
	return p
}

// --- StartProvision ---------------------------------------------------------

func TestStartProvision_ReturnsPageURLAndRef(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.startScript = func(userID uint) (string, string, error) {
		if userID != 7 {
			t.Fatalf("unexpected userID %d", userID)
		}
		return "https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli", "sess-7", nil
	}
	p := newTestProvisioner(t, cli, &fakeExchanger{})

	pageURL, ref, err := p.StartProvision(context.Background(), 7)
	if err != nil {
		t.Fatalf("StartProvision: %v", err)
	}
	if !strings.HasPrefix(pageURL, "https://open.feishu.cn/page/cli") {
		t.Fatalf("page URL mismatch: %q", pageURL)
	}
	if ref == "" {
		t.Fatal("session ref must be non-empty")
	}
}

func TestStartProvision_PropagatesError(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.startScript = func(uint) (string, string, error) {
		return "", "", errors.New("lark-cli not found")
	}
	p := newTestProvisioner(t, cli, &fakeExchanger{})

	if _, _, err := p.StartProvision(context.Background(), 1); err == nil {
		t.Fatal("StartProvision should surface CLI error")
	}
}

// --- PollCredentials --------------------------------------------------------

func TestPollCredentials_NotReady(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.pollScript = func(ref string) (string, string, bool, error) {
		return "", "", false, nil
	}
	p := newTestProvisioner(t, cli, &fakeExchanger{})

	appID, secEnc, done, err := p.PollCredentials(context.Background(), "sess-x")
	if err != nil {
		t.Fatalf("PollCredentials: %v", err)
	}
	if done {
		t.Fatal("should not be done yet")
	}
	if appID != "" || secEnc != nil {
		t.Fatalf("not-ready must yield empty creds, got appID=%q secEnc=%v", appID, secEnc)
	}
}

func TestPollCredentials_ReadyEncryptsSecret(t *testing.T) {
	cipher := newTestCipher(t)
	cli := newFakeCLIRunner()
	cli.pollScript = func(ref string) (string, string, bool, error) {
		return "cli_abc123", "supersecret", true, nil
	}
	p, err := NewProvisioner(cipher, cli, &fakeExchanger{})
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}

	appID, secEnc, done, err := p.PollCredentials(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("PollCredentials: %v", err)
	}
	if !done {
		t.Fatal("should be done")
	}
	if appID != "cli_abc123" {
		t.Fatalf("appID mismatch: %q", appID)
	}
	// The returned secret must be ciphertext, never plaintext.
	if string(secEnc) == "supersecret" {
		t.Fatal("app secret must be encrypted, not plaintext")
	}
	plain, err := cipher.Decrypt(secEnc)
	if err != nil {
		t.Fatalf("decrypt app secret: %v", err)
	}
	if string(plain) != "supersecret" {
		t.Fatalf("decrypted secret mismatch: %q", plain)
	}
}

func TestPollCredentials_DoneButEmptySecretIsError(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.pollScript = func(ref string) (string, string, bool, error) {
		// CLI reports done but yields no usable credentials → treat as failure,
		// never persist an empty/blank secret.
		return "cli_abc", "", true, nil
	}
	p := newTestProvisioner(t, cli, &fakeExchanger{})

	if _, _, _, err := p.PollCredentials(context.Background(), "sess-1"); err == nil {
		t.Fatal("done-but-empty-secret must be an error")
	}
}

func TestPollCredentials_PropagatesError(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.pollScript = func(ref string) (string, string, bool, error) {
		return "", "", false, errors.New("read config failed")
	}
	p := newTestProvisioner(t, cli, &fakeExchanger{})

	if _, _, _, err := p.PollCredentials(context.Background(), "sess-1"); err == nil {
		t.Fatal("PollCredentials should surface CLI error")
	}
}

// --- PollCredentialsForUser (agent-driven poll-by-user) ---------------------

func TestPollCredentialsForUser_DerivesRefAndEncrypts(t *testing.T) {
	cipher := newTestCipher(t)
	cli := newFakeCLIRunner()
	// pollScript is keyed by the ref; assert it received the per-user deterministic
	// ref (so the tool need not carry the sessionRef across a yield).
	cli.pollScript = func(ref string) (string, string, bool, error) {
		if ref != "u42" {
			t.Fatalf("PollCredentialsForUser must derive ref from userID, got %q", ref)
		}
		return "cli_u42", "user42-secret", true, nil
	}
	p, err := NewProvisioner(cipher, cli, &fakeExchanger{})
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}

	appID, secEnc, done, err := p.PollCredentialsForUser(context.Background(), 42)
	if err != nil {
		t.Fatalf("PollCredentialsForUser: %v", err)
	}
	if !done || appID != "cli_u42" {
		t.Fatalf("expected done with appID cli_u42, got done=%t appID=%q", done, appID)
	}
	if string(secEnc) == "user42-secret" {
		t.Fatal("secret must be encrypted, not plaintext")
	}
	plain, err := cipher.Decrypt(secEnc)
	if err != nil || string(plain) != "user42-secret" {
		t.Fatalf("decrypt mismatch: %q err=%v", plain, err)
	}
}

func TestPollCredentialsForUser_NotReady(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.pollScript = func(string) (string, string, bool, error) { return "", "", false, nil }
	p := newTestProvisioner(t, cli, &fakeExchanger{})

	appID, secEnc, done, err := p.PollCredentialsForUser(context.Background(), 9)
	if err != nil {
		t.Fatalf("PollCredentialsForUser: %v", err)
	}
	if done || appID != "" || secEnc != nil {
		t.Fatalf("not-ready must yield empty creds, got done=%t appID=%q secEnc=%v", done, appID, secEnc)
	}
}

func TestPollCredentialsForUser_ZeroUserErrors(t *testing.T) {
	p := newTestProvisioner(t, newFakeCLIRunner(), &fakeExchanger{})
	if _, _, _, err := p.PollCredentialsForUser(context.Background(), 0); err == nil {
		t.Fatal("userID 0 must error")
	}
}

// --- ExchangeCode -----------------------------------------------------------

func TestExchangeCode_EncryptsTokens(t *testing.T) {
	cipher := newTestCipher(t)
	ex := &fakeExchanger{resp: &oauthTokenResp{
		AccessToken:  "u-at-xyz",
		RefreshToken: "u-rt-abc",
		ExpiresIn:    7200,
		Scope:        "docx:document im:message",
	}}
	p, err := NewProvisioner(cipher, newFakeCLIRunner(), ex)
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}

	before := time.Now()
	access, refresh, exp, scopes, err := p.ExchangeCode(context.Background(), "cli_abc", "the-code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if ex.gotApp != "cli_abc" || ex.gotCode != "the-code" {
		t.Fatalf("exchanger received wrong args: app=%q code=%q", ex.gotApp, ex.gotCode)
	}
	if scopes != "docx:document im:message" {
		t.Fatalf("scopes mismatch: %q", scopes)
	}
	// Tokens must be ciphertext.
	if string(access) == "u-at-xyz" || string(refresh) == "u-rt-abc" {
		t.Fatal("tokens must be encrypted, not plaintext")
	}
	plainA, err := cipher.Decrypt(access)
	if err != nil {
		t.Fatalf("decrypt access: %v", err)
	}
	if string(plainA) != "u-at-xyz" {
		t.Fatalf("access mismatch: %q", plainA)
	}
	plainR, err := cipher.Decrypt(refresh)
	if err != nil {
		t.Fatalf("decrypt refresh: %v", err)
	}
	if string(plainR) != "u-rt-abc" {
		t.Fatalf("refresh mismatch: %q", plainR)
	}
	// exp ~= now + 7200s.
	if exp == nil {
		t.Fatal("exp must be set when expires_in > 0")
	}
	wantMin := before.Add(7100 * time.Second)
	wantMax := before.Add(7300 * time.Second)
	if exp.Before(wantMin) || exp.After(wantMax) {
		t.Fatalf("exp out of expected window: %v (want ~%v)", exp, before.Add(7200*time.Second))
	}
}

func TestExchangeCode_NoRefreshToken(t *testing.T) {
	cipher := newTestCipher(t)
	ex := &fakeExchanger{resp: &oauthTokenResp{
		AccessToken: "u-at-only",
		ExpiresIn:   3600,
		Scope:       "docx:document",
	}}
	p, err := NewProvisioner(cipher, newFakeCLIRunner(), ex)
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}

	access, refresh, exp, _, err := p.ExchangeCode(context.Background(), "cli_abc", "code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if access == nil {
		t.Fatal("access token must be present")
	}
	// Feishu may omit refresh_token → refresh ciphertext must be nil (NOT an
	// encrypted empty string), so the store writes NULL.
	if refresh != nil {
		t.Fatalf("refresh must be nil when none returned, got %v", refresh)
	}
	if exp == nil {
		t.Fatal("exp must be set")
	}
}

func TestExchangeCode_NoExpiresIn(t *testing.T) {
	cipher := newTestCipher(t)
	ex := &fakeExchanger{resp: &oauthTokenResp{
		AccessToken: "u-at",
		Scope:       "docx:document",
		// ExpiresIn == 0 → unknown expiry.
	}}
	p, err := NewProvisioner(cipher, newFakeCLIRunner(), ex)
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}

	_, _, exp, _, err := p.ExchangeCode(context.Background(), "cli_abc", "code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if exp != nil {
		t.Fatalf("exp must be nil when expires_in absent, got %v", exp)
	}
}

func TestExchangeCode_EmptyAccessTokenIsError(t *testing.T) {
	cipher := newTestCipher(t)
	ex := &fakeExchanger{resp: &oauthTokenResp{
		// no access token → upstream gave us nothing usable
		RefreshToken: "rt",
		ExpiresIn:    3600,
	}}
	p, err := NewProvisioner(cipher, newFakeCLIRunner(), ex)
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}

	if _, _, _, _, err := p.ExchangeCode(context.Background(), "cli_abc", "code"); err == nil {
		t.Fatal("empty access token must be an error")
	}
}

func TestExchangeCode_PropagatesExchangerError(t *testing.T) {
	ex := &fakeExchanger{err: errors.New("feishu 400 invalid code")}
	p := newTestProvisioner(t, newFakeCLIRunner(), ex)

	if _, _, _, _, err := p.ExchangeCode(context.Background(), "cli_abc", "bad"); err == nil {
		t.Fatal("ExchangeCode should surface exchanger error")
	}
}

func TestExchangeCode_ResolvesAndPassesAppSecret(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.secretScript = func(appID string) (string, error) {
		if appID != "cli_abc" {
			t.Fatalf("ReadAppSecret got wrong appID %q", appID)
		}
		return "resolved-secret", nil
	}
	ex := &fakeExchanger{resp: &oauthTokenResp{AccessToken: "at", ExpiresIn: 3600}}
	p := newTestProvisioner(t, cli, ex)

	if _, _, _, _, err := p.ExchangeCode(context.Background(), "cli_abc", "code"); err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if ex.gotSecret != "resolved-secret" {
		t.Fatalf("exchanger should receive the resolved app secret, got %q", ex.gotSecret)
	}
}

func TestExchangeCode_SecretResolutionFailure(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.secretScript = func(appID string) (string, error) { return "", errors.New("config home missing") }
	p := newTestProvisioner(t, cli, &fakeExchanger{resp: &oauthTokenResp{AccessToken: "at"}})

	if _, _, _, _, err := p.ExchangeCode(context.Background(), "cli_abc", "code"); err == nil {
		t.Fatal("ExchangeCode must fail when app secret cannot be resolved")
	}
}

func TestExchangeCode_MissingArgs(t *testing.T) {
	p := newTestProvisioner(t, newFakeCLIRunner(), &fakeExchanger{resp: &oauthTokenResp{AccessToken: "at"}})
	if _, _, _, _, err := p.ExchangeCode(context.Background(), "", "code"); err == nil {
		t.Fatal("empty appID must error")
	}
	if _, _, _, _, err := p.ExchangeCode(context.Background(), "cli_abc", ""); err == nil {
		t.Fatal("empty code must error")
	}
}

// --- constructor guards -----------------------------------------------------

func TestNewProvisioner_NilDeps(t *testing.T) {
	cipher := newTestCipher(t)
	cli := newFakeCLIRunner()
	ex := &fakeExchanger{}

	if _, err := NewProvisioner(nil, cli, ex); err == nil {
		t.Fatal("nil cipher must error")
	}
	if _, err := NewProvisioner(cipher, nil, ex); err == nil {
		t.Fatal("nil cli runner must error")
	}
	if _, err := NewProvisioner(cipher, cli, nil); err == nil {
		t.Fatal("nil exchanger must error")
	}
}

// --- page URL parsing (pure helper, exercised by real CLI output) -----------

func TestParseDeviceCodeURL(t *testing.T) {
	out := "打开以下链接配置应用:\n" +
		"  https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli\n" +
		"等待配置应用...\n"
	got, err := parseDeviceCodeURL(out)
	if err != nil {
		t.Fatalf("parseDeviceCodeURL: %v", err)
	}
	if got != "https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli" {
		t.Fatalf("parsed URL mismatch: %q", got)
	}
}

func TestParseDeviceCodeURL_NotFound(t *testing.T) {
	if _, err := parseDeviceCodeURL("some unrelated output without a link"); err == nil {
		t.Fatal("parseDeviceCodeURL should error when no page URL present")
	}
}

// --- config.json parsing (lark-cli ~/.lark-cli/config.json apps[0]) ---------

func TestParseAppFromConfigJSON(t *testing.T) {
	raw := []byte(`{"apps":[{"appId":"cli_abc123","appSecret":"shhh-secret","brand":"feishu","users":[{"userOpenId":"ou_x","userName":"Z"}]}]}`)
	appID, secret, err := parseAppFromConfigJSON(raw)
	if err != nil {
		t.Fatalf("parseAppFromConfigJSON: %v", err)
	}
	if appID != "cli_abc123" {
		t.Fatalf("appID mismatch: %q", appID)
	}
	if secret != "shhh-secret" {
		t.Fatalf("secret mismatch: %q", secret)
	}
}

func TestParseAppFromConfigJSON_FirstAppWins(t *testing.T) {
	// apps[0] is the source of truth even if more apps are present.
	raw := []byte(`{"apps":[{"appId":"cli_first","appSecret":"s1"},{"appId":"cli_second","appSecret":"s2"}]}`)
	appID, secret, err := parseAppFromConfigJSON(raw)
	if err != nil {
		t.Fatalf("parseAppFromConfigJSON: %v", err)
	}
	if appID != "cli_first" || secret != "s1" {
		t.Fatalf("expected apps[0], got appID=%q secret=%q", appID, secret)
	}
}

func TestParseAppFromConfigJSON_NoApps(t *testing.T) {
	// config init not finished yet → no apps → in-progress signal (error).
	if _, _, err := parseAppFromConfigJSON([]byte(`{"apps":[]}`)); err == nil {
		t.Fatal("empty apps must error (treated as in-progress)")
	}
	if _, _, err := parseAppFromConfigJSON([]byte(`{}`)); err == nil {
		t.Fatal("missing apps must error (treated as in-progress)")
	}
}

func TestParseAppFromConfigJSON_Incomplete(t *testing.T) {
	if _, _, err := parseAppFromConfigJSON([]byte(`{"apps":[{"appId":"cli_only"}]}`)); err == nil {
		t.Fatal("missing appSecret must error")
	}
	if _, _, err := parseAppFromConfigJSON([]byte(`{"apps":[{"appSecret":"only"}]}`)); err == nil {
		t.Fatal("missing appId must error")
	}
}

func TestParseAppFromConfigJSON_Garbage(t *testing.T) {
	if _, _, err := parseAppFromConfigJSON([]byte(`not json`)); err == nil {
		t.Fatal("unparseable config.json must error")
	}
}
