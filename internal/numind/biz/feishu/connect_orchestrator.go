// Package feishu — connect_orchestrator.go is the biz-layer engine behind the
// agent-driven 飞书 connect tool (feishu-agent-connect R3). It exposes the
// connect flow as a set of non-agent primitives the agent tool drives, so the
// CONNECTION is modelled as an agent tool (the agent gives the user a link and
// resumes on its own) WITHOUT biz/feishu depending on biz/agent.
//
// Design (R3-connect-tool):
//
//   - The DB row (user_third_party_account) is the durable source of truth for
//     the connect phase — there is no in-memory cross-yield state to lose:
//     · no row at all                 → phase create_app (run lark-cli, hand
//     the device-code page URL to the user)
//     · row with app_id, no token     → phase authorize (mint a signed OAuth
//     state + the 飞书 authorize URL)
//     · row with a valid access token  → phase done (already connected)
//   - PollAndPersistApp bridges the create_app → authorize transition: after the
//     user finishes the device-code browser step and the run resumes, the tool
//     re-calls it; it polls the user's lark-cli config.json by userID (no
//     sessionRef carried across the yield) and, when creds are ready, UPSERTs the
//     app_id + AES-256-GCM-encrypted app_secret (NO token yet).
//
// Security (CLAUDE.md / .claude/rules): the orchestrator returns ONLY
// non-sensitive info to its caller — a phase enum + URLs (device-code page /
// authorize). It NEVER returns app_secret / access_token / refresh_token: the
// encrypted app_secret only flows store-ward (Poller → Upsert), never back to the
// tool (and thus never to the LLM). Plaintext secrets are never logged. 飞书 is an
// external business API, NOT routed through aiservice.
package feishu

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// Default 飞书 OAuth endpoints + first-batch scopes (design.md §8). Exported so
// both the biz composition root (feishu_adapter.go) and the agent tool factory
// can fall back to identical values when feishu.* config is unset.
const (
	// DefaultAuthorizeURL is the 飞书 v1 OAuth authorize endpoint.
	DefaultAuthorizeURL = "https://open.feishu.cn/open-apis/authen/v1/authorize"
	// DefaultScopes is the first-batch scope set requested in one shot (缺则后续 403).
	DefaultScopes = "docx:document im:message bitable:app:readonly"
)

// Connect phase discriminants returned by NextConnectStep.
const (
	// ConnectPhaseDone: the user already has a valid 飞书 access token → connected.
	ConnectPhaseDone = "done"
	// ConnectPhaseCreateApp: the user has no self-built 飞书 app yet → they must
	// open the device-code page URL to create+configure it.
	ConnectPhaseCreateApp = "create_app"
	// ConnectPhaseAuthorize: the app exists → the user must grant OAuth scopes via
	// the 飞书 authorize URL (which carries the signed state).
	ConnectPhaseAuthorize = "authorize"
)

// connectStateValidity bounds an OAuth state token's lifetime — long enough for
// a human authorize step, short enough to bound replay exposure (the one-time
// nonce is the primary replay guard). Mirrors service.go stateValidity.
const connectStateValidity = 15 * time.Minute

// AppPoller polls per-user lark-cli app-create progress and, when complete,
// returns the appID + AES-256-GCM-encrypted app_secret. The concrete impl is
// *Provisioner.PollCredentialsForUser; an interface so the orchestrator is unit
// tested without a real lark-cli runner. The returned secret is ALWAYS
// ciphertext (never plaintext) — it only ever flows store-ward.
type AppPoller interface {
	PollCredentialsForUser(ctx context.Context, userID uint) (appID string, appSecretEnc []byte, done bool, err error)
}

// ConnectStep is the non-sensitive result NextConnectStep hands the agent tool.
// It carries the phase + the URL to show the user (empty for done) — NEVER any
// secret/token (those are stored, never returned).
type ConnectStep struct {
	Phase string // ConnectPhaseDone | ConnectPhaseCreateApp | ConnectPhaseAuthorize
	URL   string // device-code page URL (create_app) or authorize URL (authorize); "" for done
}

// ConnectOrchestratorDeps wires the orchestrator. All non-string deps required.
type ConnectOrchestratorDeps struct {
	Store        store.IThirdPartyAccountStore
	Signer       *StateSigner
	Starter      AppStarter // StartProvision (create_app)
	Poller       AppPoller  // PollCredentialsForUser (create_app → authorize bridge)
	AuthorizeURL string     // 飞书 authorize endpoint
	RedirectURI  string     // OAuth redirect_uri registered in 飞书 console
	ScopesCSV    string     // space-separated first-batch scopes to request
}

// ConnectOrchestrator drives the agent-tool connect flow. Safe for concurrent
// use: it holds only immutable deps; per-user state lives in the DB row.
type ConnectOrchestrator struct {
	store        store.IThirdPartyAccountStore
	signer       *StateSigner
	starter      AppStarter
	poller       AppPoller
	authorizeURL string
	redirectURI  string
	scopes       string
	now          func() time.Time
}

// NewConnectOrchestrator builds the orchestrator, failing fast on any missing
// required dep so a misconfigured deploy aborts rather than nil-panicking.
func NewConnectOrchestrator(d ConnectOrchestratorDeps) (*ConnectOrchestrator, error) {
	if d.Store == nil {
		return nil, errors.New("feishu: nil store for connect orchestrator")
	}
	if d.Signer == nil {
		return nil, errors.New("feishu: nil state signer for connect orchestrator")
	}
	if d.Starter == nil {
		return nil, errors.New("feishu: nil app starter for connect orchestrator")
	}
	if d.Poller == nil {
		return nil, errors.New("feishu: nil app poller for connect orchestrator")
	}
	if d.AuthorizeURL == "" {
		return nil, errors.New("feishu: empty authorize URL for connect orchestrator")
	}
	if d.RedirectURI == "" {
		return nil, errors.New("feishu: empty redirect URI for connect orchestrator")
	}
	return &ConnectOrchestrator{
		store:        d.Store,
		signer:       d.Signer,
		starter:      d.Starter,
		poller:       d.Poller,
		authorizeURL: d.AuthorizeURL,
		redirectURI:  d.RedirectURI,
		scopes:       d.ScopesCSV,
		now:          time.Now,
	}, nil
}

// NextConnectStep decides where the user is in the connect flow and returns the
// non-sensitive next action (phase + URL). runID + questionText come from the
// paused agent run; they are signed into the OAuth state so the callback can
// resume that exact run with the matching answer key (= questionText).
//
//   - valid token   → ConnectPhaseDone (URL empty).
//   - app, no token → ConnectPhaseAuthorize + signed authorize URL.
//   - no app row    → ConnectPhaseCreateApp + device-code page URL (StartProvision).
func (o *ConnectOrchestrator) NextConnectStep(ctx context.Context, userID uint, runID uint64, questionText string) (*ConnectStep, error) {
	acc, err := o.store.Get(ctx, userID, ProviderLark)
	switch {
	case err == nil && o.hasValidToken(acc):
		return &ConnectStep{Phase: ConnectPhaseDone}, nil
	case err == nil && acc.AppID != "":
		// App exists (with or without an expired token) → authorize step.
		state, serr := o.signState(ctx, userID, runID, questionText)
		if serr != nil {
			return nil, serr
		}
		return &ConnectStep{Phase: ConnectPhaseAuthorize, URL: o.buildAuthorizeURL(acc.AppID, state)}, nil
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

// PollAndPersistApp checks whether the user's lark-cli device-code app-create
// has finished and, if so, UPSERTs the app_id + encrypted app_secret (NO token
// — the token is obtained later in the authorize phase). Idempotent and safe to
// re-call: when not yet ready it returns (false, nil) and writes nothing; an
// existing token/scopes on the row are preserved.
func (o *ConnectOrchestrator) PollAndPersistApp(ctx context.Context, userID uint) (persisted bool, err error) {
	appID, secEnc, done, err := o.poller.PollCredentialsForUser(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("feishu.PollAndPersistApp: poll (user %d): %w", userID, err)
	}
	if !done {
		return false, nil
	}

	// Preserve any existing token/scopes (a stale re-provision must not wipe a
	// live connection); only refresh app_id + app_secret.
	row := &model.UserThirdPartyAccount{
		UserID:       userID,
		Provider:     ProviderLark,
		AppID:        appID,
		AppSecretEnc: secEnc,
	}
	if existing, gerr := o.store.Get(ctx, userID, ProviderLark); gerr == nil {
		row.AccessTokenEnc = existing.AccessTokenEnc
		row.RefreshTokenEnc = existing.RefreshTokenEnc
		row.TokenExpiresAt = existing.TokenExpiresAt
		row.Scopes = existing.Scopes
	}
	if uerr := o.store.Upsert(ctx, row); uerr != nil {
		return false, fmt.Errorf("feishu.PollAndPersistApp: upsert app (user %d): %w", userID, uerr)
	}
	return true, nil
}

// Compile-time guards: the concrete *Provisioner satisfies both seams the
// orchestrator depends on, so production wiring passes a single *Provisioner.
var (
	_ AppStarter = (*Provisioner)(nil)
	_ AppPoller  = (*Provisioner)(nil)
)

// hasValidToken reports whether the row carries a usable access token. A nil
// expiry is treated as valid (飞书 may omit expires_in; do not misjudge as
// expired — mirrors service.go Status semantics).
func (o *ConnectOrchestrator) hasValidToken(acc *model.UserThirdPartyAccount) bool {
	if acc == nil || len(acc.AccessTokenEnc) == 0 {
		return false
	}
	if acc.TokenExpiresAt != nil && !acc.TokenExpiresAt.After(o.now()) {
		return false
	}
	return true
}

// signState mints a signed OAuth state binding (userID, runID, questionText).
func (o *ConnectOrchestrator) signState(ctx context.Context, userID uint, runID uint64, questionText string) (string, error) {
	state, err := o.signer.Sign(ctx, Payload{
		UserID:       userID,
		RunID:        strconv.FormatUint(runID, 10),
		Step:         ConnectPhaseAuthorize,
		QuestionText: questionText,
	}, connectStateValidity)
	if err != nil {
		return "", fmt.Errorf("feishu: sign state (user %d): %w", userID, err)
	}
	return state, nil
}

// buildAuthorizeURL builds the 飞书 OAuth authorize URL. It requests the
// first-batch scopes in one shot (缺则后续 403).
func (o *ConnectOrchestrator) buildAuthorizeURL(appID, state string) string {
	q := url.Values{}
	q.Set("app_id", appID)
	q.Set("redirect_uri", o.redirectURI)
	q.Set("state", state)
	if o.scopes != "" {
		q.Set("scope", o.scopes)
	}
	sep := "?"
	if strings.Contains(o.authorizeURL, "?") {
		sep = "&"
	}
	return o.authorizeURL + sep + q.Encode()
}
