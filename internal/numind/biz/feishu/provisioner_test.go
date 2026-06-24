package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"

	"numind-server/internal/pkg/crypto"
)

// --- test doubles -----------------------------------------------------------

// testKey is the deterministic base64 AES-256 key used across feishu package
// tests (32 bytes "0123456789abcdef0123456789abcdef").
const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

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

// fakeCLIRunner implements cliRunner (config-init seam + authRunner device-code
// seam) with scripted behaviour, no os/exec.
//
// The handle-based seam is bridged with a scratch-path session ref: startScript
// returns the page URL + the scratch path that becomes handle.home; pollScript is
// keyed by that same path (resolveHandle just wraps the ref into a handle).
type fakeCLIRunner struct {
	// startScript returns the page URL + scratch path (or error) for StartAppCreate.
	startScript func(userID uint) (pageURL, scratch string, err error)
	// pollScript returns the credential snapshot for a scratch path (handle.home).
	pollScript func(home string) (appID, appSecret string, done bool, err error)

	// device-code authRunner scripts (default to benign no-ops).
	startAuthScript    func(userID uint) (string, error)
	completeAuthScript func(userID uint) error
	pendingScript      func(userID uint) bool
	statusScript       func(userID uint) (bool, error)
}

func newFakeCLIRunner() *fakeCLIRunner {
	return &fakeCLIRunner{
		startAuthScript:    func(uint) (string, error) { return "https://verify", nil },
		completeAuthScript: func(uint) error { return nil },
		pendingScript:      func(uint) bool { return false },
		statusScript:       func(uint) (bool, error) { return false, nil },
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

// resolveHandle mirrors the production seam: wrap a scratch-path ref into a
// path-only handle (the fake keeps no in-memory sessions).
func (f *fakeCLIRunner) resolveHandle(sessionRef string) *AppCreateHandle {
	if sessionRef == "" {
		return nil
	}
	return &AppCreateHandle{home: sessionRef}
}

// sessionRefForUser mirrors the production deterministic per-user scratch ref.
func (f *fakeCLIRunner) sessionRefForUser(userID uint) string {
	if userID == 0 {
		return ""
	}
	return "u" + itoa(userID)
}

// --- authRunner seam (device-code) ------------------------------------------

func (f *fakeCLIRunner) StartAuthLogin(_ context.Context, userID uint) (string, error) {
	return f.startAuthScript(userID)
}
func (f *fakeCLIRunner) CompleteAuthLogin(_ context.Context, userID uint) error {
	return f.completeAuthScript(userID)
}
func (f *fakeCLIRunner) HasPendingDeviceCode(userID uint) bool { return f.pendingScript(userID) }
func (f *fakeCLIRunner) AuthStatus(_ context.Context, userID uint) (bool, error) {
	return f.statusScript(userID)
}

func newTestProvisioner(t *testing.T, cli cliRunner) *Provisioner {
	t.Helper()
	p, err := NewProvisioner(newTestCipher(t), cli)
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
	p := newTestProvisioner(t, cli)

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
	p := newTestProvisioner(t, cli)

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
	p := newTestProvisioner(t, cli)

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
	p, err := NewProvisioner(cipher, cli)
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
	p := newTestProvisioner(t, cli)

	if _, _, _, err := p.PollCredentials(context.Background(), "sess-1"); err == nil {
		t.Fatal("done-but-empty-secret must be an error")
	}
}

func TestPollCredentials_PropagatesError(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.pollScript = func(ref string) (string, string, bool, error) {
		return "", "", false, errors.New("read config failed")
	}
	p := newTestProvisioner(t, cli)

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
	p, err := NewProvisioner(cipher, cli)
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
	p := newTestProvisioner(t, cli)

	appID, secEnc, done, err := p.PollCredentialsForUser(context.Background(), 9)
	if err != nil {
		t.Fatalf("PollCredentialsForUser: %v", err)
	}
	if done || appID != "" || secEnc != nil {
		t.Fatalf("not-ready must yield empty creds, got done=%t appID=%q secEnc=%v", done, appID, secEnc)
	}
}

func TestPollCredentialsForUser_ZeroUserErrors(t *testing.T) {
	p := newTestProvisioner(t, newFakeCLIRunner())
	if _, _, _, err := p.PollCredentialsForUser(context.Background(), 0); err == nil {
		t.Fatal("userID 0 must error")
	}
}

// --- device-code authorization (Authorizer) ---------------------------------

func TestStartAuthorize_ReturnsVerificationURL(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.startAuthScript = func(userID uint) (string, error) {
		if userID != 11 {
			t.Fatalf("unexpected userID %d", userID)
		}
		return "https://open.feishu.cn/device?user_code=ABCD", nil
	}
	p := newTestProvisioner(t, cli)

	url, err := p.StartAuthorize(context.Background(), 11)
	if err != nil {
		t.Fatalf("StartAuthorize: %v", err)
	}
	if !strings.HasPrefix(url, "https://open.feishu.cn/") {
		t.Fatalf("verification URL mismatch: %q", url)
	}
}

func TestStartAuthorize_ZeroUserErrors(t *testing.T) {
	p := newTestProvisioner(t, newFakeCLIRunner())
	if _, err := p.StartAuthorize(context.Background(), 0); err == nil {
		t.Fatal("userID 0 must error")
	}
}

func TestStartAuthorize_PropagatesError(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.startAuthScript = func(uint) (string, error) { return "", errors.New("auth login boom") }
	p := newTestProvisioner(t, cli)
	if _, err := p.StartAuthorize(context.Background(), 3); err == nil {
		t.Fatal("StartAuthorize should surface CLI error")
	}
}

func TestCompleteAuthorize_DelegatesToCLI(t *testing.T) {
	cli := newFakeCLIRunner()
	called := false
	cli.completeAuthScript = func(userID uint) error {
		called = true
		if userID != 12 {
			t.Fatalf("unexpected userID %d", userID)
		}
		return nil
	}
	p := newTestProvisioner(t, cli)
	if err := p.CompleteAuthorize(context.Background(), 12); err != nil {
		t.Fatalf("CompleteAuthorize: %v", err)
	}
	if !called {
		t.Fatal("CompleteAuthorize must delegate to the CLI runner")
	}
}

func TestCompleteAuthorize_ZeroUserErrors(t *testing.T) {
	p := newTestProvisioner(t, newFakeCLIRunner())
	if err := p.CompleteAuthorize(context.Background(), 0); err == nil {
		t.Fatal("userID 0 must error")
	}
}

func TestHasPendingAuthorize_Delegates(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.pendingScript = func(userID uint) bool { return userID == 5 }
	p := newTestProvisioner(t, cli)
	if !p.HasPendingAuthorize(5) {
		t.Fatal("expected pending for user 5")
	}
	if p.HasPendingAuthorize(6) {
		t.Fatal("expected no pending for user 6")
	}
	if p.HasPendingAuthorize(0) {
		t.Fatal("userID 0 must report no pending")
	}
}

func TestIsAuthorized_Delegates(t *testing.T) {
	cli := newFakeCLIRunner()
	cli.statusScript = func(userID uint) (bool, error) { return userID == 8, nil }
	p := newTestProvisioner(t, cli)
	ok, err := p.IsAuthorized(context.Background(), 8)
	if err != nil || !ok {
		t.Fatalf("expected authorized for user 8, got ok=%t err=%v", ok, err)
	}
	if _, err := p.IsAuthorized(context.Background(), 0); err == nil {
		t.Fatal("userID 0 must error")
	}
}

// --- constructor guards -----------------------------------------------------

func TestNewProvisioner_NilDeps(t *testing.T) {
	cipher := newTestCipher(t)
	cli := newFakeCLIRunner()

	if _, err := NewProvisioner(nil, cli); err == nil {
		t.Fatal("nil cipher must error")
	}
	if _, err := NewProvisioner(cipher, nil); err == nil {
		t.Fatal("nil cli runner must error")
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
