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
	mu             sync.Mutex
	session        *model.FeishuAuthSession
	claimCalls     int
	claimErr       error
	attach         *store.FeishuDeviceAuthCredentialAttach
	attachErr      error
	attachEntered  chan struct{}
	attachRelease  <-chan struct{}
	attachOnce     sync.Once
	releaseCalls   int
	terminalStates []string
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
	if f.session == nil || f.session.UserID != userID || f.session.Generation != generation || f.session.ID != id {
		return nil, gorm.ErrRecordNotFound
	}
	return cloneDeviceAuthFlowSession(f.session), nil
}

func (f *deviceAuthFlowStoreFake) ClaimSession(_ context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls++
	if f.claimErr != nil {
		return false, f.claimErr
	}
	if f.session == nil || f.session.UserID != userID || f.session.Generation != generation || f.session.ID != id ||
		f.session.State != model.FeishuAuthSessionPending || (f.session.LeaseUntil != nil && f.session.LeaseUntil.After(now)) {
		return false, nil
	}
	f.session.LeaseOwner = owner
	value := leaseUntil.UTC()
	f.session.LeaseUntil = &value
	return true, nil
}

func (f *deviceAuthFlowStoreFake) RenewSession(_ context.Context, userID uint, generation uint64, id, owner string, now, leaseUntil time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.session == nil || f.session.UserID != userID || f.session.Generation != generation || f.session.ID != id ||
		f.session.LeaseOwner != owner || f.session.LeaseUntil == nil || !f.session.LeaseUntil.After(now) {
		return false, nil
	}
	value := leaseUntil.UTC()
	f.session.LeaseUntil = &value
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
	if f.session == nil || f.session.LeaseOwner != input.LeaseOwner {
		return gorm.ErrRecordNotFound
	}
	f.session.ResumeCredentialCiphertext = append([]byte(nil), input.Ciphertext...)
	f.session.ResumeKeyVersion = input.KeyVersion
	expiresAt := input.ResumeExpiry.UTC()
	f.session.ResumeExpiresAt = &expiresAt
	f.session.ScopeHash = input.ScopeHash
	f.session.LeaseOwner = ""
	f.session.LeaseUntil = nil
	return nil
}

func (f *deviceAuthFlowStoreFake) ReleaseDeviceAuthLease(_ context.Context, userID uint, generation uint64, id, owner string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	if f.session == nil || f.session.UserID != userID || f.session.Generation != generation || f.session.ID != id || f.session.LeaseOwner != owner {
		return false, nil
	}
	f.session.LeaseOwner = ""
	f.session.LeaseUntil = nil
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
	f.session.LeaseOwner = ""
	f.session.LeaseUntil = nil
	return nil
}

func (f *deviceAuthFlowStoreFake) ReplaceDeviceAuthSession(context.Context, store.FeishuDeviceAuthReplacement) (*model.FeishuAuthSession, error) {
	return nil, gorm.ErrRecordNotFound
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
