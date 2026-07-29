package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

type authSessionCLIFake struct {
	mu        sync.Mutex
	urls      []string
	runErr    error
	status    bool
	statusErr error
	release   <-chan struct{}
	releases  []<-chan struct{}
	// holdAfterCancel deliberately keeps the fake runner (and therefore the
	// temporary HOME owned by the vault callback) alive after cancellation. It
	// makes lifecycle teardown ordering observable without timing guesses.
	holdAfterCancel <-chan struct{}
	cancelObserved  chan<- struct{}
	urlDelays       []time.Duration
	argv            [][]string
	statusCalls     int
	appID           string
	appIDErr        error
	activeRuns      int
	deviceCodes     []string
	deviceExpiresIn time.Duration
	completeOutcome DeviceAuthOutcome
	completeErr     error
	completeCalls   int
}

func releaseAuthSessionCLIFake(t *testing.T, release chan struct{}) func() {
	t.Helper()
	var once sync.Once
	releaseOnce := func() {
		once.Do(func() { close(release) })
	}
	t.Cleanup(releaseOnce)
	return releaseOnce
}

func (f *authSessionCLIFake) RunBlocking(
	ctx context.Context,
	_ string,
	argv []string,
	onURL func(string) error,
) error {
	f.mu.Lock()
	f.activeRuns++
	defer func() {
		f.mu.Lock()
		f.activeRuns--
		f.mu.Unlock()
	}()
	index := len(f.argv)
	f.argv = append(f.argv, append([]string(nil), argv...))
	url := ""
	if index < len(f.urls) {
		url = f.urls[index]
	}
	release := f.release
	if index < len(f.releases) {
		release = f.releases[index]
	}
	var urlDelay time.Duration
	if index < len(f.urlDelays) {
		urlDelay = f.urlDelays[index]
	}
	runErr := f.runErr
	f.mu.Unlock()
	if urlDelay > 0 {
		timer := time.NewTimer(urlDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if url != "" {
		if err := onURL(url); err != nil {
			return err
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.holdAfterCancel != nil {
		<-ctx.Done()
		if f.cancelObserved != nil {
			select {
			case f.cancelObserved <- struct{}{}:
			default:
			}
		}
		<-f.holdAfterCancel
		return ctx.Err()
	}
	return runErr
}

func (f *authSessionCLIFake) ActiveRuns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeRuns
}

func (f *authSessionCLIFake) StartUserAuth(_ context.Context, _ string, scopes []string) (DeviceAuthStart, error) {
	f.mu.Lock()
	f.activeRuns++
	defer func() {
		f.activeRuns--
		f.mu.Unlock()
	}()
	index := len(f.argv)
	f.argv = append(f.argv, []string{"auth", "login", "--scope", strings.Join(scopes, " "), "--no-wait", "--json"})
	url := ""
	if index < len(f.urls) {
		url = f.urls[index]
	}
	deviceCode := "restart-safe-device-code"
	if index < len(f.deviceCodes) {
		deviceCode = f.deviceCodes[index]
	}
	expiresIn := f.deviceExpiresIn
	if expiresIn <= 0 {
		expiresIn = 10 * time.Minute
	}
	return DeviceAuthStart{VerificationURL: url, DeviceCode: deviceCode, ExpiresIn: expiresIn}, f.runErr
}

func (f *authSessionCLIFake) CompleteUserAuth(context.Context, string, string, []string) (DeviceAuthOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeCalls++
	if f.completeOutcome == "" && f.completeErr == nil {
		return DeviceAuthProtocolFailure, errors.New("completion is outside auth-session start tests")
	}
	return f.completeOutcome, f.completeErr
}

func (f *authSessionCLIFake) AuthStatus(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return f.status, f.statusErr
}

func (f *authSessionCLIFake) AppIDFromHome(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appID, f.appIDErr
}

func (f *authSessionCLIFake) snapshot() ([][]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	argv := make([][]string, len(f.argv))
	for index := range f.argv {
		argv[index] = append([]string(nil), f.argv[index]...)
	}
	return argv, f.statusCalls
}

type authSessionVaultFake struct {
	mu      sync.Mutex
	changed []bool
	calls   int
	err     error
}

func (f *authSessionVaultFake) WithHome(
	ctx context.Context,
	_ uint,
	_ uint64,
	callback func(string) (bool, error),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	vaultErr := f.err
	f.mu.Unlock()
	if vaultErr != nil {
		return vaultErr
	}
	changed, err := callback("/tmp/auth-session-home")
	f.mu.Lock()
	f.calls++
	f.changed = append(f.changed, changed)
	f.mu.Unlock()
	return err
}

func (f *authSessionVaultFake) WithHomeCandidate(
	ctx context.Context,
	userID uint,
	generation uint64,
	callback func(string) error,
) (*CLIHomeCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := callback("/tmp/auth-session-candidate-home"); err != nil {
		return nil, err
	}
	ciphertext := []byte("sealed-auth-session-candidate")
	f.mu.Lock()
	f.calls++
	f.changed = append(f.changed, true)
	f.mu.Unlock()
	return &CLIHomeCandidate{
		Vault: model.FeishuCLIVault{
			UserID: userID, Generation: generation, Ciphertext: ciphertext, KeyVersion: "v1",
			Checksum: fmt.Sprintf("%x", sha256.Sum256(ciphertext)), Revision: 1,
		},
		ExpectedRevision: 0,
	}, nil
}

func (f *authSessionVaultFake) snapshot() (int, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, append([]bool(nil), f.changed...)
}

type authSessionDispatcherFake struct {
	mu    sync.Mutex
	calls []string
	errs  []error
	ready chan struct{}
}

type authSessionDeadlineDispatcherFake struct {
	deadline time.Time
	has      bool
}

func (f *authSessionDeadlineDispatcherFake) DispatchResume(ctx context.Context, _ uint, _ string) error {
	f.deadline, f.has = ctx.Deadline()
	return nil
}

type authSessionCompleteBeforeClaimStore struct {
	AuthSessionStore
	db   *gorm.DB
	once sync.Once
}

// authSessionRefreshClaimFailureStore fails exactly one replacement-worker
// claim after the first operation worker has started. It makes the durable
// post-refresh recovery boundary observable without relying on timing.
type authSessionRefreshClaimFailureStore struct {
	AuthSessionStore
	mu              sync.Mutex
	claims          int
	failOnClaim     int
	restoreFailures int
}

// authSessionRestoreRaceStore claims the replacement immediately before the
// durable restore transaction. It models another local recovery that wins the
// lease after a stale-card precheck but before the authoritative store fence.
type authSessionRestoreRaceStore struct {
	AuthSessionStore
	onRestore func()
	once      sync.Once
}

func (s *authSessionRestoreRaceStore) RestoreOperationSessionRefresh(
	ctx context.Context,
	userID uint,
	generation uint64,
	oldSessionID, replacementSessionID, operationID, waitingState string,
	oldSummary []byte,
	now time.Time,
) error {
	s.once.Do(s.onRestore)
	return s.AuthSessionStore.RestoreOperationSessionRefresh(
		ctx, userID, generation, oldSessionID, replacementSessionID, operationID, waitingState, oldSummary, now,
	)
}

func (s *authSessionRefreshClaimFailureStore) ClaimSession(
	ctx context.Context,
	userID uint,
	generation uint64,
	id, owner string,
	now, leaseUntil time.Time,
) (bool, error) {
	s.mu.Lock()
	s.claims++
	shouldFail := s.claims == s.failOnClaim
	s.mu.Unlock()
	if shouldFail {
		return false, errors.New("replacement worker claim unavailable")
	}
	return s.AuthSessionStore.ClaimSession(ctx, userID, generation, id, owner, now, leaseUntil)
}

func (s *authSessionRefreshClaimFailureStore) RestoreOperationSessionRefresh(
	ctx context.Context,
	userID uint,
	generation uint64,
	oldSessionID, replacementSessionID, operationID, waitingState string,
	oldSummary []byte,
	now time.Time,
) error {
	s.mu.Lock()
	shouldFail := s.restoreFailures > 0
	if shouldFail {
		s.restoreFailures--
	}
	s.mu.Unlock()
	if shouldFail {
		return errors.New("operation refresh restore unavailable")
	}
	return s.AuthSessionStore.RestoreOperationSessionRefresh(
		ctx, userID, generation, oldSessionID, replacementSessionID, operationID, waitingState, oldSummary, now,
	)
}

func (s *authSessionCompleteBeforeClaimStore) ClaimSession(
	_ context.Context,
	userID uint,
	generation uint64,
	id, _ string,
	_, _ time.Time,
) (bool, error) {
	var updateErr error
	s.once.Do(func() {
		completedAt := time.Now().UTC()
		updateErr = s.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.UserThirdPartyAccount{}).
				Where("user_id = ? AND provider = ? AND generation = ?", userID, ProviderLark, generation).
				Updates(map[string]any{
					"connection_state": model.FeishuConnectionConnected,
					"connected":        true,
					"connected_at":     completedAt,
				}).Error; err != nil {
				return err
			}
			return tx.Model(&model.FeishuAuthSession{}).
				Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
				Updates(map[string]any{
					"state": model.FeishuAuthSessionCompleted, "completed_at": completedAt,
					"lease_owner": "", "lease_until": nil,
				}).Error
		})
	})
	return false, updateErr
}

func (f *authSessionDispatcherFake) DispatchResume(_ context.Context, _ uint, operationID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, operationID)
	var dispatchErr error
	if len(f.errs) > 0 {
		dispatchErr = f.errs[0]
		f.errs = f.errs[1:]
	}
	f.mu.Unlock()
	if f.ready != nil {
		select {
		case f.ready <- struct{}{}:
		default:
		}
	}
	return dispatchErr
}

func (f *authSessionDispatcherFake) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestAuthSessionService_DispatchResumeDetachedOutlivesAuthorizationStartWindow(t *testing.T) {
	dispatcher := &authSessionDeadlineDispatcherFake{}
	service := &AuthSessionService{dispatcher: dispatcher}
	startedAt := time.Now()

	require.NoError(t, service.dispatchResumeDetached(context.Background(), 7, "operation-stage-handoff"))
	require.True(t, dispatcher.has, "durable dispatch must remain bounded")
	budget := dispatcher.deadline.Sub(startedAt)
	require.Greater(t, budget, authSessionDefaultStartTimeout,
		"dispatch must outlive the authorization URL-start window")
	require.LessOrEqual(t, budget, authSessionCLIHardCeiling+time.Second,
		"dispatch must not exceed the controlled CLI ceiling")
}

type authSessionHarness struct {
	t          *testing.T
	ctx        context.Context
	db         *gorm.DB
	dataStore  store.IStore
	cli        *authSessionCLIFake
	vault      *authSessionVaultFake
	dispatcher *authSessionDispatcherFake
	now        time.Time
	idMu       sync.Mutex
	nextID     int
	leaseMu    sync.Mutex
	nextLease  int
}

func newAuthSessionHarness(t *testing.T) *authSessionHarness {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", "?", "_", "=", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&model.UserThirdPartyAccount{},
		&model.FeishuCLIVault{},
		&model.FeishuAuthSession{},
		&model.FeishuOperation{},
		&model.FeishuOperationProofConsumption{},
		&model.FeishuOperationExecutionGate{},
	))
	return &authSessionHarness{
		t: t, ctx: context.Background(), db: db, dataStore: store.NewTestStore(db),
		cli: &authSessionCLIFake{appID: "cli_test_app"}, vault: &authSessionVaultFake{},
		dispatcher: &authSessionDispatcherFake{ready: make(chan struct{}, 8)},
		now:        time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC),
	}
}

func (h *authSessionHarness) createAccount(state string) {
	h.t.Helper()
	require.NoError(h.t, h.db.Create(&model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: "cli_app", Generation: 1,
		ConnectionState: state, Connected: state == model.FeishuConnectionConnected,
	}).Error)
}

func (h *authSessionHarness) newService(owner string) *AuthSessionService {
	return h.newServiceWithSessions(owner, h.dataStore.FeishuWorkspace())
}

func (h *authSessionHarness) newServiceWithSessions(owner string, sessions AuthSessionStore) *AuthSessionService {
	h.t.Helper()
	deviceAuth := h.newDeviceAuthFlow(owner+"-device-auth", h.cli)
	service, err := NewAuthSessionService(AuthSessionServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: sessions,
		Vault: h.vault, CLI: h.cli, DeviceAuth: deviceAuth, Dispatcher: h.dispatcher, Owner: owner,
		Now: func() time.Time { return h.now },
		NewID: func() string {
			h.idMu.Lock()
			defer h.idMu.Unlock()
			h.nextID++
			return "00000000-0000-4000-8000-" + leftPad12(h.nextID)
		},
		NewLeaseToken: func() string {
			h.leaseMu.Lock()
			defer h.leaseMu.Unlock()
			h.nextLease++
			return "lease-token-" + leftPad12(h.nextLease)
		},
		LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second,
	})
	require.NoError(h.t, err)
	return service
}

func (h *authSessionHarness) newDeviceAuthFlow(owner string, cli DeviceAuthCLI) *DeviceAuthFlow {
	h.t.Helper()
	deviceAuth, err := NewDeviceAuthFlow(DeviceAuthFlowDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: h.vault, CLI: cli, Cipher: newDeviceAuthFlowCredentialCipher(h.t), Dispatcher: h.dispatcher,
		Owner: owner, Now: func() time.Time { return h.now },
		NewID: func() string { return "00000000-0000-4000-8000-999999999999" },
		NewLeaseToken: func() string {
			h.leaseMu.Lock()
			defer h.leaseMu.Unlock()
			h.nextLease++
			return "device-lease-" + leftPad12(h.nextLease)
		},
		LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second, CompletionTimeout: 30 * time.Second,
	})
	require.NoError(h.t, err)
	return deviceAuth
}

func leftPad12(value int) string {
	text := []byte("000000000000")
	for index := len(text) - 1; value > 0; index-- {
		text[index] = byte('0' + value%10)
		value /= 10
	}
	return string(text)
}

func waitAuthDispatch(t *testing.T, dispatcher *authSessionDispatcherFake) {
	t.Helper()
	select {
	case <-dispatcher.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for operation resume dispatch")
	}
}

func createWaitingDeviceAuthOperation(
	t *testing.T,
	h *authSessionHarness,
	operationID string,
	sessionID string,
) {
	t.Helper()
	summary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingUserAuth, Phase: model.FeishuAuthPhaseUserAuth, SessionID: sessionID,
		RecoveryKind: RecoveryUserScope, RecoveryScopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Create(&model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 1, AgentRunID: 700, ToolCallID: "tool-" + operationID,
		IdempotencyKey: operationID,
		CommandPath:    "docs +fetch", Domain: SkillDomainDocs, RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("opaque-request"), KeyVersion: "v1", RequestFingerprint: strings.Repeat("a", 64),
		State: model.FeishuOperationWaitingUserAuth, ResultSummaryJSON: summary,
	}).Error)
}

func TestAuthSessionService_ManualConnectRequestsOfflineAccessOnly(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	release := make(chan struct{})
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=MANUAL"}
	h.cli.release = release
	service := h.newService("worker-manual")

	action, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthPhaseUserAuth, action.Phase)
	require.Equal(t, []string{"offline_access"}, action.Scopes)
	argv, _ := h.cli.snapshot()
	require.Equal(t, [][]string{{"auth", "login", "--scope", "offline_access", "--no-wait", "--json"}}, argv)

	stored, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.JSONEq(t, `["offline_access"]`, string(stored.RequestedScopesJSON))
	serialized, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "open.feishu.cn")
	require.NotContains(t, string(serialized), "user_code")
	close(release)
}

func TestAuthSessionService_UserAuthStartLeavesNoWorkerAndPersistsExactOperationCredential(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	release := make(chan struct{})
	releaseAuthSessionCLIFake(t, release)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=RESTART_SAFE"}
	h.cli.release = release
	service := h.newService("split-protocol-regression")
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = service.StopGenerationAndWait(stopCtx, 7, 1)
	})
	operationID := "op-restart-safe"

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, Kind: RecoveryUserScope,
		OperationID: operationID, Scopes: []string{"offline_access"},
	})
	require.NoError(t, err)
	require.NotNil(t, action)
	require.Equal(t, operationID, action.OperationID)
	require.Equal(t, model.FeishuAuthPhaseUserAuth, action.Phase)
	assert.Eventually(t, func() bool { return h.cli.ActiveRuns() == 0 }, time.Second, 10*time.Millisecond,
		"split user authorization start must not leave a blocking worker")
	argv, _ := h.cli.snapshot()
	assert.Equal(t, [][]string{{
		"auth", "login", "--scope", "offline_access", "--no-wait", "--json",
	}}, argv)

	var persisted struct {
		OperationID string
		Ciphertext  []byte
	}
	err = h.db.Raw(`SELECT operation_id, resume_credential_ciphertext AS ciphertext
		FROM feishu_auth_session WHERE id = ?`, action.SessionID).Scan(&persisted).Error
	if assert.NoError(t, err) {
		assert.Equal(t, operationID, persisted.OperationID)
		assert.NotEmpty(t, persisted.Ciphertext)
	}
}

func TestAuthSessionService_UserAuthUsesRestartSafeSplitProtocol(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=SPLIT"}
	service := h.newService("split-protocol-service")

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-split", Kind: RecoveryUserScope,
		Scopes: []string{"offline_access"},
	})
	require.NoError(t, err)
	require.NotNil(t, action)
	require.NotEmpty(t, action.URL)
	service.workerMu.Lock()
	require.Empty(t, service.workers, "user-auth must not register a blocking worker")
	require.Empty(t, service.starts, "short start must leave no pre-worker handoff")
	service.workerMu.Unlock()
	stored, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.EqualValues(t, 2, stored.ProtocolVersion)
	require.NotEmpty(t, stored.ResumeCredentialCiphertext)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
}

func TestAuthSessionService_ManualConnectRejectsDisconnectingGeneration(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	_, nextGeneration, err := h.dataStore.ThirdPartyAccounts().RetireGeneration(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.EqualValues(t, 2, nextGeneration)
	service := h.newService("disconnecting-manual-owner")

	_, err = service.ConnectManual(h.ctx, 7)
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	argv, _ := h.cli.snapshot()
	require.Empty(t, argv, "disconnecting accounts must not start another authorization worker")

	account, getErr := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, getErr)
	require.Equal(t, model.FeishuConnectionDisconnecting, account.ConnectionState)
	require.EqualValues(t, nextGeneration, account.Generation)
}

func TestAuthSessionService_RefreshSupersedesLiveSessionStopsWorkerAndStartsNewURL(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	release := make(chan struct{})
	defer close(release)
	h.cli.urls = []string{
		"https://open.feishu.cn/suite/passport/oauth/device?user_code=OLD",
		"https://open.feishu.cn/suite/passport/oauth/device?user_code=NEW",
	}
	h.cli.releases = []<-chan struct{}{release, release}
	service := h.newService("refresh-owner")

	first, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Contains(t, first.URL, "OLD")

	refreshed, err := service.RefreshAction(h.ctx, 7, 1, first.SessionID)
	require.NoError(t, err)
	require.NotNil(t, refreshed)
	require.NotEqual(t, first.SessionID, refreshed.SessionID)
	require.Contains(t, refreshed.URL, "NEW")

	var oldSession model.FeishuAuthSession
	require.NoError(t, h.db.Where("id = ?", first.SessionID).Take(&oldSession).Error)
	require.Equal(t, model.FeishuAuthSessionSuperseded, oldSession.State)

	service.StopGeneration(7, 1)
}

func TestAuthSessionService_RefreshOperationAtomicallyRebindsBeforeActivatingReplacement(t *testing.T) {
	h := newAuthSessionHarness(t)
	oldRelease := make(chan struct{})
	newRelease := make(chan struct{})
	releaseNew := releaseAuthSessionCLIFake(t, newRelease)
	h.cli.urls = []string{
		"https://open.feishu.cn/page/cli?user_code=OLD_OPERATION",
		"https://open.feishu.cn/page/cli?user_code=NEW_OPERATION",
	}
	h.cli.releases = []<-chan struct{}{oldRelease, newRelease}
	service := h.newService("refresh-operation-owner")
	operation := &model.FeishuOperation{
		ID: "operation-refresh-atomic", UserID: 7, Generation: 1, AgentRunID: 7, ToolCallID: "tool-refresh-atomic",
		IdempotencyKey: "refresh-atomic", CommandPath: "docs document get", Domain: "docs", RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: "v1", RequestFingerprint: "refresh-atomic-fingerprint",
		State: model.FeishuOperationWaitingConnection,
	}
	require.NoError(t, h.db.Create(operation).Error)

	first, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: operation.ID, Kind: RecoveryCreateApp, Scopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.NoError(t, service.Activate(h.ctx, first.SessionID))
	oldSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingConnection, Phase: model.FeishuAuthPhaseCreateApp,
		SessionID: first.SessionID, RecoveryKind: RecoveryCreateApp,
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", oldSummary).Error)

	refreshed, err := service.RefreshOperationAction(
		h.ctx, 7, 1, first.SessionID, operation.ID, model.FeishuOperationWaitingConnection, oldSummary,
	)
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, refreshed.SessionID)
	require.Contains(t, refreshed.URL, "NEW_OPERATION")

	storedOperation, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operation.ID)
	require.NoError(t, err)
	storedSummary, err := decodeOperationSummary(storedOperation.ResultSummaryJSON)
	require.NoError(t, err)
	require.Equal(t, refreshed.SessionID, storedSummary.SessionID)
	require.NotNil(t, storedSummary.ExpiresAt)
	var storedOld model.FeishuAuthSession
	require.NoError(t, h.db.Where("id = ?", first.SessionID).Take(&storedOld).Error)
	require.Equal(t, model.FeishuAuthSessionSuperseded, storedOld.State)
	storedNew, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, refreshed.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, storedNew.State)

	releaseNew()
	waitAuthDispatch(t, h.dispatcher)
	require.Equal(t, []string{operation.ID}, h.dispatcher.snapshot())
	close(oldRelease)
}

func TestAuthSessionService_RefreshAppScopeReconstructsCanonicalURLAfterRestart(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionWaitingAppApproval)
	operation := &model.FeishuOperation{
		ID: "operation-refresh-app-scope", UserID: 7, Generation: 1, AgentRunID: 7,
		ToolCallID: "tool-refresh-app-scope", IdempotencyKey: "refresh-app-scope",
		CommandPath: "docs document get", Domain: "docs", RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: "v1",
		RequestFingerprint: "refresh-app-scope-fingerprint", State: model.FeishuOperationWaitingAppScope,
	}
	require.NoError(t, h.db.Create(operation).Error)
	old := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000088", UserID: 7, Generation: 1,
		OperationID: &operation.ID, Phase: model.FeishuAuthPhaseAppScope,
		RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State:               model.FeishuAuthSessionPending, ExpiresAt: h.now.Add(10 * time.Minute),
	}
	require.NoError(t, h.dataStore.FeishuWorkspace().CreateSession(h.ctx, old))
	oldSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingAppScope, Phase: model.FeishuAuthPhaseAppScope,
		SessionID: old.ID, RecoveryKind: RecoveryAppScope,
		RecoveryScopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", oldSummary).Error)

	// A fresh service has no in-memory copy of the classifier URL. It must use
	// the current generation's non-secret app id to rebuild the narrow official
	// approval route instead of persisting or guessing the original URL.
	action, err := h.newService("refresh-app-scope-after-restart").RefreshOperationAction(
		h.ctx, 7, 1, old.ID, operation.ID, model.FeishuOperationWaitingAppScope, oldSummary,
	)

	require.NoError(t, err)
	require.NotEqual(t, old.ID, action.SessionID)
	require.Equal(t, "https://open.feishu.cn/app/cli_app/auth", action.URL)
	storedOperation, getErr := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operation.ID)
	require.NoError(t, getErr)
	require.NotContains(t, string(storedOperation.ResultSummaryJSON), "open.feishu.cn")
	require.NotContains(t, string(storedOperation.ResultSummaryJSON), "console")
}

func TestAuthSessionService_RefreshOperationRepairsLegacySupersededBinding(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	release := make(chan struct{})
	releaseWorker := releaseAuthSessionCLIFake(t, release)
	h.cli.urls = []string{"https://open.feishu.cn/page/cli?user_code=LEGACY_REPAIRED"}
	h.cli.releases = []<-chan struct{}{release}
	service := h.newService("refresh-operation-legacy")
	operation := &model.FeishuOperation{
		ID: "operation-refresh-legacy", UserID: 7, Generation: 1, AgentRunID: 7, ToolCallID: "tool-refresh-legacy",
		IdempotencyKey: "refresh-legacy", CommandPath: "docs document get", Domain: "docs", RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: "v1", RequestFingerprint: "refresh-legacy-fingerprint",
		State: model.FeishuOperationWaitingConnection,
	}
	require.NoError(t, h.db.Create(operation).Error)
	old := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000099", UserID: 7, Generation: 1, OperationID: &operation.ID,
		Phase: model.FeishuAuthPhaseCreateApp, RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State: model.FeishuAuthSessionSuperseded, ExpiresAt: h.now.Add(10 * time.Minute),
	}
	require.NoError(t, h.dataStore.FeishuWorkspace().CreateSession(h.ctx, old))
	oldSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingConnection, Phase: model.FeishuAuthPhaseCreateApp,
		SessionID: old.ID, RecoveryKind: RecoveryCreateApp,
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", oldSummary).Error)

	action, err := service.RefreshOperationAction(
		h.ctx, 7, 1, old.ID, operation.ID, model.FeishuOperationWaitingConnection, oldSummary,
	)
	require.NoError(t, err)
	require.NotEqual(t, old.ID, action.SessionID)
	require.Contains(t, action.URL, "LEGACY_REPAIRED")
	storedOperation, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operation.ID)
	require.NoError(t, err)
	storedSummary, err := decodeOperationSummary(storedOperation.ResultSummaryJSON)
	require.NoError(t, err)
	require.Equal(t, action.SessionID, storedSummary.SessionID)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	releaseWorker()
}

func TestAuthSessionService_RefreshLegacyUserAuthUsesAtomicDeviceReplacement(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	h.cli.urls = []string{
		"https://open.feishu.cn/suite/passport/oauth/device?user_code=LEGACY_DEVICE_REPLACED",
	}
	service := h.newService("refresh-legacy-user-auth")
	operation := &model.FeishuOperation{
		ID: "operation-refresh-legacy-user-auth", UserID: 7, Generation: 1,
		AgentRunID: 7, ToolCallID: "tool-refresh-legacy-user-auth",
		IdempotencyKey: "refresh-legacy-user-auth", CommandPath: "docs document get",
		Domain: "docs", RiskLevel: string(RiskRead), RequestCiphertext: []byte("encrypted-request"),
		KeyVersion: "v1", RequestFingerprint: "refresh-legacy-user-auth-fingerprint",
		State: model.FeishuOperationWaitingUserAuth,
	}
	require.NoError(t, h.db.Create(operation).Error)
	legacy := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000198", UserID: 7, Generation: 1,
		OperationID: &operation.ID, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State:               model.FeishuAuthSessionPending, ProtocolVersion: 1,
		ExpiresAt: h.now.Add(10 * time.Minute),
	}
	require.NoError(t, h.dataStore.FeishuWorkspace().CreateSession(h.ctx, legacy))
	oldSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingUserAuth, Phase: model.FeishuAuthPhaseUserAuth,
		SessionID: legacy.ID, RecoveryKind: RecoveryUserScope,
		RecoveryScopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", oldSummary).Error)

	action, err := service.RefreshOperationAction(
		h.ctx, 7, 1, legacy.ID, operation.ID, model.FeishuOperationWaitingUserAuth, oldSummary,
	)
	require.NoError(t, err)
	require.NotNil(t, action)
	require.Equal(t, "00000000-0000-4000-8000-999999999999", action.SessionID)
	require.Equal(t, operation.ID, action.OperationID)
	require.Contains(t, action.URL, "LEGACY_DEVICE_REPLACED")
	require.Empty(t, action.Scopes)
	storedOld, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionSuperseded, storedOld.State)
	storedNew, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.EqualValues(t, 2, storedNew.ProtocolVersion)
	require.NotEmpty(t, storedNew.ResumeCredentialCiphertext)
	storedOperation, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operation.ID)
	require.NoError(t, err)
	storedSummary, err := decodeOperationSummary(storedOperation.ResultSummaryJSON)
	require.NoError(t, err)
	require.Equal(t, action.SessionID, storedSummary.SessionID)
	argv, _ := h.cli.snapshot()
	require.Equal(t,
		[]string{"auth", "login", "--scope", "docx:document:readonly", "--no-wait", "--json"},
		argv[0],
	)
	require.Empty(t, h.dispatcher.snapshot(), "Task 9 owns resume dispatch")
}

func TestAuthSessionService_RefreshConnectionOnlyReauthNoopEscalatesToCreateApp(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionWaitingUserAuth)
	h.cli.urls = []string{"https://open.feishu.cn/page/cli?user_code=RECONNECT_CREATE_APP"}
	service := h.newService("refresh-connection-only-reauth-noop")
	operation := &model.FeishuOperation{
		ID: "operation-refresh-connection-noop", UserID: 7, Generation: 1,
		AgentRunID: 7, ToolCallID: "tool-refresh-connection-noop",
		IdempotencyKey: "refresh-connection-noop", CommandPath: connectionOnlyCommandPath,
		Domain: connectionOnlyDomain, RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: "v1",
		RequestFingerprint: "refresh-connection-noop-fingerprint",
		State:              model.FeishuOperationWaitingUserAuth,
	}
	require.NoError(t, h.db.Create(operation).Error)
	old := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000298", UserID: 7, Generation: 1,
		OperationID: &operation.ID, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: []byte(`["offline_access"]`),
		State:               model.FeishuAuthSessionPending, ProtocolVersion: 2,
		ScopeHash: deviceAuthScopeHash([]string{"offline_access"}),
		ExpiresAt: h.now.Add(10 * time.Minute),
	}
	require.NoError(t, h.dataStore.FeishuWorkspace().CreateSession(h.ctx, old))
	oldSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingUserAuth, Phase: model.FeishuAuthPhaseUserAuth,
		SessionID: old.ID, RecoveryKind: RecoveryReauth,
		RecoveryScopes: []string{"offline_access"},
		SupersededSessionIDs: []string{
			"00000000-0000-4000-8000-000000000297",
		},
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", oldSummary).Error)

	action, err := service.RefreshOperationAction(
		h.ctx, 7, 1, old.ID, operation.ID, model.FeishuOperationWaitingUserAuth, oldSummary,
	)

	require.NoError(t, err)
	require.NotNil(t, action)
	require.Equal(t, operation.ID, action.OperationID)
	require.Equal(t, model.FeishuAuthPhaseCreateApp, action.Phase)
	require.Contains(t, action.URL, "RECONNECT_CREATE_APP")
	storedOld, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, old.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionSuperseded, storedOld.State)
	storedNew, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthPhaseCreateApp, storedNew.Phase)
	require.Equal(t, model.FeishuAuthSessionPending, storedNew.State)
	storedOperation, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operation.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, storedOperation.State)
	storedSummary, err := decodeOperationSummary(storedOperation.ResultSummaryJSON)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, storedSummary.Status)
	require.Equal(t, model.FeishuAuthPhaseCreateApp, storedSummary.Phase)
	require.Equal(t, RecoveryCreateApp, storedSummary.RecoveryKind)
	require.Equal(t, action.SessionID, storedSummary.SessionID)
	argv, _ := h.cli.snapshot()
	require.Equal(t, []string{"config", "init", "--new"}, argv[0])
}

func TestAuthSessionService_RefreshMigratedSupersededUserAuthUsesDeviceReplacement(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	h.cli.urls = []string{
		"https://open.feishu.cn/suite/passport/oauth/device?user_code=MIGRATED_SUPERSEDED",
	}
	service := h.newService("refresh-migrated-superseded-user-auth")
	operation := &model.FeishuOperation{
		ID: "operation-refresh-migrated-superseded", UserID: 7, Generation: 1,
		AgentRunID: 7, ToolCallID: "tool-refresh-migrated-superseded",
		IdempotencyKey: "refresh-migrated-superseded", CommandPath: "docs document get",
		Domain: "docs", RiskLevel: string(RiskRead), RequestCiphertext: []byte("encrypted-request"),
		KeyVersion: "v1", RequestFingerprint: "refresh-migrated-superseded-fingerprint",
		State: model.FeishuOperationWaitingUserAuth,
	}
	require.NoError(t, h.db.Create(operation).Error)
	legacy := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000199", UserID: 7, Generation: 1,
		OperationID: &operation.ID, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State:               model.FeishuAuthSessionSuperseded, ProtocolVersion: 1,
		ExpiresAt: h.now.Add(-time.Minute),
	}
	require.NoError(t, h.dataStore.FeishuWorkspace().CreateSession(h.ctx, legacy))
	oldSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingUserAuth, Phase: model.FeishuAuthPhaseUserAuth,
		SessionID: legacy.ID, RecoveryKind: RecoveryUserScope,
		RecoveryScopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", oldSummary).Error)

	action, err := service.RefreshOperationAction(
		h.ctx, 7, 1, legacy.ID, operation.ID, model.FeishuOperationWaitingUserAuth, oldSummary,
	)
	require.NoError(t, err)
	require.NotNil(t, action)
	require.Equal(t, "00000000-0000-4000-8000-999999999999", action.SessionID)
	require.Contains(t, action.URL, "MIGRATED_SUPERSEDED")
	storedOld, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionSuperseded, storedOld.State)
	storedNew, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.EqualValues(t, 2, storedNew.ProtocolVersion)
	require.NotEmpty(t, storedNew.ResumeCredentialCiphertext)
	argv, _ := h.cli.snapshot()
	require.Len(t, argv, 1, "legacy user auth must never start the blocking worker")
	require.Equal(t,
		[]string{"auth", "login", "--scope", "docx:document:readonly", "--no-wait", "--json"},
		argv[0],
	)
}

func TestAuthSessionService_RefreshOperationRetriesCurrentFailedBinding(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	release := make(chan struct{})
	releaseWorker := releaseAuthSessionCLIFake(t, release)
	h.cli.urls = []string{"https://open.feishu.cn/page/cli?user_code=FAILED_RETRY"}
	h.cli.releases = []<-chan struct{}{release}
	service := h.newService("refresh-operation-failed")
	operation := &model.FeishuOperation{
		ID: "operation-refresh-failed", UserID: 7, Generation: 1, AgentRunID: 7, ToolCallID: "tool-refresh-failed",
		IdempotencyKey: "refresh-failed", CommandPath: "docs document get", Domain: "docs", RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: "v1", RequestFingerprint: "refresh-failed-fingerprint",
		State: model.FeishuOperationWaitingConnection,
	}
	require.NoError(t, h.db.Create(operation).Error)
	failed := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000177", UserID: 7, Generation: 1, OperationID: &operation.ID,
		Phase: model.FeishuAuthPhaseCreateApp, RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State: model.FeishuAuthSessionFailed, ExpiresAt: h.now.Add(10 * time.Minute),
	}
	require.NoError(t, h.dataStore.FeishuWorkspace().CreateSession(h.ctx, failed))
	failedSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingConnection, Phase: model.FeishuAuthPhaseCreateApp,
		SessionID: failed.ID, RecoveryKind: RecoveryCreateApp,
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", failedSummary).Error)

	action, err := service.RefreshOperationAction(
		h.ctx, 7, 1, failed.ID, operation.ID, model.FeishuOperationWaitingConnection, failedSummary,
	)
	require.NoError(t, err)
	require.NotEqual(t, failed.ID, action.SessionID)
	require.Contains(t, action.URL, "FAILED_RETRY")
	storedFailed, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, failed.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionSuperseded, storedFailed.State)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	releaseWorker()
}

func TestAuthSessionService_RefreshOperationRestoresOriginalBindingWhenReplacementCannotStart(t *testing.T) {
	h := newAuthSessionHarness(t)
	oldRelease := make(chan struct{})
	recoveredRelease := make(chan struct{})
	releaseRecovered := releaseAuthSessionCLIFake(t, recoveredRelease)
	h.cli.urls = []string{
		"https://open.feishu.cn/page/cli?user_code=OLD_OPERATION",
		"https://open.feishu.cn/page/cli?user_code=RECOVERED_OPERATION",
	}
	h.cli.releases = []<-chan struct{}{oldRelease, recoveredRelease}
	sessions := &authSessionRefreshClaimFailureStore{
		AuthSessionStore: h.dataStore.FeishuWorkspace(),
		failOnClaim:      2,
	}
	service := h.newServiceWithSessions("refresh-operation-compensation", sessions)
	operation := &model.FeishuOperation{
		ID: "operation-refresh-compensation", UserID: 7, Generation: 1, AgentRunID: 7, ToolCallID: "tool-refresh-compensation",
		IdempotencyKey: "refresh-compensation", CommandPath: "docs document get", Domain: "docs", RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: "v1", RequestFingerprint: "refresh-compensation-fingerprint",
		State: model.FeishuOperationWaitingConnection,
	}
	require.NoError(t, h.db.Create(operation).Error)

	first, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: operation.ID, Kind: RecoveryCreateApp, Scopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.NoError(t, service.Activate(h.ctx, first.SessionID))
	oldSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingConnection, Phase: model.FeishuAuthPhaseCreateApp,
		SessionID: first.SessionID, RecoveryKind: RecoveryCreateApp,
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", oldSummary).Error)

	_, err = service.RefreshOperationAction(
		h.ctx, 7, 1, first.SessionID, operation.ID, model.FeishuOperationWaitingConnection, oldSummary,
	)
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)

	storedOperation, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operation.ID)
	require.NoError(t, err)
	storedSummary, err := decodeOperationSummary(storedOperation.ResultSummaryJSON)
	require.NoError(t, err)
	require.Equal(t, first.SessionID, storedSummary.SessionID)
	var restoredOld model.FeishuAuthSession
	require.NoError(t, h.db.Where("id = ?", first.SessionID).Take(&restoredOld).Error)
	require.Equal(t, model.FeishuAuthSessionPending, restoredOld.State)
	require.Empty(t, restoredOld.LeaseOwner)
	require.Nil(t, restoredOld.LeaseUntil)

	// The original card retains its session ID after a failed refresh. It must
	// therefore be able to retry that exact refresh and receive a new action.
	retried, err := service.RefreshOperationAction(
		h.ctx, 7, 1, first.SessionID, operation.ID, model.FeishuOperationWaitingConnection, oldSummary,
	)
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, retried.SessionID)
	require.Contains(t, retried.URL, "RECOVERED_OPERATION")
	require.NoError(t, service.Activate(h.ctx, retried.SessionID))
	releaseRecovered()
	close(oldRelease)
}

func TestAuthSessionService_RecoverOperationRefreshRetriesFailedCompensationFromOriginalCard(t *testing.T) {
	h := newAuthSessionHarness(t)
	oldRelease := make(chan struct{})
	recoveredRelease := make(chan struct{})
	releaseRecovered := releaseAuthSessionCLIFake(t, recoveredRelease)
	h.cli.urls = []string{
		"https://open.feishu.cn/page/cli?user_code=OLD_COMPENSATION",
		"https://open.feishu.cn/page/cli?user_code=RECOVERED_COMPENSATION",
	}
	h.cli.releases = []<-chan struct{}{oldRelease, recoveredRelease}
	sessions := &authSessionRefreshClaimFailureStore{
		AuthSessionStore: h.dataStore.FeishuWorkspace(),
		failOnClaim:      2,
		restoreFailures:  1,
	}
	service := h.newServiceWithSessions("refresh-operation-reconcile", sessions)
	operation := &model.FeishuOperation{
		ID: "operation-refresh-reconcile", UserID: 7, Generation: 1, AgentRunID: 7, ToolCallID: "tool-refresh-reconcile",
		IdempotencyKey: "refresh-reconcile", CommandPath: "docs document get", Domain: "docs", RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: "v1", RequestFingerprint: "refresh-reconcile-fingerprint",
		State: model.FeishuOperationWaitingConnection,
	}
	require.NoError(t, h.db.Create(operation).Error)

	first, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: operation.ID, Kind: RecoveryCreateApp, Scopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.NoError(t, service.Activate(h.ctx, first.SessionID))
	oldSummary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingConnection, Phase: model.FeishuAuthPhaseCreateApp,
		SessionID: first.SessionID, RecoveryKind: RecoveryCreateApp,
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).
		Update("result_summary_json", oldSummary).Error)

	_, err = service.RefreshOperationAction(
		h.ctx, 7, 1, first.SessionID, operation.ID, model.FeishuOperationWaitingConnection, oldSummary,
	)
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	storedOperation, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operation.ID)
	require.NoError(t, err)

	// The first compensating database write is unavailable, so the original
	// card's ID is superseded while the operation names its replacement. Once
	// storage recovers, retrying that exact old ID must repair and refresh it.
	recovered, err := service.RecoverOperationRefreshAction(
		h.ctx, 7, 1, first.SessionID, operation.ID, model.FeishuOperationWaitingConnection,
		storedOperation.ResultSummaryJSON,
	)
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, recovered.SessionID)
	require.Contains(t, recovered.URL, "RECOVERED_COMPENSATION")
	require.NoError(t, service.Activate(h.ctx, recovered.SessionID))
	releaseRecovered()
	close(oldRelease)
}

func TestAuthSessionService_RecoverOperationRefreshDoesNotStopWorkerClaimedAfterStalePrecheck(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	operation := &model.FeishuOperation{
		ID: "operation-refresh-live-race", UserID: 7, Generation: 1, AgentRunID: 7, ToolCallID: "tool-refresh-live-race",
		IdempotencyKey: "refresh-live-race", CommandPath: "docs document get", Domain: "docs", RiskLevel: string(RiskRead),
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: "v1", RequestFingerprint: "refresh-live-race-fingerprint",
		State: model.FeishuOperationWaitingConnection,
	}
	require.NoError(t, h.db.Create(operation).Error)
	oldSession := &model.FeishuAuthSession{
		ID: "session-old-live-race", UserID: 7, Generation: 1, OperationID: &operation.ID,
		Phase: model.FeishuAuthPhaseCreateApp, RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State: model.FeishuAuthSessionPending, ExpiresAt: h.now.Add(10 * time.Minute),
	}
	oldSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-old-live-race","recovery_kind":"create_app"}`)
	replacement := &model.FeishuAuthSession{
		ID: "session-new-live-race", UserID: 7, Generation: 1, OperationID: &operation.ID,
		Phase: model.FeishuAuthPhaseCreateApp, RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State: model.FeishuAuthSessionPending, ExpiresAt: h.now.Add(10 * time.Minute),
	}
	replacementSummary := []byte(`{"status":"waiting_connection","phase":"create_app","session_id":"session-new-live-race","recovery_kind":"create_app"}`)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
		"state": model.FeishuOperationWaitingConnection, "result_summary_json": oldSummary,
	}).Error)
	base := h.dataStore.FeishuWorkspace()
	require.NoError(t, base.CreateSession(h.ctx, oldSession))
	_, err := base.RefreshOperationSession(
		h.ctx, 7, 1, oldSession.ID, operation.ID, model.FeishuOperationWaitingConnection,
		model.FeishuConnectionCreatingApp, replacement, replacementSummary, h.now,
	)
	require.NoError(t, err)

	var claimed bool
	var claimErr error
	raceStore := &authSessionRestoreRaceStore{AuthSessionStore: base}
	raceStore.onRestore = func() {
		claimed, claimErr = base.ClaimSession(h.ctx, 7, 1, replacement.ID, "racing-worker", h.now, h.now.Add(time.Minute))
	}
	service := h.newServiceWithSessions("refresh-operation-live-race", raceStore)
	cancelled := make(chan struct{})
	var cancelOnce sync.Once
	key := authSessionRegistryKey(replacement)
	service.workerMu.Lock()
	service.workers[key] = &authSessionWorker{
		cancel: func() { cancelOnce.Do(func() { close(cancelled) }) },
		exited: make(chan struct{}),
	}
	service.workerMu.Unlock()
	t.Cleanup(func() {
		service.workerMu.Lock()
		delete(service.workers, key)
		service.workerMu.Unlock()
	})

	_, err = service.RecoverOperationRefreshAction(
		h.ctx, 7, 1, oldSession.ID, operation.ID, model.FeishuOperationWaitingConnection, replacementSummary,
	)
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	require.NoError(t, claimErr)
	require.True(t, claimed)
	select {
	case <-cancelled:
		t.Fatal("a stale card must not cancel a worker that acquired the replacement lease")
	default:
	}

	stored, err := base.GetOperationForUser(h.ctx, 7, 1, operation.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(replacementSummary), string(stored.ResultSummaryJSON))
	storedReplacement, err := base.GetSessionForUser(h.ctx, 7, 1, replacement.ID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, storedReplacement.State)
	require.Equal(t, "racing-worker", storedReplacement.LeaseOwner)
}

func TestAuthSessionService_StopGenerationAndWaitJoinsRetiredWorkerBeforeReturning(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=STOP"}
	service := h.newService("stop-join-owner")

	action, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, action)
	require.Zero(t, h.cli.ActiveRuns(), "split user auth start must not leave a local worker to join")
	require.NoError(t, service.StopGenerationAndWait(context.Background(), 7, 1))
	service.workerMu.Lock()
	require.Empty(t, service.workers)
	require.Empty(t, service.starts)
	service.workerMu.Unlock()
}

func TestAuthSessionService_StopGenerationReclaimsRetiredTombstoneAfterAllStartsJoin(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=RECLAIM"}
	h.cli.release = make(chan struct{})
	service := h.newService("retired-tombstone-reclaim")

	_, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	retired, _, err := h.dataStore.ThirdPartyAccounts().RetireGeneration(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.NoError(t, service.StopGenerationAndWait(context.Background(), 7, retired))

	service.workerMu.Lock()
	_, retiredStillTracked := service.retiredGeneration[7]
	require.Empty(t, service.workers)
	require.Empty(t, service.starts)
	service.workerMu.Unlock()
	require.False(t, retiredStillTracked, "a completed local join may reclaim its per-user tombstone")

	// Releasing the in-memory tombstone must not reopen a retired generation:
	// the durable account fence rejects a stale recovery before it can register
	// another local worker.
	_, err = service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: retired, OperationID: "retired-after-reclaim",
		Kind: RecoveryUserScope, Scopes: []string{"docx:document:readonly"},
	})
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	argv, _ := h.cli.snapshot()
	require.Len(t, argv, 1)
}

func TestAuthSessionService_RetireBeforeLateWorkerRegistrationCannotStartRetiredGeneration(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=LATE"}
	service := h.newService("late-register-owner")
	action, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, action)
	require.Zero(t, h.cli.ActiveRuns())
	retiredGeneration, _, err := h.dataStore.ThirdPartyAccounts().RetireGeneration(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.EqualValues(t, 1, retiredGeneration)
	require.NoError(t, service.StopGenerationAndWait(context.Background(), 7, retiredGeneration))
	_, err = service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: retiredGeneration, OperationID: "retired-late-start",
		Kind: RecoveryUserScope, Scopes: []string{"docx:document:readonly"},
	})
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	argv, _ := h.cli.snapshot()
	require.Len(t, argv, 1, "a retired generation must never reach lark-cli again")
}

func TestAuthSessionService_ManualConnectCreatesAppBeforeOfflineAuthorization(t *testing.T) {
	h := newAuthSessionHarness(t)
	createRelease := make(chan struct{})
	userRelease := make(chan struct{})
	h.cli.urls = []string{
		"https://open.feishu.cn/page/cli?user_code=MANUAL_CREATE",
		"https://open.feishu.cn/suite/passport/oauth/device?user_code=MANUAL_USER",
	}
	h.cli.releases = []<-chan struct{}{createRelease, userRelease}
	service := h.newService("worker-manual-create")

	action, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthPhaseCreateApp, action.Phase)
	require.Equal(t, []string{"offline_access"}, action.Scopes)
	close(createRelease)
	require.Eventually(t, func() bool {
		argv, _ := h.cli.snapshot()
		return len(argv) == 2
	}, 2*time.Second, 10*time.Millisecond, "create completion must automatically start user authorization")

	userAction, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthPhaseUserAuth, userAction.Phase)
	require.Equal(t, "https://open.feishu.cn/suite/passport/oauth/device?user_code=MANUAL_USER", userAction.URL)
	argv, _ := h.cli.snapshot()
	require.Equal(t, [][]string{
		{"config", "init", "--new"},
		{"auth", "login", "--scope", "offline_access", "--no-wait", "--json"},
	}, argv, "reading the pending action must not launch a duplicate login")

	close(userRelease)
	require.Eventually(t, func() bool {
		account, getErr := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
		return getErr == nil && !account.Connected && account.ConnectionState == model.FeishuConnectionWaitingUserAuth
	}, 2*time.Second, 10*time.Millisecond)
}

func TestAuthSessionService_OperationCreateAppDoesNotStartManualOfflineAuthorization(t *testing.T) {
	h := newAuthSessionHarness(t)
	createRelease := make(chan struct{})
	releaseCreate := releaseAuthSessionCLIFake(t, createRelease)
	h.cli.urls = []string{"https://open.feishu.cn/page/cli?user_code=OPERATION_CREATE"}
	h.cli.release = createRelease
	service := h.newService("worker-operation-create")

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-create-recovery",
		Kind: RecoveryCreateApp, Scopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthPhaseCreateApp, action.Phase)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	releaseCreate()

	require.Eventually(t, func() bool {
		calls := h.dispatcher.snapshot()
		return len(calls) == 1 && calls[0] == "operation-create-recovery"
	}, time.Second, 10*time.Millisecond, "operation-bound app creation must dispatch its original operation")

	require.Never(t, func() bool {
		var count int64
		err := h.db.Model(&model.FeishuAuthSession{}).
			Where("operation_id IS NULL AND phase = ?", model.FeishuAuthPhaseUserAuth).
			Count(&count).Error
		return err == nil && count > 0
	}, 250*time.Millisecond, 10*time.Millisecond,
		"operation-bound app creation must not start the settings-only offline_access chain")
}

func TestAuthSessionService_CreateAppPersistsOnlyProvenStatusMetadata(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.cli.urls = []string{"https://open.feishu.cn/page/cli?user_code=METADATA"}
	release := make(chan struct{})
	h.cli.release = release
	service := h.newService("metadata-create")
	service.verifiedCLIVersion = LarkCLIVersion

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "metadata-create-operation",
		Kind: RecoveryCreateApp, Scopes: []string{"docx:document:create"},
	})
	require.NoError(t, err)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	close(release)
	require.Eventually(t, func() bool {
		account, getErr := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
		return getErr == nil && account.ConnectionState == model.FeishuConnectionAppReady
	}, time.Second, 10*time.Millisecond)

	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.Equal(t, "cli_test_app", account.AppID)
	require.Equal(t, LarkCLIVersion, account.LarkCLIVersion)
	require.Empty(t, account.AppSecretEnc)
	require.Empty(t, account.AccessTokenEnc)
	require.Empty(t, account.RefreshTokenEnc)
	require.Empty(t, account.GrantedScopesJSON, "requested scopes must not be presented as granted capability metadata")
}

func TestAuthSessionService_ManualChainAllowsURLAfterFinalizeWindow(t *testing.T) {
	h := newAuthSessionHarness(t)
	createRelease := make(chan struct{})
	delayedURL := "https://open.feishu.cn/suite/passport/oauth/device?user_code=DELAYED_MANUAL_USER"
	h.cli.urls = []string{
		"https://open.feishu.cn/page/cli?user_code=DELAYED_MANUAL_CREATE",
		delayedURL,
	}
	h.cli.releases = []<-chan struct{}{createRelease}
	h.cli.urlDelays = []time.Duration{0, authSessionFinalizeTimeout + 100*time.Millisecond}
	h.cli.completeOutcome = DeviceAuthCompleted
	h.cli.status = true
	service := h.newService("worker-manual-delayed-chain")
	service.startTimeout = authSessionFinalizeTimeout + 2*time.Second

	createAction, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthPhaseCreateApp, createAction.Phase)
	close(createRelease)

	var userSession model.FeishuAuthSession
	require.Eventually(t, func() bool {
		return h.db.Where("user_id = ? AND phase = ? AND state = ?", 7,
			model.FeishuAuthPhaseUserAuth, model.FeishuAuthSessionPending).
			Order("created_at DESC").Take(&userSession).Error == nil
	}, 2*time.Second, 10*time.Millisecond)
	userAction, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, userSession.ID, userAction.SessionID)
	require.Equal(t, delayedURL, userAction.URL)
	argv, _ := h.cli.snapshot()
	require.Len(t, argv, 2)
	completion, err := service.CompleteUserAuthorization(h.ctx, 7, 1, userSession.ID)
	require.NoError(t, err)
	require.True(t, completion.Completed)
	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.True(t, account.Connected)
}

func TestAuthSessionService_OperationUsesExactCanonicalScopes(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	release := make(chan struct{})
	h.cli.urls = []string{"https://open.larksuite.com/suite/passport/oauth/device?user_code=EXACT"}
	h.cli.release = release
	service := h.newService("worker-exact")

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-exact", Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:write_only", "docx:document:readonly", "docx:document:readonly"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"docx:document:readonly", "docx:document:write_only"}, action.Scopes)
	argv, _ := h.cli.snapshot()
	require.Equal(t, []string{
		"auth", "login", "--scope", "docx:document:readonly docx:document:write_only", "--no-wait", "--json",
	}, argv[0])
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	close(release)
}

func TestAuthSessionService_CreateAppAndOfficialAppApprovalPhases(t *testing.T) {
	t.Run("create app worker", func(t *testing.T) {
		h := newAuthSessionHarness(t)
		h.createAccount(model.FeishuConnectionNone)
		release := make(chan struct{})
		h.cli.urls = []string{"https://open.feishu.cn/page/cli?user_code=CREATE"}
		h.cli.release = release
		service := h.newService("worker-create")
		action, err := service.StartRecovery(h.ctx, RecoveryRequest{
			UserID: 7, Generation: 1, OperationID: "operation-create", Kind: RecoveryCreateApp,
			Scopes: []string{"docx:document:create"},
		})
		require.NoError(t, err)
		require.Equal(t, model.FeishuAuthPhaseCreateApp, action.Phase)
		argv, _ := h.cli.snapshot()
		require.Equal(t, []string{"config", "init", "--new"}, argv[0])
		require.NoError(t, service.Activate(h.ctx, action.SessionID))
		close(release)
	})

	t.Run("official console url", func(t *testing.T) {
		h := newAuthSessionHarness(t)
		h.createAccount(model.FeishuConnectionConnected)
		service := h.newService("worker-app-scope")
		action, err := service.StartRecovery(h.ctx, RecoveryRequest{
			UserID: 7, Generation: 1, OperationID: "operation-app-scope", Kind: RecoveryAppScope,
			Scopes: []string{"docx:document:readonly"}, ConsoleURL: "https://open.feishu.cn/app/cli_app/auth?q=1",
		})
		require.NoError(t, err)
		require.Equal(t, model.FeishuAuthPhaseAppScope, action.Phase)
		require.Equal(t, "https://open.feishu.cn/app/cli_app/auth?q=1", action.URL)
		account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
		require.NoError(t, err)
		require.Equal(t, model.FeishuConnectionWaitingAppApproval, account.ConnectionState)
		argv, _ := h.cli.snapshot()
		require.Empty(t, argv)
		require.NoError(t, service.CompleteAppApproval(h.ctx, 7, 1, action.SessionID))
		waitAuthDispatch(t, h.dispatcher)
		stored, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuAuthSessionCompleted, stored.State)
	})

	for _, malicious := range []string{
		"http://open.feishu.cn/app/cli_app/auth",
		"https://open.feishu.cn.evil.example/app/cli_app/auth",
		"https://evil.example/?next=https://open.feishu.cn/app/cli_app/auth",
		"https://user@open.feishu.cn/app/cli_app/auth",
		"https://open.feishu.cn/suite/passport/oauth/device?user_code=NOT_CONSOLE",
	} {
		t.Run("reject "+malicious, func(t *testing.T) {
			h := newAuthSessionHarness(t)
			h.createAccount(model.FeishuConnectionConnected)
			_, err := h.newService("worker-evil").StartRecovery(h.ctx, RecoveryRequest{
				UserID: 7, Generation: 1, OperationID: "operation-app-scope", Kind: RecoveryAppScope,
				Scopes: []string{"docx:document:readonly"}, ConsoleURL: malicious,
			})
			require.Error(t, err)
			var count int64
			require.NoError(t, h.db.Model(&model.FeishuAuthSession{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestAuthSessionService_ConcurrentAppApprovalRedispatchesIdempotently(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	serviceA := h.newService("approval-instance-a")
	action, err := serviceA.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-approval-race", Kind: RecoveryAppScope,
		Scopes: []string{"docx:document:readonly"}, ConsoleURL: "https://open.feishu.cn/app/cli_app/auth",
	})
	require.NoError(t, err)
	serviceB := h.newService("approval-instance-b")

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, service := range []*AuthSessionService{serviceA, serviceB} {
		wg.Add(1)
		go func(candidate *AuthSessionService) {
			defer wg.Done()
			errs <- candidate.CompleteAppApproval(h.ctx, 7, 1, action.SessionID)
		}(service)
	}
	wg.Wait()
	close(errs)
	successes := 0
	for approvalErr := range errs {
		if approvalErr == nil {
			successes++
			continue
		}
		require.ErrorIs(t, approvalErr, ErrAuthSessionUnavailable)
	}
	// Both requests may acknowledge success when the second read observes the
	// already-completed session. The completed-session contract intentionally
	// redispatches so a lost first response can recover; the real dispatcher
	// applies the durable operation/Agent claims that make those attempts
	// idempotent. This fake records attempts, so either interleaving is valid.
	require.GreaterOrEqual(t, successes, 1)
	dispatched := h.dispatcher.snapshot()
	require.GreaterOrEqual(t, len(dispatched), 1)
	require.LessOrEqual(t, len(dispatched), 2)
	for _, operationID := range dispatched {
		require.Equal(t, "operation-approval-race", operationID)
	}
	stored, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, stored.State)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
}

func TestAuthSessionService_CompletedAppApprovalRetriesFailedDispatchIdempotently(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	h.dispatcher.errs = []error{errors.New("temporary dispatch failure"), nil}
	service := h.newService("approval-dispatch-retry")
	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-approval-dispatch-retry", Kind: RecoveryAppScope,
		Scopes: []string{"docx:document:readonly"}, ConsoleURL: "https://open.feishu.cn/app/cli_app/auth",
	})
	require.NoError(t, err)

	err = service.CompleteAppApproval(h.ctx, 7, 1, action.SessionID)
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	completed, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, completed.State)
	require.NotNil(t, completed.CompletedAt)
	completedAt := *completed.CompletedAt

	require.NoError(t, service.CompleteAppApproval(h.ctx, 7, 1, action.SessionID))
	retried, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, retried.State)
	require.Equal(t, completedAt, *retried.CompletedAt, "idempotent dispatch retry must not rewrite terminal state")
	require.Equal(t, []string{
		"operation-approval-dispatch-retry", "operation-approval-dispatch-retry",
	}, h.dispatcher.snapshot())
}

func TestAuthSessionService_WorkerSealsBeforeCompletingAndDispatching(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=WORKER"}
	h.cli.completeOutcome = DeviceAuthCompleted
	h.cli.status = true
	h.cli.appID = "cli_app"
	service := h.newService("worker-success")
	request := RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-success", Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:create"},
	}
	action, err := service.StartRecovery(h.ctx, request)
	require.NoError(t, err)
	createWaitingDeviceAuthOperation(t, h, request.OperationID, action.SessionID)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	completion, err := service.CompleteUserAuthorization(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.True(t, completion.Completed)

	calls, changed := h.vault.snapshot()
	require.Equal(t, 2, calls)
	require.Equal(t, []bool{false, true}, changed, "successful CLI HOME must be sealed before dispatch")
	stored, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, stored.State)
	require.Equal(t, []string{"operation-success"}, h.dispatcher.snapshot())

	restartedAction, err := h.newService("worker-restarted").StartRecovery(h.ctx, request)
	require.NoError(t, err)
	require.Nil(t, restartedAction, "a restarted service must observe durable completion")
	require.Equal(t, []string{"operation-success"}, h.dispatcher.snapshot(),
		"OperationService.Resume owns continuation after StartRecovery observes completed auth")
	argv, _ := h.cli.snapshot()
	require.Len(t, argv, 1, "durable completion must not launch the same login again")
}

func TestAuthSessionService_OperationWorkerWaitsForPersistedWaitingActivation(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=FAST"}
	h.cli.completeOutcome = DeviceAuthCompleted
	h.cli.status = true
	h.cli.appID = "cli_app"
	service := h.newService("worker-fast")

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-fast", Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:readonly"},
	})
	require.NoError(t, err)
	require.NotNil(t, action)
	time.Sleep(30 * time.Millisecond)
	stored, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionPending, stored.State)
	require.Empty(t, h.dispatcher.snapshot(), "fast CLI success must not resume before operation waiting is durable")

	createWaitingDeviceAuthOperation(t, h, "operation-fast", action.SessionID)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	completion, err := service.CompleteUserAuthorization(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.True(t, completion.Completed)
	stored, err = h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, stored.State)
}

func TestAuthSessionService_OperationWorkerQuickSuccessWithoutURLFailsClosed(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	service := h.newService("worker-no-url")

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-no-url", Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:readonly"},
	})
	require.Nil(t, action)
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	var stored model.FeishuAuthSession
	require.NoError(t, h.db.Where("user_id = ? AND operation_id = ?", 7, "operation-no-url").Take(&stored).Error)
	require.NotEqual(t, model.FeishuAuthSessionCompleted, stored.State)
	require.Empty(t, h.dispatcher.snapshot())
}

func TestAuthSessionService_CompletedUserIntentDoesNotSynchronouslyRedispatch(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=RETRY_DISPATCH"}
	h.cli.completeOutcome = DeviceAuthCompleted
	h.cli.status = true
	h.cli.appID = "cli_app"
	h.dispatcher.errs = []error{errors.New("temporary dispatch failure"), nil}
	service := h.newService("worker-dispatch-retry")
	request := RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-dispatch-retry", Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:readonly"},
	}
	action, err := service.StartRecovery(h.ctx, request)
	require.NoError(t, err)
	createWaitingDeviceAuthOperation(t, h, request.OperationID, action.SessionID)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	completion, err := service.CompleteUserAuthorization(h.ctx, 7, 1, action.SessionID)
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	require.Nil(t, completion)
	require.Eventually(t, func() bool {
		stored, getErr := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
		return getErr == nil && stored.State == model.FeishuAuthSessionCompleted
	}, time.Second, 10*time.Millisecond)

	retried, err := h.newService("worker-dispatch-compensation").CompleteUserAuthorization(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.True(t, retried.Completed)
	require.Equal(t, []string{"operation-dispatch-retry", "operation-dispatch-retry"}, h.dispatcher.snapshot(),
		"completed authorization must compensate a lost dispatcher response without restarting auth")
	argv, _ := h.cli.snapshot()
	require.Len(t, argv, 1, "dispatch compensation must not restart authorization")
}

func TestAuthSessionService_ExpiredClaimRaceObservesCompletionAndCompensatesDispatch(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	operationID := "operation-expired-completion-race"
	expired := h.now.Add(-time.Minute)
	session := &model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-666666666666", UserID: 7, Generation: 1,
		OperationID: &operationID, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State:               model.FeishuAuthSessionPending, LeaseOwner: "dead-worker", LeaseUntil: &expired,
		ExpiresAt: expired,
	}
	require.NoError(t, h.db.Create(session).Error)
	racingStore := &authSessionCompleteBeforeClaimStore{
		AuthSessionStore: h.dataStore.FeishuWorkspace(), db: h.db,
	}
	service := h.newServiceWithSessions("completion-race-instance", racingStore)

	action, err := service.StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: operationID, Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:readonly"},
	})
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	require.Nil(t, action)
	require.Empty(t, h.dispatcher.snapshot())
	argv, statusCalls := h.cli.snapshot()
	require.Len(t, argv, 1, "an expired legacy session is replaced by one protocol-v2 start attempt")
	require.Zero(t, statusCalls, "the losing claimant must trust the fresh completed row and not inspect vault")
}

func TestAuthSessionService_ExpiredLeaseRecoveryChecksVaultThenCompletesOrSupersedes(t *testing.T) {
	for _, status := range []bool{true, false} {
		status := status
		t.Run(map[bool]string{true: "completed", false: "superseded"}[status], func(t *testing.T) {
			h := newAuthSessionHarness(t)
			h.createAccount(model.FeishuConnectionConnected)
			h.cli.status = status
			operationID := "operation-expired"
			expired := h.now.Add(-time.Minute)
			require.NoError(t, h.db.Create(&model.FeishuAuthSession{
				ID: "00000000-0000-4000-8000-999999999999", UserID: 7, Generation: 1,
				OperationID: &operationID, Phase: model.FeishuAuthPhaseUserAuth,
				RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
				State:               model.FeishuAuthSessionPending, LeaseOwner: "dead-worker", LeaseUntil: &expired,
				ExpiresAt: expired,
			}).Error)
			service := h.newService("recovery-worker")
			action, err := service.StartRecovery(h.ctx, RecoveryRequest{
				UserID: 7, Generation: 1, OperationID: operationID, Kind: RecoveryUserScope,
				Scopes: []string{"docx:document:readonly"},
			})
			require.ErrorIs(t, err, ErrAuthSessionUnavailable)
			require.Nil(t, action)
			_, statusCalls := h.cli.snapshot()
			require.Zero(t, statusCalls, "legacy user-auth sessions require the explicit durable refresh path")
			calls, changed := h.vault.snapshot()
			require.Equal(t, 1, calls)
			require.Equal(t, []bool{false}, changed)

			old, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, "00000000-0000-4000-8000-999999999999")
			require.NoError(t, err)
			require.Equal(t, model.FeishuAuthSessionPending, old.State)
			require.Empty(t, h.dispatcher.snapshot())
		})
	}
}

func TestAuthSessionService_ExpiredCreateLeaseWithAuthorizedVaultRecoversConnected(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionCreatingApp)
	h.cli.status = true
	operationID := "operation-create-expired"
	expired := h.now.Add(-time.Minute)
	require.NoError(t, h.db.Create(&model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-888888888888", UserID: 7, Generation: 1,
		OperationID: &operationID, Phase: model.FeishuAuthPhaseCreateApp,
		RequestedScopesJSON: []byte(`["docx:document:create"]`),
		State:               model.FeishuAuthSessionPending, LeaseOwner: "dead-create-worker", LeaseUntil: &expired,
		ExpiresAt: expired,
	}).Error)

	action, err := h.newService("create-recovery-worker").StartRecovery(h.ctx, RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: operationID, Kind: RecoveryCreateApp,
		Scopes: []string{"docx:document:create"},
	})
	require.NoError(t, err)
	require.Nil(t, action)
	require.Empty(t, h.dispatcher.snapshot())
	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionConnected, account.ConnectionState)
	require.True(t, account.Connected)
}

func TestAuthSessionService_ExpiredRecoveryErrorSupersedesAndReleasesLease(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		vaultErr  error
		statusErr error
	}{
		{name: "vault error", vaultErr: errors.New("vault unavailable")},
		{name: "auth status error", statusErr: errors.New("status unavailable")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newAuthSessionHarness(t)
			h.createAccount(model.FeishuConnectionConnected)
			h.vault.err = testCase.vaultErr
			h.cli.statusErr = testCase.statusErr
			operationID := "operation-expired-error"
			expired := h.now.Add(-time.Minute)
			session := &model.FeishuAuthSession{
				ID: "00000000-0000-4000-8000-777777777777", UserID: 7, Generation: 1,
				OperationID: &operationID, Phase: model.FeishuAuthPhaseUserAuth,
				RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
				State:               model.FeishuAuthSessionPending, LeaseOwner: "dead-worker", LeaseUntil: &expired,
				ExpiresAt: expired,
			}
			require.NoError(t, h.db.Create(session).Error)
			service := h.newService("recovery-error-worker")
			service.urls.put(authSessionRegistryKey(session), "https://open.feishu.cn/suite/passport/oauth/device?user_code=STALE", h.now.Add(time.Minute))

			_, err := service.StartRecovery(h.ctx, RecoveryRequest{
				UserID: 7, Generation: 1, OperationID: operationID, Kind: RecoveryUserScope,
				Scopes: []string{"docx:document:readonly"},
			})
			require.ErrorIs(t, err, ErrAuthSessionUnavailable)
			stored, getErr := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, session.ID)
			require.NoError(t, getErr)
			require.Equal(t, model.FeishuAuthSessionPending, stored.State)
			require.Equal(t, "dead-worker", stored.LeaseOwner)
			require.NotNil(t, stored.LeaseUntil)
		})
	}
}

func TestAuthSessionService_PendingIntentIsReusedAcrossServiceInstances(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	release := make(chan struct{})
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=INSTANCE_A"}
	h.cli.release = release
	request := RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-shared", Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:readonly"},
	}
	firstService := h.newService("instance-a")
	first, err := firstService.StartRecovery(h.ctx, request)
	require.NoError(t, err)

	secondCLI := &authSessionCLIFake{}
	second, err := NewAuthSessionService(AuthSessionServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: h.vault, CLI: secondCLI, DeviceAuth: h.newDeviceAuthFlow("instance-b-device-auth", secondCLI),
		Dispatcher: h.dispatcher, Owner: "instance-b",
		Now: func() time.Time { return h.now }, NewID: func() string { return "00000000-0000-4000-8000-222222222222" },
		LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second,
	})
	require.NoError(t, err)
	secondAction, err := second.StartRecovery(h.ctx, request)
	require.NoError(t, err)
	require.Equal(t, first.SessionID, secondAction.SessionID)
	require.Empty(t, secondAction.URL, "a different process returns the durable session without persisting or inventing a URL")
	// Task 13 may add an explicit same-session URL refresh; until then this
	// no-URL action is the cross-process, no-duplicate contract.
	secondArgv, _ := secondCLI.snapshot()
	require.Empty(t, secondArgv, "the second instance must not start a duplicate blocking login")
	var count int64
	require.NoError(t, h.db.Model(&model.FeishuAuthSession{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, firstService.Activate(h.ctx, first.SessionID))
	close(release)
}

func TestAuthSessionService_ConstructorRejectsMissingDependencies(t *testing.T) {
	_, err := NewAuthSessionService(AuthSessionServiceDeps{})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAuthSessionUnavailable))
}

func TestAuthSessionService_ConstructorRejectsMissingDeviceAuth(t *testing.T) {
	h := newAuthSessionHarness(t)
	_, err := NewAuthSessionService(AuthSessionServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: h.vault, CLI: h.cli, Dispatcher: h.dispatcher, Owner: "missing-device-auth",
	})
	require.ErrorIs(t, err, ErrAuthSessionUnavailable)
}

func TestControlledLarkCLIRunner_AuthSessionStreamsURLBeforeBlockingCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test CLI uses a POSIX shell")
	}
	home := t.TempDir()
	binary := filepath.Join(t.TempDir(), "lark-cli")
	script := `#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "login" ]; then
  echo 'https://open.feishu.cn/suite/passport/oauth/device?user_code=STREAMED'
  while [ ! -f "$HOME/approved" ]; do sleep 0.01; done
  printf token > "$HOME/token"
  printf '{"ok":true}\n'
  exit 0
fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  printf '{"identities":{"user":{"available":true}}}\n'
  exit 0
fi
exit 2
`
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o700))
	runner := &ControlledLarkCLIRunner{binary: binary}
	urlReady := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runner.RunBlocking(context.Background(), home,
			[]string{"auth", "login", "--json", "--scope", "offline_access"},
			func(value string) error {
				urlReady <- value
				return nil
			},
		)
	}()

	select {
	case got := <-urlReady:
		require.Equal(t, "https://open.feishu.cn/suite/passport/oauth/device?user_code=STREAMED", got)
	case <-time.After(3 * time.Second):
		t.Fatal("verification URL was not streamed before authorization completion")
	}
	select {
	case err := <-done:
		t.Fatalf("blocking login exited before approval: %v", err)
	default:
	}
	require.NoError(t, os.WriteFile(filepath.Join(home, "approved"), []byte("1"), 0o600))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("blocking login did not finish after approval")
	}
	require.FileExists(t, filepath.Join(home, "token"))

	authorized, err := runner.AuthStatus(context.Background(), home)
	require.NoError(t, err)
	require.True(t, authorized)
}

func TestControlledLarkCLIRunner_ConfigInitAcceptsOfficialKeychainBackedAppEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test CLI uses a POSIX shell")
	}
	home := t.TempDir()
	binary := filepath.Join(t.TempDir(), "lark-cli")
	script := `#!/bin/sh
if [ "$1" = "config" ] && [ "$2" = "init" ] && [ "$3" = "--new" ]; then
  mkdir -p "$HOME/.lark-cli"
  mkdir -p "$HOME/.local/share/lark-cli"
  echo 'https://open.feishu.cn/page/cli?user_code=CREATED'
  printf '{"apps":[{"appId":"cli_created","appSecret":{"source":"keychain","id":"appsecret:cli_created"},"brand":"feishu"}]}\n' > "$HOME/.lark-cli/config.json"
  printf 'test-master-key' > "$HOME/.local/share/lark-cli/master.key"
  printf 'encrypted-test-secret' > "$HOME/.local/share/lark-cli/appsecret_cli_created.enc"
  echo '应用创建完成'
  exit 0
fi
exit 2
`
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o700))
	runner := &ControlledLarkCLIRunner{binary: binary}

	var observedURL string
	err := runner.RunBlocking(context.Background(), home, []string{"config", "init", "--new"}, func(value string) error {
		observedURL = value
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "https://open.feishu.cn/page/cli?user_code=CREATED", observedURL)
	appID, evidenceErr := runner.AppIDFromHome(context.Background(), home)
	require.NoError(t, evidenceErr)
	require.Equal(t, "cli_created", appID)
	require.FileExists(t, filepath.Join(home, ".local", "share", "lark-cli", "master.key"))
	require.FileExists(t, filepath.Join(home, ".local", "share", "lark-cli", "appsecret_cli_created.enc"))
}

func TestParseControlledAppIDEvidence_StrictlyBindsOfficialKeychainReference(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantID  string
		wantErr bool
	}{
		{
			name:   "official keychain reference",
			raw:    `{"apps":[{"appId":"cli_created","appSecret":{"source":"keychain","id":"appsecret:cli_created"},"brand":"feishu","users":[]}]}`,
			wantID: "cli_created",
		},
		{
			name:   "legacy nonempty plaintext",
			raw:    `{"apps":[{"appId":"cli_legacy","appSecret":"legacy-secret"}]}`,
			wantID: "cli_legacy",
		},
		{
			name:    "wrong source",
			raw:     `{"apps":[{"appId":"cli_created","appSecret":{"source":"file","id":"appsecret:cli_created"}}]}`,
			wantErr: true,
		},
		{
			name:    "reference belongs to another app",
			raw:     `{"apps":[{"appId":"cli_created","appSecret":{"source":"keychain","id":"appsecret:cli_other"}}]}`,
			wantErr: true,
		},
		{
			name:    "unknown reference field",
			raw:     `{"apps":[{"appId":"cli_created","appSecret":{"source":"keychain","id":"appsecret:cli_created","path":"elsewhere"}}]}`,
			wantErr: true,
		},
		{
			name:    "duplicate reference field",
			raw:     `{"apps":[{"appId":"cli_created","appSecret":{"source":"keychain","source":"keychain","id":"appsecret:cli_created"}}]}`,
			wantErr: true,
		},
		{
			name:    "case variant reference field",
			raw:     `{"apps":[{"appId":"cli_created","appSecret":{"Source":"keychain","id":"appsecret:cli_created"}}]}`,
			wantErr: true,
		},
		{
			name:    "case variant app secret field",
			raw:     `{"apps":[{"appId":"cli_created","AppSecret":{"source":"keychain","id":"appsecret:cli_created"}}]}`,
			wantErr: true,
		},
		{
			name:    "empty plaintext",
			raw:     `{"apps":[{"appId":"cli_created","appSecret":""}]}`,
			wantErr: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseControlledAppIDEvidence([]byte(testCase.raw))
			if testCase.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.wantID, got)
		})
	}
}

func TestControlledLarkCLIRunner_AuthSessionSuccessWithoutURLFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test CLI uses a POSIX shell")
	}
	home := t.TempDir()
	binary := filepath.Join(t.TempDir(), "lark-cli")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nprintf '{\"ok\":true}\\n'\n"), 0o700))
	runner := &ControlledLarkCLIRunner{binary: binary}

	err := runner.RunBlocking(context.Background(), home,
		[]string{"auth", "login", "--json", "--scope", "offline_access"},
		func(string) error {
			t.Fatal("callback must not be invoked without a URL")
			return nil
		},
	)
	require.Error(t, err)
}

func TestControlledLarkCLIRunner_AuthSessionCancellationKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test CLI uses POSIX process groups")
	}
	home := t.TempDir()
	binary := filepath.Join(t.TempDir(), "lark-cli")
	script := `#!/bin/sh
echo 'https://open.feishu.cn/suite/passport/oauth/device?user_code=CANCEL'
(sleep 1; printf orphan > "$HOME/orphan") &
wait
`
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o700))
	runner := &ControlledLarkCLIRunner{binary: binary}
	ctx, cancel := context.WithCancel(context.Background())
	urlReady := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- runner.RunBlocking(ctx, home,
			[]string{"auth", "login", "--json", "--scope", "offline_access"},
			func(string) error {
				urlReady <- struct{}{}
				return nil
			},
		)
	}()
	select {
	case <-urlReady:
	case <-time.After(3 * time.Second):
		t.Fatal("verification URL was not streamed")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled auth process group did not exit")
	}
	time.Sleep(1100 * time.Millisecond)
	require.NoFileExists(t, filepath.Join(home, "orphan"), "descendant must not outlive the cancelled process group")
}
