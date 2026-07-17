package feishu

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	pkgcrypto "numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"
)

type deviceAuthFlowAccountStoreFake struct {
	account *model.UserThirdPartyAccount
}

func (f *deviceAuthFlowAccountStoreFake) Get(context.Context, uint, string) (*model.UserThirdPartyAccount, error) {
	if f.account == nil {
		return nil, gorm.ErrRecordNotFound
	}
	copyAccount := *f.account
	return &copyAccount, nil
}

func (f *deviceAuthFlowAccountStoreFake) EnsurePlaceholder(context.Context, uint, string) (*model.UserThirdPartyAccount, error) {
	return nil, gorm.ErrRecordNotFound
}

type deviceAuthFlowStoreFake struct {
	mu                    sync.Mutex
	session               *model.FeishuAuthSession
	operation             *model.FeishuOperation
	claimCalls            int
	claimSessionIDs       []string
	claimErr              error
	claimEntered          chan struct{}
	claimRelease          <-chan struct{}
	claimWaitForContext   bool
	claimContextDeadline  time.Time
	claimContextCanceled  bool
	claimOnce             sync.Once
	attach                *store.FeishuDeviceAuthCredentialAttach
	attachErr             error
	attachEntered         chan struct{}
	attachRelease         <-chan struct{}
	attachOnce            sync.Once
	releaseCalls          int
	terminalStates        []string
	replaceCalls          int
	replacement           *model.FeishuAuthSession
	replaceInput          *store.FeishuDeviceAuthReplacement
	replaceErr            error
	releaseCallsAtReplace int
	replaceOwnerLive      bool
}

func cloneDeviceAuthFlowSession(session *model.FeishuAuthSession) *model.FeishuAuthSession {
	if session == nil {
		return nil
	}
	copySession := *session
	copySession.RequestedScopesJSON = append([]byte(nil), session.RequestedScopesJSON...)
	copySession.ResumeCredentialCiphertext = append([]byte(nil), session.ResumeCredentialCiphertext...)
	if session.OperationID != nil {
		value := *session.OperationID
		copySession.OperationID = &value
	}
	if session.LeaseUntil != nil {
		value := *session.LeaseUntil
		copySession.LeaseUntil = &value
	}
	if session.ResumeExpiresAt != nil {
		value := *session.ResumeExpiresAt
		copySession.ResumeExpiresAt = &value
	}
	return &copySession
}

func (f *deviceAuthFlowStoreFake) GetSessionForUser(_ context.Context, userID uint, generation uint64, id string) (*model.FeishuAuthSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session := f.sessionForIDLocked(id)
	if session == nil || session.UserID != userID || session.Generation != generation {
		return nil, gorm.ErrRecordNotFound
	}
	return cloneDeviceAuthFlowSession(session), nil
}

func (f *deviceAuthFlowStoreFake) GetOperationForUser(_ context.Context, userID uint, generation uint64, id string) (*model.FeishuOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.operation == nil || f.operation.ID != id || f.operation.UserID != userID || f.operation.Generation != generation {
		return nil, gorm.ErrRecordNotFound
	}
	operation := *f.operation
	operation.ResultSummaryJSON = append([]byte(nil), f.operation.ResultSummaryJSON...)
	return &operation, nil
}

func (f *deviceAuthFlowStoreFake) sessionForIDLocked(id string) *model.FeishuAuthSession {
	if f.session != nil && f.session.ID == id {
		return f.session
	}
	if f.replacement != nil && f.replacement.ID == id {
		return f.replacement
	}
	return nil
}

func (f *deviceAuthFlowStoreFake) ClaimSession(ctx context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error) {
	f.mu.Lock()
	f.claimCalls++
	f.claimSessionIDs = append(f.claimSessionIDs, id)
	if f.claimErr != nil {
		f.mu.Unlock()
		return false, f.claimErr
	}
	session := f.sessionForIDLocked(id)
	if session == nil || session.UserID != userID || session.Generation != generation ||
		session.State != model.FeishuAuthSessionPending || (session.LeaseUntil != nil && session.LeaseUntil.After(now)) {
		f.mu.Unlock()
		return false, nil
	}
	waitForContext := f.claimWaitForContext
	if deadline, ok := ctx.Deadline(); ok {
		f.claimContextDeadline = deadline
	}
	entered := f.claimEntered
	release := f.claimRelease
	if waitForContext {
		f.mu.Unlock()
		f.claimOnce.Do(func() {
			if entered != nil {
				close(entered)
			}
		})
		<-ctx.Done()
		f.mu.Lock()
		f.claimContextCanceled = true
		f.mu.Unlock()
		return false, ctx.Err()
	}
	session.LeaseOwner = owner
	value := leaseUntil.UTC()
	session.LeaseUntil = &value
	f.mu.Unlock()
	f.claimOnce.Do(func() {
		if entered != nil {
			close(entered)
		}
	})
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return true, nil
}

func (f *deviceAuthFlowStoreFake) RenewSession(_ context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session := f.sessionForIDLocked(id)
	if session == nil || session.UserID != userID || session.Generation != generation ||
		session.LeaseOwner != owner || session.LeaseUntil == nil || !session.LeaseUntil.After(now) {
		return false, nil
	}
	value := leaseUntil.UTC()
	session.LeaseUntil = &value
	return true, nil
}

func (f *deviceAuthFlowStoreFake) AttachDeviceAuthCredential(ctx context.Context, input store.FeishuDeviceAuthCredentialAttach) error {
	f.attachOnce.Do(func() {
		if f.attachEntered != nil {
			close(f.attachEntered)
		}
	})
	if f.attachRelease != nil {
		select {
		case <-f.attachRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	copyInput := input
	copyInput.Ciphertext = append([]byte(nil), input.Ciphertext...)
	f.attach = &copyInput
	if f.attachErr != nil {
		return f.attachErr
	}
	session := f.sessionForIDLocked(input.SessionID)
	if session == nil || session.LeaseOwner != input.LeaseOwner {
		return gorm.ErrRecordNotFound
	}
	session.ResumeCredentialCiphertext = append([]byte(nil), input.Ciphertext...)
	session.ResumeKeyVersion = input.KeyVersion
	expiresAt := input.ResumeExpiry.UTC()
	session.ResumeExpiresAt = &expiresAt
	session.ScopeHash = input.ScopeHash
	session.LeaseOwner = ""
	session.LeaseUntil = nil
	return nil
}

func (f *deviceAuthFlowStoreFake) ReleaseDeviceAuthLease(_ context.Context, userID uint, generation uint64, id, owner string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	session := f.sessionForIDLocked(id)
	if session == nil || session.UserID != userID || session.Generation != generation || session.LeaseOwner != owner {
		return false, nil
	}
	session.LeaseOwner = ""
	session.LeaseUntil = nil
	return true, nil
}

func (f *deviceAuthFlowStoreFake) TerminalizeDeviceAuthSession(_ context.Context, _ uint, _ uint64, _ string, owner, state string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.session == nil || f.session.LeaseOwner != owner {
		return gorm.ErrRecordNotFound
	}
	f.terminalStates = append(f.terminalStates, state)
	f.session.State = state
	completedAt := time.Now().UTC()
	f.session.CompletedAt = &completedAt
	f.session.ResumeCredentialCiphertext = nil
	f.session.ResumeKeyVersion = ""
	f.session.ResumeExpiresAt = nil
	f.session.LeaseOwner = ""
	f.session.LeaseUntil = nil
	return nil
}

func (f *deviceAuthFlowStoreFake) ReplaceDeviceAuthSession(_ context.Context, input store.FeishuDeviceAuthReplacement) (*model.FeishuAuthSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaceCalls++
	f.releaseCallsAtReplace = f.releaseCalls
	copyInput := input
	copyInput.OldSummary = append([]byte(nil), input.OldSummary...)
	copyInput.NewSummary = append([]byte(nil), input.NewSummary...)
	copyInput.NewSession = cloneDeviceAuthFlowSession(input.NewSession)
	f.replaceInput = &copyInput
	old := f.sessionForIDLocked(input.OldSessionID)
	f.replaceOwnerLive = old != nil && old.LeaseOwner == input.LeaseOwner && old.LeaseUntil != nil && old.LeaseUntil.After(input.Now)
	if f.replaceErr != nil {
		return nil, f.replaceErr
	}
	terminalSource := old != nil && old.LeaseOwner == "" && old.LeaseUntil == nil &&
		((old.ProtocolVersion == 1 && old.State == model.FeishuAuthSessionSuperseded && input.TerminalState == old.State) ||
			(old.ProtocolVersion == 2 && (old.State == model.FeishuAuthSessionRejected || old.State == model.FeishuAuthSessionExpired) && input.TerminalState == old.State))
	if old == nil || (!f.replaceOwnerLive && !terminalSource) || input.NewSession == nil {
		return nil, gorm.ErrRecordNotFound
	}
	old.State = input.TerminalState
	completedAt := input.Now.UTC()
	old.CompletedAt = &completedAt
	old.ResumeCredentialCiphertext = nil
	old.ResumeKeyVersion = ""
	old.ResumeExpiresAt = nil
	old.LeaseOwner = ""
	old.LeaseUntil = nil
	f.replacement = cloneDeviceAuthFlowSession(input.NewSession)
	return cloneDeviceAuthFlowSession(f.replacement), nil
}

func (f *deviceAuthFlowStoreFake) FinalizeDeviceAuthSuccess(context.Context, store.FeishuDeviceAuthSuccess) error {
	return gorm.ErrRecordNotFound
}

type deviceAuthFlowVaultFake struct {
	mu      sync.Mutex
	calls   int
	changed []bool
}

func (f *deviceAuthFlowVaultFake) WithHome(ctx context.Context, _ uint, _ uint64, callback func(string) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	changed, err := callback("/tmp/device-auth-flow-home")
	f.mu.Lock()
	f.calls++
	f.changed = append(f.changed, changed)
	f.mu.Unlock()
	return err
}

func (f *deviceAuthFlowVaultFake) WithHomeCandidate(context.Context, uint, uint64, func(string) error) (*CLIHomeCandidate, error) {
	return nil, gorm.ErrRecordNotFound
}

type deviceAuthFlowCLIFake struct {
	mu           sync.Mutex
	start        DeviceAuthStart
	startErr     error
	startCalls   int
	active       int
	startEntered chan struct{}
	startRelease <-chan struct{}
	startOnce    sync.Once
	scopes       [][]string
}

func (f *deviceAuthFlowCLIFake) StartUserAuth(ctx context.Context, _ string, scopes []string) (DeviceAuthStart, error) {
	f.mu.Lock()
	f.startCalls++
	f.active++
	f.scopes = append(f.scopes, append([]string(nil), scopes...))
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	f.startOnce.Do(func() {
		if f.startEntered != nil {
			close(f.startEntered)
		}
	})
	if f.startRelease != nil {
		select {
		case <-f.startRelease:
		case <-ctx.Done():
			return DeviceAuthStart{}, ctx.Err()
		}
	}
	return f.start, f.startErr
}

func (f *deviceAuthFlowCLIFake) CompleteUserAuth(context.Context, string, string) (DeviceAuthOutcome, error) {
	return DeviceAuthProtocolFailure, errors.New("not implemented in start fake")
}

func (f *deviceAuthFlowCLIFake) AuthStatus(context.Context, string) (bool, error) {
	return false, errors.New("not implemented in start fake")
}

func (f *deviceAuthFlowCLIFake) AppIDFromHome(context.Context, string) (string, error) {
	return "", errors.New("not implemented in start fake")
}

type deviceAuthFlowDispatcherFake struct{}

func (deviceAuthFlowDispatcherFake) DispatchResume(context.Context, uint, string) error { return nil }

type deviceAuthFlowStartFixture struct {
	now     time.Time
	account *model.UserThirdPartyAccount
	session *model.FeishuAuthSession
	store   *deviceAuthFlowStoreFake
	vault   *deviceAuthFlowVaultFake
	cli     *deviceAuthFlowCLIFake
	cipher  *DeviceAuthCredentialCipher
	flow    *DeviceAuthFlow
	scopes  []string
}

func newDeviceAuthFlowStartFixture(t *testing.T, operationID string) deviceAuthFlowStartFixture {
	t.Helper()
	now := time.Date(2026, 7, 17, 4, 5, 6, 123456789, time.UTC)
	scopes := []string{"docx:document:readonly", "offline_access"}
	encodedScopes, err := json.Marshal(scopes)
	require.NoError(t, err)
	var operationIDPointer *string
	if operationID != "" {
		operationIDPointer = &operationID
	}
	session := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000061", UserID: 7, Generation: 3,
		OperationID: operationIDPointer, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: encodedScopes, State: model.FeishuAuthSessionPending, ProtocolVersion: 2,
		ScopeHash: testDeviceAuthScopeHash(scopes), ExpiresAt: now.Add(10 * time.Minute),
	}
	account := &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: "cli_app_7", Generation: 3,
		ConnectionState: model.FeishuConnectionAppReady,
	}
	storeFake := &deviceAuthFlowStoreFake{session: cloneDeviceAuthFlowSession(session)}
	vault := &deviceAuthFlowVaultFake{}
	cli := &deviceAuthFlowCLIFake{start: DeviceAuthStart{
		VerificationURL: "https://open.feishu.cn/suite/passport/oauth/device?user_code=FLOW",
		DeviceCode:      "opaque-device-code", ExpiresIn: 3 * time.Minute,
	}}
	cipher := newDeviceAuthFlowCredentialCipher(t)
	flow, err := NewDeviceAuthFlow(DeviceAuthFlowDeps{
		Accounts: &deviceAuthFlowAccountStoreFake{account: account}, Sessions: storeFake,
		Vault: vault, CLI: cli, Cipher: cipher, Dispatcher: deviceAuthFlowDispatcherFake{}, Owner: "device-auth-flow-test",
		Now: func() time.Time { return now }, NewID: func() string { return "00000000-0000-4000-8000-000000000062" },
		NewLeaseToken: func() string { return "device-auth-start-lease" },
		LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute, HeartbeatInterval: 20 * time.Second,
		StartTimeout: time.Second, CompletionTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	return deviceAuthFlowStartFixture{
		now: now, account: account, session: session, store: storeFake, vault: vault,
		cli: cli, cipher: cipher, flow: flow, scopes: scopes,
	}
}

func newDeviceAuthFlowCredentialCipher(t *testing.T) *DeviceAuthCredentialCipher {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{37}, pkgcrypto.KeyLen))
	cipher, err := pkgcrypto.NewCipher(key)
	require.NoError(t, err)
	credentialCipher, err := NewDeviceAuthCredentialCipher(map[string]*pkgcrypto.Cipher{"v1": cipher}, "v1")
	require.NoError(t, err)
	return credentialCipher
}

func testDeviceAuthScopeHash(scopes []string) string {
	canonical := append([]string(nil), scopes...)
	// Fixtures pass already-canonical values; sorting in production is asserted
	// by the captured CLI scopes and stored hash tests below.
	sum := sha256.Sum256([]byte(strings.Join(canonical, " ")))
	return fmt.Sprintf("%x", sum)
}

func TestDeviceAuthFlow_StartReturnsURLOnlyAfterCredentialAttach(t *testing.T) {
	fixture := newDeviceAuthFlowStartFixture(t, "operation-device-start")
	fixture.store.attachEntered = make(chan struct{})
	attachRelease := make(chan struct{})
	fixture.store.attachRelease = attachRelease
	type result struct {
		action *OperationAction
		err    error
	}
	done := make(chan result, 1)
	go func() {
		action, err := fixture.flow.StartUserAuthorization(context.Background(), fixture.account, fixture.session, fixture.scopes)
		done <- result{action: action, err: err}
	}()
	<-fixture.store.attachEntered
	select {
	case premature := <-done:
		t.Fatalf("start returned before credential attach committed: %#v", premature)
	default:
	}
	require.Empty(t, fixture.flow.liveURLs.get(authSessionRegistryKey(fixture.session), fixture.now))
	close(attachRelease)
	started := <-done
	require.NoError(t, started.err)
	require.Equal(t, fixture.cli.start.VerificationURL, started.action.URL)
	require.Equal(t, fixture.cli.start.VerificationURL, fixture.flow.liveURLs.get(authSessionRegistryKey(fixture.session), fixture.now))
	fixture.vault.mu.Lock()
	require.Equal(t, []bool{false}, fixture.vault.changed, "start HOME must be read-only and never publish CLI side effects")
	fixture.vault.mu.Unlock()
	fixture.cli.mu.Lock()
	require.Zero(t, fixture.cli.active, "short start must leave no CLI process")
	fixture.cli.mu.Unlock()
}

func TestDeviceAuthFlow_StartAttachFailureDiscardsURLAndReleasesLease(t *testing.T) {
	fixture := newDeviceAuthFlowStartFixture(t, "operation-device-attach-failure")
	fixture.store.attachErr = errors.New("attach unavailable")

	action, err := fixture.flow.StartUserAuthorization(context.Background(), fixture.account, fixture.session, fixture.scopes)
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	require.Nil(t, action)
	require.Empty(t, fixture.flow.liveURLs.get(authSessionRegistryKey(fixture.session), fixture.now))
	fixture.store.mu.Lock()
	require.Equal(t, 1, fixture.store.releaseCalls)
	require.Empty(t, fixture.store.session.LeaseOwner)
	require.Empty(t, fixture.store.session.ResumeCredentialCiphertext)
	fixture.store.mu.Unlock()
}

func TestDeviceAuthFlow_StartConcurrentOwnersInvokeCLIOnce(t *testing.T) {
	fixture := newDeviceAuthFlowStartFixture(t, "operation-device-concurrent")
	fixture.cli.startEntered = make(chan struct{})
	startRelease := make(chan struct{})
	fixture.cli.startRelease = startRelease
	firstDone := make(chan error, 1)
	go func() {
		_, err := fixture.flow.StartUserAuthorization(context.Background(), fixture.account, fixture.session, fixture.scopes)
		firstDone <- err
	}()
	<-fixture.cli.startEntered

	secondAction, secondErr := fixture.flow.StartUserAuthorization(context.Background(), fixture.account, fixture.session, fixture.scopes)
	require.ErrorIs(t, secondErr, ErrDeviceAuthProcessing)
	require.Nil(t, secondAction)
	close(startRelease)
	require.NoError(t, <-firstDone)
	fixture.cli.mu.Lock()
	require.Equal(t, 1, fixture.cli.startCalls)
	fixture.cli.mu.Unlock()
}

func TestDeviceAuthFlow_StartRereadsDurablePreStartShapeAfterClaim(t *testing.T) {
	fixture := newDeviceAuthFlowStartFixture(t, "operation-device-stale-caller")
	resumeExpiry := fixture.now.Add(2 * time.Minute)
	fixture.store.session.ResumeCredentialCiphertext = []byte("already-attached-by-concurrent-owner")
	fixture.store.session.ResumeKeyVersion = "v1"
	fixture.store.session.ResumeExpiresAt = &resumeExpiry

	action, err := fixture.flow.StartUserAuthorization(
		context.Background(), fixture.account, fixture.session, fixture.scopes,
	)
	require.ErrorIs(t, err, ErrDeviceAuthProcessing)
	require.Nil(t, action)
	fixture.cli.mu.Lock()
	require.Zero(t, fixture.cli.startCalls, "a stale caller snapshot must not start a second CLI authorization")
	fixture.cli.mu.Unlock()
	fixture.store.mu.Lock()
	require.Equal(t, 1, fixture.store.releaseCalls)
	require.Empty(t, fixture.store.session.LeaseOwner)
	require.Nil(t, fixture.store.session.LeaseUntil)
	fixture.store.mu.Unlock()
}

func TestDeviceAuthFlow_StartClaimErrorReturnsProcessing(t *testing.T) {
	fixture := newDeviceAuthFlowStartFixture(t, "operation-device-claim-error")
	fixture.store.claimErr = errors.New("claim unavailable")

	action, err := fixture.flow.StartUserAuthorization(
		context.Background(), fixture.account, fixture.session, fixture.scopes,
	)
	require.ErrorIs(t, err, ErrDeviceAuthProcessing)
	require.Nil(t, action)
	fixture.cli.mu.Lock()
	require.Zero(t, fixture.cli.startCalls)
	fixture.cli.mu.Unlock()
}

func TestDeviceAuthFlow_StartUsesEarliestMillisecondExpiryForSealAndAttach(t *testing.T) {
	fixture := newDeviceAuthFlowStartFixture(t, "operation-device-expiry")
	fixture.cli.start.ExpiresIn = 90*time.Second + 987654*time.Nanosecond

	_, err := fixture.flow.StartUserAuthorization(context.Background(), fixture.account, fixture.session, fixture.scopes)
	require.NoError(t, err)
	expectedExpiry := fixture.now.Add(fixture.cli.start.ExpiresIn).UTC().Truncate(time.Millisecond)
	fixture.store.mu.Lock()
	attached := *fixture.store.attach
	fixture.store.mu.Unlock()
	require.Equal(t, expectedExpiry, attached.ResumeExpiry)
	require.Zero(t, attached.ResumeExpiry.Nanosecond()%int(time.Millisecond))
	binding := DeviceAuthCredentialBinding{
		UserID: fixture.session.UserID, Generation: fixture.session.Generation, AppID: fixture.account.AppID,
		OperationID: *fixture.session.OperationID, SessionID: fixture.session.ID,
		ScopeHash: attached.ScopeHash, ResumeExpiresAt: attached.ResumeExpiry,
	}
	deviceCode, err := fixture.cipher.Open(binding, attached.KeyVersion, attached.Ciphertext)
	require.NoError(t, err, "Seal and Attach must use the same exact millisecond expiry AAD")
	require.Equal(t, fixture.cli.start.DeviceCode, deviceCode)
}

func TestDeviceAuthFlow_StartRejectsIMScopeBeforeClaim(t *testing.T) {
	fixture := newDeviceAuthFlowStartFixture(t, "operation-device-im")

	action, err := fixture.flow.StartUserAuthorization(context.Background(), fixture.account, fixture.session, []string{"im:message:send"})
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	require.Nil(t, action)
	fixture.store.mu.Lock()
	require.Zero(t, fixture.store.claimCalls)
	fixture.store.mu.Unlock()
	fixture.cli.mu.Lock()
	require.Zero(t, fixture.cli.startCalls)
	fixture.cli.mu.Unlock()
}

func TestCanonicalDeviceAuthScopes_RejectsNonCatalogLookalikes(t *testing.T) {
	for _, scope := range []string{"docx:evil", "base:evil", "wiki:evil"} {
		t.Run(scope, func(t *testing.T) {
			canonical, err := canonicalDeviceAuthScopes([]string{scope})
			require.ErrorIs(t, err, ErrAuthSessionUnavailable)
			require.Nil(t, canonical)
		})
	}
}

func TestCanonicalDeviceAuthScopes_AcceptsCatalogScopesAndOfflineAccess(t *testing.T) {
	canonical, err := canonicalDeviceAuthScopes([]string{
		"wiki:node:retrieve", "offline_access", "docx:document:readonly", "base:record:read",
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		"base:record:read", "docx:document:readonly", "offline_access", "wiki:node:retrieve",
	}, canonical)
}

func TestDeviceAuthFlow_StartSeparatesManualAndOperationAAD(t *testing.T) {
	for _, operationID := range []string{"", "operation-device-aad"} {
		name := "manual"
		if operationID != "" {
			name = "operation"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newDeviceAuthFlowStartFixture(t, operationID)
			_, err := fixture.flow.StartUserAuthorization(context.Background(), fixture.account, fixture.session, fixture.scopes)
			require.NoError(t, err)
			fixture.store.mu.Lock()
			attached := *fixture.store.attach
			fixture.store.mu.Unlock()
			bindingOperationID := operationID
			if bindingOperationID == "" {
				bindingOperationID = "manual"
			}
			binding := DeviceAuthCredentialBinding{
				UserID: fixture.session.UserID, Generation: fixture.session.Generation, AppID: fixture.account.AppID,
				OperationID: bindingOperationID, SessionID: fixture.session.ID,
				ScopeHash: attached.ScopeHash, ResumeExpiresAt: attached.ResumeExpiry,
			}
			opened, err := fixture.cipher.Open(binding, attached.KeyVersion, attached.Ciphertext)
			require.NoError(t, err)
			require.Equal(t, fixture.cli.start.DeviceCode, opened)
			if operationID == "" {
				binding.OperationID = "operation-device-aad"
			} else {
				binding.OperationID = ""
			}
			_, err = fixture.cipher.Open(binding, attached.KeyVersion, attached.Ciphertext)
			require.Error(t, err, "manual and operation credentials must not share AAD")
		})
	}
}

type deviceAuthCompletionCLIFake struct {
	mu                     sync.Mutex
	start                  DeviceAuthStart
	startErr               error
	startCalls             int
	startContext           context.Context
	startScopes            [][]string
	outcome                DeviceAuthOutcome
	completeErr            error
	completeWaitForContext bool
	completeHook           func()
	completeCalls          int
	completeContext        context.Context
	completeHome           string
	completeDeviceCode     string
	authStatus             bool
	authStatusErr          error
	authStatusCalls        int
	authStatusContext      context.Context
	authStatusContextLive  bool
	authStatusHome         string
	appID                  string
	appIDErr               error
	appIDHook              func()
	appIDCalls             int
	appIDHome              string
	events                 []string
}

func (f *deviceAuthCompletionCLIFake) StartUserAuth(ctx context.Context, _ string, scopes []string) (DeviceAuthStart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	f.startContext = ctx
	f.startScopes = append(f.startScopes, append([]string(nil), scopes...))
	return f.start, f.startErr
}

func (f *deviceAuthCompletionCLIFake) CompleteUserAuth(ctx context.Context, home, deviceCode string) (DeviceAuthOutcome, error) {
	f.mu.Lock()
	f.completeCalls++
	f.completeContext = ctx
	f.completeHome = home
	f.completeDeviceCode = deviceCode
	f.events = append(f.events, "complete")
	waitForContext := f.completeWaitForContext
	hook := f.completeHook
	outcome := f.outcome
	err := f.completeErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if waitForContext {
		<-ctx.Done()
	}
	return outcome, err
}

func (f *deviceAuthCompletionCLIFake) AuthStatus(ctx context.Context, home string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authStatusCalls++
	f.authStatusContext = ctx
	f.authStatusContextLive = ctx.Err() == nil
	f.authStatusHome = home
	f.events = append(f.events, "auth_status")
	return f.authStatus, f.authStatusErr
}

func (f *deviceAuthCompletionCLIFake) AppIDFromHome(_ context.Context, home string) (string, error) {
	f.mu.Lock()
	f.appIDCalls++
	f.appIDHome = home
	f.events = append(f.events, "app_id")
	hook := f.appIDHook
	appID := f.appID
	err := f.appIDErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return appID, err
}

type deviceAuthCompletionVaultFake struct {
	mu        sync.Mutex
	calls     int
	home      string
	candidate *CLIHomeCandidate
}

func (*deviceAuthCompletionVaultFake) WithHome(ctx context.Context, _ uint, _ uint64, callback func(string) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := callback("/tmp/device-auth-replacement-home")
	return err
}

func (f *deviceAuthCompletionVaultFake) WithHomeCandidate(
	ctx context.Context,
	_ uint,
	_ uint64,
	callback func(string) error,
) (*CLIHomeCandidate, error) {
	home := "/tmp/device-auth-completion-home"
	f.mu.Lock()
	f.calls++
	f.home = home
	f.mu.Unlock()
	if err := callback(home); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	candidate := *f.candidate
	candidate.Vault.Ciphertext = append([]byte(nil), f.candidate.Vault.Ciphertext...)
	return &candidate, nil
}

type deviceAuthCompletionStoreFake struct {
	*deviceAuthFlowStoreFake
	getCalls              int
	blockPostClaimRead    bool
	postClaimReadDelay    time.Duration
	postClaimReadEntered  chan struct{}
	postClaimReadOnce     sync.Once
	postClaimReadCanceled bool
	renewCalls            int
	renewSignal           chan struct{}
	renewFail             bool
	renewErr              error
	renewFailAfter        int
	finalizeCalls         int
	finalizeInput         *store.FeishuDeviceAuthSuccess
	finalizeContextLive   bool
	finalizeErr           error
	finalizeLoseOwner     bool
	finalizeEntered       chan struct{}
	finalizeRelease       <-chan struct{}
	finalizeOnce          sync.Once
	published             *model.FeishuCLIVault
}

func (f *deviceAuthCompletionStoreFake) GetSessionForUser(
	ctx context.Context,
	userID uint,
	generation uint64,
	id string,
) (*model.FeishuAuthSession, error) {
	f.mu.Lock()
	f.getCalls++
	block := f.blockPostClaimRead && f.getCalls == 2
	delay := time.Duration(0)
	if f.getCalls == 2 {
		delay = f.postClaimReadDelay
	}
	entered := f.postClaimReadEntered
	f.mu.Unlock()
	if block {
		f.postClaimReadOnce.Do(func() {
			if entered != nil {
				close(entered)
			}
		})
		select {
		case <-ctx.Done():
			f.mu.Lock()
			f.postClaimReadCanceled = true
			f.mu.Unlock()
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
			return nil, errors.New("post-claim read exceeded safety window")
		}
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return f.deviceAuthFlowStoreFake.GetSessionForUser(ctx, userID, generation, id)
}

func (f *deviceAuthCompletionStoreFake) RenewSession(
	ctx context.Context,
	userID uint,
	generation uint64,
	id, owner string,
	now, leaseUntil time.Time,
) (bool, error) {
	f.mu.Lock()
	attempt := f.renewCalls + 1
	renewFail := f.renewFail || (f.renewFailAfter > 0 && attempt > f.renewFailAfter)
	renewErr := f.renewErr
	f.mu.Unlock()
	renewed, err := false, error(nil)
	if renewErr != nil {
		err = renewErr
	} else if !renewFail {
		renewed, err = f.deviceAuthFlowStoreFake.RenewSession(ctx, userID, generation, id, owner, now, leaseUntil)
	}
	f.mu.Lock()
	f.renewCalls++
	signal := f.renewSignal
	f.mu.Unlock()
	if signal != nil {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	return renewed, err
}

func (f *deviceAuthCompletionStoreFake) FinalizeDeviceAuthSuccess(
	ctx context.Context,
	input store.FeishuDeviceAuthSuccess,
) error {
	f.finalizeOnce.Do(func() {
		if f.finalizeEntered != nil {
			close(f.finalizeEntered)
		}
	})
	if f.finalizeRelease != nil {
		select {
		case <-f.finalizeRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalizeCalls++
	f.finalizeContextLive = ctx.Err() == nil
	copyInput := input
	copyInput.Candidate.Ciphertext = append([]byte(nil), input.Candidate.Ciphertext...)
	f.finalizeInput = &copyInput
	if f.finalizeLoseOwner && f.session != nil {
		f.session.LeaseOwner = "new-owner"
	}
	if f.finalizeErr != nil {
		return f.finalizeErr
	}
	if f.session == nil || f.session.LeaseOwner != input.LeaseOwner ||
		f.session.LeaseUntil == nil || !f.session.LeaseUntil.After(input.Now) {
		return gorm.ErrRecordNotFound
	}
	published := input.Candidate
	published.Ciphertext = append([]byte(nil), input.Candidate.Ciphertext...)
	f.published = &published
	f.session.State = model.FeishuAuthSessionCompleted
	f.session.ResumeCredentialCiphertext = nil
	f.session.ResumeKeyVersion = ""
	f.session.ResumeExpiresAt = nil
	f.session.LeaseOwner = ""
	f.session.LeaseUntil = nil
	return nil
}

type deviceAuthCompletionDispatcherFake struct {
	mu    sync.Mutex
	calls []string
}

func (f *deviceAuthCompletionDispatcherFake) DispatchResume(_ context.Context, _ uint, operationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, operationID)
	return nil
}

type deviceAuthCompletionFixture struct {
	now        time.Time
	account    *model.UserThirdPartyAccount
	session    *model.FeishuAuthSession
	store      *deviceAuthCompletionStoreFake
	vault      *deviceAuthCompletionVaultFake
	cli        *deviceAuthCompletionCLIFake
	dispatcher *deviceAuthCompletionDispatcherFake
	flow       *DeviceAuthFlow
}

func newDeviceAuthCompletionFixture(t *testing.T) deviceAuthCompletionFixture {
	t.Helper()
	now := time.Date(2026, 7, 17, 7, 0, 0, 0, time.UTC)
	operationID := "operation-device-complete"
	scopes := []string{"docx:document:readonly"}
	resumeExpiry := now.Add(5 * time.Minute)
	account := &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: "cli_app_7", Generation: 3,
		ConnectionState: model.FeishuConnectionWaitingUserAuth,
	}
	session := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000072", UserID: 7, Generation: 3,
		OperationID: &operationID, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State:               model.FeishuAuthSessionPending, ProtocolVersion: 2,
		ScopeHash: deviceAuthScopeHash(scopes), ResumeExpiresAt: &resumeExpiry,
		ExpiresAt: now.Add(10 * time.Minute),
	}
	credentialCipher := newDeviceAuthFlowCredentialCipher(t)
	ciphertext, keyVersion, err := credentialCipher.Seal(DeviceAuthCredentialBinding{
		UserID: session.UserID, Generation: session.Generation, AppID: account.AppID,
		OperationID: operationID, SessionID: session.ID, ScopeHash: session.ScopeHash,
		ResumeExpiresAt: resumeExpiry,
	}, "opaque-completion-device-code")
	require.NoError(t, err)
	session.ResumeCredentialCiphertext = ciphertext
	session.ResumeKeyVersion = keyVersion
	storeFake := &deviceAuthCompletionStoreFake{deviceAuthFlowStoreFake: &deviceAuthFlowStoreFake{
		session: cloneDeviceAuthFlowSession(session),
		operation: &model.FeishuOperation{
			ID: operationID, UserID: session.UserID, Generation: session.Generation,
			State:             model.FeishuOperationWaitingUserAuth,
			ResultSummaryJSON: []byte(`{"status":"waiting_user_auth","phase":"user_auth","session_id":"00000000-0000-4000-8000-000000000072"}`),
		},
	}}
	candidateCiphertext := []byte("sealed-device-auth-candidate")
	vault := &deviceAuthCompletionVaultFake{candidate: &CLIHomeCandidate{
		Vault: model.FeishuCLIVault{
			UserID: 7, Generation: 3, Ciphertext: candidateCiphertext, KeyVersion: "v1",
			Checksum: fmt.Sprintf("%x", sha256.Sum256(candidateCiphertext)), Revision: 5,
		},
		ExpectedRevision: 4,
	}}
	cli := &deviceAuthCompletionCLIFake{
		start: DeviceAuthStart{
			VerificationURL: "https://open.feishu.cn/suite/passport/oauth/device?user_code=REPLACEMENT",
			DeviceCode:      "opaque-replacement-device-code", ExpiresIn: 3 * time.Minute,
		},
		outcome: DeviceAuthPending, appID: account.AppID,
	}
	dispatcher := &deviceAuthCompletionDispatcherFake{}
	flow, err := NewDeviceAuthFlow(DeviceAuthFlowDeps{
		Accounts: &deviceAuthFlowAccountStoreFake{account: account}, Sessions: storeFake,
		Vault: vault, CLI: cli, Cipher: credentialCipher,
		Dispatcher: dispatcher, Owner: "device-auth-completion-test",
		Now: func() time.Time { return now }, NewID: func() string { return "00000000-0000-4000-8000-000000000073" },
		NewLeaseToken: func() string { return "device-auth-completion-lease" },
		LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 20 * time.Second, StartTimeout: time.Second, CompletionTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	return deviceAuthCompletionFixture{
		now: now, account: account, session: session, store: storeFake, vault: vault,
		cli: cli, dispatcher: dispatcher, flow: flow,
	}
}

func TestDeviceAuthFlow_CompletePendingRetainsCredential(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.cli.outcome = DeviceAuthPending

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.NoError(t, err)
	require.Equal(t, AuthorizationPending, result.NoticeCode)
	require.False(t, result.Completed)
	fixture.store.mu.Lock()
	require.Equal(t, 1, fixture.store.releaseCalls)
	require.NotEmpty(t, fixture.store.session.ResumeCredentialCiphertext)
	require.NotEmpty(t, fixture.store.session.ResumeKeyVersion)
	require.NotNil(t, fixture.store.session.ResumeExpiresAt)
	require.Zero(t, fixture.store.replaceCalls)
	fixture.store.mu.Unlock()
	fixture.dispatcher.mu.Lock()
	require.Empty(t, fixture.dispatcher.calls, "Task 9 owns durable dispatch")
	fixture.dispatcher.mu.Unlock()
}

func TestDeviceAuthFlow_CompleteRejectedTerminalizesBeforeReplacement(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.cli.outcome = DeviceAuthRejected

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, result.Action)
	require.Equal(t, "00000000-0000-4000-8000-000000000073", result.Action.SessionID)
	require.Contains(t, result.Action.URL, "REPLACEMENT")
	require.Empty(t, result.Action.Scopes, "replacement responses must not echo durable scopes")
	require.Empty(t, result.NoticeCode, "the terminal attempt must not be exposed before its replacement")

	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	require.Equal(t, 1, fixture.store.replaceCalls)
	require.Zero(t, fixture.store.releaseCallsAtReplace, "complete must retain its lease through replacement")
	require.True(t, fixture.store.replaceOwnerLive)
	require.Equal(t, "device-auth-completion-lease", fixture.store.replaceInput.LeaseOwner)
	require.Equal(t, model.FeishuAuthSessionRejected, fixture.store.replaceInput.TerminalState)
	require.Equal(t, model.FeishuAuthSessionRejected, fixture.store.session.State)
	require.Empty(t, fixture.store.session.ResumeCredentialCiphertext)
	require.Empty(t, fixture.store.session.ResumeKeyVersion)
	require.Nil(t, fixture.store.session.ResumeExpiresAt)
}

func TestDeviceAuthFlow_CompleteExpiredReturnsLiveReplacement(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.cli.outcome = DeviceAuthExpired

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, result.Action)
	require.Equal(t, *fixture.session.OperationID, result.Action.OperationID)
	require.Equal(t, model.FeishuAuthPhaseUserAuth, result.Action.Phase)
	require.True(t, result.Action.ExpiresAt.After(fixture.now))
	require.Contains(t, result.Action.URL, "REPLACEMENT")
	require.Empty(t, result.Action.Scopes)

	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	require.Equal(t, 1, fixture.store.replaceCalls)
	require.Zero(t, fixture.store.releaseCallsAtReplace)
	require.True(t, fixture.store.replaceOwnerLive)
	require.Equal(t, "device-auth-completion-lease", fixture.store.replaceInput.LeaseOwner)
	require.Equal(t, model.FeishuAuthSessionExpired, fixture.store.replaceInput.TerminalState)
	require.JSONEq(t,
		`{"status":"waiting_user_auth","phase":"user_auth","session_id":"00000000-0000-4000-8000-000000000073"}`,
		string(fixture.store.replaceInput.NewSummary),
	)
}

func TestDeviceAuthFlow_CompleteManualTerminalizesRejectedAndExpired(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		outcome DeviceAuthOutcome
		state   string
		notice  AuthorizationNoticeCode
	}{
		{name: "rejected", outcome: DeviceAuthRejected, state: model.FeishuAuthSessionRejected, notice: AuthorizationRejected},
		{name: "expired", outcome: DeviceAuthExpired, state: model.FeishuAuthSessionExpired, notice: AuthorizationExpired},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDeviceAuthCompletionFixture(t)
			fixture.cli.outcome = testCase.outcome
			fixture.session.OperationID = nil
			fixture.store.session.OperationID = nil
			ciphertext, keyVersion, err := fixture.flow.cipher.Seal(DeviceAuthCredentialBinding{
				UserID: fixture.session.UserID, Generation: fixture.session.Generation, AppID: fixture.account.AppID,
				OperationID: deviceAuthManualBindingID, SessionID: fixture.session.ID,
				ScopeHash: fixture.session.ScopeHash, ResumeExpiresAt: fixture.session.ResumeExpiresAt.UTC(),
			}, "manual-device-code")
			require.NoError(t, err)
			fixture.session.ResumeCredentialCiphertext = ciphertext
			fixture.session.ResumeKeyVersion = keyVersion
			fixture.store.session.ResumeCredentialCiphertext = append([]byte(nil), ciphertext...)
			fixture.store.session.ResumeKeyVersion = keyVersion
			fixture.flow.liveURLs.put(
				authSessionRegistryKey(fixture.session),
				"https://open.feishu.cn/suite/passport/oauth/device?user_code=MANUAL_OLD",
				fixture.now.Add(time.Minute),
			)

			result, err := fixture.flow.CompleteUserAuthorization(
				context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
			)
			require.NoError(t, err)
			require.Nil(t, result.Action)
			require.Equal(t, testCase.notice, result.NoticeCode)
			require.Empty(t, fixture.flow.liveURLs.get(authSessionRegistryKey(fixture.session), fixture.now))
			fixture.store.mu.Lock()
			defer fixture.store.mu.Unlock()
			require.Equal(t, testCase.state, fixture.store.session.State)
			require.Empty(t, fixture.store.session.ResumeCredentialCiphertext)
			require.Empty(t, fixture.store.session.ResumeKeyVersion)
			require.Nil(t, fixture.store.session.ResumeExpiresAt)
			require.Empty(t, fixture.store.session.LeaseOwner)
			require.Nil(t, fixture.store.session.LeaseUntil)
			require.Zero(t, fixture.store.releaseCalls)
			require.Zero(t, fixture.store.replaceCalls)
		})
	}
}

func TestDeviceAuthFlow_RefreshLegacyPendingSupersedesAndRebinds(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.store.session.ProtocolVersion = 1
	fixture.store.session.ScopeHash = ""
	fixture.store.session.ResumeCredentialCiphertext = nil
	fixture.store.session.ResumeKeyVersion = ""
	fixture.store.session.ResumeExpiresAt = nil
	oldSummary := []byte(`{"status":"waiting_user_auth","phase":"user_auth","session_id":"00000000-0000-4000-8000-000000000072","recovery_kind":"user_scope","recovery_scopes":["docx:document:readonly"]}`)

	result, err := fixture.flow.RefreshUserAuthorization(context.Background(), DeviceAuthRefreshRequest{
		UserID: fixture.session.UserID, Generation: fixture.session.Generation,
		OldSessionID: fixture.session.ID, OperationID: *fixture.session.OperationID,
		WaitingState: model.FeishuOperationWaitingUserAuth, OperationSummary: oldSummary,
	})
	require.NoError(t, err)
	require.NotNil(t, result.Action)
	require.Equal(t, "00000000-0000-4000-8000-000000000073", result.Action.SessionID)
	require.Equal(t, *fixture.session.OperationID, result.Action.OperationID)
	require.Contains(t, result.Action.URL, "REPLACEMENT")
	require.Empty(t, result.Action.Scopes)

	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	require.GreaterOrEqual(t, fixture.store.claimCalls, 2)
	require.Equal(t, fixture.session.ID, fixture.store.claimSessionIDs[0],
		"legacy refresh must first claim the exact old session")
	require.Equal(t, model.FeishuAuthSessionSuperseded, fixture.store.session.State)
	require.Equal(t, model.FeishuAuthSessionSuperseded, fixture.store.replaceInput.TerminalState)
	require.True(t, fixture.store.replaceOwnerLive)
	require.EqualValues(t, 2, fixture.store.replacement.ProtocolVersion)
	require.Equal(t, deviceAuthScopeHash([]string{"docx:document:readonly"}), fixture.store.replacement.ScopeHash)
	require.JSONEq(t, string(oldSummary), string(fixture.store.replaceInput.OldSummary))
	newSummary, decodeErr := decodeOperationSummary(fixture.store.replaceInput.NewSummary)
	require.NoError(t, decodeErr)
	require.Equal(t, fixture.store.replacement.ID, newSummary.SessionID)
	require.Equal(t, RecoveryUserScope, newSummary.RecoveryKind)
	require.Equal(t, []string{"docx:document:readonly"}, newSummary.RecoveryScopes)
}

func TestDeviceAuthFlow_RefreshCurrentV2ReplacementFencesLeaseAndTerminalSources(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		state          string
		credential     bool
		terminalState  string
		expiredSession bool
		expectedClaims int
	}{
		{name: "pending pre-start", state: model.FeishuAuthSessionPending, terminalState: model.FeishuAuthSessionSuperseded, expectedClaims: 2},
		{name: "pending full credential", state: model.FeishuAuthSessionPending, credential: true, terminalState: model.FeishuAuthSessionSuperseded, expectedClaims: 2},
		{name: "rejected terminal", state: model.FeishuAuthSessionRejected, terminalState: model.FeishuAuthSessionRejected, expiredSession: true, expectedClaims: 1},
		{name: "expired terminal", state: model.FeishuAuthSessionExpired, terminalState: model.FeishuAuthSessionExpired, expiredSession: true, expectedClaims: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDeviceAuthCompletionFixture(t)
			fixture.store.session.State = testCase.state
			if !testCase.credential {
				fixture.store.session.ResumeCredentialCiphertext = nil
				fixture.store.session.ResumeKeyVersion = ""
				fixture.store.session.ResumeExpiresAt = nil
			}
			if testCase.expiredSession {
				fixture.store.session.ExpiresAt = fixture.now.Add(-time.Minute)
			}
			oldSummary := []byte(`{"status":"waiting_user_auth","phase":"user_auth","session_id":"00000000-0000-4000-8000-000000000072","recovery_kind":"user_scope","recovery_scopes":["docx:document:readonly"]}`)

			result, err := fixture.flow.RefreshUserAuthorization(context.Background(), DeviceAuthRefreshRequest{
				UserID: fixture.session.UserID, Generation: fixture.session.Generation,
				OldSessionID: fixture.session.ID, OperationID: *fixture.session.OperationID,
				WaitingState: model.FeishuOperationWaitingUserAuth, OperationSummary: oldSummary,
			})
			require.NoError(t, err)
			require.NotNil(t, result.Action)
			require.Contains(t, result.Action.URL, "REPLACEMENT")
			fixture.store.mu.Lock()
			defer fixture.store.mu.Unlock()
			require.Equal(t, testCase.terminalState, fixture.store.session.State)
			require.Equal(t, testCase.terminalState, fixture.store.replaceInput.TerminalState)
			require.Equal(t, testCase.expectedClaims, fixture.store.claimCalls)
			require.Equal(t, model.FeishuAuthSessionPending, fixture.store.replacement.State)
			require.NotEmpty(t, fixture.store.replacement.ResumeCredentialCiphertext)
		})
	}

	t.Run("pending source with live lease fails closed", func(t *testing.T) {
		fixture := newDeviceAuthCompletionFixture(t)
		fixture.store.session.ResumeCredentialCiphertext = nil
		fixture.store.session.ResumeKeyVersion = ""
		fixture.store.session.ResumeExpiresAt = nil
		fixture.store.session.LeaseOwner = "another-live-owner"
		leaseUntil := fixture.now.Add(time.Minute)
		fixture.store.session.LeaseUntil = &leaseUntil
		oldSummary := []byte(`{"status":"waiting_user_auth","phase":"user_auth","session_id":"00000000-0000-4000-8000-000000000072","recovery_kind":"user_scope","recovery_scopes":["docx:document:readonly"]}`)

		result, err := fixture.flow.RefreshUserAuthorization(context.Background(), DeviceAuthRefreshRequest{
			UserID: fixture.session.UserID, Generation: fixture.session.Generation,
			OldSessionID: fixture.session.ID, OperationID: *fixture.session.OperationID,
			WaitingState: model.FeishuOperationWaitingUserAuth, OperationSummary: oldSummary,
		})
		require.ErrorIs(t, err, ErrDeviceAuthProcessing)
		require.Nil(t, result)
		fixture.store.mu.Lock()
		defer fixture.store.mu.Unlock()
		require.Zero(t, fixture.store.replaceCalls)
		require.Equal(t, "another-live-owner", fixture.store.session.LeaseOwner)
	})
}

func TestDeviceAuthFlow_ReplacementStartFailureKeepsReclaimableSession(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		startErr  error
		expected  error
		badResult bool
		breakSeal bool
	}{
		{name: "dependency", startErr: errDeviceAuthCLIDependency, expected: ErrDeviceAuthDependency},
		{name: "deterministic protocol error", startErr: errDeviceAuthCLIProtocol, expected: ErrAuthSessionUnavailable},
		{name: "deterministic parser result", expected: ErrAuthSessionUnavailable, badResult: true},
		{name: "deterministic crypto seal error", expected: ErrAuthSessionUnavailable, breakSeal: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDeviceAuthCompletionFixture(t)
			fixture.cli.outcome = DeviceAuthRejected
			fixture.cli.startErr = testCase.startErr
			if testCase.badResult {
				fixture.cli.start.VerificationURL = "http://attacker.invalid/device"
			}
			if testCase.breakSeal {
				fixture.cli.completeHook = func() {
					fixture.flow.cipher = &DeviceAuthCredentialCipher{}
				}
			}

			result, err := fixture.flow.CompleteUserAuthorization(
				context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
			)
			require.ErrorIs(t, err, testCase.expected)
			require.Nil(t, result)
			fixture.store.mu.Lock()
			defer fixture.store.mu.Unlock()
			require.Equal(t, model.FeishuAuthSessionRejected, fixture.store.session.State)
			require.Empty(t, fixture.store.session.ResumeCredentialCiphertext)
			require.Empty(t, fixture.store.session.ResumeKeyVersion)
			require.Nil(t, fixture.store.session.ResumeExpiresAt)
			require.NotNil(t, fixture.store.replacement)
			require.Equal(t, model.FeishuAuthSessionPending, fixture.store.replacement.State)
			require.Empty(t, fixture.store.replacement.LeaseOwner)
			require.Nil(t, fixture.store.replacement.LeaseUntil)
			require.Empty(t, fixture.store.replacement.ResumeCredentialCiphertext)
			require.Empty(t, fixture.store.replacement.ResumeKeyVersion)
			require.Nil(t, fixture.store.replacement.ResumeExpiresAt)
		})
	}
}

func TestDeviceAuthFlow_ReplacementNeverRevivesOldCredential(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.cli.outcome = DeviceAuthExpired
	fixture.cli.startErr = errDeviceAuthCLIDependency
	oldCiphertext := append([]byte(nil), fixture.store.session.ResumeCredentialCiphertext...)
	fixture.flow.liveURLs.put(
		authSessionRegistryKey(fixture.session),
		"https://open.feishu.cn/suite/passport/oauth/device?user_code=OLD",
		fixture.now.Add(time.Minute),
	)

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.ErrorIs(t, err, ErrDeviceAuthDependency)
	require.Nil(t, result)
	require.Empty(t, fixture.flow.liveURLs.get(authSessionRegistryKey(fixture.session), fixture.now),
		"a terminal attempt must never retain a reusable old URL")
	fixture.store.mu.Lock()
	defer fixture.store.mu.Unlock()
	require.NotEmpty(t, oldCiphertext)
	require.Empty(t, fixture.store.session.ResumeCredentialCiphertext)
	require.NotEqual(t, oldCiphertext, fixture.store.session.ResumeCredentialCiphertext)
	require.Empty(t, fixture.store.replacement.ResumeCredentialCiphertext)
	require.Equal(t, model.FeishuAuthSessionPending, fixture.store.replacement.State)
}

func TestDeviceAuthFlow_CompleteConcurrentOwnerReturnsProcessing(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	leaseUntil := fixture.now.Add(time.Minute)
	fixture.store.session.LeaseOwner = "other-completion-owner"
	fixture.store.session.LeaseUntil = &leaseUntil

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.ErrorIs(t, err, ErrDeviceAuthProcessing)
	require.Nil(t, result)
	fixture.cli.mu.Lock()
	require.Zero(t, fixture.cli.completeCalls)
	fixture.cli.mu.Unlock()
}

func TestDeviceAuthFlow_CompleteClaimUsesDetachedBoundedContext(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.flow.leaseDuration = 60 * time.Millisecond
	fixture.flow.heartbeatInterval = 10 * time.Millisecond
	fixture.store.claimWaitForContext = true
	fixture.store.claimEntered = make(chan struct{})
	started := time.Now()

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)

	require.ErrorIs(t, err, ErrDeviceAuthProcessing)
	require.Nil(t, result)
	require.Less(t, time.Since(started), time.Second, "claim must not inherit an unbounded caller context")
	fixture.store.mu.Lock()
	require.True(t, fixture.store.claimContextCanceled, "claim must exit through its own deadline")
	require.False(t, fixture.store.claimContextDeadline.IsZero(), "claim must receive a strict deadline")
	require.LessOrEqual(t, fixture.store.claimContextDeadline.Sub(started), 100*time.Millisecond)
	fixture.store.mu.Unlock()
	fixture.cli.mu.Lock()
	require.Zero(t, fixture.cli.completeCalls)
	fixture.cli.mu.Unlock()
}

func TestDeviceAuthFlow_CompleteClaimBudgetUsesEarliestDeadline(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		lease        time.Duration
		resumeExpiry time.Duration
		expected     time.Duration
	}{
		{name: "five second ceiling", lease: time.Minute, resumeExpiry: 5 * time.Minute, expected: 5 * time.Second},
		{name: "lease expiry", lease: 80 * time.Millisecond, resumeExpiry: 5 * time.Minute, expected: 80 * time.Millisecond},
		{name: "credential expiry", lease: time.Minute, resumeExpiry: 120 * time.Millisecond, expected: 120 * time.Millisecond},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDeviceAuthCompletionFixture(t)
			fixture.flow.leaseDuration = testCase.lease
			fixture.flow.heartbeatInterval = min(10*time.Millisecond, testCase.lease/3)
			resumeExpiry := fixture.now.Add(testCase.resumeExpiry)
			fixture.session.ResumeExpiresAt = &resumeExpiry
			fixture.store.session.ResumeExpiresAt = &resumeExpiry
			fixture.store.renewFail = true
			started := time.Now()

			result, err := fixture.flow.CompleteUserAuthorization(
				context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
			)

			require.ErrorIs(t, err, ErrDeviceAuthConflict)
			require.Nil(t, result)
			fixture.store.mu.Lock()
			remaining := fixture.store.claimContextDeadline.Sub(started)
			fixture.store.mu.Unlock()
			require.InDelta(t, testCase.expected.Seconds(), remaining.Seconds(), 0.05,
				"claim deadline must be min(5s, lease expiry, credential expiry)")
			fixture.cli.mu.Lock()
			require.Zero(t, fixture.cli.completeCalls)
			fixture.cli.mu.Unlock()
		})
	}
}

func TestDeviceAuthFlow_CompletePostClaimRereadIsDetachedAndBounded(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.flow.leaseDuration = 60 * time.Millisecond
	fixture.flow.heartbeatInterval = 10 * time.Millisecond
	fixture.store.blockPostClaimRead = true
	fixture.store.postClaimReadEntered = make(chan struct{})
	started := time.Now()

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.ErrorIs(t, err, ErrDeviceAuthConflict)
	require.Nil(t, result)
	require.Less(t, time.Since(started), time.Second)
	fixture.cli.mu.Lock()
	require.Zero(t, fixture.cli.completeCalls)
	fixture.cli.mu.Unlock()
	fixture.store.mu.Lock()
	require.True(t, fixture.store.postClaimReadCanceled, "post-claim reads must exit through the shared bounded context")
	require.Equal(t, 1, fixture.store.releaseCalls)
	fixture.store.mu.Unlock()
}

func TestDeviceAuthFlow_CompleteSuccessPublishesCandidateAtomically(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.cli.outcome = DeviceAuthCompleted
	fixture.cli.authStatus = true

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.NoError(t, err)
	require.True(t, result.Completed)
	require.Empty(t, result.NoticeCode)
	fixture.store.mu.Lock()
	require.Equal(t, 1, fixture.store.finalizeCalls)
	require.NotNil(t, fixture.store.finalizeInput)
	require.Equal(t, fixture.vault.candidate.ExpectedRevision, fixture.store.finalizeInput.ExpectedVaultRevision)
	require.Equal(t, fixture.vault.candidate.Vault, fixture.store.finalizeInput.Candidate)
	require.Equal(t, fixture.account.AppID, fixture.store.finalizeInput.ExpectedAppID)
	require.Equal(t, LarkCLIVersion, fixture.store.finalizeInput.Evidence.CLIVersion)
	require.Equal(t, model.FeishuAuthSessionCompleted, fixture.store.session.State)
	require.NotNil(t, fixture.store.published)
	fixture.store.mu.Unlock()
	fixture.dispatcher.mu.Lock()
	require.Empty(t, fixture.dispatcher.calls, "Task 9 owns durable dispatch after completion")
	fixture.dispatcher.mu.Unlock()
	fixture.cli.mu.Lock()
	require.Equal(t, "opaque-completion-device-code", fixture.cli.completeDeviceCode)
	require.Equal(t, []string{"complete", "auth_status", "app_id"}, fixture.cli.events)
	fixture.cli.mu.Unlock()

	t.Run("app identity mismatch cannot publish", func(t *testing.T) {
		mismatch := newDeviceAuthCompletionFixture(t)
		mismatch.cli.outcome = DeviceAuthCompleted
		mismatch.cli.authStatus = true
		mismatch.cli.appID = "different_app"

		result, err := mismatch.flow.CompleteUserAuthorization(
			context.Background(), mismatch.session.UserID, mismatch.session.Generation, mismatch.session.ID,
		)
		require.Error(t, err)
		require.Nil(t, result)
		mismatch.store.mu.Lock()
		require.Nil(t, mismatch.store.published)
		mismatch.store.mu.Unlock()
	})
}

func TestDeviceAuthFlow_CompleteAppIDEvidenceTimeoutRetainsCredential(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		expire bool
		notice AuthorizationNoticeCode
	}{
		{name: "credential remains live", notice: AuthorizationPending},
		{name: "credential expires during evidence read", expire: true, notice: AuthorizationExpired},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDeviceAuthCompletionFixture(t)
			fixture.cli.outcome = DeviceAuthCompleted
			fixture.cli.authStatus = true
			fixture.cli.appIDErr = context.DeadlineExceeded
			if testCase.expire {
				fixture.flow.leaseDuration = 10 * time.Minute
				currentNow := fixture.now
				fixture.flow.now = func() time.Time { return currentNow }
				fixture.cli.appIDHook = func() { currentNow = fixture.session.ResumeExpiresAt.UTC() }
			}

			result, err := fixture.flow.CompleteUserAuthorization(
				context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
			)
			require.NoError(t, err)
			if testCase.expire {
				require.Empty(t, result.NoticeCode)
				require.NotNil(t, result.Action)
				require.NotEqual(t, fixture.session.ID, result.Action.SessionID)
			} else {
				require.Equal(t, testCase.notice, result.NoticeCode)
				require.False(t, result.Completed)
			}
			fixture.store.mu.Lock()
			if testCase.expire {
				require.Zero(t, fixture.store.releaseCallsAtReplace)
				require.Equal(t, model.FeishuAuthSessionExpired, fixture.store.session.State)
				require.Empty(t, fixture.store.session.ResumeCredentialCiphertext)
				require.Empty(t, fixture.store.session.ResumeKeyVersion)
				require.Nil(t, fixture.store.session.ResumeExpiresAt)
			} else {
				require.Equal(t, 1, fixture.store.releaseCalls)
				require.NotEmpty(t, fixture.store.session.ResumeCredentialCiphertext)
				require.NotEmpty(t, fixture.store.session.ResumeKeyVersion)
				require.NotNil(t, fixture.store.session.ResumeExpiresAt)
			}
			require.Empty(t, fixture.store.terminalStates)
			require.Zero(t, fixture.store.finalizeCalls)
			require.Nil(t, fixture.store.published)
			fixture.store.mu.Unlock()
		})
	}
}

func TestNewDeviceAuthFlow_ClampsHeartbeatToLeaseThirdAndRejectsTinyLease(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	newFlow := func(leaseDuration, heartbeatInterval time.Duration) (*DeviceAuthFlow, error) {
		return NewDeviceAuthFlow(DeviceAuthFlowDeps{
			Accounts: &deviceAuthFlowAccountStoreFake{account: fixture.account}, Sessions: fixture.store,
			Vault: fixture.vault, CLI: fixture.cli, Cipher: newDeviceAuthFlowCredentialCipher(t),
			Dispatcher: fixture.dispatcher, Owner: "device-auth-heartbeat-gate",
			LeaseDuration: leaseDuration, HeartbeatInterval: heartbeatInterval,
		})
	}

	t.Run("clamp", func(t *testing.T) {
		clamped, err := newFlow(time.Minute, 50*time.Second)
		require.NoError(t, err)
		require.Equal(t, 20*time.Second, clamped.heartbeatInterval)
	})

	t.Run("production defaults unchanged", func(t *testing.T) {
		defaulted, err := newFlow(0, 0)
		require.NoError(t, err)
		require.Equal(t, authSessionDefaultLeaseDuration, defaulted.leaseDuration)
		require.Equal(t, authSessionDefaultHeartbeatInterval, defaulted.heartbeatInterval)
	})

	t.Run("tiny lease", func(t *testing.T) {
		tiny, err := newFlow(2*time.Nanosecond, time.Nanosecond)
		require.ErrorIs(t, err, ErrAuthSessionUnavailable)
		require.Nil(t, tiny)
	})
}

func TestDeviceAuthFlow_CompleteAmbiguousReconcilesAuthStatus(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.cli.outcome = DeviceAuthAmbiguous
	fixture.cli.authStatus = true

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.NoError(t, err)
	require.True(t, result.Completed)
	fixture.vault.mu.Lock()
	require.Equal(t, 1, fixture.vault.calls, "completion and reconciliation must share one candidate HOME")
	home := fixture.vault.home
	fixture.vault.mu.Unlock()
	fixture.cli.mu.Lock()
	require.Equal(t, []string{"complete", "auth_status", "app_id"}, fixture.cli.events)
	require.Equal(t, home, fixture.cli.completeHome)
	require.Equal(t, home, fixture.cli.authStatusHome)
	require.Equal(t, home, fixture.cli.appIDHome)
	fixture.cli.mu.Unlock()
}

func TestDeviceAuthFlow_CompleteLateOwnerCannotPublish(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.cli.outcome = DeviceAuthCompleted
	fixture.cli.authStatus = true
	fixture.store.finalizeLoseOwner = true

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.ErrorIs(t, err, ErrDeviceAuthConflict)
	require.Nil(t, result)
	fixture.store.mu.Lock()
	require.Nil(t, fixture.store.published)
	require.Equal(t, model.FeishuAuthSessionPending, fixture.store.session.State)
	require.NotEmpty(t, fixture.store.session.ResumeCredentialCiphertext)
	fixture.store.mu.Unlock()

	t.Run("heartbeat fence loss cancels CLI", func(t *testing.T) {
		lost := newDeviceAuthCompletionFixture(t)
		lost.flow.leaseDuration = 500 * time.Millisecond
		lost.flow.heartbeatInterval = 10 * time.Millisecond
		lost.store.renewFailAfter = 1
		lost.cli.outcome = DeviceAuthAmbiguous
		lost.cli.completeWaitForContext = true

		result, err := lost.flow.CompleteUserAuthorization(
			context.Background(), lost.session.UserID, lost.session.Generation, lost.session.ID,
		)
		require.ErrorIs(t, err, ErrDeviceAuthConflict)
		require.Nil(t, result)
		lost.cli.mu.Lock()
		require.ErrorIs(t, lost.cli.completeContext.Err(), context.Canceled)
		lost.cli.mu.Unlock()
		lost.store.mu.Lock()
		require.Zero(t, lost.store.finalizeCalls)
		require.Nil(t, lost.store.published)
		lost.store.mu.Unlock()
	})
}

func TestDeviceAuthFlow_CompleteRenewsOwnedLeaseBeforeCLI(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	const (
		leaseDuration     = 600 * time.Millisecond
		heartbeatInterval = 200 * time.Millisecond
		postClaimDelay    = 425 * time.Millisecond
	)
	logicalStart := fixture.now
	wallStart := time.Now()
	fixture.flow.now = func() time.Time {
		return logicalStart.Add(time.Since(wallStart))
	}
	fixture.flow.leaseDuration = leaseDuration
	fixture.flow.heartbeatInterval = heartbeatInterval
	fixture.store.postClaimReadDelay = postClaimDelay
	fixture.cli.outcome = DeviceAuthPending

	var (
		renewCallsBeforeCLI int
		leaseUntilAtCLI     time.Time
		leaseLiveAtCLI      bool
	)
	fixture.cli.completeHook = func() {
		now := fixture.flow.now()
		fixture.store.mu.Lock()
		renewCallsBeforeCLI = fixture.store.renewCalls
		if fixture.store.session.LeaseUntil != nil {
			leaseUntilAtCLI = fixture.store.session.LeaseUntil.UTC()
			leaseLiveAtCLI = leaseUntilAtCLI.After(now)
		}
		fixture.store.mu.Unlock()
	}

	result, err := fixture.flow.CompleteUserAuthorization(
		context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
	)
	require.NoError(t, err)
	require.False(t, result.Completed)
	require.GreaterOrEqual(t, renewCallsBeforeCLI, 1,
		"a successful exact-owner renewal must fence the lease before the one-shot CLI starts")
	require.True(t, leaseLiveAtCLI, "CLI must start under a live refreshed lease")
	require.True(t, leaseUntilAtCLI.After(logicalStart.Add(leaseDuration+postClaimDelay/2)),
		"the lease visible to CLI must be newer than the original post-claim lease")
}

func TestDeviceAuthFlow_CompletePreCLIRenewFailureReleasesLease(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		renewErr error
	}{
		{name: "compare and swap miss"},
		{name: "store error", renewErr: errors.New("renew unavailable")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newDeviceAuthCompletionFixture(t)
			fixture.store.renewFail = testCase.renewErr == nil
			fixture.store.renewErr = testCase.renewErr

			result, err := fixture.flow.CompleteUserAuthorization(
				context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
			)

			require.ErrorIs(t, err, ErrDeviceAuthConflict)
			require.Nil(t, result)
			fixture.cli.mu.Lock()
			require.Zero(t, fixture.cli.completeCalls, "CLI must not run without a synchronous exact-owner renewal")
			fixture.cli.mu.Unlock()
			fixture.store.mu.Lock()
			require.Equal(t, 1, fixture.store.renewCalls)
			require.Equal(t, 1, fixture.store.releaseCalls, "failed renewal must best-effort release the claimed lease")
			require.Empty(t, fixture.store.session.LeaseOwner)
			require.Nil(t, fixture.store.session.LeaseUntil)
			fixture.store.mu.Unlock()
		})
	}
}

func TestDeviceAuthFlow_CompleteRenewsLeaseUntilFinalize(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.cli.outcome = DeviceAuthCompleted
	fixture.cli.authStatus = true
	fixture.flow.leaseDuration = 500 * time.Millisecond
	fixture.flow.heartbeatInterval = 10 * time.Millisecond
	fixture.store.renewSignal = make(chan struct{}, 1)
	fixture.store.finalizeEntered = make(chan struct{})
	finalizeRelease := make(chan struct{})
	fixture.store.finalizeRelease = finalizeRelease
	type completionResult struct {
		value *DeviceAuthCompletion
		err   error
	}
	done := make(chan completionResult, 1)
	go func() {
		value, err := fixture.flow.CompleteUserAuthorization(
			context.Background(), fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
		)
		done <- completionResult{value: value, err: err}
	}()
	select {
	case <-fixture.store.finalizeEntered:
	case <-time.After(time.Second):
		t.Fatal("completion did not reach atomic finalize")
	}
	require.Eventually(t, func() bool {
		fixture.store.mu.Lock()
		defer fixture.store.mu.Unlock()
		return fixture.store.renewCalls >= 2
	}, time.Second, time.Millisecond, "lease heartbeat stopped before atomic finalize returned")
	select {
	case premature := <-done:
		t.Fatalf("completion returned before finalize released: %#v", premature)
	default:
	}
	close(finalizeRelease)
	completed := <-done
	require.NoError(t, completed.err)
	require.True(t, completed.value.Completed)
	fixture.store.mu.Lock()
	require.GreaterOrEqual(t, fixture.store.renewCalls, 2)
	fixture.store.mu.Unlock()
}

func TestDeviceAuthFlow_ReconcileUsesFreshContextAfterCLITimeout(t *testing.T) {
	fixture := newDeviceAuthCompletionFixture(t)
	fixture.flow.completionTimeout = 20 * time.Millisecond
	fixture.cli.outcome = DeviceAuthAmbiguous
	fixture.cli.completeWaitForContext = true
	fixture.cli.authStatus = true
	fixture.store.claimEntered = make(chan struct{})
	claimRelease := make(chan struct{})
	fixture.store.claimRelease = claimRelease
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	type completionResult struct {
		value *DeviceAuthCompletion
		err   error
	}
	done := make(chan completionResult, 1)
	go func() {
		value, err := fixture.flow.CompleteUserAuthorization(
			callerCtx, fixture.session.UserID, fixture.session.Generation, fixture.session.ID,
		)
		done <- completionResult{value: value, err: err}
	}()
	<-fixture.store.claimEntered
	cancelCaller()
	close(claimRelease)
	completed := <-done
	result, err := completed.value, completed.err
	require.NoError(t, err)
	require.True(t, result.Completed)
	fixture.cli.mu.Lock()
	require.ErrorIs(t, fixture.cli.completeContext.Err(), context.DeadlineExceeded)
	require.True(t, fixture.cli.authStatusContextLive, "reconciliation must not reuse the expired CLI context")
	require.NotEqual(t, fixture.cli.completeContext, fixture.cli.authStatusContext)
	fixture.cli.mu.Unlock()
	fixture.store.mu.Lock()
	require.True(t, fixture.store.finalizeContextLive, "atomic mutation must use a fresh live context")
	fixture.store.mu.Unlock()
}
