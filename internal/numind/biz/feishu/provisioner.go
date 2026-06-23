// Package feishu — provisioner.go implements per-user 飞书自建应用 provisioning
// (device-code flow via lark-cli) + the OAuth authorization-code → token
// exchange. This is the feishu-integration T6 building block.
//
// Architecture (design.md §5, spike-bootstrap.md):
//
//   - There is NO public REST API to create a 飞书 self-built app. The supported
//     programmatic path is lark-cli's device-code flow ("Claude Code style"):
//     `lark-cli config init --new` blocks, prints an open.feishu.cn/page/cli URL,
//     the user opens it in a browser to create+configure the app, and lark-cli
//     polls until done, persisting the appId/appSecret in an isolated config home.
//   - 有数's server runs lark-cli once per user, each pinned to its own HOME so the
//     credentials never collide across users (lark-cli reads ~/.lark-cli/).
//   - OAuth token exchange (`/open-apis/authen/v2/oauth/token`) is a plain REST
//     call — done natively here, NOT through lark-cli.
//
// Security: app_secret / access_token / refresh_token are AES-256-GCM encrypted
// at THIS boundary (via the injected crypto.Cipher) before being returned, so
// callers (T7 service) get ciphertext to hand straight to the store. Plaintext
// secrets are never returned, never logged. NOT routed through aiservice (飞书 is
// an external business API, not an LLM gateway).
//
// Testability: the lark-cli invocation (cliRunner) and the OAuth HTTP call
// (tokenExchanger) are interfaces, so the orchestration is fully unit-tested with
// in-memory fakes (no live 飞书, no os/exec). The real implementations live in
// provisioner_cli.go and require a dev environment + browser to exercise
// end-to-end (see blockers in the feature notes).
package feishu

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/pkg/errno"
)

// Encrypter is the subset of crypto.Cipher the provisioner needs to seal
// credentials at the store boundary. Declared as an interface so tests can
// substitute a fake, though production always passes *crypto.Cipher.
type Encrypter interface {
	Encrypt(plain []byte) ([]byte, error)
}

// cliRunner abstracts the lark-cli device-code interaction so the provisioner
// orchestration can be unit-tested without spawning a real process.
//
//   - StartInit kicks off `lark-cli config init --new` for userID in an isolated
//     config home, returning the device-code page URL the user must open and an
//     opaque session ref (used by ReadCredentials to find the same config home /
//     background process).
//   - ReadCredentials checks whether the user has finished the browser step. When
//     done, it returns the appId + the (plaintext) appSecret materialized from
//     lark-cli; otherwise done=false with empty creds.
//   - ReadAppSecret resolves the plaintext app_secret for an already-provisioned
//     appID (needed later as client_secret for the OAuth token exchange). It is
//     separate from ReadCredentials because by exchange time we key off the appID,
//     not the original session ref.
type cliRunner interface {
	StartInit(ctx context.Context, userID uint) (pageURL, sessionRef string, err error)
	ReadCredentials(ctx context.Context, sessionRef string) (appID, appSecret string, done bool, err error)
	ReadAppSecret(ctx context.Context, appID string) (appSecret string, err error)
}

// oauthTokenResp is the relevant subset of the 飞书 v2 OAuth token response
// (`POST /open-apis/authen/v2/oauth/token`). 飞书 nests these under "data" on
// success; the tokenExchanger implementation unwraps that envelope.
type oauthTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds; 0 = unknown
	Scope        string `json:"scope"`
}

// tokenExchanger performs the authorization-code → user_access_token exchange.
// Abstracted so the HTTP call is mocked in tests.
type tokenExchanger interface {
	Exchange(ctx context.Context, appID, appSecret, code string) (*oauthTokenResp, error)
}

// Provisioner orchestrates app provisioning + OAuth token exchange. Safe for
// concurrent use: it holds only immutable dependencies; per-user state lives in
// the cliRunner (isolated config homes keyed by session ref).
type Provisioner struct {
	cipher Encrypter
	cli    cliRunner
	ex     tokenExchanger
}

// NewProvisioner wires the provisioner. All dependencies are required — a nil
// cipher would mean storing plaintext credentials, so it fails fast rather than
// silently degrading security.
func NewProvisioner(cipher Encrypter, cli cliRunner, ex tokenExchanger) (*Provisioner, error) {
	if cipher == nil {
		return nil, errors.New("feishu: nil cipher for provisioner")
	}
	if cli == nil {
		return nil, errors.New("feishu: nil cli runner for provisioner")
	}
	if ex == nil {
		return nil, errors.New("feishu: nil token exchanger for provisioner")
	}
	return &Provisioner{cipher: cipher, cli: cli, ex: ex}, nil
}

// StartProvision begins provisioning a 飞书 app for userID. It returns the
// device-code page URL (shown to the user so they open it in a browser to create
// the app) and an opaque session ref the caller passes back to PollCredentials.
func (p *Provisioner) StartProvision(ctx context.Context, userID uint) (pageURL, sessionRef string, err error) {
	pageURL, sessionRef, err = p.cli.StartInit(ctx, userID)
	if err != nil {
		return "", "", fmt.Errorf("feishu: start provision (user %d): %w", userID, err)
	}
	if pageURL == "" || sessionRef == "" {
		return "", "", fmt.Errorf("feishu: start provision (user %d): empty page URL or session ref", userID)
	}
	return pageURL, sessionRef, nil
}

// PollCredentials checks whether provisioning finished for the given session.
// When not yet done it returns done=false with empty creds (caller keeps
// polling). When done it returns the appID plus the AES-256-GCM-encrypted
// app_secret. A "done but blank secret" result is treated as a failure so an
// empty/garbage credential is never persisted.
func (p *Provisioner) PollCredentials(ctx context.Context, sessionRef string) (appID string, appSecretEnc []byte, done bool, err error) {
	appID, appSecret, done, err := p.cli.ReadCredentials(ctx, sessionRef)
	if err != nil {
		return "", nil, false, fmt.Errorf("feishu: poll credentials: %w", err)
	}
	if !done {
		return "", nil, false, nil
	}
	if appID == "" || appSecret == "" {
		return "", nil, false, fmt.Errorf("feishu: provisioning reported done but credentials are incomplete (appID empty=%t, secret empty=%t)", appID == "", appSecret == "")
	}
	secEnc, err := p.cipher.Encrypt([]byte(appSecret))
	if err != nil {
		return "", nil, false, fmt.Errorf("feishu: encrypt app secret: %w", err)
	}
	return appID, secEnc, true, nil
}

// ExchangeCode exchanges an OAuth authorization code for a user_access_token (and
// refresh_token, if 飞书 returns one), encrypting both before returning.
//
//   - access is always non-nil on success (an empty access_token from upstream is
//     an error — nothing usable to store).
//   - refresh is nil when 飞书 omits a refresh_token (so the store writes NULL,
//     not an encrypted empty string).
//   - exp is the absolute expiry derived from expires_in; nil when expires_in is
//     absent/zero (unknown — caller must not treat nil as "already expired").
//
// The app_secret needed for the exchange is read from the user's lark-cli config
// home via the cliRunner (so we never require the caller to pass the plaintext
// secret around). appID identifies which config home to read.
func (p *Provisioner) ExchangeCode(ctx context.Context, appID, code string) (access, refresh []byte, exp *time.Time, scopes string, err error) {
	if appID == "" || code == "" {
		return nil, nil, nil, "", fmt.Errorf("%w: missing app_id or code", errno.ErrLarkCallFailed)
	}

	appSecret, err := p.appSecretFor(ctx, appID)
	if err != nil {
		return nil, nil, nil, "", err
	}

	resp, err := p.ex.Exchange(ctx, appID, appSecret, code)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("feishu: exchange code: %w", err)
	}
	if resp == nil || resp.AccessToken == "" {
		return nil, nil, nil, "", fmt.Errorf("%w: empty access_token in token response", errno.ErrLarkCallFailed)
	}

	access, err = p.cipher.Encrypt([]byte(resp.AccessToken))
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("feishu: encrypt access token: %w", err)
	}

	if resp.RefreshToken != "" {
		refresh, err = p.cipher.Encrypt([]byte(resp.RefreshToken))
		if err != nil {
			return nil, nil, nil, "", fmt.Errorf("feishu: encrypt refresh token: %w", err)
		}
	}

	if resp.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
		exp = &t
	}

	return access, refresh, exp, resp.Scope, nil
}

// appSecretFor reads the plaintext app_secret for appID from the user's lark-cli
// config home. The OAuth token exchange needs it as client_secret. We resolve it
// through the cliRunner (which knows how to materialize the secret lark-cli keeps
// encrypted at rest) rather than asking callers to carry it.
func (p *Provisioner) appSecretFor(ctx context.Context, appID string) (string, error) {
	secret, err := p.cli.ReadAppSecret(ctx, appID)
	if err != nil {
		return "", fmt.Errorf("feishu: read app secret for exchange: %w", err)
	}
	if secret == "" {
		return "", fmt.Errorf("%w: app secret unavailable for app %s", errno.ErrLarkCallFailed, appID)
	}
	return secret, nil
}

// --- pure parsers (shared by the real cliRunner, unit-tested in isolation) ---

// deviceCodeURLMarker is the public 飞书 device-code page the lark-cli prints.
// We match on this exact host+path so we don't accidentally grab some other URL
// from the CLI output.
const deviceCodeURLMarker = "https://open.feishu.cn/page/cli"

// parseDeviceCodeURL extracts the device-code page URL from lark-cli's stdout.
// Real output looks like:
//
//	打开以下链接配置应用:
//	  https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli
//	等待配置应用...
func parseDeviceCodeURL(cliOutput string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(cliOutput))
	// Allow long lines (the URL + flags can exceed the default 64KB only in
	// pathological cases, but be safe).
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if idx := strings.Index(line, deviceCodeURLMarker); idx >= 0 {
			// Take from the marker to the end of the (already trimmed) line; the
			// query string is part of the URL.
			return line[idx:], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("feishu: scan cli output: %w", err)
	}
	return "", errors.New("feishu: device-code page URL not found in lark-cli output")
}

// parseEnvCredentials parses the FEISHU_APP_ID / FEISHU_APP_SECRET pair from the
// dotenv-style file lark-cli materializes (e.g. `apps +init` / `env-pull` writes
// a .env.local with these keys). Both keys are required; values may be optionally
// single- or double-quoted.
func parseEnvCredentials(envFileContent string) (appID, appSecret string, err error) {
	sc := bufio.NewScanner(strings.NewReader(envFileContent))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = stripQuotes(strings.TrimSpace(val))
		switch key {
		case "FEISHU_APP_ID":
			appID = val
		case "FEISHU_APP_SECRET":
			appSecret = val
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", fmt.Errorf("feishu: scan env file: %w", err)
	}
	if appID == "" {
		return "", "", errors.New("feishu: FEISHU_APP_ID not found in credential file")
	}
	if appSecret == "" {
		return "", "", errors.New("feishu: FEISHU_APP_SECRET not found in credential file")
	}
	return appID, appSecret, nil
}

// stripQuotes removes a single matching pair of surrounding single or double
// quotes, if present.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
