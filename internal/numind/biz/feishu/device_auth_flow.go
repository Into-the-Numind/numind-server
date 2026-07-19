package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

const (
	deviceAuthManualBindingID          = "manual"
	deviceAuthDefaultCompletionTimeout = 45 * time.Second
	deviceAuthMaxCompletionTimeout     = 45 * time.Second
	deviceAuthReconciliationTimeout    = 5 * time.Second
	deviceAuthMutationTimeout          = 5 * time.Second
)

var (
	ErrDeviceAuthProcessing = errors.New("feishu authorization is processing")
	ErrDeviceAuthConflict   = errors.New("feishu authorization state conflict")
	ErrDeviceAuthDependency = errors.New("feishu authorization dependency unavailable")
)

type AuthorizationNoticeCode string

const (
	AuthorizationPending    AuthorizationNoticeCode = "authorization_pending"
	AuthorizationProcessing AuthorizationNoticeCode = "authorization_processing"
	AuthorizationRejected   AuthorizationNoticeCode = "authorization_rejected"
	AuthorizationExpired    AuthorizationNoticeCode = "authorization_expired"
	AuthorizationUpdated    AuthorizationNoticeCode = "authorization_updated"
)

// DeviceAuthStore is the narrow persistence surface shared by the split start,
// completion, replacement, and fenced-publication phases.
type DeviceAuthStore interface {
	GetSessionForUser(context.Context, uint, uint64, string) (*model.FeishuAuthSession, error)
	GetOperationForUser(context.Context, uint, uint64, string) (*model.FeishuOperation, error)
	ClaimSession(context.Context, uint, uint64, string, string, time.Time, time.Time) (bool, error)
	RenewSession(context.Context, uint, uint64, string, string, time.Time, time.Time) (bool, error)
	AttachDeviceAuthCredential(context.Context, store.FeishuDeviceAuthCredentialAttach) error
	ReleaseDeviceAuthLease(context.Context, uint, uint64, string, string, time.Time) (bool, error)
	TerminalizeDeviceAuthSession(context.Context, uint, uint64, string, string, string, time.Time) error
	ReplaceDeviceAuthSession(context.Context, store.FeishuDeviceAuthReplacement) (*model.FeishuAuthSession, error)
	FinalizeDeviceAuthSuccess(context.Context, store.FeishuDeviceAuthSuccess) error
	SweepDeviceAuthCredentials(context.Context, time.Time, string, int) (store.FeishuDeviceAuthCleanupPage, error)
}

// DeviceAuthHomeVault exposes the read-only start materialization and the
// unpublished completion candidate without widening the flow to store internals.
type DeviceAuthHomeVault interface {
	WithHome(context.Context, uint, uint64, func(string) (bool, error)) error
	WithHomeCandidate(context.Context, uint, uint64, func(string) error) (*CLIHomeCandidate, error)
}

type DeviceAuthRefreshRequest struct {
	UserID           uint
	Generation       uint64
	OldSessionID     string
	OperationID      string
	WaitingState     string
	OperationSummary []byte
}

// DeviceAuthCompletion is the safe result vocabulary used by later completion
// tasks. Task 6 exposes it now so unimplemented paths can fail closed.
type DeviceAuthCompletion struct {
	Completed  bool
	NoticeCode AuthorizationNoticeCode
	Action     *OperationAction
}

// DeviceAuthObservation is a strict allowlist for authorization telemetry.
// It intentionally cannot carry scopes, URLs, HOME paths, credentials, raw
// errors, or operation content.
type DeviceAuthObservation struct {
	UserID       uint          `json:"user_id"`
	Generation   uint64        `json:"generation"`
	OperationID  string        `json:"operation_id,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	Phase        string        `json:"phase"`
	OutcomeClass string        `json:"outcome_class"`
	CLIVersion   string        `json:"cli_version,omitempty"`
	Duration     time.Duration `json:"duration"`
}

// DeviceAuthObserver receives only the allowlisted DeviceAuthObservation.
type DeviceAuthObserver interface {
	ObserveDeviceAuth(DeviceAuthObservation)
}

type DeviceAuthFlowDeps struct {
	Accounts          AuthSessionAccountStore
	Sessions          DeviceAuthStore
	Vault             DeviceAuthHomeVault
	CLI               DeviceAuthCLI
	Cipher            *DeviceAuthCredentialCipher
	Dispatcher        OperationResumeDispatcher
	Owner             string
	Now               func() time.Time
	NewID             func() string
	NewLeaseToken     func() string
	LeaseDuration     time.Duration
	SessionDuration   time.Duration
	HeartbeatInterval time.Duration
	StartTimeout      time.Duration
	CompletionTimeout time.Duration
	Observer          DeviceAuthObserver
}

// DeviceAuthFlow owns only the restart-safe split user-authorization protocol.
type DeviceAuthFlow struct {
	accounts          AuthSessionAccountStore
	sessions          DeviceAuthStore
	vault             DeviceAuthHomeVault
	cli               DeviceAuthCLI
	cipher            *DeviceAuthCredentialCipher
	dispatcher        OperationResumeDispatcher
	now               func() time.Time
	newID             func() string
	newLeaseToken     func() string
	leaseDuration     time.Duration
	sessionDuration   time.Duration
	heartbeatInterval time.Duration
	startTimeout      time.Duration
	completionTimeout time.Duration
	observer          DeviceAuthObserver
	liveURLs          *authSessionURLRegistry
	cleanupMu         sync.Mutex
	cleanupCursor     string
}

func NewDeviceAuthFlow(deps DeviceAuthFlowDeps) (*DeviceAuthFlow, error) {
	if deps.Accounts == nil || deps.Sessions == nil || deps.Vault == nil || deps.CLI == nil || deps.Cipher == nil ||
		deps.Dispatcher == nil || strings.TrimSpace(deps.Owner) == "" {
		return nil, fmt.Errorf("%w: missing dependency", ErrAuthSessionUnavailable)
	}
	now := deps.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	newID := deps.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	newLeaseToken := deps.NewLeaseToken
	if newLeaseToken == nil {
		owner := strings.TrimSpace(deps.Owner)
		if len(owner) > 80 {
			owner = owner[:80]
		}
		newLeaseToken = func() string { return owner + ":" + uuid.NewString() }
	}
	leaseDuration := positiveDurationOr(deps.LeaseDuration, authSessionDefaultLeaseDuration)
	sessionDuration := positiveDurationOr(deps.SessionDuration, authSessionDefaultDuration)
	heartbeatInterval := positiveDurationOr(deps.HeartbeatInterval, authSessionDefaultHeartbeatInterval)
	startTimeout := positiveDurationOr(deps.StartTimeout, authSessionDefaultStartTimeout)
	completionTimeout := positiveDurationOr(deps.CompletionTimeout, deviceAuthDefaultCompletionTimeout)
	if completionTimeout > deviceAuthMaxCompletionTimeout {
		completionTimeout = deviceAuthMaxCompletionTimeout
	}
	heartbeatCeiling := leaseDuration / 3
	if heartbeatCeiling <= 0 {
		return nil, fmt.Errorf("%w: lease duration is too small", ErrAuthSessionUnavailable)
	}
	if heartbeatInterval > heartbeatCeiling {
		heartbeatInterval = heartbeatCeiling
	}
	if heartbeatInterval <= 0 || heartbeatInterval >= leaseDuration {
		return nil, fmt.Errorf("%w: heartbeat must precede lease expiry", ErrAuthSessionUnavailable)
	}
	return &DeviceAuthFlow{
		accounts: deps.Accounts, sessions: deps.Sessions, vault: deps.Vault, cli: deps.CLI,
		cipher: deps.Cipher, dispatcher: deps.Dispatcher, now: now, newID: newID,
		newLeaseToken: newLeaseToken, leaseDuration: leaseDuration, sessionDuration: sessionDuration,
		heartbeatInterval: heartbeatInterval, startTimeout: startTimeout, completionTimeout: completionTimeout,
		observer: deps.Observer,
		liveURLs: newAuthSessionURLRegistry(),
	}, nil
}

// StartUserAuthorization runs one short no-wait CLI invocation, encrypts its
// opaque device code, and exposes the live URL only after durable attach has
// atomically stored the credential and released the start lease.
func (f *DeviceAuthFlow) StartUserAuthorization(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	session *model.FeishuAuthSession,
	scopes []string,
) (*OperationAction, error) {
	f.cleanupExpiredCredentialsBestEffort(ctx)
	return f.startUserAuthorization(ctx, account, session, scopes, false)
}

func (f *DeviceAuthFlow) startUserAuthorization(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	session *model.FeishuAuthSession,
	scopes []string,
	preservePendingOnFailure bool,
) (action *OperationAction, retErr error) {
	if f == nil {
		return nil, ErrAuthSessionUnavailable
	}
	startedAt := f.now().UTC()
	defer func() {
		outcome := "succeeded"
		if retErr != nil {
			outcome = deviceAuthErrorOutcome(retErr)
		}
		f.observeDeviceAuth(session, "start", outcome, "", f.now().UTC().Sub(startedAt))
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	canonicalScopes, err := canonicalDeviceAuthScopes(scopes)
	if err != nil {
		return nil, ErrAuthSessionUnavailable
	}
	scopeHash := deviceAuthScopeHash(canonicalScopes)
	now := f.now().UTC()
	if !validDeviceAuthStart(account, session, canonicalScopes, scopeHash, now) {
		return nil, ErrAuthSessionUnavailable
	}
	current, err := f.accounts.Get(ctx, session.UserID, ProviderLark)
	if err != nil || current == nil || current.UserID != account.UserID || current.Provider != ProviderLark ||
		current.Generation != account.Generation || current.AppID != account.AppID ||
		current.ConnectionState == model.FeishuConnectionDisconnecting {
		return nil, ErrAuthSessionUnavailable
	}

	leaseToken := strings.TrimSpace(f.newLeaseToken())
	if leaseToken == "" || len(leaseToken) > 128 {
		return nil, ErrAuthSessionUnavailable
	}
	claimed, err := f.sessions.ClaimSession(
		ctx, session.UserID, session.Generation, session.ID, leaseToken, now, now.Add(f.leaseDuration),
	)
	if err != nil {
		f.observeDeviceAuth(session, "lease_claim", "dependency", "", 0)
		return nil, ErrDeviceAuthProcessing
	}
	if !claimed {
		f.observeDeviceAuth(session, "lease_claim", "contended", "", 0)
		return nil, ErrDeviceAuthProcessing
	}
	f.observeDeviceAuth(session, "lease_claim", "claimed", "", 0)
	durableNow := f.now().UTC()
	durable, err := f.sessions.GetSessionForUser(ctx, session.UserID, session.Generation, session.ID)
	if err != nil || durable == nil || durable.ID != session.ID ||
		!validDeviceAuthStart(current, durable, canonicalScopes, scopeHash, durableNow) ||
		durable.LeaseOwner != leaseToken || durable.LeaseUntil == nil || !durable.LeaseUntil.After(durableNow) {
		f.releaseOwnedStartLease(ctx, session, leaseToken)
		return nil, ErrDeviceAuthProcessing
	}
	session = durable

	start, err := f.startCLIInReadOnlyHome(ctx, session, canonicalScopes, durableNow)
	if err != nil {
		f.releaseOrFailOwnedStart(ctx, session, leaseToken, err, preservePendingOnFailure)
		return nil, classifyDeviceAuthStartError(err)
	}
	expiresAt, err := earliestDeviceAuthExpiry(f.now().UTC(), session.ExpiresAt, start.ExpiresIn)
	if err != nil {
		f.releaseOrFailOwnedStart(ctx, session, leaseToken, err, preservePendingOnFailure)
		return nil, ErrAuthSessionUnavailable
	}
	binding := DeviceAuthCredentialBinding{
		UserID: session.UserID, Generation: session.Generation, AppID: account.AppID,
		OperationID: deviceAuthOperationBindingID(session), SessionID: session.ID,
		ScopeHash: scopeHash, ResumeExpiresAt: expiresAt,
	}
	ciphertext, keyVersion, err := f.cipher.Seal(binding, start.DeviceCode)
	if err != nil {
		f.releaseOrFailOwnedStart(ctx, session, leaseToken, errDeviceAuthCLIProtocol, preservePendingOnFailure)
		return nil, ErrAuthSessionUnavailable
	}
	err = f.sessions.AttachDeviceAuthCredential(ctx, store.FeishuDeviceAuthCredentialAttach{
		UserID: session.UserID, Generation: session.Generation, SessionID: session.ID,
		LeaseOwner: leaseToken, AppID: account.AppID, Ciphertext: ciphertext,
		KeyVersion: keyVersion, ResumeExpiry: expiresAt, ScopeHash: scopeHash, Now: f.now().UTC(),
	})
	if err != nil {
		f.releaseOwnedStartLease(ctx, session, leaseToken)
		return nil, ErrAuthSessionUnavailable
	}
	f.liveURLs.put(authSessionRegistryKey(session), start.VerificationURL, expiresAt)
	return f.actionFor(session, canonicalScopes, start.VerificationURL, expiresAt), nil
}

func (f *DeviceAuthFlow) startCLIInReadOnlyHome(
	ctx context.Context,
	session *model.FeishuAuthSession,
	scopes []string,
	now time.Time,
) (DeviceAuthStart, error) {
	budget := f.startTimeout
	if remaining := session.ExpiresAt.Sub(now); remaining < budget {
		budget = remaining
	}
	if budget <= 0 {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	startCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var start DeviceAuthStart
	err := f.vault.WithHome(startCtx, session.UserID, session.Generation, func(home string) (bool, error) {
		value, startErr := f.cli.StartUserAuth(startCtx, home, append([]string(nil), scopes...))
		start = value
		return false, startErr
	})
	if err != nil {
		return DeviceAuthStart{}, err
	}
	if !validDeviceAuthVerificationURL(start.VerificationURL) || !validDeviceAuthDeviceCode(start.DeviceCode) ||
		start.ExpiresIn <= 0 || start.ExpiresIn > deviceAuthCLIMaxExpiresIn {
		return DeviceAuthStart{}, errDeviceAuthCLIProtocol
	}
	return start, nil
}

func validDeviceAuthStart(
	account *model.UserThirdPartyAccount,
	session *model.FeishuAuthSession,
	canonicalScopes []string,
	scopeHash string,
	now time.Time,
) bool {
	if account == nil || session == nil || account.UserID == 0 || account.UserID != session.UserID ||
		account.Provider != ProviderLark || account.Generation == 0 || account.Generation != session.Generation ||
		!validAuthSessionAppID(account.AppID) || account.ConnectionState == model.FeishuConnectionDisconnecting ||
		session.ProtocolVersion != 2 || session.Phase != model.FeishuAuthPhaseUserAuth ||
		session.State != model.FeishuAuthSessionPending || session.CompletedAt != nil || !session.ExpiresAt.After(now) ||
		len(session.ResumeCredentialCiphertext) != 0 || session.ResumeKeyVersion != "" || session.ResumeExpiresAt != nil ||
		session.ScopeHash != scopeHash || len(canonicalScopes) == 0 {
		return false
	}
	return authSessionScopeJSONEqual(session.RequestedScopesJSON, mustMarshalAuthScopes(canonicalScopes))
}

func canonicalDeviceAuthScopes(scopes []string) ([]string, error) {
	canonical, err := canonicalAuthScopes(scopes)
	if err != nil || len(canonical) == 0 {
		return nil, ErrAuthSessionUnavailable
	}
	allowed := map[string]struct{}{"offline_access": {}}
	for _, catalogScopes := range []map[string][]string{docsScopes, baseScopes, wikiScopes, driveScopes} {
		for _, commandScopes := range catalogScopes {
			for _, scope := range commandScopes {
				allowed[scope] = struct{}{}
			}
		}
	}
	for _, scope := range canonical {
		if _, ok := allowed[scope]; !ok {
			return nil, ErrAuthSessionUnavailable
		}
	}
	return canonical, nil
}

func deviceAuthScopeHash(scopes []string) string {
	canonical := append([]string(nil), scopes...)
	sort.Strings(canonical)
	unique := canonical[:0]
	for _, scope := range canonical {
		if len(unique) == 0 || unique[len(unique)-1] != scope {
			unique = append(unique, scope)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(unique, " ")))
	return fmt.Sprintf("%x", sum)
}

func earliestDeviceAuthExpiry(now, sessionExpiry time.Time, expiresIn time.Duration) (time.Time, error) {
	cliExpiry := now.Add(expiresIn)
	result := cliExpiry
	if sessionExpiry.Before(result) {
		result = sessionExpiry
	}
	result = result.UTC().Truncate(time.Millisecond)
	if expiresIn <= 0 || !result.After(now.UTC()) {
		return time.Time{}, errDeviceAuthCLIProtocol
	}
	return result, nil
}

func deviceAuthOperationBindingID(session *model.FeishuAuthSession) string {
	if session != nil && session.OperationID != nil {
		return *session.OperationID
	}
	return deviceAuthManualBindingID
}

func (f *DeviceAuthFlow) actionFor(session *model.FeishuAuthSession, scopes []string, url string, expiresAt time.Time) *OperationAction {
	action := &OperationAction{
		Provider: ProviderLark, SessionID: session.ID, Phase: session.Phase, URL: url,
		Scopes: append([]string(nil), scopes...), ExpiresAt: expiresAt.UTC(),
	}
	if session.OperationID != nil {
		action.OperationID = *session.OperationID
	}
	return action
}

func (f *DeviceAuthFlow) releaseOwnedStartLease(ctx context.Context, session *model.FeishuAuthSession, leaseToken string) {
	releaseCtx, cancel := authSessionDetachedContext(ctx)
	defer cancel()
	_, _ = f.sessions.ReleaseDeviceAuthLease(
		releaseCtx, session.UserID, session.Generation, session.ID, leaseToken, f.now().UTC(),
	)
}

func (f *DeviceAuthFlow) releaseOrFailOwnedStart(
	ctx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	startErr error,
	preservePendingOnFailure bool,
) {
	if !preservePendingOnFailure && (errors.Is(startErr, errDeviceAuthCLIProtocol) || errors.Is(startErr, errDeviceAuthCredentialRejected)) {
		terminalCtx, cancel := authSessionDetachedContext(ctx)
		defer cancel()
		_ = f.sessions.TerminalizeDeviceAuthSession(
			terminalCtx, session.UserID, session.Generation, session.ID, leaseToken,
			model.FeishuAuthSessionFailed, f.now().UTC(),
		)
		return
	}
	f.releaseOwnedStartLease(ctx, session, leaseToken)
}

func classifyDeviceAuthStartError(err error) error {
	if errors.Is(err, errDeviceAuthCLIDependency) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrDeviceAuthDependency
	}
	return ErrAuthSessionUnavailable
}

// CompleteUserAuthorization consumes one encrypted device-code credential
// under an exact renewable lease. Candidate HOME state remains unpublished
// until the store atomically fences the account, session, operation, and vault.
func (f *DeviceAuthFlow) CompleteUserAuthorization(
	ctx context.Context,
	userID uint,
	generation uint64,
	sessionID string,
) (completion *DeviceAuthCompletion, retErr error) {
	if f == nil || userID == 0 || generation == 0 || strings.TrimSpace(sessionID) == "" {
		return nil, ErrAuthSessionUnavailable
	}
	startedAt := f.now().UTC()
	var observedSession *model.FeishuAuthSession
	defer func() {
		outcome := "succeeded"
		if retErr != nil {
			outcome = deviceAuthErrorOutcome(retErr)
		} else if completion != nil && completion.NoticeCode != "" {
			outcome = string(completion.NoticeCode)
		}
		f.observeDeviceAuth(observedSession, "complete", outcome, LarkCLIVersion, f.now().UTC().Sub(startedAt))
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	f.cleanupExpiredCredentialsBestEffort(ctx)
	session, err := f.sessions.GetSessionForUser(ctx, userID, generation, sessionID)
	if err != nil || session == nil {
		return nil, ErrAuthSessionUnavailable
	}
	observedSession = session
	account, err := f.accounts.Get(ctx, userID, ProviderLark)
	if err != nil || account == nil {
		return nil, ErrAuthSessionUnavailable
	}
	if session.State == model.FeishuAuthSessionCompleted {
		return f.dispatchCompletedDeviceAuth(ctx, account, session)
	}
	if !validDeviceAuthCompletion(account, session, f.now().UTC()) {
		return nil, ErrAuthSessionUnavailable
	}
	if session.OperationID != nil {
		operation, operationErr := f.sessions.GetOperationForUser(
			ctx, session.UserID, session.Generation, *session.OperationID,
		)
		if operationErr != nil || !validPendingDeviceAuthOperation(operation, session) {
			f.observeDeviceAuth(session, "binding", "rejected", "", 0)
			return nil, ErrAuthSessionUnavailable
		}
		f.observeDeviceAuth(session, "binding", "verified", "", 0)
	}

	leaseToken := strings.TrimSpace(f.newLeaseToken())
	if leaseToken == "" || len(leaseToken) > 128 {
		return nil, ErrAuthSessionUnavailable
	}
	claimNow := f.now().UTC()
	claimUntil := claimNow.Add(f.leaseDuration)
	claimBudget := deviceAuthBoundedBudget(
		deviceAuthMutationTimeout, claimNow, claimUntil, *session.ResumeExpiresAt,
	)
	if claimBudget <= 0 {
		return nil, ErrDeviceAuthProcessing
	}
	claimCtx, cancelClaim := context.WithTimeout(context.WithoutCancel(ctx), claimBudget)
	claimed, err := f.sessions.ClaimSession(
		claimCtx, userID, generation, sessionID, leaseToken, claimNow, claimUntil,
	)
	cancelClaim()
	if err != nil || !claimed {
		outcome := "contended"
		if err != nil {
			outcome = "dependency"
		}
		f.observeDeviceAuth(session, "lease_claim", outcome, "", 0)
		return nil, ErrDeviceAuthProcessing
	}
	f.observeDeviceAuth(session, "lease_claim", "claimed", "", 0)

	rereadNow := f.now().UTC()
	rereadBudget := deviceAuthBoundedBudget(
		deviceAuthMutationTimeout, rereadNow, claimUntil, *session.ResumeExpiresAt,
	)
	if rereadBudget <= 0 {
		f.releaseOwnedCompletionLease(ctx, session, leaseToken, claimUntil)
		return nil, ErrDeviceAuthConflict
	}
	rereadCtx, cancelReread := context.WithTimeout(context.WithoutCancel(ctx), rereadBudget)
	currentAccount, accountErr := f.accounts.Get(rereadCtx, userID, ProviderLark)
	durable, err := f.sessions.GetSessionForUser(rereadCtx, userID, generation, sessionID)
	cancelReread()
	durableNow := f.now().UTC()
	if accountErr != nil || currentAccount == nil || currentAccount.AppID != account.AppID ||
		err != nil || !validDeviceAuthCompletion(currentAccount, durable, durableNow) || durable.ID != sessionID ||
		durable.LeaseOwner != leaseToken || durable.LeaseUntil == nil || !durable.LeaseUntil.After(durableNow) {
		f.releaseOwnedCompletionLease(ctx, session, leaseToken, claimUntil)
		return nil, ErrDeviceAuthConflict
	}
	oldLeaseUntil := durable.LeaseUntil.UTC()
	renewNow := f.now().UTC()
	renewBudget := deviceAuthBoundedBudget(deviceAuthMutationTimeout, renewNow, oldLeaseUntil)
	if renewBudget <= 0 {
		f.releaseOwnedCompletionLease(ctx, durable, leaseToken, oldLeaseUntil)
		return nil, ErrDeviceAuthConflict
	}
	renewCtx, cancelRenew := context.WithTimeout(context.WithoutCancel(ctx), renewBudget)
	refreshedLeaseUntil := renewNow.Add(f.leaseDuration)
	renewed, renewErr := f.sessions.RenewSession(
		renewCtx, durable.UserID, durable.Generation, durable.ID,
		leaseToken, renewNow, refreshedLeaseUntil,
	)
	cancelRenew()
	if renewErr != nil || !renewed {
		f.observeDeviceAuth(session, "lease_renew", "lost", "", 0)
		f.releaseOwnedCompletionLease(ctx, durable, leaseToken, oldLeaseUntil)
		return nil, ErrDeviceAuthConflict
	}
	refreshedLeaseUntil = refreshedLeaseUntil.UTC()
	durable.LeaseUntil = &refreshedLeaseUntil
	account = currentAccount
	session = durable
	ownerCtx, cancelOwner := context.WithCancel(context.WithoutCancel(ctx))
	fence := newDeviceAuthLeaseFence(*session.LeaseUntil)
	heartbeatDone := make(chan struct{})
	go f.runDeviceAuthCompletionHeartbeat(ownerCtx, cancelOwner, session, leaseToken, fence, heartbeatDone)
	defer func() {
		cancelOwner()
		<-heartbeatDone
	}()

	binding := DeviceAuthCredentialBinding{
		UserID: session.UserID, Generation: session.Generation, AppID: account.AppID,
		OperationID: deviceAuthOperationBindingID(session), SessionID: session.ID,
		ScopeHash: session.ScopeHash, ResumeExpiresAt: session.ResumeExpiresAt.UTC(),
	}
	deviceCode, err := f.cipher.Open(binding, session.ResumeKeyVersion, session.ResumeCredentialCiphertext)
	if err != nil {
		return nil, f.failOwnedCompletion(ownerCtx, session, leaseToken, fence)
	}

	homeNow := f.now().UTC()
	homeBudget := deviceAuthBoundedBudget(
		f.completionTimeout+deviceAuthReconciliationTimeout,
		homeNow,
		*session.ResumeExpiresAt,
	)
	if homeBudget <= 0 || fence.lost() {
		return nil, ErrDeviceAuthConflict
	}
	homeCtx, cancelHome := context.WithTimeout(ownerCtx, homeBudget)
	var outcome DeviceAuthOutcome
	evidenceAppID := ""
	candidate, candidateErr := f.vault.WithHomeCandidate(
		homeCtx, session.UserID, session.Generation,
		func(home string) error {
			outcome, evidenceAppID = f.completeInCandidateHome(
				ownerCtx, account, session, fence, home, deviceCode,
			)
			return nil
		},
	)
	cancelHome()
	if candidateErr != nil {
		f.observeDeviceAuth(session, "candidate", "conflict", "", 0)
		if fence.lost() || ownerCtx.Err() != nil {
			return nil, ErrDeviceAuthConflict
		}
		if releaseErr := f.releaseOwnedCompletion(ownerCtx, session, leaseToken, fence); releaseErr != nil {
			return nil, releaseErr
		}
		return nil, ErrDeviceAuthDependency
	}
	if outcome == DeviceAuthCompleted && candidate == nil {
		outcome = DeviceAuthProtocolFailure
	}

	mutationNow := f.now().UTC()
	mutationDeadlines := []time.Time{fence.until()}
	if outcome != DeviceAuthExpired {
		mutationDeadlines = append(mutationDeadlines, *session.ResumeExpiresAt)
	}
	mutationBudget := deviceAuthBoundedBudget(deviceAuthMutationTimeout, mutationNow, mutationDeadlines...)
	if mutationBudget <= 0 || fence.lost() {
		return nil, ErrDeviceAuthConflict
	}
	mutationCtx, cancelMutation := context.WithTimeout(ownerCtx, mutationBudget)
	defer cancelMutation()

	switch outcome {
	case DeviceAuthCompleted:
		return f.commitDeviceAuthCandidate(
			mutationCtx, account, session, leaseToken, evidenceAppID, candidate, mutationNow,
		)
	case DeviceAuthPending:
		return f.releaseDeviceAuthOutcome(
			mutationCtx, session, leaseToken, mutationNow, AuthorizationPending,
		)
	case DeviceAuthRejected:
		if session.OperationID == nil {
			return f.terminalizeManualDeviceAuthOutcome(
				mutationCtx, session, leaseToken, mutationNow,
				model.FeishuAuthSessionRejected, AuthorizationRejected,
			)
		}
		oldSummary, newSummary, summaryErr := f.ownedOperationReplacementSummaries(mutationCtx, session, mutationNow)
		if summaryErr != nil {
			return nil, ErrDeviceAuthConflict
		}
		return f.replaceOwnedSession(
			mutationCtx, ctx, account, session, leaseToken, model.FeishuAuthSessionRejected,
			model.FeishuOperationWaitingUserAuth, oldSummary, newSummary,
		)
	case DeviceAuthExpired:
		if session.OperationID == nil {
			return f.terminalizeManualDeviceAuthOutcome(
				mutationCtx, session, leaseToken, mutationNow,
				model.FeishuAuthSessionExpired, AuthorizationExpired,
			)
		}
		oldSummary, newSummary, summaryErr := f.ownedOperationReplacementSummaries(mutationCtx, session, mutationNow)
		if summaryErr != nil {
			return nil, ErrDeviceAuthConflict
		}
		return f.replaceOwnedSession(
			mutationCtx, ctx, account, session, leaseToken, model.FeishuAuthSessionExpired,
			model.FeishuOperationWaitingUserAuth, oldSummary, newSummary,
		)
	case DeviceAuthProtocolFailure:
		if err := f.sessions.TerminalizeDeviceAuthSession(
			mutationCtx, session.UserID, session.Generation, session.ID, leaseToken,
			model.FeishuAuthSessionFailed, mutationNow,
		); err != nil {
			return nil, ErrDeviceAuthConflict
		}
		return nil, ErrAuthSessionUnavailable
	default:
		return nil, ErrDeviceAuthConflict
	}
}

type deviceAuthLeaseFence struct {
	mu         sync.RWMutex
	leaseUntil time.Time
	leaseLost  bool
}

func newDeviceAuthLeaseFence(leaseUntil time.Time) *deviceAuthLeaseFence {
	return &deviceAuthLeaseFence{leaseUntil: leaseUntil.UTC()}
}

func (f *deviceAuthLeaseFence) renew(leaseUntil time.Time) {
	f.mu.Lock()
	f.leaseUntil = leaseUntil.UTC()
	f.mu.Unlock()
}

func (f *deviceAuthLeaseFence) lose() {
	f.mu.Lock()
	f.leaseLost = true
	f.mu.Unlock()
}

func (f *deviceAuthLeaseFence) until() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.leaseUntil
}

func (f *deviceAuthLeaseFence) lost() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.leaseLost
}

func (f *DeviceAuthFlow) runDeviceAuthCompletionHeartbeat(
	ctx context.Context,
	cancel context.CancelFunc,
	session *model.FeishuAuthSession,
	leaseToken string,
	fence *deviceAuthLeaseFence,
	done chan<- struct{},
) {
	defer close(done)
	ticker := time.NewTicker(f.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := f.now().UTC()
			leaseUntil := now.Add(f.leaseDuration)
			renewBudget := f.leaseDuration
			if renewBudget > deviceAuthMutationTimeout {
				renewBudget = deviceAuthMutationTimeout
			}
			renewCtx, cancelRenew := context.WithTimeout(ctx, renewBudget)
			renewed, err := f.sessions.RenewSession(
				renewCtx, session.UserID, session.Generation, session.ID,
				leaseToken, now, leaseUntil,
			)
			cancelRenew()
			if err != nil || !renewed {
				f.observeDeviceAuth(session, "lease_renew", "lost", "", 0)
				fence.lose()
				cancel()
				return
			}
			fence.renew(leaseUntil)
		}
	}
}

func validDeviceAuthCompletion(
	account *model.UserThirdPartyAccount,
	session *model.FeishuAuthSession,
	now time.Time,
) bool {
	if account == nil || session == nil || account.UserID == 0 || account.UserID != session.UserID ||
		account.Provider != ProviderLark || account.Generation == 0 || account.Generation != session.Generation ||
		!validAuthSessionAppID(account.AppID) || account.ConnectionState == model.FeishuConnectionDisconnecting ||
		session.ProtocolVersion != 2 || session.Phase != model.FeishuAuthPhaseUserAuth ||
		session.State != model.FeishuAuthSessionPending || session.CompletedAt != nil ||
		!session.ExpiresAt.After(now) || session.ResumeExpiresAt == nil || !session.ResumeExpiresAt.After(now) ||
		session.ResumeExpiresAt.After(session.ExpiresAt) || len(session.ResumeCredentialCiphertext) == 0 ||
		session.ResumeKeyVersion == "" || len(session.ScopeHash) != 64 {
		return false
	}
	var scopes []string
	if json.Unmarshal(session.RequestedScopesJSON, &scopes) != nil {
		return false
	}
	canonical, err := canonicalDeviceAuthScopes(scopes)
	return err == nil && deviceAuthScopeHash(canonical) == session.ScopeHash &&
		authSessionScopeJSONEqual(session.RequestedScopesJSON, mustMarshalAuthScopes(canonical))
}

func (f *DeviceAuthFlow) completeInCandidateHome(
	ownerCtx context.Context,
	account *model.UserThirdPartyAccount,
	session *model.FeishuAuthSession,
	fence *deviceAuthLeaseFence,
	home string,
	deviceCode string,
) (DeviceAuthOutcome, string) {
	now := f.now().UTC()
	cliBudget := deviceAuthBoundedBudget(f.completionTimeout, now, *session.ResumeExpiresAt)
	if cliBudget <= 0 {
		return DeviceAuthExpired, ""
	}
	cliCtx, cancelCLI := context.WithTimeout(ownerCtx, cliBudget)
	var expectedScopes []string
	if json.Unmarshal(session.RequestedScopesJSON, &expectedScopes) != nil {
		cancelCLI()
		return DeviceAuthProtocolFailure, ""
	}
	expectedScopes, scopeErr := canonicalDeviceAuthScopes(expectedScopes)
	if scopeErr != nil {
		cancelCLI()
		return DeviceAuthProtocolFailure, ""
	}
	cliStartedAt := f.now().UTC()
	outcome, err := f.cli.CompleteUserAuth(cliCtx, home, deviceCode, expectedScopes)
	cancelCLI()
	if err != nil {
		outcome = DeviceAuthRetryableDependency
	}
	f.observeDeviceAuth(session, "cli_complete", outcome.String(), LarkCLIVersion, f.now().UTC().Sub(cliStartedAt))
	switch outcome {
	case DeviceAuthPending, DeviceAuthRejected, DeviceAuthExpired, DeviceAuthProtocolFailure:
		return outcome, ""
	case DeviceAuthAmbiguous, DeviceAuthRetryableDependency,
		DeviceAuthPollingPendingTimeout, DeviceAuthPollingNetworkFailure,
		DeviceAuthPollingReadFailure, DeviceAuthPollingParseFailure, DeviceAuthPollingSlowDown:
		// Auth status and AppID prove only that some user token exists for the
		// expected application. They do not prove that the durable scopes for
		// this authorization attempt were granted; an older token in the HOME
		// could satisfy both checks. Without structured granted-scope evidence,
		// keep the session pending and never publish the candidate HOME.
		if !session.ResumeExpiresAt.After(f.now().UTC()) {
			return DeviceAuthExpired, ""
		}
		return DeviceAuthPending, ""
	case DeviceAuthCompleted:
	default:
		return DeviceAuthProtocolFailure, ""
	}

	reconcileNow := f.now().UTC()
	reconcileBudget := deviceAuthBoundedBudget(
		deviceAuthReconciliationTimeout, reconcileNow, *session.ResumeExpiresAt, fence.until(),
	)
	if reconcileBudget <= 0 || fence.lost() {
		if !session.ResumeExpiresAt.After(reconcileNow) {
			return DeviceAuthExpired, ""
		}
		return DeviceAuthAmbiguous, ""
	}
	reconcileCtx, cancelReconcile := context.WithTimeout(ownerCtx, reconcileBudget)
	available, statusErr := f.cli.AuthStatus(reconcileCtx, home)
	if statusErr != nil || !available {
		statusOutcome := "unavailable"
		if statusErr != nil {
			statusOutcome = "dependency"
		}
		f.observeDeviceAuth(session, "reconcile_status", statusOutcome, LarkCLIVersion, 0)
		cancelReconcile()
		if !session.ResumeExpiresAt.After(f.now().UTC()) {
			return DeviceAuthExpired, ""
		}
		return DeviceAuthPending, ""
	}
	f.observeDeviceAuth(session, "reconcile_status", "available", LarkCLIVersion, 0)
	appID, appErr := f.cli.AppIDFromHome(reconcileCtx, home)
	cancelReconcile()
	if appErr != nil {
		f.observeDeviceAuth(session, "reconcile_app", "dependency", LarkCLIVersion, 0)
		if !session.ResumeExpiresAt.After(f.now().UTC()) {
			return DeviceAuthExpired, ""
		}
		return DeviceAuthPending, ""
	}
	if appID != account.AppID {
		f.observeDeviceAuth(session, "reconcile_app", "mismatch", LarkCLIVersion, 0)
		if outcome == DeviceAuthCompleted {
			return DeviceAuthProtocolFailure, ""
		}
		if !session.ResumeExpiresAt.After(f.now().UTC()) {
			return DeviceAuthExpired, ""
		}
		return DeviceAuthPending, ""
	}
	f.observeDeviceAuth(session, "reconcile_app", "matched", LarkCLIVersion, 0)
	return DeviceAuthCompleted, appID
}

// validPendingDeviceAuthOperation re-establishes the complete durable link
// before the opaque device code is decrypted or lark-cli is invoked. Legacy
// manual operations may carry neither Agent field; an Agent operation must
// carry both, and a partially populated link is always corruption.
func validPendingDeviceAuthOperation(
	operation *model.FeishuOperation,
	session *model.FeishuAuthSession,
) bool {
	if operation == nil || session == nil || session.OperationID == nil ||
		operation.ID != *session.OperationID || operation.UserID != session.UserID ||
		operation.Generation != session.Generation ||
		operation.State != model.FeishuOperationWaitingUserAuth {
		return false
	}
	if operation.AgentRunID == 0 && strings.TrimSpace(operation.ToolCallID) == "" {
		// Manual/legacy operations deliberately have no Agent continuation.
	} else if operation.AgentRunID == 0 ||
		!validStableIdentifier(operation.ToolCallID, operationMaxToolCallIDBytes) {
		return false
	}
	summary, err := decodeOperationSummary(operation.ResultSummaryJSON)
	return err == nil && summary.Status == model.FeishuOperationWaitingUserAuth &&
		summary.SessionID == session.ID && summary.Phase == model.FeishuAuthPhaseUserAuth &&
		phaseForRecovery(summary.RecoveryKind) == model.FeishuAuthPhaseUserAuth
}

func deviceAuthBoundedBudget(limit time.Duration, now time.Time, deadlines ...time.Time) time.Duration {
	if limit <= 0 {
		return 0
	}
	budget := limit
	for _, deadline := range deadlines {
		remaining := deadline.Sub(now)
		if remaining < budget {
			budget = remaining
		}
	}
	return budget
}

func (f *DeviceAuthFlow) commitDeviceAuthCandidate(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	session *model.FeishuAuthSession,
	leaseToken string,
	evidenceAppID string,
	candidate *CLIHomeCandidate,
	now time.Time,
) (*DeviceAuthCompletion, error) {
	operationID := ""
	waitingState := ""
	if session.OperationID != nil {
		operationID = *session.OperationID
		waitingState = model.FeishuOperationWaitingUserAuth
	}
	err := f.sessions.FinalizeDeviceAuthSuccess(ctx, store.FeishuDeviceAuthSuccess{
		UserID: session.UserID, Generation: session.Generation, SessionID: session.ID,
		OperationID: operationID, LeaseOwner: leaseToken, ExpectedAppID: account.AppID,
		ExpectedWaitingState: waitingState, Candidate: candidate.Vault,
		ExpectedVaultRevision: candidate.ExpectedRevision,
		Evidence:              model.FeishuConnectionEvidence{AppID: evidenceAppID, CLIVersion: LarkCLIVersion},
		Now:                   now,
	})
	if err != nil {
		f.observeDeviceAuth(session, "candidate", "conflict", "", 0)
		return nil, ErrDeviceAuthConflict
	}
	durable, durableErr := f.sessions.GetSessionForUser(ctx, session.UserID, session.Generation, session.ID)
	if durableErr != nil || durable == nil {
		return nil, ErrAuthSessionUnavailable
	}
	currentAccount, accountErr := f.accounts.Get(ctx, session.UserID, ProviderLark)
	if accountErr != nil || currentAccount == nil {
		return nil, ErrAuthSessionUnavailable
	}
	return f.dispatchCompletedDeviceAuth(ctx, currentAccount, durable)
}

func (f *DeviceAuthFlow) dispatchCompletedDeviceAuth(
	ctx context.Context,
	account *model.UserThirdPartyAccount,
	session *model.FeishuAuthSession,
) (*DeviceAuthCompletion, error) {
	if f == nil || account == nil || session == nil || account.UserID == 0 ||
		account.UserID != session.UserID || account.Provider != ProviderLark ||
		account.Generation == 0 || account.Generation != session.Generation ||
		account.ConnectionState == model.FeishuConnectionDisconnecting ||
		session.ProtocolVersion != 2 || session.Phase != model.FeishuAuthPhaseUserAuth ||
		session.State != model.FeishuAuthSessionCompleted || session.CompletedAt == nil ||
		len(session.ResumeCredentialCiphertext) != 0 || session.ResumeKeyVersion != "" || session.ResumeExpiresAt != nil {
		return nil, ErrAuthSessionUnavailable
	}
	if session.OperationID == nil {
		return &DeviceAuthCompletion{Completed: true}, nil
	}
	operationID := strings.TrimSpace(*session.OperationID)
	if operationID == "" || operationID != *session.OperationID {
		return nil, ErrAuthSessionUnavailable
	}
	operation, err := f.sessions.GetOperationForUser(
		ctx, session.UserID, session.Generation, operationID,
	)
	if err != nil || !validCompletedDeviceAuthOperation(operation, session) {
		return nil, ErrAuthSessionUnavailable
	}
	dispatchCtx, cancelDispatch := authSessionDispatchContext(ctx)
	defer cancelDispatch()
	if err := f.dispatcher.DispatchResume(dispatchCtx, session.UserID, operationID); err != nil {
		f.observeDeviceAuth(session, "dispatch", "retry", "", 0)
		return nil, ErrAuthSessionUnavailable
	}
	f.observeDeviceAuth(session, "dispatch", "succeeded", "", 0)
	return &DeviceAuthCompletion{Completed: true}, nil
}

func validCompletedDeviceAuthOperation(operation *model.FeishuOperation, session *model.FeishuAuthSession) bool {
	if operation == nil || session == nil || session.OperationID == nil || operation.ID != *session.OperationID ||
		operation.UserID != session.UserID || operation.Generation != session.Generation {
		return false
	}
	switch operation.State {
	case model.FeishuOperationWaitingUserAuth:
		summary, err := decodeOperationSummary(operation.ResultSummaryJSON)
		return err == nil && summary.Status == model.FeishuOperationWaitingUserAuth &&
			summary.SessionID == session.ID && summary.Phase == model.FeishuAuthPhaseUserAuth
	case model.FeishuOperationExecuting, model.FeishuOperationSucceeded, model.FeishuOperationFailed,
		model.FeishuOperationUnknown, model.FeishuOperationCancelled:
		return true
	default:
		return false
	}
}

func (f *DeviceAuthFlow) observeDeviceAuth(
	session *model.FeishuAuthSession,
	phase string,
	outcomeClass string,
	cliVersion string,
	duration time.Duration,
) {
	if f == nil || f.observer == nil {
		return
	}
	event := DeviceAuthObservation{
		Phase: phase, OutcomeClass: outcomeClass, CLIVersion: cliVersion, Duration: duration,
	}
	if session != nil {
		event.UserID = session.UserID
		event.Generation = session.Generation
		event.SessionID = session.ID
		if session.OperationID != nil {
			event.OperationID = *session.OperationID
		}
	}
	f.observer.ObserveDeviceAuth(event)
}

func deviceAuthErrorOutcome(err error) string {
	switch {
	case errors.Is(err, ErrDeviceAuthProcessing):
		return "processing"
	case errors.Is(err, ErrDeviceAuthConflict):
		return "conflict"
	case errors.Is(err, ErrDeviceAuthDependency):
		return "dependency"
	default:
		return "unavailable"
	}
}

func (f *DeviceAuthFlow) releaseDeviceAuthOutcome(
	ctx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	now time.Time,
	notice AuthorizationNoticeCode,
) (*DeviceAuthCompletion, error) {
	released, err := f.sessions.ReleaseDeviceAuthLease(
		ctx, session.UserID, session.Generation, session.ID, leaseToken, now,
	)
	if err != nil || !released {
		return nil, ErrDeviceAuthConflict
	}
	return &DeviceAuthCompletion{NoticeCode: notice}, nil
}

func (f *DeviceAuthFlow) terminalizeManualDeviceAuthOutcome(
	ctx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	now time.Time,
	terminalState string,
	notice AuthorizationNoticeCode,
) (*DeviceAuthCompletion, error) {
	if session == nil || session.OperationID != nil ||
		(terminalState != model.FeishuAuthSessionRejected && terminalState != model.FeishuAuthSessionExpired) {
		return nil, ErrDeviceAuthConflict
	}
	if err := f.sessions.TerminalizeDeviceAuthSession(
		ctx, session.UserID, session.Generation, session.ID, leaseToken, terminalState, now,
	); err != nil {
		return nil, ErrDeviceAuthConflict
	}
	f.liveURLs.remove(authSessionRegistryKey(session))
	return &DeviceAuthCompletion{NoticeCode: notice}, nil
}

func (f *DeviceAuthFlow) releaseOwnedCompletion(
	ownerCtx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	fence *deviceAuthLeaseFence,
) error {
	now := f.now().UTC()
	budget := deviceAuthBoundedBudget(
		deviceAuthMutationTimeout, now, *session.ResumeExpiresAt, fence.until(),
	)
	if budget <= 0 || fence.lost() {
		return ErrDeviceAuthConflict
	}
	mutationCtx, cancel := context.WithTimeout(ownerCtx, budget)
	defer cancel()
	released, err := f.sessions.ReleaseDeviceAuthLease(
		mutationCtx, session.UserID, session.Generation, session.ID, leaseToken, now,
	)
	if err != nil || !released {
		return ErrDeviceAuthConflict
	}
	return nil
}

func (f *DeviceAuthFlow) failOwnedCompletion(
	ownerCtx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	fence *deviceAuthLeaseFence,
) error {
	now := f.now().UTC()
	budget := deviceAuthBoundedBudget(
		deviceAuthMutationTimeout, now, *session.ResumeExpiresAt, fence.until(),
	)
	if budget <= 0 || fence.lost() {
		return ErrDeviceAuthConflict
	}
	mutationCtx, cancel := context.WithTimeout(ownerCtx, budget)
	defer cancel()
	if err := f.sessions.TerminalizeDeviceAuthSession(
		mutationCtx, session.UserID, session.Generation, session.ID, leaseToken,
		model.FeishuAuthSessionFailed, now,
	); err != nil {
		return ErrDeviceAuthConflict
	}
	return ErrAuthSessionUnavailable
}

func (f *DeviceAuthFlow) releaseOwnedCompletionLease(
	ctx context.Context,
	session *model.FeishuAuthSession,
	leaseToken string,
	leaseUntil time.Time,
) {
	now := f.now().UTC()
	budget := deviceAuthBoundedBudget(deviceAuthMutationTimeout, now, leaseUntil)
	if budget <= 0 {
		return
	}
	mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	_, _ = f.sessions.ReleaseDeviceAuthLease(
		mutationCtx, session.UserID, session.Generation, session.ID, leaseToken, now,
	)
}

func (f *DeviceAuthFlow) deviceAuthReplacementSummaries(
	oldSession *model.FeishuAuthSession,
	oldSummary []byte,
	now time.Time,
) ([]byte, []byte, error) {
	if oldSession == nil || oldSession.OperationID == nil {
		return nil, nil, ErrAuthSessionUnavailable
	}
	if len(oldSummary) == 0 {
		return nil, nil, ErrAuthSessionUnavailable
	}
	summary, err := decodeOperationSummary(oldSummary)
	if err != nil || summary.Status != model.FeishuOperationWaitingUserAuth ||
		summary.Phase != model.FeishuAuthPhaseUserAuth || summary.SessionID != oldSession.ID {
		return nil, nil, ErrAuthSessionUnavailable
	}
	summary.SessionID = f.newID()
	if strings.TrimSpace(summary.SessionID) == "" || summary.SessionID == oldSession.ID {
		return nil, nil, ErrAuthSessionUnavailable
	}
	expiresAt := now.UTC().Add(f.sessionDuration)
	if summary.ExpiresAt != nil {
		summary.ExpiresAt = &expiresAt
	}
	newSummary, err := json.Marshal(summary)
	if err != nil {
		return nil, nil, ErrAuthSessionUnavailable
	}
	return append([]byte(nil), oldSummary...), newSummary, nil
}

func (f *DeviceAuthFlow) ownedOperationReplacementSummaries(
	ctx context.Context,
	oldSession *model.FeishuAuthSession,
	now time.Time,
) ([]byte, []byte, error) {
	if oldSession == nil || oldSession.OperationID == nil {
		return nil, nil, ErrAuthSessionUnavailable
	}
	operation, err := f.sessions.GetOperationForUser(
		ctx, oldSession.UserID, oldSession.Generation, *oldSession.OperationID,
	)
	if err != nil || operation == nil || operation.ID != *oldSession.OperationID ||
		operation.State != model.FeishuOperationWaitingUserAuth {
		return nil, nil, ErrAuthSessionUnavailable
	}
	return f.deviceAuthReplacementSummaries(oldSession, operation.ResultSummaryJSON, now)
}

func (f *DeviceAuthFlow) replaceOwnedSession(
	mutationCtx context.Context,
	startParent context.Context,
	account *model.UserThirdPartyAccount,
	oldSession *model.FeishuAuthSession,
	leaseToken string,
	terminalState string,
	waitingState string,
	oldSummary []byte,
	newSummary []byte,
) (*DeviceAuthCompletion, error) {
	if account == nil || oldSession == nil || oldSession.OperationID == nil ||
		waitingState != model.FeishuOperationWaitingUserAuth {
		return nil, ErrDeviceAuthConflict
	}
	notice, ok := authorizationNoticeForReplacement(terminalState)
	if !ok {
		return nil, ErrDeviceAuthConflict
	}
	var scopes []string
	if json.Unmarshal(oldSession.RequestedScopesJSON, &scopes) != nil {
		return nil, ErrDeviceAuthConflict
	}
	canonicalScopes, err := canonicalDeviceAuthScopes(scopes)
	if err != nil || !authSessionScopeJSONEqual(oldSession.RequestedScopesJSON, mustMarshalAuthScopes(canonicalScopes)) {
		return nil, ErrDeviceAuthConflict
	}
	newSummaryValue, err := decodeOperationSummary(newSummary)
	if err != nil || newSummaryValue.Status != waitingState || newSummaryValue.Phase != model.FeishuAuthPhaseUserAuth ||
		strings.TrimSpace(newSummaryValue.SessionID) == "" || newSummaryValue.SessionID == oldSession.ID {
		return nil, ErrDeviceAuthConflict
	}
	operationID := *oldSession.OperationID
	replacementExpiresAt := f.now().UTC().Add(f.sessionDuration)
	if newSummaryValue.ExpiresAt != nil {
		replacementExpiresAt = newSummaryValue.ExpiresAt.UTC()
	}
	replacement := &model.FeishuAuthSession{
		ID: newSummaryValue.SessionID, UserID: oldSession.UserID, Generation: oldSession.Generation,
		OperationID: &operationID, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: mustMarshalAuthScopes(canonicalScopes), State: model.FeishuAuthSessionPending,
		ExpiresAt: replacementExpiresAt, ProtocolVersion: 2,
		ScopeHash: deviceAuthScopeHash(canonicalScopes),
	}
	stored, err := f.sessions.ReplaceDeviceAuthSession(mutationCtx, store.FeishuDeviceAuthReplacement{
		UserID: oldSession.UserID, Generation: oldSession.Generation, OldSessionID: oldSession.ID,
		LeaseOwner: leaseToken, TerminalState: terminalState, NewSession: replacement,
		OperationID: operationID, ExpectedWaitingState: waitingState,
		OldSummary: append([]byte(nil), oldSummary...), NewSummary: append([]byte(nil), newSummary...),
		Now: f.now().UTC(),
	})
	if err != nil || stored == nil {
		f.observeDeviceAuth(oldSession, "replacement", "conflict", "", 0)
		return nil, ErrDeviceAuthConflict
	}
	f.observeDeviceAuth(oldSession, "replacement", "succeeded", "", 0)
	f.liveURLs.remove(authSessionRegistryKey(oldSession))
	if startParent == nil {
		startParent = context.Background()
	}
	startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(startParent), f.startTimeout)
	defer cancelStart()
	action, err := f.startUserAuthorization(startCtx, account, stored, canonicalScopes, true)
	if err != nil {
		return nil, err
	}
	if action == nil || action.SessionID != stored.ID || action.OperationID != operationID {
		return nil, ErrDeviceAuthConflict
	}
	// Requested scopes are recovered only from the canonical durable session;
	// they are not reflected back through the refresh/completion response.
	action.Scopes = nil
	return &DeviceAuthCompletion{NoticeCode: notice, Action: action}, nil
}

func authorizationNoticeForReplacement(terminalState string) (AuthorizationNoticeCode, bool) {
	switch terminalState {
	case model.FeishuAuthSessionRejected:
		return AuthorizationRejected, true
	case model.FeishuAuthSessionExpired:
		return AuthorizationExpired, true
	case model.FeishuAuthSessionSuperseded:
		return AuthorizationUpdated, true
	default:
		return "", false
	}
}

// RefreshUserAuthorization atomically replaces an exact operation-linked
// legacy or terminal device session with a credential-free v2 attempt.
func (f *DeviceAuthFlow) RefreshUserAuthorization(
	ctx context.Context,
	request DeviceAuthRefreshRequest,
) (*DeviceAuthCompletion, error) {
	if f == nil || request.UserID == 0 || request.Generation == 0 ||
		strings.TrimSpace(request.OldSessionID) == "" || strings.TrimSpace(request.OperationID) == "" ||
		request.WaitingState != model.FeishuOperationWaitingUserAuth {
		return nil, ErrAuthSessionUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := f.now().UTC()
	oldSession, err := f.sessions.GetSessionForUser(ctx, request.UserID, request.Generation, request.OldSessionID)
	if err != nil || oldSession == nil || oldSession.Phase != model.FeishuAuthPhaseUserAuth ||
		oldSession.OperationID == nil || *oldSession.OperationID != request.OperationID ||
		(oldSession.CompletedAt != nil && oldSession.State == model.FeishuAuthSessionPending) {
		return nil, ErrAuthSessionUnavailable
	}
	var scopes []string
	if json.Unmarshal(oldSession.RequestedScopesJSON, &scopes) != nil {
		return nil, ErrAuthSessionUnavailable
	}
	canonicalScopes, err := canonicalDeviceAuthScopes(scopes)
	if err != nil || !authSessionScopeJSONEqual(oldSession.RequestedScopesJSON, mustMarshalAuthScopes(canonicalScopes)) {
		return nil, ErrAuthSessionUnavailable
	}
	requiresClaim, terminalState, validSource := deviceAuthRefreshSource(oldSession, canonicalScopes, now)
	if !validSource {
		return nil, ErrAuthSessionUnavailable
	}
	summary, err := decodeOperationSummary(request.OperationSummary)
	if err != nil || summary.Status != request.WaitingState || summary.Phase != oldSession.Phase ||
		summary.SessionID != oldSession.ID || phaseForRecovery(summary.RecoveryKind) != oldSession.Phase ||
		(len(summary.RecoveryScopes) > 0 && !authSessionScopeJSONEqual(
			mustMarshalAuthScopes(summary.RecoveryScopes), mustMarshalAuthScopes(canonicalScopes),
		)) {
		return nil, ErrAuthSessionUnavailable
	}
	account, err := f.accounts.Get(ctx, request.UserID, ProviderLark)
	if err != nil || account == nil || account.UserID != request.UserID || account.Generation != request.Generation ||
		account.Provider != ProviderLark || !validAuthSessionAppID(account.AppID) ||
		account.ConnectionState == model.FeishuConnectionDisconnecting {
		return nil, ErrAuthSessionUnavailable
	}
	leaseToken := ""
	claimUntil := time.Time{}
	if requiresClaim {
		leaseToken = strings.TrimSpace(f.newLeaseToken())
		if leaseToken == "" || len(leaseToken) > 128 {
			return nil, ErrAuthSessionUnavailable
		}
		claimNow := f.now().UTC()
		claimUntil = claimNow.Add(f.leaseDuration)
		claimBudget := deviceAuthBoundedBudget(deviceAuthMutationTimeout, claimNow, claimUntil, oldSession.ExpiresAt)
		if claimBudget <= 0 {
			return nil, ErrDeviceAuthProcessing
		}
		claimCtx, cancelClaim := context.WithTimeout(context.WithoutCancel(ctx), claimBudget)
		claimed, claimErr := f.sessions.ClaimSession(
			claimCtx, request.UserID, request.Generation, request.OldSessionID,
			leaseToken, claimNow, claimUntil,
		)
		cancelClaim()
		if claimErr != nil || !claimed {
			return nil, ErrDeviceAuthProcessing
		}
	}
	oldSummary, newSummary, err := f.deviceAuthReplacementSummaries(oldSession, request.OperationSummary, f.now().UTC())
	if err != nil {
		if requiresClaim {
			f.releaseOwnedCompletionLease(ctx, oldSession, leaseToken, claimUntil)
		}
		return nil, ErrAuthSessionUnavailable
	}
	mutationNow := f.now().UTC()
	mutationBudget := deviceAuthMutationTimeout
	if requiresClaim {
		mutationBudget = deviceAuthBoundedBudget(deviceAuthMutationTimeout, mutationNow, claimUntil, oldSession.ExpiresAt)
	}
	if mutationBudget <= 0 {
		if requiresClaim {
			f.releaseOwnedCompletionLease(ctx, oldSession, leaseToken, claimUntil)
		}
		return nil, ErrDeviceAuthConflict
	}
	mutationCtx, cancelMutation := context.WithTimeout(context.WithoutCancel(ctx), mutationBudget)
	result, replaceErr := f.replaceOwnedSession(
		mutationCtx, ctx, account, oldSession, leaseToken, terminalState,
		request.WaitingState, oldSummary, newSummary,
	)
	cancelMutation()
	if requiresClaim && errors.Is(replaceErr, ErrDeviceAuthConflict) {
		f.releaseOwnedCompletionLease(ctx, oldSession, leaseToken, claimUntil)
	}
	return result, replaceErr
}

func deviceAuthRefreshSource(
	session *model.FeishuAuthSession,
	canonicalScopes []string,
	now time.Time,
) (bool, string, bool) {
	if session == nil || session.Phase != model.FeishuAuthPhaseUserAuth || len(canonicalScopes) == 0 {
		return false, "", false
	}
	hasCiphertext := len(session.ResumeCredentialCiphertext) > 0
	hasKey := session.ResumeKeyVersion != ""
	hasExpiry := session.ResumeExpiresAt != nil
	credentialFree := !hasCiphertext && !hasKey && !hasExpiry
	credentialComplete := hasCiphertext && hasKey && hasExpiry
	switch session.ProtocolVersion {
	case 1:
		if !credentialFree || session.ScopeHash != "" {
			return false, "", false
		}
		switch session.State {
		case model.FeishuAuthSessionPending:
			return true, model.FeishuAuthSessionSuperseded, session.ExpiresAt.After(now)
		case model.FeishuAuthSessionSuperseded:
			return false, model.FeishuAuthSessionSuperseded, session.LeaseOwner == "" && session.LeaseUntil == nil
		}
	case 2:
		if session.ScopeHash != deviceAuthScopeHash(canonicalScopes) || (!credentialFree && !credentialComplete) {
			return false, "", false
		}
		switch session.State {
		case model.FeishuAuthSessionPending:
			return true, model.FeishuAuthSessionSuperseded, session.ExpiresAt.After(now)
		case model.FeishuAuthSessionRejected, model.FeishuAuthSessionExpired:
			return false, session.State, credentialFree && session.LeaseOwner == "" && session.LeaseUntil == nil
		}
	}
	return false, "", false
}

// CleanupExpiredCredentials clears one bounded keyset page and advances an
// in-memory cursor for the next invocation. Done resets the cursor so a later
// invocation begins a new sweep; one call never drains the full table.
func (f *DeviceAuthFlow) CleanupExpiredCredentials(ctx context.Context, scanLimit int) (int64, error) {
	if f == nil || f.sessions == nil {
		return 0, ErrDeviceAuthDependency
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.cleanupMu.Lock()
	defer f.cleanupMu.Unlock()
	return f.cleanupExpiredCredentialsLocked(ctx, scanLimit)
}

func (f *DeviceAuthFlow) cleanupExpiredCredentialsBestEffort(ctx context.Context) {
	if f == nil || f.sessions == nil || !f.cleanupMu.TryLock() {
		return
	}
	defer f.cleanupMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	_, _ = f.cleanupExpiredCredentialsLocked(ctx, 20)
}

func (f *DeviceAuthFlow) cleanupExpiredCredentialsLocked(ctx context.Context, scanLimit int) (int64, error) {
	page, err := f.sessions.SweepDeviceAuthCredentials(ctx, f.now().UTC(), f.cleanupCursor, scanLimit)
	if err != nil {
		return 0, ErrDeviceAuthDependency
	}
	if page.Done {
		f.cleanupCursor = ""
	} else {
		next := strings.TrimSpace(page.NextSessionID)
		if next == "" || next <= f.cleanupCursor {
			return 0, ErrDeviceAuthDependency
		}
		f.cleanupCursor = next
	}
	return int64(page.Cleared), nil
}
