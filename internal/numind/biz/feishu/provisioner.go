// Package feishu — provisioner.go implements per-user 飞书自建应用 provisioning
// (config-init flow via lark-cli) + the OAuth authorization-code → token
// exchange. This is the feishu-integration T6 building block.
//
// Architecture (design.md §5, spike-bootstrap.md):
//
//   - There is NO public REST API to create a 飞书 self-built app. The supported
//     programmatic path is lark-cli's `config init --new` flow ("Claude Code
//     style"): it BLOCKS, prints an open.feishu.cn/page/cli URL, the user opens it
//     in a browser to create+configure the app, and lark-cli exits once the app is
//     created — persisting the new app's appId/appSecret into that config home's
//     ~/.lark-cli/config.json ({"apps":[{"appId":"cli_xxx","appSecret":"..."}]}).
//   - 有数's server runs lark-cli once per user, each pinned to its own scratch
//     HOME so the credentials never collide across users (lark-cli reads
//     $HOME/.lark-cli/config.json).
//   - OAuth token exchange (`/open-apis/authen/v2/oauth/token`) is a plain REST
//     call — done natively here, NOT through lark-cli.
//
// Provisioning is a two-call flow at the cliRunner seam (rewritten 2026-06-24):
//
//   - StartAppCreate(ctx, userID): launch `lark-cli config init --new` backgrounded
//     in the user's scratch HOME, scrape the page URL from its stdout, return the
//     URL + an opaque handle (scratch path + process). The process keeps blocking
//     in the background until the user finishes in the browser.
//   - PollAppCreated(ctx, handle): report whether the process finished AND
//     apps[0] is readable from scratch/.lark-cli/config.json; when done, return
//     the appId + plaintext appSecret read straight from config.json.
//
// Security: app_secret / access_token / refresh_token are AES-256-GCM encrypted
// at THIS boundary (via the injected crypto.Cipher) before being returned, so
// callers (T7 service) get ciphertext to hand straight to the store. Plaintext
// secrets are never returned to the LLM, never logged. NOT routed through
// aiservice (飞书 is an external business API, not an LLM gateway).
//
// Testability: the lark-cli invocation (cliRunner) and the OAuth HTTP call
// (tokenExchanger) are interfaces, so the orchestration is fully unit-tested with
// in-memory fakes. The real cliRunner (provisioner_cli.go) drives a real
// `lark-cli config init` process; its config.json reader is exercised with a fake
// lark-cli shell script (provisioner_cli_test.go).
package feishu

import (
	"context"
	"errors"
	"fmt"
	"time"

	"numind-server/internal/pkg/errno"
)

// Encrypter is the subset of crypto.Cipher the provisioner needs to seal
// credentials at the store boundary. Declared as an interface so tests can
// substitute a fake, though production always passes *crypto.Cipher.
type Encrypter interface {
	Encrypt(plain []byte) ([]byte, error)
}

// AppCreateHandle is the opaque token returned by StartAppCreate and passed back
// to PollAppCreated. It carries the scratch config-home path (durable — survives a
// poll on the same instance) plus the in-flight `config init` process tracking.
// The fields are unexported so callers treat it as opaque; only the same-package
// cliRunner reads them.
type AppCreateHandle struct {
	// home is the per-user scratch HOME lark-cli reads/writes ($HOME/.lark-cli/).
	// It doubles as a stable session ref so PollCredentials can re-resolve the
	// handle by path even if the in-memory tracking is lost (restart).
	home string
	// session is the in-process tracking of the backgrounded `config init`. May be
	// nil when the handle was reconstructed from a path alone (post-restart poll).
	session *cliSession
}

// cliRunner abstracts the lark-cli config-init interaction so the provisioner
// orchestration can be unit-tested without spawning a real process.
//
//   - StartAppCreate kicks off `lark-cli config init --new` for userID in an
//     isolated scratch HOME, returning the page URL the user must open and an
//     opaque handle (used by PollAppCreated to observe completion + read creds).
//   - PollAppCreated checks whether the user has finished the browser step (process
//     exited AND apps[0] present in config.json). When done it returns the appId +
//     the (plaintext) appSecret read from config.json; otherwise done=false with
//     empty creds.
//   - ReadAppSecret resolves the plaintext app_secret for an already-provisioned
//     appID (needed later as client_secret for the OAuth token exchange). It is
//     separate from PollAppCreated because by exchange time we key off the appID,
//     not the original handle.
type cliRunner interface {
	StartAppCreate(ctx context.Context, userID uint) (pageURL string, handle *AppCreateHandle, err error)
	PollAppCreated(ctx context.Context, handle *AppCreateHandle) (appID, appSecret string, done bool, err error)
	ReadAppSecret(ctx context.Context, appID string) (appSecret string, err error)

	// resolveHandle reconstructs an AppCreateHandle from a durable session ref
	// (the scratch path) so the string-based Provisioner.PollCredentials contract
	// can bridge to the handle-based PollAppCreated even after the in-memory
	// session map is lost. Returns nil for an empty/unknown ref (PollAppCreated
	// then reports not-done rather than erroring on a stale ref).
	resolveHandle(sessionRef string) *AppCreateHandle
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
// the cliRunner (isolated scratch homes keyed by userID / scratch path).
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

// StartProvision begins provisioning a 飞书 app for userID. It returns the page
// URL (shown to the user so they open it in a browser to create the app) and an
// opaque session ref the caller passes back to PollCredentials. The session ref
// is the durable scratch-home path, so a later poll resolves the same flow even
// across a server restart.
func (p *Provisioner) StartProvision(ctx context.Context, userID uint) (pageURL, sessionRef string, err error) {
	pageURL, handle, err := p.cli.StartAppCreate(ctx, userID)
	if err != nil {
		return "", "", fmt.Errorf("feishu: start provision (user %d): %w", userID, err)
	}
	if pageURL == "" || handle == nil || handle.home == "" {
		return "", "", fmt.Errorf("feishu: start provision (user %d): empty page URL or handle", userID)
	}
	// Expose the durable scratch path as the opaque session ref.
	return pageURL, handle.home, nil
}

// PollCredentials checks whether provisioning finished for the given session.
// When not yet done it returns done=false with empty creds (caller keeps
// polling). When done it returns the appID plus the AES-256-GCM-encrypted
// app_secret. A "done but blank secret" result is treated as a failure so an
// empty/garbage credential is never persisted.
func (p *Provisioner) PollCredentials(ctx context.Context, sessionRef string) (appID string, appSecretEnc []byte, done bool, err error) {
	handle := p.cli.resolveHandle(sessionRef)
	appID, appSecret, done, err := p.cli.PollAppCreated(ctx, handle)
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
// config home (config.json). The OAuth token exchange needs it as client_secret.
// We resolve it through the cliRunner rather than asking callers to carry it.
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
