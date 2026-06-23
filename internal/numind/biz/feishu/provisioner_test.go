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
type fakeCLIRunner struct {
	// startScript returns the page URL + session ref (or error) for StartProvision.
	startScript func(userID uint) (pageURL, ref string, err error)
	// pollScript returns the credential snapshot for a session ref.
	pollScript func(ref string) (appID, appSecret string, done bool, err error)
	// secretScript returns the plaintext app_secret for an appID (exchange path).
	secretScript func(appID string) (string, error)
}

func newFakeCLIRunner() *fakeCLIRunner {
	return &fakeCLIRunner{
		// default: exchange-path secret lookup succeeds (most tests don't care).
		secretScript: func(appID string) (string, error) { return "default-secret", nil },
	}
}

func (f *fakeCLIRunner) StartInit(_ context.Context, userID uint) (pageURL, ref string, err error) {
	return f.startScript(userID)
}

func (f *fakeCLIRunner) ReadCredentials(_ context.Context, ref string) (appID, appSecret string, done bool, err error) {
	return f.pollScript(ref)
}

func (f *fakeCLIRunner) ReadAppSecret(_ context.Context, appID string) (string, error) {
	return f.secretScript(appID)
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

// --- credential-file parsing (lark-cli .env.local FEISHU_APP_* output) ------

func TestParseEnvCredentials(t *testing.T) {
	env := "# generated\nFEISHU_APP_ID=cli_abc123\nFEISHU_APP_SECRET=shhh-secret\nFEISHU_DOMAIN=feishu\n"
	appID, secret, err := parseEnvCredentials(env)
	if err != nil {
		t.Fatalf("parseEnvCredentials: %v", err)
	}
	if appID != "cli_abc123" {
		t.Fatalf("appID mismatch: %q", appID)
	}
	if secret != "shhh-secret" {
		t.Fatalf("secret mismatch: %q", secret)
	}
}

func TestParseEnvCredentials_Quoted(t *testing.T) {
	env := "FEISHU_APP_ID=\"cli_q\"\nFEISHU_APP_SECRET='quoted-secret'\n"
	appID, secret, err := parseEnvCredentials(env)
	if err != nil {
		t.Fatalf("parseEnvCredentials: %v", err)
	}
	if appID != "cli_q" || secret != "quoted-secret" {
		t.Fatalf("quote stripping failed: appID=%q secret=%q", appID, secret)
	}
}

func TestParseEnvCredentials_Missing(t *testing.T) {
	if _, _, err := parseEnvCredentials("FEISHU_APP_ID=cli_only\n"); err == nil {
		t.Fatal("missing FEISHU_APP_SECRET must error")
	}
	if _, _, err := parseEnvCredentials("FEISHU_APP_SECRET=only\n"); err == nil {
		t.Fatal("missing FEISHU_APP_ID must error")
	}
}
