// Package feishu — provisioner.go implements per-user 飞书自建应用 provisioning
// (config-init flow via lark-cli) + authorization (blocking auth-login, isomorphic to
// app-create; fix/feishu-phase-from-home, 2026-06-24).
//
// Architecture (verified lark-cli reality on 2026-06-24):
//
//   - There is NO public REST API to create a 飞书 self-built app. The supported
//     programmatic path is lark-cli's `config init --new` flow: it BLOCKS, prints an
//     open.feishu.cn/page/cli URL, the user opens it in a browser to create+configure
//     the app, and lark-cli exits once the app is created — persisting the new app's
//     appId/appSecret into that config home's ~/.lark-cli/config.json.
//   - Authorization is ALSO lark-cli, ISOMORPHIC to app-create: `lark-cli auth login
//     --domain docs,im,base` BLOCKS, prints a verification URL, the user grants scopes
//     in a browser, and lark-cli exits — persisting + auto-refreshing the
//     user_access_token inside the home. There is NO redirect_uri / OAuth callback /
//     token exchange / device-code file done by us.
//   - 有数's server runs lark-cli once per user, each pinned to its own PERSISTENT
//     HOME (feishu.home_base/u{userID}) so credentials + tokens never collide across
//     users and survive a redeploy (lark-cli reads $HOME/.lark-cli/).
//   - The HOME is the connect PHASE truth source: AppExists reads config.json (app
//     built?), AuthStatus reads `auth status` (authorized?). The DB row is reconciled
//     FROM the home (UI/status), never the phase truth.
//
// Provisioning (app-create) is a two-call flow at the cliRunner seam:
//
//   - StartAppCreate(ctx, userID): launch `lark-cli config init --new` backgrounded
//     in the user's PERSISTENT HOME, scrape the page URL from its stdout, return the
//     URL + an opaque handle. The process keeps blocking until the user finishes.
//   - PollAppCreated(ctx, handle): report whether the process finished AND apps[0]
//     is readable from config.json; when done, return the appId + plaintext appSecret.
//
// Authorization is the authRunner seam (auth_cli.go): StartAuthorizeLogin (blocking
// background spawn + scrape URL) / AuthStatus (identities.user.available).
//
// Security: the app_secret returned by PollCredentials is AES-256-GCM encrypted at
// THIS boundary (via the injected crypto.Cipher). Plaintext secrets / tokens are
// never returned to the LLM, never logged. NOT routed through aiservice (飞书 is an
// external business API, not an LLM gateway).
package feishu

import (
	"context"
	"errors"
	"fmt"

	"numind-server/internal/pkg/errno"
)

// Encrypter is the subset of crypto.Cipher the provisioner needs to seal
// credentials at the store boundary. Declared as an interface so tests can
// substitute a fake, though production always passes *crypto.Cipher.
type Encrypter interface {
	Encrypt(plain []byte) ([]byte, error)
}

// AppCreateHandle is the opaque token returned by StartAppCreate and passed back
// to PollAppCreated. It carries the persistent config-home path (durable — survives
// a poll on the same instance and a restart) plus the in-flight `config init`
// process tracking. The fields are unexported so callers treat it as opaque; only
// the same-package cliRunner reads them.
type AppCreateHandle struct {
	// home is the per-user PERSISTENT HOME lark-cli reads/writes ($HOME/.lark-cli/).
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
//     isolated PERSISTENT HOME, returning the page URL the user must open and an
//     opaque handle (used by PollAppCreated to observe completion + read creds).
//   - PollAppCreated checks whether the user has finished the browser step (process
//     exited AND apps[0] present in config.json). When done it returns the appId +
//     the (plaintext) appSecret read from config.json; otherwise done=false.
type cliRunner interface {
	StartAppCreate(ctx context.Context, userID uint) (pageURL string, handle *AppCreateHandle, err error)
	PollAppCreated(ctx context.Context, handle *AppCreateHandle) (appID, appSecret string, done bool, err error)

	// AppExists reports whether userID's PERSISTENT home already holds a built app
	// (config.json apps[0].appId present). It reads the home (the phase truth source)
	// directly — no process needed — so the orchestrator can route create_app vs
	// authorize without consulting the (possibly stale) DB. A transient read failure
	// returns an error; a clean "no app yet" is (false, nil).
	AppExists(ctx context.Context, userID uint) (bool, error)

	// AppID returns the appId from userID's home (config.json apps[0]). Used to
	// reconcile the DB row on the done path. Errors when no app is present.
	AppID(ctx context.Context, userID uint) (string, error)

	// resolveHandle reconstructs an AppCreateHandle from a durable session ref
	// (the home path) so the string-based Provisioner.PollCredentials contract
	// can bridge to the handle-based PollAppCreated even after the in-memory
	// session map is lost. Returns nil for an empty/unknown ref.
	resolveHandle(sessionRef string) *AppCreateHandle

	// sessionRefForUser returns the DETERMINISTIC durable session ref (persistent
	// home path) for userID — the same path StartAppCreate uses. It lets the
	// agent-driven connect tool poll a user's app-create progress WITHOUT carrying
	// the sessionRef across an agent yield/resume. Returns "" only for userID 0.
	sessionRefForUser(userID uint) string

	// authRunner: the lark-cli device-code authorization seam (auth_cli.go). The
	// production *LarkCLIRunner satisfies both cliRunner and authRunner.
	authRunner
}

// Provisioner orchestrates app provisioning + device-code authorization. Safe for
// concurrent use: it holds only immutable dependencies; per-user state lives in
// the cliRunner (isolated persistent homes keyed by userID / home path).
type Provisioner struct {
	cipher Encrypter
	cli    cliRunner
}

// NewProvisioner wires the provisioner. All dependencies are required — a nil
// cipher would mean storing plaintext credentials, so it fails fast rather than
// silently degrading security.
func NewProvisioner(cipher Encrypter, cli cliRunner) (*Provisioner, error) {
	if cipher == nil {
		return nil, errors.New("feishu: nil cipher for provisioner")
	}
	if cli == nil {
		return nil, errors.New("feishu: nil cli runner for provisioner")
	}
	return &Provisioner{cipher: cipher, cli: cli}, nil
}

// StartProvision begins provisioning a 飞书 app for userID. It returns the page
// URL (shown to the user so they open it in a browser to create the app) and an
// opaque session ref the caller passes back to PollCredentials. The session ref
// is the durable per-user home path, so a later poll resolves the same flow even
// across a server restart.
func (p *Provisioner) StartProvision(ctx context.Context, userID uint) (pageURL, sessionRef string, err error) {
	pageURL, handle, err := p.cli.StartAppCreate(ctx, userID)
	if err != nil {
		return "", "", fmt.Errorf("feishu: start provision (user %d): %w", userID, err)
	}
	if pageURL == "" || handle == nil || handle.home == "" {
		return "", "", fmt.Errorf("feishu: start provision (user %d): empty page URL or handle", userID)
	}
	// Expose the durable per-user home path as the opaque session ref.
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

// AppExists reports whether userID's lark-cli home already holds a built app
// (config.json apps[0].appId present). It is the create_app→authorize phase
// discriminant — read straight from the home (the truth source), independent of the
// DB. userID 0 has no home → (false, error).
func (p *Provisioner) AppExists(ctx context.Context, userID uint) (bool, error) {
	if userID == 0 {
		return false, fmt.Errorf("%w: missing user id for app-exists check", errno.ErrLarkCallFailed)
	}
	return p.cli.AppExists(ctx, userID)
}

// PollCredentialsForUser is the agent-driven variant of PollCredentials: it
// re-derives the user's durable session ref (persistent home path) from userID, so
// the connect tool can poll app-create progress on a tool re-call WITHOUT having
// carried the sessionRef across the agent yield/resume.
func (p *Provisioner) PollCredentialsForUser(ctx context.Context, userID uint) (appID string, appSecretEnc []byte, done bool, err error) {
	if userID == 0 {
		return "", nil, false, fmt.Errorf("%w: missing user id for poll", errno.ErrLarkCallFailed)
	}
	return p.PollCredentials(ctx, p.cli.sessionRefForUser(userID))
}

// AppID returns the appId from userID's lark-cli home (config.json apps[0]) so the
// orchestrator can reconcile the DB row (UI/status) on the done path. Returns "" +
// error when no app is present. userID 0 errors.
func (p *Provisioner) AppID(ctx context.Context, userID uint) (string, error) {
	if userID == 0 {
		return "", fmt.Errorf("%w: missing user id for app-id read", errno.ErrLarkCallFailed)
	}
	return p.cli.AppID(ctx, userID)
}

// --- authorization (Authorizer, consumed by ConnectOrchestrator) ------------

// StartAuthorize launches the blocking `lark-cli auth login` for userID and returns
// the verification URL the user opens. The token is persisted inside the home by
// lark-cli on completion (never returned). NOT through aiservice.
func (p *Provisioner) StartAuthorize(ctx context.Context, userID uint) (string, error) {
	if userID == 0 {
		return "", fmt.Errorf("%w: missing user id for authorize", errno.ErrLarkCallFailed)
	}
	return p.cli.StartAuthorizeLogin(ctx, userID)
}

// IsAuthorized reports whether the user's home holds a usable authorization
// (lark-cli auth status: identities.user.available == true).
func (p *Provisioner) IsAuthorized(ctx context.Context, userID uint) (bool, error) {
	if userID == 0 {
		return false, fmt.Errorf("%w: missing user id for authorization check", errno.ErrLarkCallFailed)
	}
	return p.cli.AuthStatus(ctx, userID)
}

// compile-time guard: *Provisioner satisfies the orchestrator's Authorizer seam.
var _ Authorizer = (*Provisioner)(nil)
