// Package feishu — connect_orchestrator.go is the biz-layer engine behind the
// agent-driven 飞书 connect tool (R3 connect-tool). It exposes the connect flow as a
// set of non-agent primitives the agent tool drives, so the CONNECTION is modelled as
// an agent tool (the agent gives the user a link and resumes on its own) WITHOUT
// biz/feishu depending on biz/agent.
//
// PHASE FROM HOME design (fix/feishu-phase-from-home, 2026-06-24):
//
//	The connect PHASE is read from lark-cli's per-user HOME — the SINGLE source of
//	truth — NOT from the DB row (which was the root of the "re-create app + lock
//	wedge" bug: DB and home drifting apart). Both connect steps go through lark-cli,
//	BLOCKING + isomorphic to each other:
//	  phase 1 (create_app): `lark-cli config init --new` — the user builds the app;
//	                        the process prints a page URL and exits on completion.
//	  phase 2 (authorize):  `lark-cli auth login --domain docs,im,base` — the user
//	                        grants scopes; the process prints a verification URL and
//	                        exits on completion (token persisted in the home).
//	lark-cli stores + auto-refreshes the token inside the user's persistent home; our
//	DB row carries ONLY connection metadata (app_id + connected) and is reconciled
//	FROM the home on the done path — used for UI/status, NEVER as the phase truth.
//
// Phase routing (home is authoritative):
//
//	· home has NO app (AppExists=false)               → create_app (StartProvision)
//	· home has app, NOT authorized (IsAuthorized=false) → authorize (StartAuthorize)
//	· home has app, authorized (IsAuthorized=true)      → done + reconcile DB row
//
// Because the home is authoritative, a home that already has the app is NEVER
// re-provisioned, and the in-memory provisioning lock is never consulted on the
// app-exists path (no "already in progress" dead-end).
//
// Security (CLAUDE.md / .claude/rules): the orchestrator returns ONLY non-sensitive
// info to its caller — a phase enum + a URL (config-init page / auth verification).
// It NEVER returns app_secret / access_token / refresh_token (the token lives only in
// lark-cli's home). Plaintext secrets/tokens are never logged. 飞书 is an external
// business API, NOT routed through aiservice.
package feishu

import (
	"context"
	"errors"
	"fmt"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// DefaultScopes documents the first-batch business domains requested in one
// device-code grant (docs / im / base) so every 飞书 ops tool works after a single
// authorization. Kept exported for parity with the pre-device-code wiring; the
// actual request uses lark-cli's --domain (authLoginDomains).
const DefaultScopes = "docx:document im:message bitable:app:readonly"

// Connect phase discriminants returned by NextConnectStep.
const (
	// ConnectPhaseDone: the home is authorized (lark-cli holds a usable token).
	ConnectPhaseDone = "done"
	// ConnectPhaseCreateApp: the home has no self-built 飞书 app yet → the user opens
	// the config-init page URL to create+configure it.
	ConnectPhaseCreateApp = "create_app"
	// ConnectPhaseAuthorize: the home has the app but is not authorized → the user
	// opens the verification URL and grants scopes; the run resumes to re-check.
	ConnectPhaseAuthorize = "authorize"
)

// AppStarter starts the app-provisioning flow (connect's create_app step) AND
// reports whether the user's lark-cli home already holds a built app — the latter
// is the PHASE TRUTH SOURCE (the home's config.json), not the DB. Concrete impl is
// *Provisioner; interface for testability. StartProvision returns the page URL the
// user opens to create the app + an opaque session ref.
type AppStarter interface {
	StartProvision(ctx context.Context, userID uint) (pageURL, sessionRef string, err error)
	// AppExists reports whether userID's lark-cli home already holds a built app
	// (config.json apps[0].appId present). This is read as the create_app→authorize
	// phase discriminant so a home that already has the app is never re-provisioned.
	AppExists(ctx context.Context, userID uint) (bool, error)
	// AppID returns the appId from userID's lark-cli home (config.json apps[0]). Used
	// to reconcile the DB row (UI/status) on the done path. Returns "" + error when no
	// app is present (callers only call it when AppExists already reported true).
	AppID(ctx context.Context, userID uint) (string, error)
}

// AppPoller polls per-user lark-cli app-create progress and, when complete,
// returns the appID + AES-256-GCM-encrypted app_secret. The concrete impl is
// *Provisioner.PollCredentialsForUser; an interface so the orchestrator is unit
// tested without a real lark-cli runner. The returned secret is ALWAYS ciphertext
// (never plaintext) — it is no longer persisted (token lives in lark-cli's home), but
// the seam is kept so PollAndPersistApp can populate the DB app_id (UI/status) when an
// app-create finishes.
type AppPoller interface {
	PollCredentialsForUser(ctx context.Context, userID uint) (appID string, appSecretEnc []byte, done bool, err error)
}

// Authorizer drives the lark-cli authorization (G2-authorize, blocking auth-login
// model — isomorphic to app-create). The concrete impl is *Provisioner (delegating
// to the LarkCLIRunner); an interface so the orchestrator is unit tested with a fake.
// It NEVER exposes the token — only the verification URL the user opens. There is no
// separate "complete" step: once the user authorizes, lark-cli's background process
// exits with the token persisted, and the next IsAuthorized read observes it.
type Authorizer interface {
	// StartAuthorize launches the blocking `auth login` and returns the verification
	// URL the user opens. Self-healing: a concurrent in-flight login is not duplicated
	// and does NOT error — it returns the same cached verification URL so a re-poll
	// before the user finishes the browser step re-shows the link.
	StartAuthorize(ctx context.Context, userID uint) (verificationURL string, err error)
	// IsAuthorized reports whether the user's home holds a usable authorization
	// (lark-cli auth status: identities.user.available == true).
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
	Starter    AppStarter // StartProvision + AppExists/AppID (create_app + home-truth)
	Poller     AppPoller  // PollCredentialsForUser (app-create → DB app_id bridge)
	Authorizer Authorizer // lark-cli authorization (StartAuthorize / IsAuthorized)
}

// ConnectOrchestrator drives the agent-tool connect flow. Safe for concurrent use:
// it holds only immutable deps; per-user state lives in the user's lark-cli home (the
// phase truth) with the DB row reconciled FROM it for UI/status.
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

// NextConnectStep decides where the user is in the connect flow and returns the
// non-sensitive next action (phase + URL). runID + questionText are accepted for
// signature compatibility with the agent tool's yield contract but are unused (the
// run is resumed by the agent answer flow re-executing this tool).
//
// ROOT FIX (fix/feishu-phase-from-home): the PHASE is read from lark-cli's per-user
// HOME — the single source of truth — NOT from the (possibly stale) DB row:
//
//   - home has NO app (AppExists==false)                 → ConnectPhaseCreateApp
//     (StartProvision; the user builds the app in a browser).
//   - home HAS app, NOT authorized (IsAuthorized==false) → ConnectPhaseAuthorize
//     (StartAuthorize; the user grants scopes in a browser).
//   - home HAS app, authorized (IsAuthorized==true)      → ConnectPhaseDone, and the
//     DB row is UPSERTed (connected=true + app_id) — the DB is reconciled to the home
//     here, used ONLY for UI/status, never as the phase truth source.
//
// Because the home is authoritative, a home that already holds the app is NEVER
// re-provisioned (the old DB-driven bug), and the in-memory provisioning lock is
// never consulted on the app-exists path (no "already in progress" dead-end).
func (o *ConnectOrchestrator) NextConnectStep(ctx context.Context, userID uint, _ uint64, _ string) (*ConnectStep, error) {
	// Phase truth source #1: does the user's lark-cli home already hold a built app?
	appExists, err := o.starter.AppExists(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("feishu.NextConnectStep: app-exists (user %d): %w", userID, err)
	}
	if !appExists {
		// No app in the home → create-app step. (Do NOT consult the DB: even if a stale
		// row exists, the home is the truth — the app must be built first.)
		pageURL, _, perr := o.starter.StartProvision(ctx, userID)
		if perr != nil {
			return nil, fmt.Errorf("feishu.NextConnectStep: start provision (user %d): %w", userID, perr)
		}
		return &ConnectStep{Phase: ConnectPhaseCreateApp, URL: pageURL}, nil
	}

	// Phase truth source #2: is the home authorized? (auth status: user.available)
	authorized, err := o.authorizer.IsAuthorized(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("feishu.NextConnectStep: auth-status (user %d): %w", userID, err)
	}
	if authorized {
		// Done: reconcile the DB to the home (connected=true + app_id) for UI/status.
		if rerr := o.reconcileConnected(ctx, userID); rerr != nil {
			return nil, rerr
		}
		return &ConnectStep{Phase: ConnectPhaseDone}, nil
	}

	// App exists but not authorized yet → start the blocking auth-login.
	return o.startAuthorize(ctx, userID)
}

// startAuthorize begins the lark-cli device flow and returns the authorize step
// carrying the verification URL the user opens. When a previous auth-login is still
// alive (the user has not finished the browser step), StartAuthorize self-heals by
// returning the SAME cached verification URL rather than an "already in progress"
// error, so a re-poll yields a clean authorize step (the user re-opens the link)
// instead of failing the whole connect flow.
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

// reconcileConnected makes the DB row mirror the (authorized) home: it ensures a row
// with the home's app_id exists, then stamps connected=true + connected_at=now. The
// DB is used ONLY for UI/status — this is a reconcile FROM the home, never the phase
// truth. It upserts the app_id first (the row may not exist yet when the home is the
// only place that knew about the app), then marks connected. Both steps are idempotent.
func (o *ConnectOrchestrator) reconcileConnected(ctx context.Context, userID uint) error {
	appID, err := o.starter.AppID(ctx, userID)
	if err != nil {
		return fmt.Errorf("feishu.NextConnectStep: read app id (user %d): %w", userID, err)
	}
	// Ensure the row exists with the current app_id, preserving any existing connected
	// flag (a no-op refresh when already present). Upsert does not clear connected.
	if uerr := o.store.Upsert(ctx, &model.UserThirdPartyAccount{
		UserID:   userID,
		Provider: ProviderLark,
		AppID:    appID,
	}); uerr != nil {
		return fmt.Errorf("feishu.NextConnectStep: upsert app id (user %d): %w", userID, uerr)
	}
	if merr := o.store.MarkConnected(ctx, userID, ProviderLark, o.now()); merr != nil {
		return fmt.Errorf("feishu.NextConnectStep: mark connected (user %d): %w", userID, merr)
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
