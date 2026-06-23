// Package feishu — service.go is the feishu-integration T7 OAuth/connection
// service. It orchestrates the four user-facing operations behind the HTTP
// controller (controller/v1/feishu):
//
//   - Connect:       decide the next step (create the self-built app, or
//     authorize an already-created app) and mint a signed OAuth
//     state for the authorize URL.
//   - HandleCallback the no-JWT 飞书 redirect target. Verifies the state
//     (HMAC + exp + one-time nonce), enforces cross-user safety,
//     exchanges the code for tokens, encrypts + UPSERTs them, and
//     resumes the paused agent run via biz.Answer — all idempotent.
//   - Status:        report connection state (none / active / expired) + scopes.
//   - Unbind:        delete the stored token row (the 飞书 app itself is kept).
//
// Layering: this is the biz layer. It depends on the store (T3), state signer
// (T5), provisioner (T6) and a narrow Answer-resumer + run-reader seam onto the
// agent package. The Answer/Run dependencies are NARROW interfaces (not the
// concrete *agent.StudentRunService) so biz/feishu does NOT import biz/agent —
// avoiding an import cycle — and so tests inject fakes.
//
// Security (design.md §4 §5): tokens are AES-256-GCM ciphertext at the store
// boundary (the Provisioner.ExchangeCode returns ciphertext); plaintext secrets
// are never logged. The state HMAC key and the AES token key are separate.
// 飞书 is an external business API, NOT routed through aiservice.
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
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// stateValidity bounds how long an OAuth state token is honored. Long enough for
// a human to complete the browser authorize step, short enough to bound replay
// exposure (the one-time nonce is the primary replay guard; exp is belt-and-braces).
const stateValidity = 15 * time.Minute

// resumeFreeText is the synthetic free-text answer the callback feeds biz.Answer
// to resume the paused run. The answer KEY is the pending question text (carried
// in state); this is the VALUE. It exists only to satisfy answer.go's "each
// answer must carry a selection or free text" rule (design.md §6 恢复键机制).
const resumeFreeText = "已授权"

// Next-step discriminants returned by Connect (design.md §5 connect contract).
const (
	// NextStepCreateApp: the user has no self-built 飞书 app yet → they must run
	// the device-code page first (URL points at the lark-cli device-code page).
	NextStepCreateApp = "create_app"
	// NextStepAuthorize: the app exists → the user must grant OAuth scopes (URL is
	// the 飞书 authorize URL carrying the signed state).
	NextStepAuthorize = "authorize"
)

// --- narrow dependency seams (avoid importing biz/agent → no import cycle) ---

// AnswerResumer resumes a paused agent run by submitting a free-text answer to
// the pending question. The concrete implementation is a thin adapter over
// *agent.StudentRunService.Answer (wired in biz.go). questionText MUST equal the
// run's pending question text (answer.go invariant) or the resume is rejected.
type AnswerResumer interface {
	ResumeWithAnswer(ctx context.Context, userID uint, runID uint64, questionText, freeText string) error
}

// RunStateReader reads the minimal agent_run fields the callback needs for
// cross-user defense (run.UserID) and idempotency (run.StateReason). Narrow on
// purpose: *agent.StudentRunService's store satisfies it via a thin adapter.
type RunStateReader interface {
	GetRun(ctx context.Context, runID uint64) (*model.AgentRun, error)
}

// CodeExchanger exchanges an OAuth authorization code for encrypted tokens. The
// concrete implementation is *Provisioner (same package); declared as an
// interface so the service is unit-tested with a fake (no live 飞书).
type CodeExchanger interface {
	ExchangeCode(ctx context.Context, appID, code string) (access, refresh []byte, exp *time.Time, scopes string, err error)
}

// AppStarter starts the device-code app-provisioning flow (connect's create_app
// step). Concrete impl is *Provisioner; interface for testability.
type AppStarter interface {
	StartProvision(ctx context.Context, userID uint) (pageURL, sessionRef string, err error)
}

// --- DTOs ------------------------------------------------------------------

// ConnectResult is the POST /v1/feishu/connect response data.
type ConnectResult struct {
	NextStep string `json:"next_step"` // NextStepCreateApp | NextStepAuthorize
	URL      string `json:"url"`       // device-code page URL or 飞书 authorize URL
	State    string `json:"state"`     // signed OAuth state (empty for create_app)
}

// StatusResult is the GET /v1/feishu/status response data.
type StatusResult struct {
	Connected bool     `json:"connected"`
	Status    string   `json:"status"` // none | active | expired
	Scopes    []string `json:"scopes"`
	AppID     string   `json:"app_id"`
}

// CallbackResult tells the controller where to 302. The service NEVER writes the
// HTTP response itself; it returns the target so the controller stays the only
// place that touches gin (controller-discipline). Success distinguishes the
// connected vs error query suffix; RedirectURL is always set (even on error) so
// the user lands on a friendly page.
type CallbackResult struct {
	RedirectURL string
	Success     bool
}

// --- service ---------------------------------------------------------------

// Deps wires FeishuService. All non-string deps are required.
type Deps struct {
	Store        store.IThirdPartyAccountStore
	Signer       *StateSigner
	Provisioner  CodeExchanger // also AppStarter when the concrete *Provisioner is passed
	Starter      AppStarter    // optional: defaults to Provisioner if it implements AppStarter
	Answer       AnswerResumer
	Runs         RunStateReader
	WebBaseURL   string // frontend base, e.g. https://youshu.asia (callback 302 target)
	AuthorizeURL string // 飞书 authorize endpoint
	RedirectURI  string // OAuth redirect_uri registered in 飞书 console (must match exchanger)
	ScopesCSV    string // space-separated first-batch scopes to request
}

// FeishuService is the IFeishuService implementation.
type FeishuService struct {
	store        store.IThirdPartyAccountStore
	signer       *StateSigner
	exchanger    CodeExchanger
	starter      AppStarter
	answer       AnswerResumer
	runs         RunStateReader
	webBaseURL   string
	authorizeURL string
	redirectURI  string
	scopes       string
	now          func() time.Time
}

// IFeishuService is the biz interface exposed via biz.IBiz.FeishuSvc().
type IFeishuService interface {
	Connect(ctx context.Context, userID uint, runID uint64, questionText string) (*ConnectResult, error)
	HandleCallback(ctx context.Context, code, state string) (*CallbackResult, error)
	Status(ctx context.Context, userID uint) (*StatusResult, error)
	Unbind(ctx context.Context, userID uint) error
}

// compile-time guard.
var _ IFeishuService = (*FeishuService)(nil)

// NewFeishuService builds the service, failing fast on a missing required dep so
// a misconfigured deploy aborts rather than nil-panicking on first request.
func NewFeishuService(d Deps) (*FeishuService, error) {
	if d.Store == nil {
		return nil, errors.New("feishu: nil store for service")
	}
	if d.Signer == nil {
		return nil, errors.New("feishu: nil state signer for service")
	}
	if d.Provisioner == nil {
		return nil, errors.New("feishu: nil provisioner (code exchanger) for service")
	}
	if d.Answer == nil {
		return nil, errors.New("feishu: nil answer resumer for service")
	}
	if d.Runs == nil {
		return nil, errors.New("feishu: nil run reader for service")
	}
	if d.WebBaseURL == "" {
		return nil, errors.New("feishu: empty web base URL for service")
	}
	if d.AuthorizeURL == "" {
		return nil, errors.New("feishu: empty authorize URL for service")
	}
	if d.RedirectURI == "" {
		return nil, errors.New("feishu: empty redirect URI for service")
	}
	starter := d.Starter
	if starter == nil {
		// The concrete *Provisioner implements both interfaces; reuse it.
		if s, ok := d.Provisioner.(AppStarter); ok {
			starter = s
		}
	}
	return &FeishuService{
		store:        d.Store,
		signer:       d.Signer,
		exchanger:    d.Provisioner,
		starter:      starter,
		answer:       d.Answer,
		runs:         d.Runs,
		webBaseURL:   strings.TrimRight(d.WebBaseURL, "/"),
		authorizeURL: d.AuthorizeURL,
		redirectURI:  d.RedirectURI,
		scopes:       d.ScopesCSV,
		now:          time.Now,
	}, nil
}

// Status reports the connection state for userID (design.md §5 status contract).
// No row → none. A row with a future (or unknown/nil) expiry → active; a past
// expiry → expired. nil expiry is treated as active per §3 (飞书 may omit
// expires_in; do not misjudge as expired).
func (s *FeishuService) Status(ctx context.Context, userID uint) (*StatusResult, error) {
	acc, err := s.store.Get(ctx, userID, ProviderLark)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &StatusResult{Connected: false, Status: "none", Scopes: []string{}}, nil
		}
		return nil, fmt.Errorf("feishu.Status: load account (user %d): %w", userID, err)
	}
	st := "active"
	if acc.TokenExpiresAt != nil && !acc.TokenExpiresAt.After(s.now()) {
		st = "expired"
	}
	return &StatusResult{
		Connected: true,
		Status:    st,
		Scopes:    splitScopes(acc.Scopes),
		AppID:     acc.AppID,
	}, nil
}

// Connect decides the next step and returns the URL the frontend/agent card
// shows the user. runID + questionText come from the paused run that triggered
// the connection (the agent auth-yield); they are encoded into the state so the
// callback can resume that exact run with the matching answer key.
//
//   - No app row yet → NextStepCreateApp + the device-code page URL (no state,
//     because that flow has no 飞书 redirect — resume is driven by a later
//     re-Connect once the app exists; design.md §5 注).
//   - App exists → NextStepAuthorize + the 飞书 authorize URL carrying a freshly
//     signed state.
func (s *FeishuService) Connect(ctx context.Context, userID uint, runID uint64, questionText string) (*ConnectResult, error) {
	acc, err := s.store.Get(ctx, userID, ProviderLark)
	switch {
	case err == nil && acc.AppID != "":
		// App exists → authorize step.
		state, serr := s.signState(ctx, userID, runID, questionText)
		if serr != nil {
			return nil, serr
		}
		return &ConnectResult{
			NextStep: NextStepAuthorize,
			URL:      s.buildAuthorizeURL(acc.AppID, state),
			State:    state,
		}, nil
	case err == nil || errors.Is(err, gorm.ErrRecordNotFound):
		// No usable app row → create-app step.
		if s.starter == nil {
			return nil, fmt.Errorf("%w: app provisioning unavailable", errno.ErrLarkCallFailed)
		}
		pageURL, _, perr := s.starter.StartProvision(ctx, userID)
		if perr != nil {
			return nil, fmt.Errorf("feishu.Connect: start provision (user %d): %w", userID, perr)
		}
		return &ConnectResult{NextStep: NextStepCreateApp, URL: pageURL}, nil
	default:
		return nil, fmt.Errorf("feishu.Connect: load account (user %d): %w", userID, err)
	}
}

// Unbind deletes the stored token row (the 飞书 app itself is kept; design.md §5).
// Idempotent: deleting a non-existent connection is not an error.
func (s *FeishuService) Unbind(ctx context.Context, userID uint) error {
	if err := s.store.Delete(ctx, userID, ProviderLark); err != nil {
		return fmt.Errorf("feishu.Unbind: delete account (user %d): %w", userID, err)
	}
	return nil
}

// HandleCallback is the no-JWT 飞书 redirect target. It returns a *CallbackResult
// the controller 302s to; on ANY failure it returns BOTH a non-nil error (for
// logging/observability) AND a CallbackResult with an error redirect, so the
// browser always lands somewhere sensible.
//
// Order (security-critical):
//  1. Verify state (HMAC + exp + one-time nonce consume). Failure → error redirect.
//  2. Load the run; enforce run.UserID == state.UserID (cross-user defense).
//  3. Idempotency: if the run is NOT waiting AND a token already exists → success
//     redirect WITHOUT re-exchanging or re-resuming (duplicate/refresh callback).
//  4. Exchange the code (encrypted tokens), UPSERT (idempotent).
//  5. If the run IS waiting → resume via biz.Answer with key=question_text.
//  6. Success redirect.
func (s *FeishuService) HandleCallback(ctx context.Context, code, state string) (*CallbackResult, error) {
	// 1. State verification (covers malformed/expired/replayed → ErrLarkStateInvalid).
	payload, err := s.signer.Verify(ctx, state)
	if err != nil {
		// Do not log the raw state (it is HMAC-bearing) — Verify already returns a
		// non-sensitive reason wrapped in ErrLarkStateInvalid.
		return s.errorRedirect("invalid_state"), fmt.Errorf("feishu.HandleCallback: verify state: %w", err)
	}

	userID := payload.UserID
	runID, parseErr := strconv.ParseUint(payload.RunID, 10, 64)
	if parseErr != nil {
		return s.errorRedirect("invalid_state"),
			fmt.Errorf("%w: state run_id %q not numeric", errno.ErrLarkStateInvalid, payload.RunID)
	}

	if code == "" {
		return s.errorRedirect("missing_code"),
			fmt.Errorf("%w: callback missing authorization code", errno.ErrLarkStateInvalid)
	}

	// 2. Load run + cross-user defense. A missing run is treated as a state mismatch.
	run, err := s.runs.GetRun(ctx, runID)
	if err != nil {
		return s.errorRedirect("invalid_state"),
			fmt.Errorf("%w: load run %d for callback: %v", errno.ErrLarkStateInvalid, runID, err)
	}
	if run.UserID != userID {
		// Cross-user: the state claims a user that does not own the run. Reject
		// WITHOUT exchanging code / upserting / resuming (design.md §5).
		return s.errorRedirect("invalid_state"),
			fmt.Errorf("%w: state user %d != run %d owner %d (cross-user)", errno.ErrLarkStateInvalid, userID, runID, run.UserID)
	}

	waiting := run.StateReason == "waiting_for_user_choice"

	// 3. Idempotency: a duplicate/refresh callback where the run already left
	// waiting AND a token already exists → just redirect to success.
	if !waiting {
		if acc, gerr := s.store.Get(ctx, userID, ProviderLark); gerr == nil && len(acc.AccessTokenEnc) > 0 {
			log.Infow("feishu callback: idempotent no-op (run not waiting, token present)",
				"user_id", userID, "run_id", runID, "state_reason", run.StateReason)
			return s.successRedirect(), nil
		}
		// else: no token yet — fall through and still persist it (rare abandon→
		// reauthorize edge); but we will NOT resume a non-waiting run.
	}

	// Need the user's app_id to exchange against.
	acc, err := s.store.Get(ctx, userID, ProviderLark)
	if err != nil || acc.AppID == "" {
		return s.errorRedirect("not_connected"),
			fmt.Errorf("%w: no app to exchange against (user %d): %v", errno.ErrLarkNotConnected, userID, err)
	}

	// 4. Exchange code → encrypted tokens, then UPSERT (idempotent on user+provider).
	accessEnc, refreshEnc, exp, scopes, exErr := s.exchanger.ExchangeCode(ctx, acc.AppID, code)
	if exErr != nil {
		return s.errorRedirect("exchange_failed"),
			fmt.Errorf("feishu.HandleCallback: exchange code (user %d): %w", userID, exErr)
	}

	row := &model.UserThirdPartyAccount{
		UserID:          userID,
		Provider:        ProviderLark,
		AppID:           acc.AppID,
		AppSecretEnc:    acc.AppSecretEnc, // preserve existing encrypted app secret
		AccessTokenEnc:  accessEnc,
		RefreshTokenEnc: refreshEnc,
		TokenExpiresAt:  exp,
		Scopes:          firstNonEmpty(scopes, acc.Scopes, s.scopes),
	}
	if err := s.store.Upsert(ctx, row); err != nil {
		return s.errorRedirect("store_failed"),
			fmt.Errorf("feishu.HandleCallback: upsert tokens (user %d): %w", userID, err)
	}

	// 5. Resume the paused run — ONLY if it is actually waiting. The answer key
	// MUST equal the pending question text carried in the state (answer.go enforces
	// answers[qText] exists, else "question %q was not asked").
	if waiting {
		if rErr := s.answer.ResumeWithAnswer(ctx, userID, runID, payload.QuestionText, resumeFreeText); rErr != nil {
			// Token IS stored at this point (account is connected). Surface the
			// resume failure so the user retries from the agent card, but report
			// the connection itself succeeded by redirecting to success — the run
			// can be answered/retried separately. Log loud for diagnosis.
			log.Warnw("feishu callback: token stored but run resume failed",
				"user_id", userID, "run_id", runID, "error", rErr)
			return s.successRedirect(), fmt.Errorf("feishu.HandleCallback: resume run %d: %w", runID, rErr)
		}
	}

	log.Infow("feishu callback: connected"+map[bool]string{true: " + run resumed", false: ""}[waiting],
		"user_id", userID, "run_id", runID)
	return s.successRedirect(), nil
}

// --- helpers ---------------------------------------------------------------

// signState mints a signed OAuth state binding (userID, runID, questionText).
func (s *FeishuService) signState(ctx context.Context, userID uint, runID uint64, questionText string) (string, error) {
	state, err := s.signer.Sign(ctx, Payload{
		UserID:       userID,
		RunID:        strconv.FormatUint(runID, 10),
		Step:         NextStepAuthorize,
		QuestionText: questionText,
	}, stateValidity)
	if err != nil {
		return "", fmt.Errorf("feishu: sign state (user %d): %w", userID, err)
	}
	return state, nil
}

// buildAuthorizeURL builds the 飞书 OAuth authorize URL (design.md §5). It
// requests the first-batch scopes in one shot (缺则后续 403, design.md §8).
func (s *FeishuService) buildAuthorizeURL(appID, state string) string {
	q := url.Values{}
	q.Set("app_id", appID)
	q.Set("redirect_uri", s.redirectURI)
	q.Set("state", state)
	if s.scopes != "" {
		q.Set("scope", s.scopes)
	}
	sep := "?"
	if strings.Contains(s.authorizeURL, "?") {
		sep = "&"
	}
	return s.authorizeURL + sep + q.Encode()
}

// successRedirect / errorRedirect build the frontend 302 targets (design.md §5).
func (s *FeishuService) successRedirect() *CallbackResult {
	return &CallbackResult{
		RedirectURL: s.webBaseURL + "/settings/connections?feishu=connected",
		Success:     true,
	}
}

func (s *FeishuService) errorRedirect(reason string) *CallbackResult {
	return &CallbackResult{
		RedirectURL: s.webBaseURL + "/settings/connections?feishu=error&reason=" + url.QueryEscape(reason),
		Success:     false,
	}
}

// splitScopes splits a space-separated scope string into a slice (never nil so
// the JSON serializes as [] not null).
func splitScopes(scopes string) []string {
	out := []string{}
	out = append(out, strings.Fields(scopes)...)
	return out
}

// firstNonEmpty returns the first non-empty string (used to keep the best scope
// info: the exchange response, else the existing row, else the configured set).
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
