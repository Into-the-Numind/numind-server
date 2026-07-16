package feishu

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

const deviceAuthManualBindingID = "manual"

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
	ClaimSession(context.Context, uint, uint64, string, string, time.Time, time.Time) (bool, error)
	RenewSession(context.Context, uint, uint64, string, string, time.Time, time.Time) (bool, error)
	AttachDeviceAuthCredential(context.Context, store.FeishuDeviceAuthCredentialAttach) error
	ReleaseDeviceAuthLease(context.Context, uint, uint64, string, string, time.Time) (bool, error)
	TerminalizeDeviceAuthSession(context.Context, uint, uint64, string, string, string, time.Time) error
	ReplaceDeviceAuthSession(context.Context, store.FeishuDeviceAuthReplacement) (*model.FeishuAuthSession, error)
	FinalizeDeviceAuthSuccess(context.Context, store.FeishuDeviceAuthSuccess) error
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
	liveURLs          *authSessionURLRegistry
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
	completionTimeout := positiveDurationOr(deps.CompletionTimeout, authSessionDefaultStartTimeout)
	if heartbeatInterval >= leaseDuration {
		return nil, fmt.Errorf("%w: heartbeat must precede lease expiry", ErrAuthSessionUnavailable)
	}
	return &DeviceAuthFlow{
		accounts: deps.Accounts, sessions: deps.Sessions, vault: deps.Vault, cli: deps.CLI,
		cipher: deps.Cipher, dispatcher: deps.Dispatcher, now: now, newID: newID,
		newLeaseToken: newLeaseToken, leaseDuration: leaseDuration, sessionDuration: sessionDuration,
		heartbeatInterval: heartbeatInterval, startTimeout: startTimeout, completionTimeout: completionTimeout,
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
	if f == nil {
		return nil, ErrAuthSessionUnavailable
	}
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
		return nil, ErrDeviceAuthProcessing
	}
	if !claimed {
		return nil, ErrDeviceAuthProcessing
	}
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
		f.releaseOrFailOwnedStart(ctx, session, leaseToken, err)
		return nil, classifyDeviceAuthStartError(err)
	}
	expiresAt, err := earliestDeviceAuthExpiry(f.now().UTC(), session.ExpiresAt, start.ExpiresIn)
	if err != nil {
		f.releaseOrFailOwnedStart(ctx, session, leaseToken, err)
		return nil, ErrAuthSessionUnavailable
	}
	binding := DeviceAuthCredentialBinding{
		UserID: session.UserID, Generation: session.Generation, AppID: account.AppID,
		OperationID: deviceAuthOperationBindingID(session), SessionID: session.ID,
		ScopeHash: scopeHash, ResumeExpiresAt: expiresAt,
	}
	ciphertext, keyVersion, err := f.cipher.Seal(binding, start.DeviceCode)
	if err != nil {
		f.releaseOrFailOwnedStart(ctx, session, leaseToken, errDeviceAuthCLIProtocol)
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
	for _, catalogScopes := range []map[string][]string{docsScopes, baseScopes, wikiScopes} {
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

func (f *DeviceAuthFlow) releaseOrFailOwnedStart(ctx context.Context, session *model.FeishuAuthSession, leaseToken string, startErr error) {
	if errors.Is(startErr, errDeviceAuthCLIProtocol) || errors.Is(startErr, errDeviceAuthCredentialRejected) {
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

// Completion, replacement, and cleanup are deliberately fail-closed until the
// subsequent plan tasks add their state machines.
func (f *DeviceAuthFlow) CompleteUserAuthorization(context.Context, uint, uint64, string) (*DeviceAuthCompletion, error) {
	return nil, ErrDeviceAuthDependency
}

func (f *DeviceAuthFlow) RefreshUserAuthorization(context.Context, DeviceAuthRefreshRequest) (*DeviceAuthCompletion, error) {
	return nil, ErrDeviceAuthDependency
}

func (f *DeviceAuthFlow) CleanupExpiredCredentials(context.Context, int) (int64, error) {
	return 0, ErrDeviceAuthDependency
}
