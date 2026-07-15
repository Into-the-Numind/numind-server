package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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

// authSessionUpdateBarrierStore holds the start path after its last durable
// state write and before local worker registration. It models the only window
// where RetireGeneration can commit before StopGenerationAndWait snapshots the
// local registry.
type authSessionUpdateBarrierStore struct {
	AuthSessionStore
	arrived chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *authSessionUpdateBarrierStore) UpdateAccountConnectionState(
	ctx context.Context,
	userID uint,
	generation uint64,
	state string,
	connected bool,
	now time.Time,
) error {
	if err := s.AuthSessionStore.UpdateAccountConnectionState(ctx, userID, generation, state, connected, now); err != nil {
		return err
	}
	s.once.Do(func() {
		s.arrived <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	return nil
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
	service, err := NewAuthSessionService(AuthSessionServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: sessions,
		Vault: h.vault, CLI: h.cli, Dispatcher: h.dispatcher, Owner: owner,
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
	require.Equal(t, [][]string{{"auth", "login", "--json", "--scope", "offline_access"}}, argv)

	stored, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.JSONEq(t, `["offline_access"]`, string(stored.RequestedScopesJSON))
	serialized, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "open.feishu.cn")
	require.NotContains(t, string(serialized), "user_code")
	close(release)
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

func TestAuthSessionService_StopGenerationAndWaitJoinsRetiredWorkerBeforeReturning(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionAppReady)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=STOP"}
	hold := make(chan struct{})
	h.cli.holdAfterCancel = hold
	cancelObserved := make(chan struct{}, 1)
	h.cli.cancelObserved = cancelObserved
	service := h.newService("stop-join-owner")

	_, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)

	waiter, ok := any(service).(interface {
		StopGenerationAndWait(context.Context, uint, uint64) error
	})
	require.True(t, ok, "authorization teardown must expose a joinable generation stop")

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- waiter.StopGenerationAndWait(context.Background(), 7, 1)
	}()
	select {
	case <-cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("generation stop did not cancel the retired worker")
	}
	select {
	case err := <-stopDone:
		t.Fatalf("generation stop returned before retired worker and temporary HOME exited: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(hold)
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("generation stop did not join the retired worker")
	}
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
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	service := h.newServiceWithSessions("late-register-owner", &authSessionUpdateBarrierStore{
		AuthSessionStore: h.dataStore.FeishuWorkspace(), arrived: arrived, release: release,
	})

	resultCh := make(chan *OperationAction, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := service.ConnectManual(h.ctx, 7)
		resultCh <- result
		errCh <- err
	}()
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("manual connect did not reach the worker-registration barrier")
	}
	retiredGeneration, _, err := h.dataStore.ThirdPartyAccounts().RetireGeneration(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.EqualValues(t, 1, retiredGeneration)
	require.NoError(t, service.StopGenerationAndWait(context.Background(), 7, retiredGeneration))
	close(release)

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, ErrAuthSessionUnavailable)
	case <-time.After(time.Second):
		t.Fatal("late worker registration did not stop")
	}
	require.Nil(t, <-resultCh)
	argv, _ := h.cli.snapshot()
	require.Empty(t, argv, "a late retired worker must never reach lark-cli")
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
		{"auth", "login", "--json", "--scope", "offline_access"},
	}, argv, "reading the pending action must not launch a duplicate login")

	close(userRelease)
	require.Eventually(t, func() bool {
		account, getErr := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
		return getErr == nil && account.Connected && account.ConnectionState == model.FeishuConnectionConnected
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
	userRelease := make(chan struct{})
	delayedURL := "https://open.feishu.cn/suite/passport/oauth/device?user_code=DELAYED_MANUAL_USER"
	h.cli.urls = []string{
		"https://open.feishu.cn/page/cli?user_code=DELAYED_MANUAL_CREATE",
		delayedURL,
	}
	h.cli.releases = []<-chan struct{}{createRelease, userRelease}
	h.cli.urlDelays = []time.Duration{0, authSessionFinalizeTimeout + 100*time.Millisecond}
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
	require.Eventually(t, func() bool {
		return service.urls.get(authSessionRegistryKey(&userSession), h.now) == delayedURL
	}, authSessionFinalizeTimeout+3*time.Second, 20*time.Millisecond,
		"manual chaining must remain alive for StartTimeout even when the URL arrives after finalize overhead")

	userAction, err := service.ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, userSession.ID, userAction.SessionID)
	require.Equal(t, delayedURL, userAction.URL)
	argv, _ := h.cli.snapshot()
	require.Len(t, argv, 2)
	close(userRelease)
	require.Eventually(t, func() bool {
		account, getErr := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
		return getErr == nil && account.Connected
	}, 3*time.Second, 10*time.Millisecond)
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
		"auth", "login", "--json", "--scope", "docx:document:readonly docx:document:write_only",
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

func TestAuthSessionService_ConcurrentAppApprovalHasSingleLeaseWinner(t *testing.T) {
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
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, []string{"operation-approval-race"}, h.dispatcher.snapshot())
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
	release := make(chan struct{})
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=WORKER"}
	h.cli.release = release
	service := h.newService("worker-success")
	request := RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-success", Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:create"},
	}
	action, err := service.StartRecovery(h.ctx, request)
	require.NoError(t, err)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	close(release)
	waitAuthDispatch(t, h.dispatcher)

	calls, changed := h.vault.snapshot()
	require.Equal(t, 1, calls)
	require.Equal(t, []bool{true}, changed, "successful CLI HOME must be sealed before dispatch")
	stored, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthSessionCompleted, stored.State)
	require.Equal(t, []string{"operation-success"}, h.dispatcher.snapshot())

	restartedAction, err := h.newService("worker-restarted").StartRecovery(h.ctx, request)
	require.NoError(t, err)
	require.Nil(t, restartedAction, "a restarted service must observe durable completion")
	argv, _ := h.cli.snapshot()
	require.Len(t, argv, 1, "durable completion must not launch the same login again")
}

func TestAuthSessionService_OperationWorkerWaitsForPersistedWaitingActivation(t *testing.T) {
	h := newAuthSessionHarness(t)
	h.createAccount(model.FeishuConnectionConnected)
	h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=FAST"}
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

	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	waitAuthDispatch(t, h.dispatcher)
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
	h.dispatcher.errs = []error{errors.New("temporary dispatch failure"), nil}
	service := h.newService("worker-dispatch-retry")
	request := RecoveryRequest{
		UserID: 7, Generation: 1, OperationID: "operation-dispatch-retry", Kind: RecoveryUserScope,
		Scopes: []string{"docx:document:readonly"},
	}
	action, err := service.StartRecovery(h.ctx, request)
	require.NoError(t, err)
	require.NoError(t, service.Activate(h.ctx, action.SessionID))
	waitAuthDispatch(t, h.dispatcher)
	require.Eventually(t, func() bool {
		stored, getErr := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, action.SessionID)
		return getErr == nil && stored.State == model.FeishuAuthSessionCompleted
	}, time.Second, 10*time.Millisecond)

	restartedAction, err := h.newService("worker-dispatch-compensation").StartRecovery(h.ctx, request)
	require.NoError(t, err)
	require.Nil(t, restartedAction)
	require.Equal(t, []string{"operation-dispatch-retry"}, h.dispatcher.snapshot(),
		"the caller's Operation.Resume must continue replay instead of re-entering through the dispatcher")
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
	require.NoError(t, err)
	require.Nil(t, action)
	require.Empty(t, h.dispatcher.snapshot(), "the current Operation.Resume owns replay after observing completion")
	argv, statusCalls := h.cli.snapshot()
	require.Empty(t, argv)
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
			if !status {
				release := make(chan struct{})
				h.cli.urls = []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=FRESH"}
				h.cli.release = release
				defer close(release)
			}

			service := h.newService("recovery-worker")
			action, err := service.StartRecovery(h.ctx, RecoveryRequest{
				UserID: 7, Generation: 1, OperationID: operationID, Kind: RecoveryUserScope,
				Scopes: []string{"docx:document:readonly"},
			})
			require.NoError(t, err)
			_, statusCalls := h.cli.snapshot()
			require.Equal(t, 1, statusCalls, "expired lease must use recovery-only auth status inside vault")
			calls, changed := h.vault.snapshot()
			require.GreaterOrEqual(t, calls, 1)
			require.False(t, changed[0], "auth status is read-only and must not reseal HOME")

			old, err := h.dataStore.FeishuWorkspace().GetSessionForUser(h.ctx, 7, 1, "00000000-0000-4000-8000-999999999999")
			require.NoError(t, err)
			if status {
				require.Nil(t, action)
				require.Equal(t, model.FeishuAuthSessionCompleted, old.State)
				require.Empty(t, h.dispatcher.snapshot())
			} else {
				require.NotNil(t, action)
				require.NotEqual(t, old.ID, action.SessionID)
				require.Equal(t, model.FeishuAuthSessionSuperseded, old.State)
				require.NoError(t, service.Activate(h.ctx, action.SessionID))
			}
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
			require.Equal(t, model.FeishuAuthSessionSuperseded, stored.State)
			require.Empty(t, stored.LeaseOwner)
			require.Nil(t, stored.LeaseUntil)
			require.Empty(t, service.actionFor(stored, []string{"docx:document:readonly"}).URL)
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
		Vault: h.vault, CLI: secondCLI, Dispatcher: h.dispatcher, Owner: "instance-b",
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
