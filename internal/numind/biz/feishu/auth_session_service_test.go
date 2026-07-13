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
	mu          sync.Mutex
	urls        []string
	runErr      error
	status      bool
	statusErr   error
	release     <-chan struct{}
	argv        [][]string
	statusCalls int
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
	runErr := f.runErr
	f.mu.Unlock()
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
	return runErr
}

func (f *authSessionCLIFake) AuthStatus(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	return f.status, f.statusErr
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
	ready chan struct{}
}

func (f *authSessionDispatcherFake) DispatchResume(_ context.Context, _ uint, operationID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, operationID)
	f.mu.Unlock()
	if f.ready != nil {
		select {
		case f.ready <- struct{}{}:
		default:
		}
	}
	return nil
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
		cli: &authSessionCLIFake{}, vault: &authSessionVaultFake{},
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
	h.t.Helper()
	service, err := NewAuthSessionService(AuthSessionServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: h.vault, CLI: h.cli, Dispatcher: h.dispatcher, Owner: owner,
		Now: func() time.Time { return h.now },
		NewID: func() string {
			h.idMu.Lock()
			defer h.idMu.Unlock()
			h.nextID++
			return "00000000-0000-4000-8000-" + leftPad12(h.nextID)
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

func TestAuthSessionService_ManualConnectCreatesAppBeforeOfflineAuthorization(t *testing.T) {
	h := newAuthSessionHarness(t)
	release := make(chan struct{})
	h.cli.urls = []string{"https://open.feishu.cn/page/cli?user_code=MANUAL_CREATE"}
	h.cli.release = release

	action, err := h.newService("worker-manual-create").ConnectManual(h.ctx, 7)
	require.NoError(t, err)
	require.Equal(t, model.FeishuAuthPhaseCreateApp, action.Phase)
	require.Equal(t, []string{"offline_access"}, action.Scopes)
	argv, _ := h.cli.snapshot()
	require.Equal(t, [][]string{{"config", "init", "--new"}}, argv)
	close(release)
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
	close(release)
}

func TestAuthSessionService_CreateAppAndOfficialAppApprovalPhases(t *testing.T) {
	t.Run("create app worker", func(t *testing.T) {
		h := newAuthSessionHarness(t)
		h.createAccount(model.FeishuConnectionNone)
		release := make(chan struct{})
		h.cli.urls = []string{"https://open.feishu.cn/page/cli?user_code=CREATE"}
		h.cli.release = release
		action, err := h.newService("worker-create").StartRecovery(h.ctx, RecoveryRequest{
			UserID: 7, Generation: 1, OperationID: "operation-create", Kind: RecoveryCreateApp,
			Scopes: []string{"docx:document:create"},
		})
		require.NoError(t, err)
		require.Equal(t, model.FeishuAuthPhaseCreateApp, action.Phase)
		argv, _ := h.cli.snapshot()
		require.Equal(t, []string{"config", "init", "--new"}, argv[0])
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

			action, err := h.newService("recovery-worker").StartRecovery(h.ctx, RecoveryRequest{
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
				waitAuthDispatch(t, h.dispatcher)
			} else {
				require.NotNil(t, action)
				require.NotEqual(t, old.ID, action.SessionID)
				require.Equal(t, model.FeishuAuthSessionSuperseded, old.State)
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
	waitAuthDispatch(t, h.dispatcher)
	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionConnected, account.ConnectionState)
	require.True(t, account.Connected)
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
	first, err := h.newService("instance-a").StartRecovery(h.ctx, request)
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
	secondArgv, _ := secondCLI.snapshot()
	require.Empty(t, secondArgv, "the second instance must not start a duplicate blocking login")
	var count int64
	require.NoError(t, h.db.Model(&model.FeishuAuthSession{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
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
	case <-time.After(time.Second):
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
	case <-time.After(time.Second):
		t.Fatal("blocking login did not finish after approval")
	}
	require.FileExists(t, filepath.Join(home, "token"))

	authorized, err := runner.AuthStatus(context.Background(), home)
	require.NoError(t, err)
	require.True(t, authorized)
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
	case <-time.After(time.Second):
		t.Fatal("verification URL was not streamed")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancelled auth process group did not exit")
	}
	time.Sleep(1100 * time.Millisecond)
	require.NoFileExists(t, filepath.Join(home, "orphan"), "descendant must not outlive the cancelled process group")
}
