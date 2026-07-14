package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

const (
	authSessionDefaultLeaseDuration     = 2 * time.Minute
	authSessionDefaultDuration          = 12 * time.Minute
	authSessionDefaultHeartbeatInterval = 30 * time.Second
	authSessionDefaultStartTimeout      = 30 * time.Second
	authSessionCLIHardCeiling           = 12 * time.Minute
	authSessionCLIStatusTimeout         = 15 * time.Second
	authSessionCLIMaxLineBytes          = 1 << 20
	authSessionFinalizeTimeout          = 5 * time.Second
)

var (
	// ErrAuthSessionUnavailable is the public fail-closed error for malformed
	// recovery input, invalid official URLs, lease loss, and dependency failures.
	ErrAuthSessionUnavailable = errors.New("feishu authorization session unavailable")
)

// OperationResumeDispatcher resumes the already-persisted operation after a
// blocking authorization worker has sealed HOME and committed session state.
type OperationResumeDispatcher interface {
	DispatchResume(ctx context.Context, userID uint, operationID string) error
}

// AuthSessionCLI owns the blocking lark-cli lifecycle. RunBlocking must invoke
// onURL as soon as the CLI prints its verification URL, then continue waiting
// until the user finishes. AuthStatus is reserved for expired-lease recovery.
type AuthSessionCLI interface {
	RunBlocking(ctx context.Context, home string, argv []string, onURL func(string) error) error
	AuthStatus(ctx context.Context, home string) (bool, error)
}

// AuthSessionAccountStore is the account subset required to establish the
// active tenant generation before creating a durable auth session.
type AuthSessionAccountStore interface {
	Get(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error)
	EnsurePlaceholder(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error)
}

// AuthSessionStore persists only restart-safe session metadata. Verification
// URLs are intentionally absent from this interface and its model.
type AuthSessionStore interface {
	CreateOrGetPendingSession(ctx context.Context, session *model.FeishuAuthSession) (*model.FeishuAuthSession, bool, error)
	GetSessionForUser(ctx context.Context, userID uint, generation uint64, id string) (*model.FeishuAuthSession, error)
	SupersedeSessionForUser(ctx context.Context, userID uint, generation uint64, id string, now time.Time) error
	ClaimSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	RenewSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error)
	UpdateSessionState(ctx context.Context, userID uint, generation uint64, id, owner, state string, now time.Time, completedAt *time.Time) error
	FinalizeSessionCompleted(ctx context.Context, userID uint, generation uint64, id, owner, accountState string, connected bool, now time.Time) error
	UpdateAccountConnectionState(ctx context.Context, userID uint, generation uint64, state string, connected bool, now time.Time) error
}

// AuthSessionServiceDeps wires deterministic authorization without depending on
// the Agent package or the legacy connection runner.
type AuthSessionServiceDeps struct {
	Accounts   AuthSessionAccountStore
	Sessions   AuthSessionStore
	Vault      OperationHomeVault
	CLI        AuthSessionCLI
	Dispatcher OperationResumeDispatcher
	Owner      string

	Now               func() time.Time
	NewID             func() string
	NewLeaseToken     func() string
	LeaseDuration     time.Duration
	SessionDuration   time.Duration
	HeartbeatInterval time.Duration
	StartTimeout      time.Duration
}

type authSessionURLKey struct {
	userID     uint
	generation uint64
	sessionID  string
}

type authSessionURLValue struct {
	value     string
	expiresAt time.Time
}

type authSessionURLRegistry struct {
	mu     sync.RWMutex
	values map[authSessionURLKey]authSessionURLValue
}

type authSessionActivation struct {
	activate chan struct{}
	abort    chan struct{}
	once     sync.Once
}

func newAuthSessionActivation() *authSessionActivation {
	return &authSessionActivation{activate: make(chan struct{}), abort: make(chan struct{})}
}

func (a *authSessionActivation) allow() {
	a.once.Do(func() { close(a.activate) })
}

func (a *authSessionActivation) stop() {
	a.once.Do(func() { close(a.abort) })
}

func newAuthSessionURLRegistry() *authSessionURLRegistry {
	return &authSessionURLRegistry{values: make(map[authSessionURLKey]authSessionURLValue)}
}

func (r *authSessionURLRegistry) put(key authSessionURLKey, value string, expiresAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = authSessionURLValue{value: value, expiresAt: expiresAt.UTC()}
}

func (r *authSessionURLRegistry) get(key authSessionURLKey, now time.Time) string {
	r.mu.RLock()
	value, ok := r.values[key]
	r.mu.RUnlock()
	if !ok || !value.expiresAt.After(now.UTC()) {
		return ""
	}
	return value.value
}

func (r *authSessionURLRegistry) remove(key authSessionURLKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
}

// AuthSessionService creates one durable session per canonical recovery intent,
// while keeping short-lived external URLs in process memory only.
type AuthSessionService struct {
	accounts   AuthSessionAccountStore
	sessions   AuthSessionStore
	vault      OperationHomeVault
	cli        AuthSessionCLI
	dispatcher OperationResumeDispatcher

	now               func() time.Time
	newID             func() string
	newLeaseToken     func() string
	leaseDuration     time.Duration
	sessionDuration   time.Duration
	heartbeatInterval time.Duration
	startTimeout      time.Duration
	urls              *authSessionURLRegistry
	activationMu      sync.Mutex
	activations       map[string]*authSessionActivation
	workerMu          sync.Mutex
	workers           map[authSessionURLKey]context.CancelFunc
}

// NewAuthSessionService validates the one-way authorization dependencies.
func NewAuthSessionService(deps AuthSessionServiceDeps) (*AuthSessionService, error) {
	if deps.Accounts == nil || deps.Sessions == nil || deps.Vault == nil || deps.CLI == nil ||
		deps.Dispatcher == nil || strings.TrimSpace(deps.Owner) == "" {
		return nil, fmt.Errorf("%w: missing dependency", ErrAuthSessionUnavailable)
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newID := deps.NewID
	if newID == nil {
		newID = func() string { return uuid.NewString() }
	}
	newLeaseToken := deps.NewLeaseToken
	if newLeaseToken == nil {
		ownerPrefix := strings.TrimSpace(deps.Owner)
		if len(ownerPrefix) > 80 {
			ownerPrefix = ownerPrefix[:80]
		}
		newLeaseToken = func() string { return ownerPrefix + ":" + uuid.NewString() }
	}
	leaseDuration := positiveDurationOr(deps.LeaseDuration, authSessionDefaultLeaseDuration)
	sessionDuration := positiveDurationOr(deps.SessionDuration, authSessionDefaultDuration)
	heartbeatInterval := positiveDurationOr(deps.HeartbeatInterval, authSessionDefaultHeartbeatInterval)
	startTimeout := positiveDurationOr(deps.StartTimeout, authSessionDefaultStartTimeout)
	if heartbeatInterval >= leaseDuration {
		return nil, fmt.Errorf("%w: heartbeat must precede lease expiry", ErrAuthSessionUnavailable)
	}
	return &AuthSessionService{
		accounts: deps.Accounts, sessions: deps.Sessions, vault: deps.Vault, cli: deps.CLI,
		dispatcher: deps.Dispatcher, now: now, newID: newID, newLeaseToken: newLeaseToken,
		leaseDuration: leaseDuration, sessionDuration: sessionDuration,
		heartbeatInterval: heartbeatInterval, startTimeout: startTimeout,
		urls: newAuthSessionURLRegistry(), activations: make(map[string]*authSessionActivation),
		workers: make(map[authSessionURLKey]context.CancelFunc),
	}, nil
}

func (s *AuthSessionService) registerWorker(session *model.FeishuAuthSession, cancel context.CancelFunc) {
	if session == nil || cancel == nil {
		return
	}
	s.workerMu.Lock()
	s.workers[authSessionRegistryKey(session)] = cancel
	s.workerMu.Unlock()
}

func (s *AuthSessionService) removeWorker(session *model.FeishuAuthSession, cancel context.CancelFunc) {
	if session == nil {
		return
	}
	key := authSessionRegistryKey(session)
	s.workerMu.Lock()
	if current := s.workers[key]; current != nil {
		delete(s.workers, key)
	}
	s.workerMu.Unlock()
}

// StopGeneration stops only locally-owned workers for an already-retired
// account generation. The persistent generation fence is established by the
// lifecycle store first, so this is a prompt cleanup measure rather than the
// cross-process safety boundary.
func (s *AuthSessionService) StopGeneration(userID uint, generation uint64) {
	if s == nil || userID == 0 || generation == 0 {
		return
	}
	sessionIDs := make([]string, 0)
	s.workerMu.Lock()
	for key := range s.workers {
		if key.userID == userID && key.generation == generation {
			sessionIDs = append(sessionIDs, key.sessionID)
		}
	}
	s.workerMu.Unlock()
	for _, sessionID := range sessionIDs {
		s.stopSession(userID, generation, sessionID)
	}
}

func (s *AuthSessionService) stopSession(userID uint, generation uint64, sessionID string) {
	key := authSessionURLKey{userID: userID, generation: generation, sessionID: sessionID}
	s.workerMu.Lock()
	cancel := s.workers[key]
	s.workerMu.Unlock()
	s.Abort(sessionID)
	if cancel != nil {
		cancel()
	}
}

func positiveDurationOr(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func (s *AuthSessionService) leaseToken() (string, error) {
	token := strings.TrimSpace(s.newLeaseToken())
	if token == "" || len(token) > 128 {
		return "", ErrAuthSessionUnavailable
	}
	return token, nil
}

// Activate releases an operation-linked worker only after the operation's
// waiting state has been durably persisted. App-scope sessions have no worker,
// so an unknown session is an intentional no-op.
func (s *AuthSessionService) Activate(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return ErrAuthSessionUnavailable
	}
	s.activationMu.Lock()
	activation := s.activations[sessionID]
	s.activationMu.Unlock()
	if activation != nil {
		activation.allow()
	}
	return nil
}

// Abort cancels a worker that must not complete because waiting persistence or
// activation failed.
func (s *AuthSessionService) Abort(sessionID string) {
	s.activationMu.Lock()
	activation := s.activations[sessionID]
	s.activationMu.Unlock()
	if activation != nil {
		activation.stop()
	}
}

func (s *AuthSessionService) registerActivation(sessionID string) (*authSessionActivation, error) {
	activation := newAuthSessionActivation()
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	if _, exists := s.activations[sessionID]; exists {
		return nil, ErrAuthSessionUnavailable
	}
	s.activations[sessionID] = activation
	return activation, nil
}

func (s *AuthSessionService) removeActivation(sessionID string, activation *authSessionActivation) {
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	if s.activations[sessionID] == activation {
		delete(s.activations, sessionID)
	}
}

func (s *AuthSessionService) waitForActivation(ctx context.Context, activation *authSessionActivation) error {
	if activation == nil {
		return nil
	}
	timer := time.NewTimer(s.startTimeout)
	defer timer.Stop()
	select {
	case <-activation.activate:
		return nil
	case <-activation.abort:
		return ErrAuthSessionUnavailable
	case <-ctx.Done():
		return ErrAuthSessionUnavailable
	case <-timer.C:
		return ErrAuthSessionUnavailable
	}
}

// ConnectManual requests only the long-lived identity scope. Business scopes
// remain incremental and are never preloaded by this settings-page action.
func (s *AuthSessionService) ConnectManual(ctx context.Context, userID uint) (*OperationAction, error) {
	if userID == 0 {
		return nil, ErrAuthSessionUnavailable
	}
	account, err := s.accounts.Get(ctx, userID, ProviderLark)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		account, err = s.accounts.EnsurePlaceholder(ctx, userID, ProviderLark)
	}
	if err != nil || account == nil || account.Generation == 0 {
		return nil, ErrAuthSessionUnavailable
	}
	kind := RecoveryReauth
	if account.ConnectionState == model.FeishuConnectionNone || account.ConnectionState == model.FeishuConnectionCreatingApp {
		kind = RecoveryCreateApp
	}
	return s.start(ctx, RecoveryRequest{
		UserID: userID, Generation: account.Generation, Kind: kind,
		Scopes: []string{"offline_access"},
	}, true)
}

// StartRecovery implements RecoveryStarter for server-owned operation metadata.
func (s *AuthSessionService) StartRecovery(ctx context.Context, request RecoveryRequest) (*OperationAction, error) {
	return s.start(ctx, request, false)
}

func (s *AuthSessionService) start(
	ctx context.Context,
	request RecoveryRequest,
	manual bool,
) (*OperationAction, error) {
	if request.UserID == 0 || request.Generation == 0 || (!manual && request.OperationID == "") {
		return nil, ErrAuthSessionUnavailable
	}
	account, err := s.accounts.Get(ctx, request.UserID, ProviderLark)
	if errors.Is(err, gorm.ErrRecordNotFound) && request.Kind == RecoveryCreateApp {
		account, err = s.accounts.EnsurePlaceholder(ctx, request.UserID, ProviderLark)
	}
	if err != nil || account == nil || account.Generation != request.Generation {
		return nil, ErrAuthSessionUnavailable
	}

	phase, argv, scopes, err := authSessionPlan(request.Kind, request.Scopes)
	if err != nil {
		return nil, err
	}
	if manual && (len(scopes) != 1 || scopes[0] != "offline_access" ||
		(request.Kind != RecoveryCreateApp && request.Kind != RecoveryReauth)) {
		return nil, ErrAuthSessionUnavailable
	}
	if request.Kind == RecoveryAppScope && request.ConsoleURL != "" {
		if err := validateOfficialConsoleURL(request.ConsoleURL); err != nil {
			return nil, ErrAuthSessionUnavailable
		}
	}

	operationID := request.OperationID
	var operationIDPointer *string
	if operationID != "" {
		operationIDPointer = &operationID
	}
	requestedScopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return nil, ErrAuthSessionUnavailable
	}
	now := s.now().UTC()
	candidate := &model.FeishuAuthSession{
		ID: s.newID(), UserID: request.UserID, Generation: request.Generation,
		OperationID: operationIDPointer, Phase: phase, RequestedScopesJSON: requestedScopesJSON,
		State: model.FeishuAuthSessionPending, ExpiresAt: now.Add(s.sessionDuration),
	}
	session, created, err := s.sessions.CreateOrGetPendingSession(ctx, candidate)
	if err != nil || session == nil {
		return nil, ErrAuthSessionUnavailable
	}
	if !authSessionMatches(session, request.UserID, request.Generation, operationIDPointer, phase, requestedScopesJSON) {
		return nil, ErrAuthSessionUnavailable
	}
	if session.State == model.FeishuAuthSessionCompleted {
		return nil, nil
	}

	if phase == model.FeishuAuthPhaseAppScope {
		if request.ConsoleURL == "" && created {
			return nil, ErrAuthSessionUnavailable
		}
		if request.ConsoleURL != "" {
			s.urls.put(authSessionRegistryKey(session), request.ConsoleURL, session.ExpiresAt)
		}
		if err := s.sessions.UpdateAccountConnectionState(
			ctx, session.UserID, session.Generation, model.FeishuConnectionWaitingAppApproval, false, now,
		); err != nil {
			return nil, ErrAuthSessionUnavailable
		}
		return s.actionFor(session, scopes), nil
	}

	if !created {
		if session.LeaseUntil != nil && session.LeaseUntil.After(now) {
			// Another process owns the live worker. Its transient URL is not in
			// this process; Task 13 may add an explicit same-session URL refresh.
			return s.actionFor(session, scopes), nil
		}
		return s.recoverExpired(ctx, session, request, scopes)
	}
	return s.claimAndStart(ctx, session, request, argv, scopes)
}

func authSessionPlan(kind RecoveryKind, requested []string) (string, []string, []string, error) {
	scopes, err := canonicalAuthScopes(requested)
	if err != nil {
		return "", nil, nil, err
	}
	switch kind {
	case RecoveryCreateApp:
		return model.FeishuAuthPhaseCreateApp, []string{"config", "init", "--new"}, scopes, nil
	case RecoveryAppScope:
		return model.FeishuAuthPhaseAppScope, nil, scopes, nil
	case RecoveryUserScope, RecoveryReauth:
		if len(scopes) == 0 {
			return "", nil, nil, ErrAuthSessionUnavailable
		}
		return model.FeishuAuthPhaseUserAuth,
			[]string{"auth", "login", "--json", "--scope", strings.Join(scopes, " ")}, scopes, nil
	default:
		return "", nil, nil, ErrAuthSessionUnavailable
	}
}

func canonicalAuthScopes(requested []string) ([]string, error) {
	if len(requested) > 64 {
		return nil, ErrAuthSessionUnavailable
	}
	seen := make(map[string]struct{}, len(requested))
	result := make([]string, 0, len(requested))
	for _, scope := range requested {
		if scope == "" || len(scope) > 128 || strings.TrimSpace(scope) != scope ||
			strings.HasPrefix(strings.ToLower(scope), "im"+":") {
			return nil, ErrAuthSessionUnavailable
		}
		for index := 0; index < len(scope); index++ {
			char := scope[index]
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') || strings.ContainsRune(":._/-", rune(char)) {
				continue
			}
			return nil, ErrAuthSessionUnavailable
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	sort.Strings(result)
	return result, nil
}

func authSessionMatches(
	session *model.FeishuAuthSession,
	userID uint,
	generation uint64,
	operationID *string,
	phase string,
	scopesJSON []byte,
) bool {
	if session == nil || session.UserID != userID || session.Generation != generation || session.Phase != phase ||
		(session.State != model.FeishuAuthSessionPending && session.State != model.FeishuAuthSessionCompleted) ||
		!authSessionScopeJSONEqual(session.RequestedScopesJSON, scopesJSON) {
		return false
	}
	if operationID == nil {
		return session.OperationID == nil
	}
	return session.OperationID != nil && *session.OperationID == *operationID
}

func authSessionScopeJSONEqual(left, right []byte) bool {
	var leftScopes []string
	if err := json.Unmarshal(left, &leftScopes); err != nil {
		return false
	}
	var rightScopes []string
	if err := json.Unmarshal(right, &rightScopes); err != nil || len(leftScopes) != len(rightScopes) {
		return false
	}
	for index := range leftScopes {
		if leftScopes[index] != rightScopes[index] {
			return false
		}
	}
	return true
}

func parseOfficialLarkURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return nil, ErrAuthSessionUnavailable
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return nil, ErrAuthSessionUnavailable
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "open.feishu.cn", "open.larksuite.com":
	default:
		return nil, ErrAuthSessionUnavailable
	}
	if parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") {
		return nil, ErrAuthSessionUnavailable
	}
	return parsed, nil
}

func validateOfficialConsoleURL(raw string) error {
	parsed, err := parseOfficialLarkURL(raw)
	if err != nil || !strings.HasPrefix(parsed.EscapedPath(), "/app/") {
		return ErrAuthSessionUnavailable
	}
	return nil
}

func validateOfficialWorkerURL(raw, phase string) error {
	parsed, err := parseOfficialLarkURL(raw)
	if err != nil {
		return ErrAuthSessionUnavailable
	}
	path := parsed.EscapedPath()
	if phase == model.FeishuAuthPhaseCreateApp && strings.HasPrefix(path, "/page/cli") {
		return nil
	}
	if phase == model.FeishuAuthPhaseUserAuth && strings.HasPrefix(path, "/suite/passport/oauth/device") {
		return nil
	}
	return ErrAuthSessionUnavailable
}

func authSessionRegistryKey(session *model.FeishuAuthSession) authSessionURLKey {
	return authSessionURLKey{userID: session.UserID, generation: session.Generation, sessionID: session.ID}
}

func (s *AuthSessionService) actionFor(session *model.FeishuAuthSession, scopes []string) *OperationAction {
	action := &OperationAction{
		Provider: ProviderLark, SessionID: session.ID, Phase: session.Phase,
		Scopes: append([]string(nil), scopes...), ExpiresAt: session.ExpiresAt.UTC(),
	}
	if session.OperationID != nil {
		action.OperationID = *session.OperationID
	}
	action.URL = s.urls.get(authSessionRegistryKey(session), s.now())
	return action
}

func (s *AuthSessionService) recoverExpired(
	ctx context.Context,
	session *model.FeishuAuthSession,
	request RecoveryRequest,
	scopes []string,
) (*OperationAction, error) {
	now := s.now().UTC()
	leaseToken, err := s.leaseToken()
	if err != nil {
		return nil, err
	}
	claimed, err := s.sessions.ClaimSession(
		ctx, session.UserID, session.Generation, session.ID, leaseToken, now, now.Add(s.leaseDuration),
	)
	if err != nil {
		return nil, ErrAuthSessionUnavailable
	}
	if !claimed {
		fresh, getErr := s.sessions.GetSessionForUser(ctx, session.UserID, session.Generation, session.ID)
		if getErr != nil {
			return nil, ErrAuthSessionUnavailable
		}
		if fresh.State == model.FeishuAuthSessionCompleted {
			return nil, nil
		}
		if fresh.State != model.FeishuAuthSessionPending {
			return s.start(ctx, request, request.OperationID == "")
		}
		return s.actionFor(fresh, scopes), nil
	}

	authorized := false
	statusErr := s.vault.WithHome(ctx, session.UserID, session.Generation, func(home string) (bool, error) {
		value, checkErr := s.cli.AuthStatus(ctx, home)
		authorized = value
		return false, checkErr
	})
	if statusErr != nil {
		if supersedeErr := s.supersedeOwned(ctx, session, leaseToken, now); supersedeErr != nil {
			return nil, supersedeErr
		}
		return nil, ErrAuthSessionUnavailable
	}
	if authorized {
		if err := s.completeOwned(ctx, session, leaseToken, now, true, false); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := s.supersedeOwned(ctx, session, leaseToken, now); err != nil {
		return nil, ErrAuthSessionUnavailable
	}
	return s.start(ctx, request, request.OperationID == "")
}

func (s *AuthSessionService) claimAndStart(
	ctx context.Context,
	session *model.FeishuAuthSession,
	request RecoveryRequest,
	argv []string,
	scopes []string,
) (*OperationAction, error) {
	now := s.now().UTC()
	leaseToken, err := s.leaseToken()
	if err != nil {
		return nil, err
	}
	claimed, err := s.sessions.ClaimSession(
		ctx, session.UserID, session.Generation, session.ID, leaseToken, now, now.Add(s.leaseDuration),
	)
	if err != nil || !claimed {
		return nil, ErrAuthSessionUnavailable
	}
	state := model.FeishuConnectionWaitingUserAuth
	if session.Phase == model.FeishuAuthPhaseCreateApp {
		state = model.FeishuConnectionCreatingApp
	}
	if err := s.sessions.UpdateAccountConnectionState(ctx, session.UserID, session.Generation, state, false, now); err != nil {
		if supersedeErr := s.supersedeOwned(ctx, session, leaseToken, now); supersedeErr != nil {
			return nil, supersedeErr
		}
		return nil, ErrAuthSessionUnavailable
	}
	var activation *authSessionActivation
	if request.OperationID != "" {
		activation, err = s.registerActivation(session.ID)
		if err != nil {
			if supersedeErr := s.supersedeOwned(ctx, session, leaseToken, now); supersedeErr != nil {
				return nil, supersedeErr
			}
			return nil, err
		}
	}

	urlReady := make(chan struct{}, 1)
	done := make(chan error, 1)
	workerContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.registerWorker(session, cancel)
	go func() {
		defer s.removeWorker(session, cancel)
		done <- s.runWorker(workerContext, cancel, session, leaseToken, argv, urlReady, activation)
	}()

	timer := time.NewTimer(s.startTimeout)
	defer timer.Stop()
	select {
	case <-urlReady:
		return s.actionFor(session, scopes), nil
	case workerErr := <-done:
		if workerErr != nil {
			return nil, workerErr
		}
		return nil, nil
	case <-timer.C:
		s.Abort(session.ID)
		cancel()
		return nil, ErrAuthSessionUnavailable
	case <-ctx.Done():
		s.Abort(session.ID)
		cancel()
		return nil, ErrAuthSessionUnavailable
	}
}

// RefreshAction replaces one still-pending authorization session with a fresh
// worker and a new transient URL. The old URL/device flow is never reused or
// persisted. The session must belong to the caller's current generation.
func (s *AuthSessionService) RefreshAction(
	ctx context.Context,
	userID uint,
	generation uint64,
	sessionID string,
) (*OperationAction, error) {
	if s == nil || userID == 0 || generation == 0 || strings.TrimSpace(sessionID) == "" {
		return nil, ErrAuthSessionUnavailable
	}
	session, err := s.sessions.GetSessionForUser(ctx, userID, generation, sessionID)
	if err != nil || session == nil || session.State != model.FeishuAuthSessionPending {
		return nil, ErrAuthSessionUnavailable
	}
	var scopes []string
	if err := json.Unmarshal(session.RequestedScopesJSON, &scopes); err != nil {
		return nil, ErrAuthSessionUnavailable
	}
	canonicalScopes, err := canonicalAuthScopes(scopes)
	if err != nil || !authSessionScopeJSONEqual(session.RequestedScopesJSON, mustMarshalAuthScopes(canonicalScopes)) {
		return nil, ErrAuthSessionUnavailable
	}
	kind, err := refreshRecoveryKind(session)
	if err != nil {
		return nil, ErrAuthSessionUnavailable
	}
	if err := s.sessions.SupersedeSessionForUser(ctx, userID, generation, sessionID, s.now().UTC()); err != nil {
		return nil, ErrAuthSessionUnavailable
	}
	s.stopSession(userID, generation, sessionID)
	s.urls.remove(authSessionRegistryKey(session))
	request := RecoveryRequest{UserID: userID, Generation: generation, Kind: kind, Scopes: canonicalScopes}
	manual := session.OperationID == nil
	if session.OperationID != nil {
		request.OperationID = *session.OperationID
	}
	return s.start(ctx, request, manual)
}

func mustMarshalAuthScopes(scopes []string) []byte {
	encoded, _ := json.Marshal(scopes)
	return encoded
}

func refreshRecoveryKind(session *model.FeishuAuthSession) (RecoveryKind, error) {
	if session == nil {
		return RecoveryNone, ErrAuthSessionUnavailable
	}
	switch session.Phase {
	case model.FeishuAuthPhaseCreateApp:
		return RecoveryCreateApp, nil
	case model.FeishuAuthPhaseUserAuth:
		if session.OperationID == nil {
			return RecoveryReauth, nil
		}
		return RecoveryUserScope, nil
	// App-scope console URLs are transient classifier evidence. Recreating a
	// URL without that evidence would either persist it or guess a broad flow,
	// both of which violate the fail-closed contract.
	case model.FeishuAuthPhaseAppScope:
		return RecoveryNone, ErrAuthSessionUnavailable
	default:
		return RecoveryNone, ErrAuthSessionUnavailable
	}
}

func (s *AuthSessionService) runWorker(
	ctx context.Context,
	cancel context.CancelFunc,
	session *model.FeishuAuthSession,
	leaseToken string,
	argv []string,
	urlReady chan<- struct{},
	activation *authSessionActivation,
) error {
	defer cancel()
	defer s.removeActivation(session.ID, activation)
	heartbeatDone := make(chan struct{})
	go s.runHeartbeat(ctx, session, leaseToken, cancel, heartbeatDone)
	var activationPassed atomic.Bool

	runErr := s.vault.WithHome(ctx, session.UserID, session.Generation, func(home string) (bool, error) {
		err := s.cli.RunBlocking(ctx, home, append([]string(nil), argv...), func(rawURL string) error {
			if err := validateOfficialWorkerURL(rawURL, session.Phase); err != nil {
				return ErrAuthSessionUnavailable
			}
			s.urls.put(authSessionRegistryKey(session), rawURL, session.ExpiresAt)
			select {
			case urlReady <- struct{}{}:
			default:
			}
			if err := s.waitForActivation(ctx, activation); err != nil {
				return err
			}
			if activation != nil {
				activationPassed.Store(true)
			}
			return nil
		})
		return err == nil, err
	})
	if runErr == nil && activation != nil && !activationPassed.Load() {
		runErr = ErrAuthSessionUnavailable
	}
	cancel()
	<-heartbeatDone
	if runErr != nil {
		now := s.now().UTC()
		finalizeCtx, finalizeCancel := authSessionDetachedContext(ctx)
		_ = s.sessions.UpdateSessionState(finalizeCtx, session.UserID, session.Generation, session.ID, leaseToken,
			model.FeishuAuthSessionFailed, now, nil)
		finalizeCancel()
		s.urls.remove(authSessionRegistryKey(session))
		return ErrAuthSessionUnavailable
	}
	if err := s.completeOwned(ctx, session, leaseToken, s.now().UTC(), false, session.OperationID != nil); err != nil {
		return err
	}
	return nil
}

func (s *AuthSessionService) runHeartbeat(
	ctx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	cancel context.CancelFunc,
	done chan<- struct{},
) {
	defer close(done)
	ticker := time.NewTicker(s.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.now().UTC()
			renewed, err := s.sessions.RenewSession(
				ctx, session.UserID, session.Generation, session.ID, leaseToken, now, now.Add(s.leaseDuration),
			)
			if err != nil || !renewed {
				cancel()
				return
			}
		}
	}
}

func (s *AuthSessionService) completeOwned(
	ctx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	now time.Time,
	authorizedRecovery bool,
	dispatch bool,
) error {
	completedAt := now.UTC()
	state := model.FeishuConnectionConnected
	connected := true
	if session.Phase == model.FeishuAuthPhaseCreateApp && !authorizedRecovery {
		state = model.FeishuConnectionAppReady
		connected = false
	}
	finalizeCtx, finalizeCancel := authSessionDetachedContext(ctx)
	err := s.sessions.FinalizeSessionCompleted(
		finalizeCtx, session.UserID, session.Generation, session.ID, leaseToken, state, connected, completedAt,
	)
	finalizeCancel()
	if err != nil {
		return ErrAuthSessionUnavailable
	}
	s.urls.remove(authSessionRegistryKey(session))
	if dispatch && session.OperationID != nil && *session.OperationID != "" {
		return s.dispatchResumeDetached(ctx, session.UserID, *session.OperationID)
	}
	if session.Phase == model.FeishuAuthPhaseCreateApp && !authorizedRecovery {
		chainCtx, chainCancel := authSessionManualChainContext(ctx, s.startTimeout)
		_, chainErr := s.start(chainCtx, RecoveryRequest{
			UserID: session.UserID, Generation: session.Generation, Kind: RecoveryReauth,
			Scopes: []string{"offline_access"},
		}, true)
		chainCancel()
		if chainErr != nil {
			return ErrAuthSessionUnavailable
		}
	}
	return nil
}

func authSessionManualChainContext(ctx context.Context, startTimeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if startTimeout <= 0 {
		startTimeout = authSessionDefaultStartTimeout
	}
	// The chain is bounded by the CLI hard ceiling, while preserving the full
	// configured URL-start window plus detached finalization overhead.
	if startTimeout > authSessionCLIHardCeiling {
		startTimeout = authSessionCLIHardCeiling
	}
	return context.WithTimeout(context.WithoutCancel(ctx), startTimeout+authSessionFinalizeTimeout)
}

func authSessionDetachedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), authSessionFinalizeTimeout)
}

func (s *AuthSessionService) dispatchResumeDetached(ctx context.Context, userID uint, operationID string) error {
	dispatchCtx, dispatchCancel := authSessionDetachedContext(ctx)
	defer dispatchCancel()
	if err := s.dispatcher.DispatchResume(dispatchCtx, userID, operationID); err != nil {
		return ErrAuthSessionUnavailable
	}
	return nil
}

func (s *AuthSessionService) supersedeOwned(
	ctx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	now time.Time,
) error {
	finalizeCtx, finalizeCancel := authSessionDetachedContext(ctx)
	err := s.sessions.UpdateSessionState(
		finalizeCtx, session.UserID, session.Generation, session.ID, leaseToken,
		model.FeishuAuthSessionSuperseded, now.UTC(), nil,
	)
	finalizeCancel()
	s.urls.remove(authSessionRegistryKey(session))
	if err != nil {
		return ErrAuthSessionUnavailable
	}
	return nil
}

// CompleteAppApproval marks a user-confirmed app-scope approval under a fresh
// lease, then resumes the exact persisted operation. The replay itself verifies
// whether the approval is effective.
func (s *AuthSessionService) CompleteAppApproval(
	ctx context.Context,
	userID uint,
	generation uint64,
	sessionID string,
) error {
	session, err := s.sessions.GetSessionForUser(ctx, userID, generation, sessionID)
	if err != nil || session.Phase != model.FeishuAuthPhaseAppScope {
		return ErrAuthSessionUnavailable
	}
	if session.State == model.FeishuAuthSessionCompleted {
		if session.OperationID == nil || *session.OperationID == "" {
			return ErrAuthSessionUnavailable
		}
		return s.dispatchResumeDetached(ctx, session.UserID, *session.OperationID)
	}
	if session.State != model.FeishuAuthSessionPending {
		return ErrAuthSessionUnavailable
	}
	now := s.now().UTC()
	leaseToken, err := s.leaseToken()
	if err != nil {
		return err
	}
	claimed, err := s.sessions.ClaimSession(ctx, userID, generation, sessionID, leaseToken, now, now.Add(s.leaseDuration))
	if err != nil || !claimed {
		return ErrAuthSessionUnavailable
	}
	return s.completeOwned(ctx, session, leaseToken, now, true, true)
}

type authSessionStreamState struct {
	onURL  func(string) error
	cancel context.CancelFunc
	once   sync.Once
	mu     sync.Mutex
	err    error
	seen   bool
}

func (s *authSessionStreamState) observeLine(line []byte) {
	candidate := authSessionURLFromLine(string(line))
	if candidate == "" {
		return
	}
	if _, err := parseOfficialLarkURL(candidate); err != nil {
		return
	}
	s.once.Do(func() {
		if err := s.onURL(candidate); err != nil {
			s.fail(err)
			return
		}
		s.mu.Lock()
		s.seen = true
		s.mu.Unlock()
	})
}

func (s *authSessionStreamState) fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.err == nil {
		s.err = err
		s.cancel()
	}
	s.mu.Unlock()
}

func (s *authSessionStreamState) snapshotError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *authSessionStreamState) observedURL() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

func authSessionURLFromLine(line string) string {
	start := strings.Index(line, "https://")
	if start < 0 {
		return ""
	}
	remainder := line[start:]
	end := len(remainder)
	for index, char := range remainder {
		if unicodeSpaceOrURLDelimiter(char) {
			end = index
			break
		}
	}
	return strings.TrimRight(remainder[:end], "'\"<>),]")
}

func unicodeSpaceOrURLDelimiter(char rune) bool {
	return char == ' ' || char == '\t' || char == '\r' || char == '\n' || char == '\x00' ||
		char == '\'' || char == '"' || char == '<' || char == '>'
}

func captureAuthSessionStream(
	reader *os.File,
	limit int,
	state *authSessionStreamState,
) <-chan controlledCapture {
	result := make(chan controlledCapture, 1)
	go func() {
		defer close(result)
		defer reader.Close()
		capture := controlledCapture{bytes: make([]byte, 0, min(limit, 32<<10))}
		line := make([]byte, 0, 4096)
		chunk := make([]byte, 32<<10)
		for {
			n, readErr := reader.Read(chunk)
			if n > 0 {
				remaining := limit - len(capture.bytes)
				if remaining > 0 {
					keep := min(n, remaining)
					capture.bytes = append(capture.bytes, chunk[:keep]...)
					if keep < n {
						capture.truncated = true
						state.fail(errControlledCLIOutputLimit)
					}
				} else {
					capture.truncated = true
					state.fail(errControlledCLIOutputLimit)
				}
				line = append(line, chunk[:n]...)
				for {
					newline := bytes.IndexByte(line, '\n')
					if newline < 0 {
						break
					}
					state.observeLine(line[:newline])
					line = append(line[:0], line[newline+1:]...)
				}
				if len(line) > authSessionCLIMaxLineBytes {
					state.fail(errControlledCLIOutputLimit)
					line = line[:0]
				}
			}
			if readErr != nil {
				if len(line) > 0 {
					state.observeLine(line)
				}
				if !errors.Is(readErr, io.EOF) {
					state.fail(readErr)
				}
				result <- capture
				return
			}
		}
	}()
	return result
}

func validateAuthSessionCLIArgv(argv []string) error {
	if err := validateControlledCLIInput(argv, nil); err != nil {
		return err
	}
	if len(argv) == 3 && argv[0] == "config" && argv[1] == "init" && argv[2] == "--new" {
		return nil
	}
	if len(argv) != 5 || argv[0] != "auth" || argv[1] != "login" || argv[2] != "--json" || argv[3] != "--scope" {
		return errControlledCLIInvalidInput
	}
	scopes, err := canonicalAuthScopes(strings.Fields(argv[4]))
	if err != nil || len(scopes) == 0 || strings.Join(scopes, " ") != argv[4] {
		return errControlledCLIInvalidInput
	}
	return nil
}

func decodeAuthSessionFinalEnvelope(raw []byte) error {
	objectStart := bytes.IndexByte(raw, '{')
	if objectStart < 0 {
		return errControlledCLIInvalidJSON
	}
	prefix := bytes.TrimSpace(raw[:objectStart])
	if bytes.IndexByte(prefix, '{') >= 0 || bytes.IndexByte(prefix, '}') >= 0 {
		return errControlledCLIInvalidJSON
	}
	envelope, err := decodeControlledCLIEnvelope(bytes.TrimSpace(raw[objectStart:]))
	if err != nil {
		return err
	}
	if envelope == nil || !envelope.OK {
		return errControlledCLIBusiness
	}
	return nil
}

// RunBlocking implements AuthSessionCLI on the same hardened process boundary as
// business commands, while streaming at most one official URL before waiting for
// the final JSON success envelope.
func (r *ControlledLarkCLIRunner) RunBlocking(
	ctx context.Context,
	home string,
	argv []string,
	onURL func(string) error,
) error {
	if onURL == nil {
		return errControlledCLIInvalidInput
	}
	binary, err := r.binaryPath()
	if err != nil {
		return err
	}
	if err := validateControlledCLIHome(home); err != nil {
		return err
	}
	if err := validateAuthSessionCLIArgv(argv); err != nil {
		return err
	}

	runCtx, cancel := context.WithTimeout(ctx, authSessionCLIHardCeiling)
	defer cancel()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("feishu: create auth stdout pipe: %w", err)
	}
	defer stdoutReader.Close()
	defer stdoutWriter.Close()
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("feishu: create auth stderr pipe: %w", err)
	}
	defer stderrReader.Close()
	defer stderrWriter.Close()

	cmd := exec.CommandContext(runCtx, binary, argv...) // #nosec G204 -- fixed absolute binary; validated argv; no shell
	cmd.Env = controlledCLIEnvironment(home)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = controlledProcessGroupWait
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(killErr, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return killErr
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("feishu: start auth lark-cli: %w", err)
	}
	pid := cmd.Process.Pid
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	streamState := &authSessionStreamState{onURL: onURL, cancel: cancel}
	stdoutCapture := captureAuthSessionStream(stdoutReader, ControlledLarkCLIMaxStdoutBytes, streamState)
	stderrCapture := captureAuthSessionStream(stderrReader, ControlledLarkCLIMaxStderrBytes, streamState)

	waitErr := cmd.Wait()
	groupErr := terminateAndWaitControlledProcessGroup(pid, controlledProcessGroupWait)
	stdout := waitControlledCapture(stdoutCapture, stdoutReader, controlledProcessGroupWait)
	stderr := waitControlledCapture(stderrCapture, stderrReader, controlledProcessGroupWait)
	if streamErr := streamState.snapshotError(); streamErr != nil {
		return fmt.Errorf("feishu: auth lark-cli stream rejected: %w", streamErr)
	}
	if stdout.truncated || stderr.truncated {
		return fmt.Errorf("feishu: auth lark-cli output rejected: %w", errControlledCLIOutputLimit)
	}
	if runErr := runCtx.Err(); runErr != nil {
		return fmt.Errorf("feishu: auth lark-cli context ended: %w", runErr)
	}
	if groupErr != nil {
		return fmt.Errorf("feishu: reap auth lark-cli process group: %w", groupErr)
	}
	if waitErr != nil {
		return fmt.Errorf("feishu: auth lark-cli process failed: %w", waitErr)
	}
	if !streamState.observedURL() {
		return fmt.Errorf("feishu: auth lark-cli URL missing: %w", errControlledCLIInvalidJSON)
	}
	if err := decodeAuthSessionFinalEnvelope(stdout.bytes); err != nil {
		return fmt.Errorf("feishu: auth lark-cli final envelope rejected: %w", err)
	}
	return nil
}

type authSessionStatusEnvelope struct {
	OK         *bool `json:"ok"`
	Identities *struct {
		User *struct {
			Available bool `json:"available"`
		} `json:"user"`
	} `json:"identities"`
}

// AuthStatus is recovery-only. It does not run on the connected business hot
// path and accepts exactly one bounded JSON status object.
func (r *ControlledLarkCLIRunner) AuthStatus(ctx context.Context, home string) (bool, error) {
	binary, err := r.binaryPath()
	if err != nil {
		return false, err
	}
	if err := validateControlledCLIHome(home); err != nil {
		return false, err
	}
	argv := []string{"auth", "status", "--json"}
	result, waitErr, processErr := r.runProcess(ctx, binary, argv, nil, home, authSessionCLIStatusTimeout)
	if processErr != nil {
		return false, processErr
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return false, errControlledCLIOutputLimit
	}
	if waitErr != nil {
		return false, waitErr
	}
	decoder := json.NewDecoder(bytes.NewReader(result.Stdout))
	var status authSessionStatusEnvelope
	if err := decoder.Decode(&status); err != nil {
		return false, errControlledCLIInvalidJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false, errControlledCLIInvalidJSON
	}
	if status.OK != nil && !*status.OK {
		return false, nil
	}
	return status.Identities != nil && status.Identities.User != nil && status.Identities.User.Available, nil
}

var (
	_ RecoveryStarter  = (*AuthSessionService)(nil)
	_ AuthSessionStore = (store.IFeishuWorkspaceStore)(nil)
	_ AuthSessionCLI   = (*ControlledLarkCLIRunner)(nil)
)
