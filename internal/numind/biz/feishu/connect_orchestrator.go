// Package feishu — connect_orchestrator.go is the biz-layer engine behind the
// agent-driven 飞书 connect tool (R3 connect-tool, rewritten 2026-06-24 for the
// G2-authorize device-code redesign). It exposes the connect flow as a set of
// non-agent primitives the agent tool drives, so the CONNECTION is modelled as an
// agent tool (the agent gives the user a link and resumes on its own) WITHOUT
// biz/feishu depending on biz/agent.
//
// Device-code design (G2-authorize):
//
//	Authorization no longer uses redirect-OAuth (no redirect_uri / authorize URL /
//	OAuth callback / token exchange). BOTH connect steps go through lark-cli:
//	  phase 1 (create_app): `lark-cli config init --new` — the user builds the app.
//	  phase 2 (authorize):  `lark-cli auth login --no-wait --json --domain docs,im,base`
//	                        returns a verification_url the user opens; on resume,
//	                        `lark-cli auth login --device-code <code>` finishes it.
//	lark-cli stores + auto-refreshes the token inside the user's persistent home;
//	our DB row only carries CONNECTION METADATA (app_id + connected + connected_at).
//	No token, no app_secret, no device_code ever enters the DB.
//
// Phase routing (the DB row + lark-cli auth status are the durable truth; nothing
// is carried across the agent yield):
//
//	· no row / no app_id                              → create_app  (StartProvision)
//	· app, not connected, NO pending device code      → authorize   (StartAuthLogin)
//	· app, not connected, pending device code present  → complete it (CompleteAuthLogin
//	                                                     + verify) → done; on failure
//	                                                     restart a fresh authorize
//	· app, connected (DB flag, or lark-cli auth status) → done
//
// Security (CLAUDE.md / .claude/rules): the orchestrator returns ONLY
// non-sensitive info to its caller — a phase enum + a URL (device-code page /
// verification). It NEVER returns app_secret / access_token / refresh_token /
// device_code (the token lives only in lark-cli's home; the device_code lives only
// in a home-local 0600 file). Plaintext secrets are never logged. 飞书 is an
// external business API, NOT routed through aiservice.
package feishu

import (
	"context"
	"errors"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// DefaultScopes documents the first-batch business domains requested in one
// device-code grant (docs / im / base) so every 飞书 ops tool works after a single
// authorization. Kept exported for parity with the pre-device-code wiring; the
// actual request uses lark-cli's --domain (authLoginDomains).
const DefaultScopes = "docx:document im:message bitable:app:readonly"

// Connect phase discriminants returned by NextConnectStep.
const (
	// ConnectPhaseDone: the user is already connected (lark-cli holds a token).
	ConnectPhaseDone = "done"
	// ConnectPhaseCreateApp: the user has no self-built 飞书 app yet → they must
	// open the device-code page URL to create+configure it.
	ConnectPhaseCreateApp = "create_app"
	// ConnectPhaseAuthorize: the app exists → the user must open the device-code
	// verification URL and grant scopes; the run resumes to complete it.
	ConnectPhaseAuthorize = "authorize"
)

// AppStarter starts the device-code app-provisioning flow (connect's create_app
// step). Concrete impl is *Provisioner; interface for testability. It returns the
// page URL the user opens to create the app + an opaque session ref.
type AppStarter interface {
	StartProvision(ctx context.Context, userID uint) (pageURL, sessionRef string, err error)
}

// AppPoller polls per-user lark-cli app-create progress and, when complete,
// returns the appID + AES-256-GCM-encrypted app_secret. The concrete impl is
// *Provisioner.PollCredentialsForUser; an interface so the orchestrator is unit
// tested without a real lark-cli runner. The returned secret is ALWAYS ciphertext
// (never plaintext) — under device-code it is no longer stored, but the seam is
// kept so PollAndPersistApp can still detect app-create completion via the appID.
type AppPoller interface {
	PollCredentialsForUser(ctx context.Context, userID uint) (appID string, appSecretEnc []byte, done bool, err error)
}

// Authorizer drives the lark-cli device-code authorization (G2-authorize). The
// concrete impl is *Provisioner (delegating to the LarkCLIRunner); an interface so
// the orchestrator is unit tested with a fake. It NEVER exposes the token or the
// device_code — only the verification URL the user opens.
type Authorizer interface {
	// StartAuthorize starts the device flow and returns the verification URL.
	StartAuthorize(ctx context.Context, userID uint) (verificationURL string, err error)
	// CompleteAuthorize finishes the in-flight device flow (after the user
	// authorized in the browser). Returns nil on success (token now in the home).
	CompleteAuthorize(ctx context.Context, userID uint) error
	// HasPendingAuthorize reports whether a StartAuthorize is awaiting completion.
	HasPendingAuthorize(userID uint) bool
	// IsAuthorized reports whether the user's home holds a usable authorization.
	IsAuthorized(ctx context.Context, userID uint) (bool, error)
}

// ConnectStep is the non-sensitive result NextConnectStep hands the agent tool.
// It carries the phase + the URL to show the user (empty for done) — NEVER any
// secret/token/device_code.
type ConnectStep struct {
	Phase string // ConnectPhaseDone | ConnectPhaseCreateApp | ConnectPhaseAuthorize
	URL   string // device-code page URL (create_app) or verification URL (authorize); "" for done
}

// ConnectOrchestratorDeps wires the orchestrator. All deps required.
type ConnectOrchestratorDeps struct {
	Store      store.IThirdPartyAccountStore
	Starter    AppStarter // StartProvision (create_app)
	Poller     AppPoller  // PollCredentialsForUser (create_app → app-row bridge)
	Authorizer Authorizer // lark-cli device-code authorization (authorize / complete / status)
}

// ConnectOrchestrator drives the agent-tool connect flow. Safe for concurrent
// use: it holds only immutable deps; per-user state lives in the DB row + the
// user's lark-cli home.
type ConnectOrchestrator struct {
	store      store.IThirdPartyAccountStore
	starter    AppStarter
	poller     AppPoller
	authorizer Authorizer
	now        func() time.Time
}

// NewConnectOrchestrator builds the orchestrator, failing fast on any missing
// required dep so a misconfigured deploy aborts rather than nil-panicking.
func NewConnectOrchestrator(d ConnectOrchestratorDeps) (*ConnectOrchestrator, error) {
	if d.Store == nil {
		return nil, errors.New("feishu: nil store for connect orchestrator")
	}
	if d.Starter == nil {
		return nil, errors.New("feishu: nil app starter for connect orchestrator")
	}
	if d.Poller == nil {
		return nil, errors.New("feishu: nil app poller for connect orchestrator")
	}
	if d.Authorizer == nil {
		return nil, errors.New("feishu: nil authorizer for connect orchestrator")
	}
	return &ConnectOrchestrator{
		store:      d.Store,
		starter:    d.Starter,
		poller:     d.Poller,
		authorizer: d.Authorizer,
		now:        time.Now,
	}, nil
}

// NextConnectStep decides where the user is in the device-code connect flow and
// returns the non-sensitive next action (phase + URL). runID + questionText are
// accepted for signature compatibility with the agent tool's yield contract but
// are unused under device-code (there is no OAuth callback to sign them into; the
// run is resumed by the agent answer flow re-executing this tool).
//
//   - no app row    → ConnectPhaseCreateApp + device-code page URL (StartProvision).
//   - app, pending device code → complete it; on success → ConnectPhaseDone, else
//     restart a fresh authorize.
//   - app, connected → ConnectPhaseDone.
//   - app, not connected, none pending → ConnectPhaseAuthorize + verification URL.
func (o *ConnectOrchestrator) NextConnectStep(ctx context.Context, userID uint, _ uint64, _ string) (*ConnectStep, error) {
	acc, err := o.store.Get(ctx, userID, ProviderLark)
	switch {
	case err == nil && acc.AppID != "":
		return o.stepForExistingApp(ctx, userID, acc)
	case err == nil || errors.Is(err, gorm.ErrRecordNotFound):
		// No usable app row → create-app step.
		pageURL, _, perr := o.starter.StartProvision(ctx, userID)
		if perr != nil {
			return nil, fmt.Errorf("feishu.NextConnectStep: start provision (user %d): %w", userID, perr)
		}
		return &ConnectStep{Phase: ConnectPhaseCreateApp, URL: pageURL}, nil
	default:
		return nil, fmt.Errorf("feishu.NextConnectStep: load account (user %d): %w", userID, err)
	}
}

// stepForExistingApp resolves the authorize/complete/done branch for a user who
// already has a self-built app (app_id present).
func (o *ConnectOrchestrator) stepForExistingApp(ctx context.Context, userID uint, acc *model.UserThirdPartyAccount) (*ConnectStep, error) {
	// Already connected per the durable DB flag → done (no extra round-trip).
	if acc.Connected {
		return &ConnectStep{Phase: ConnectPhaseDone}, nil
	}

	// A device flow is awaiting completion (the user just authorized in the
	// browser and the run resumed) → finish it.
	if o.authorizer.HasPendingAuthorize(userID) {
		if cerr := o.authorizer.CompleteAuthorize(ctx, userID); cerr != nil {
			// Completion failed (e.g. the device code expired before the user
			// authorized). Don't dead-end: start a fresh authorize so the user gets
			// a new link. Log the cause for diagnosis (it carries no secret).
			log.C(ctx).Warnw("feishu connect: device-code completion failed, restarting authorize",
				"user_id", userID, "err", cerr)
			return o.startAuthorize(ctx, userID)
		}
		// Completed: lark-cli now holds the token. Persist the connection metadata.
		if merr := o.markConnected(ctx, userID); merr != nil {
			return nil, merr
		}
		return &ConnectStep{Phase: ConnectPhaseDone}, nil
	}

	// Out-of-band check: lark-cli may already hold a valid token (e.g. a prior flow
	// that never updated the DB). Treat that as connected and reconcile the flag.
	if ok, serr := o.authorizer.IsAuthorized(ctx, userID); serr == nil && ok {
		if merr := o.markConnected(ctx, userID); merr != nil {
			return nil, merr
		}
		return &ConnectStep{Phase: ConnectPhaseDone}, nil
	}

	// App exists but no authorization yet → start the device flow.
	return o.startAuthorize(ctx, userID)
}

// startAuthorize begins the lark-cli device flow and returns the authorize step
// carrying the verification URL the user opens.
func (o *ConnectOrchestrator) startAuthorize(ctx context.Context, userID uint) (*ConnectStep, error) {
	verifyURL, err := o.authorizer.StartAuthorize(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("feishu.NextConnectStep: start authorize (user %d): %w", userID, err)
	}
	if verifyURL == "" {
		return nil, fmt.Errorf("feishu.NextConnectStep: authorize (user %d): empty verification URL", userID)
	}
	return &ConnectStep{Phase: ConnectPhaseAuthorize, URL: verifyURL}, nil
}

// markConnected stamps the DB row connected=true + connected_at=now.
func (o *ConnectOrchestrator) markConnected(ctx context.Context, userID uint) error {
	if err := o.store.MarkConnected(ctx, userID, ProviderLark, o.now()); err != nil {
		return fmt.Errorf("feishu.NextConnectStep: mark connected (user %d): %w", userID, err)
	}
	return nil
}

// PollAndPersistApp checks whether the user's lark-cli device-code app-create has
// finished and, if so, UPSERTs the connection metadata (app_id). It does NOT mark
// connected (that happens after the separate authorize step) and does NOT store
// any token/secret (device-code: the token lives only in lark-cli's home).
// Idempotent and safe to re-call: when not yet ready it returns (false, nil) and
// writes nothing; an existing connected flag is preserved.
func (o *ConnectOrchestrator) PollAndPersistApp(ctx context.Context, userID uint) (persisted bool, err error) {
	appID, _, done, err := o.poller.PollCredentialsForUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("feishu.PollAndPersistApp: poll (user %d): %w", userID, err)
	}
	if !done {
		return false, nil
	}

	// Upsert the app_id metadata, preserving an existing connected flag (a stale
	// re-provision must not wipe a live connection).
	row := &model.UserThirdPartyAccount{
		UserID:   userID,
		Provider: ProviderLark,
		AppID:    appID,
	}
	if existing, gerr := o.store.Get(ctx, userID, ProviderLark); gerr == nil {
		row.Connected = existing.Connected
		row.ConnectedAt = existing.ConnectedAt
	}
	if uerr := o.store.Upsert(ctx, row); uerr != nil {
		return false, fmt.Errorf("feishu.PollAndPersistApp: upsert app (user %d): %w", userID, uerr)
	}
	return true, nil
}

// Compile-time guards: the concrete *Provisioner satisfies the app seams, and the
// production authorizer adapter satisfies Authorizer (see provisioner.go).
var (
	_ AppStarter = (*Provisioner)(nil)
	_ AppPoller  = (*Provisioner)(nil)
)
