package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// svcAccountStore remains shared test support for the legacy orchestrator
// package tests while Task 13 replaces the HTTP entry point above it.
type svcAccountStore struct {
	mu      sync.Mutex
	rows    map[string]*model.UserThirdPartyAccount
	upserts int
}

func newSvcAccountStore() *svcAccountStore {
	return &svcAccountStore{rows: map[string]*model.UserThirdPartyAccount{}}
}

func (f *svcAccountStore) key(userID uint, provider string) string {
	return provider + "|" + strconv.FormatUint(uint64(userID), 10)
}

func (f *svcAccountStore) put(acc *model.UserThirdPartyAccount) {
	copy := *acc
	f.rows[f.key(acc.UserID, acc.Provider)] = &copy
}

func (f *svcAccountStore) Get(_ context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, found := f.rows[f.key(userID, provider)]
	if !found {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *row
	return &copy, nil
}

func (f *svcAccountStore) Upsert(_ context.Context, account *model.UserThirdPartyAccount) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts++
	f.put(account)
	return nil
}

func (f *svcAccountStore) EnsurePlaceholder(_ context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.key(userID, provider)
	if _, found := f.rows[key]; !found {
		f.put(&model.UserThirdPartyAccount{UserID: userID, Provider: provider, Generation: 1, ConnectionState: model.FeishuConnectionNone})
	}
	copy := *f.rows[key]
	return &copy, nil
}

func (f *svcAccountStore) Delete(_ context.Context, userID uint, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, f.key(userID, provider))
	return nil
}

func (f *svcAccountStore) UpdateTokens(_ context.Context, _ uint, _ string, _, _ []byte, _ *time.Time) error {
	return nil
}

func (f *svcAccountStore) MarkConnected(_ context.Context, userID uint, provider string, connectedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, found := f.rows[f.key(userID, provider)]
	if !found {
		return gorm.ErrRecordNotFound
	}
	row.Connected = true
	row.ConnectedAt = &connectedAt
	return nil
}

func (f *svcAccountStore) RetireGeneration(_ context.Context, userID uint, provider string) (uint64, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, found := f.rows[f.key(userID, provider)]
	if !found || row.Generation == 0 {
		return 0, 0, gorm.ErrRecordNotFound
	}
	old := row.Generation
	row.Generation++
	row.ConnectionState = model.FeishuConnectionDisconnecting
	row.Connected = false
	return old, row.Generation, nil
}

func (f *svcAccountStore) FinalizeDisconnect(_ context.Context, userID uint, provider string, generation uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, found := f.rows[f.key(userID, provider)]
	if !found || row.Generation != generation {
		return gorm.ErrRecordNotFound
	}
	row.ConnectionState = model.FeishuConnectionNone
	row.Connected = false
	row.AppID = ""
	return nil
}

// Task13 lifecycle fakes deliberately expose only server-owned identifiers.
// They make the HTTP/service contract prove that browser input cannot inject
// argv, scopes, app ids, or another user's operation/session identity.
type lifecycleAccountFake struct {
	account           *model.UserThirdPartyAccount
	getErr            error
	retireCalls       int
	finalizeCalls     int
	retiredGeneration uint64
}

func (f *lifecycleAccountFake) Get(_ context.Context, _ uint, _ string) (*model.UserThirdPartyAccount, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.account == nil {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *f.account
	return &copy, nil
}

func (f *lifecycleAccountFake) RetireGeneration(_ context.Context, _ uint, _ string) (uint64, uint64, error) {
	f.retireCalls++
	if f.account == nil {
		return 0, 0, gorm.ErrRecordNotFound
	}
	if f.account.ConnectionState == model.FeishuConnectionDisconnecting {
		if f.account.Generation <= 1 {
			return 0, 0, gorm.ErrRecordNotFound
		}
		f.retiredGeneration = f.account.Generation - 1
		return f.retiredGeneration, f.account.Generation, nil
	}
	f.retiredGeneration = f.account.Generation
	f.account.Generation++
	f.account.ConnectionState = model.FeishuConnectionDisconnecting
	f.account.Connected = false
	return f.retiredGeneration, f.account.Generation, nil
}

func (f *lifecycleAccountFake) FinalizeDisconnect(_ context.Context, _ uint, _ string, generation uint64) error {
	f.finalizeCalls++
	if f.account == nil || f.account.Generation != generation {
		return gorm.ErrRecordNotFound
	}
	f.account.ConnectionState = model.FeishuConnectionNone
	f.account.Connected = false
	f.account.AppID = ""
	return nil
}

type lifecycleWorkspaceFake struct {
	operation        *model.FeishuOperation
	operationErr     error
	activeSession    *model.FeishuAuthSession
	activeSessionErr error
	deleteVaultCalls int
}

func (f *lifecycleWorkspaceFake) GetOperationForUser(_ context.Context, _ uint, _ uint64, _ string) (*model.FeishuOperation, error) {
	if f.operationErr != nil {
		return nil, f.operationErr
	}
	if f.operation == nil {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *f.operation
	return &copy, nil
}

func (f *lifecycleWorkspaceFake) FindActiveSessionForUser(_ context.Context, _ uint, _ uint64) (*model.FeishuAuthSession, error) {
	if f.activeSessionErr != nil {
		return nil, f.activeSessionErr
	}
	if f.activeSession == nil {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *f.activeSession
	return &copy, nil
}

func (f *lifecycleWorkspaceFake) GetSessionForUser(_ context.Context, userID uint, generation uint64, sessionID string) (*model.FeishuAuthSession, error) {
	if f.activeSessionErr != nil {
		return nil, f.activeSessionErr
	}
	if f.activeSession == nil || f.activeSession.ID != sessionID || f.activeSession.UserID != userID || f.activeSession.Generation != generation {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *f.activeSession
	return &copy, nil
}

func (f *lifecycleWorkspaceFake) DeleteVault(_ context.Context, _ uint, _ uint64) error {
	f.deleteVaultCalls++
	return nil
}

type lifecycleAuthFake struct {
	connectCalls             int
	refreshCalls             int
	completeAppApprovalCalls []lifecycleAppApprovalCall
	completeAppApproval      func(context.Context, uint, uint64, string) error
	stopped                  []uint64
	action                   *OperationAction
	stopWait                 func(context.Context) error
}

type lifecycleAppApprovalCall struct {
	userID     uint
	generation uint64
	sessionID  string
}

func (f *lifecycleAuthFake) ConnectManual(_ context.Context, _ uint) (*OperationAction, error) {
	f.connectCalls++
	return cloneOperationAction(f.action), nil
}

func (f *lifecycleAuthFake) RefreshAction(_ context.Context, _ uint, _ uint64, _ string) (*OperationAction, error) {
	f.refreshCalls++
	return cloneOperationAction(f.action), nil
}

func (f *lifecycleAuthFake) CompleteAppApproval(ctx context.Context, userID uint, generation uint64, sessionID string) error {
	f.completeAppApprovalCalls = append(f.completeAppApprovalCalls, lifecycleAppApprovalCall{
		userID: userID, generation: generation, sessionID: sessionID,
	})
	if f.completeAppApproval != nil {
		return f.completeAppApproval(ctx, userID, generation, sessionID)
	}
	return nil
}

func (f *lifecycleAuthFake) StopGenerationAndWait(ctx context.Context, _ uint, generation uint64) error {
	f.stopped = append(f.stopped, generation)
	if f.stopWait != nil {
		return f.stopWait(ctx)
	}
	return nil
}

type lifecycleDispatcherFake struct{ calls int }

func (f *lifecycleDispatcherFake) DispatchResume(_ context.Context, _ uint, _ string) error {
	f.calls++
	return nil
}

type lifecycleOperationsFake struct {
	confirmed int
	cancelled int
}

func (f *lifecycleOperationsFake) Confirm(_ context.Context, _ uint, id string) (*OperationResult, error) {
	f.confirmed++
	return &OperationResult{OperationID: id, State: model.FeishuOperationExecuting}, nil
}

func (f *lifecycleOperationsFake) Cancel(_ context.Context, _ uint, id string) (*OperationResult, error) {
	f.cancelled++
	return &OperationResult{OperationID: id, State: model.FeishuOperationCancelled}, nil
}

type lifecycleTeardownFake struct {
	calls []uint64
	err   error
}

func (f *lifecycleTeardownFake) LogoutRetired(_ context.Context, _ uint, generation uint64) (RetiredWorkspaceTeardownResult, error) {
	f.calls = append(f.calls, generation)
	return RetiredWorkspaceTeardownResult{LogoutAttempted: true, LogoutSucceeded: f.err == nil}, f.err
}

func newLifecycleService(t *testing.T, account *model.UserThirdPartyAccount, operation *model.FeishuOperation) (*WorkspaceLifecycleService, *lifecycleAccountFake, *lifecycleWorkspaceFake, *lifecycleAuthFake, *lifecycleDispatcherFake, *lifecycleOperationsFake, *lifecycleTeardownFake) {
	t.Helper()
	accounts := &lifecycleAccountFake{account: account}
	workspace := &lifecycleWorkspaceFake{operation: operation}
	auth := &lifecycleAuthFake{action: &OperationAction{Provider: ProviderLark, SessionID: "session-1", Phase: model.FeishuAuthPhaseUserAuth, URL: "https://open.feishu.cn/suite/passport/oauth/device", ExpiresAt: time.Now().Add(time.Minute)}}
	dispatcher := &lifecycleDispatcherFake{}
	operations := &lifecycleOperationsFake{}
	teardown := &lifecycleTeardownFake{}
	svc, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{
		Accounts: accounts, Workspace: workspace, Auth: auth, Dispatcher: dispatcher, Operations: operations, Teardown: teardown,
	})
	require.NoError(t, err)
	return svc, accounts, workspace, auth, dispatcher, operations, teardown
}

func TestWorkspaceLifecycleStatusIsReadOnlyAndNeverReturnsAuthorizationURL(t *testing.T) {
	appID := "cli_12345678"
	svc, _, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: appID, Generation: 3,
		ConnectionState:     model.FeishuConnectionWaitingUserAuth,
		CapabilityStateJSON: []byte(`{"docs":{"state":"available"}}`),
	}, nil)
	workspace.activeSession = &model.FeishuAuthSession{
		ID: "session-active", UserID: 7, Generation: 3, Phase: model.FeishuAuthPhaseUserAuth,
		State: model.FeishuAuthSessionPending, ExpiresAt: time.Now().Add(time.Minute),
	}

	status, err := svc.Status(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionWaitingUserAuth, status.State)
	require.False(t, status.Connected)
	require.NotEqual(t, appID, status.AppIDMasked)
	require.NotContains(t, status.AppIDMasked, "12345678")
	require.Equal(t, 0, auth.connectCalls, "GET status must not create a worker or URL")
	require.NotNil(t, status.ActiveAction)
	require.Equal(t, "session-active", status.ActiveAction.SessionID)
	require.False(t, status.ActiveAction.LinkAvailable, "status may expose metadata only, never a live authorization URL")
}

func TestWorkspaceLifecycleConnectUsesOnlyManualOfflineAccessFlow(t *testing.T) {
	svc, _, _, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 1}, nil)

	result, err := svc.Connect(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, result.Action)
	require.Equal(t, model.FeishuConnectionWaitingUserAuth, result.State, "connect returns a lifecycle state, not an internal auth phase")
	require.Equal(t, 1, auth.connectCalls)
}

func TestWorkspaceLifecycleResumeUsesSharedDispatcherForUserCompleted(t *testing.T) {
	operationID := "op-1"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationWaitingUserAuth,
		ResultSummaryJSON: lifecycleRecoverySummary(t, model.FeishuOperationWaitingUserAuth, "session-user-auth", model.FeishuAuthPhaseUserAuth, RecoveryUserScope),
	}
	svc, _, workspace, _, dispatcher, operations, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 2}, op)
	workspace.activeSession = &model.FeishuAuthSession{
		ID: "session-user-auth", UserID: 7, Generation: 2, OperationID: &operationID,
		Phase: model.FeishuAuthPhaseUserAuth, State: model.FeishuAuthSessionCompleted,
	}

	result, err := svc.Resume(context.Background(), 7, "op-1", ResumeActionUserCompleted)
	require.NoError(t, err)
	require.Equal(t, "op-1", result.OperationID)
	require.Equal(t, 1, dispatcher.calls)
	require.Zero(t, operations.confirmed)
	require.Zero(t, operations.cancelled)
}

func TestWorkspaceLifecycleResumeCompletesOwnedAppScopeThenReplaysExactlyOnce(t *testing.T) {
	operationID := "op-app-scope"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationWaitingAppScope,
		ResultSummaryJSON: lifecycleRecoverySummary(t, model.FeishuOperationWaitingAppScope, "session-app-scope", model.FeishuAuthPhaseAppScope, RecoveryAppScope),
	}
	svc, _, workspace, auth, dispatcher, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)
	workspace.activeSession = &model.FeishuAuthSession{
		ID: "session-app-scope", UserID: 7, Generation: 2, OperationID: &operationID,
		Phase: model.FeishuAuthPhaseAppScope, State: model.FeishuAuthSessionPending,
		ExpiresAt: time.Now().Add(time.Minute),
	}
	auth.completeAppApproval = func(ctx context.Context, userID uint, generation uint64, sessionID string) error {
		require.Equal(t, uint(7), userID)
		require.Equal(t, uint64(2), generation)
		require.Equal(t, "session-app-scope", sessionID)
		workspace.activeSession.State = model.FeishuAuthSessionCompleted
		workspace.operation.State = model.FeishuOperationSucceeded
		return dispatcher.DispatchResume(ctx, userID, operationID)
	}

	result, err := svc.Resume(context.Background(), 7, operationID, ResumeActionUserCompleted)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	require.Equal(t, []lifecycleAppApprovalCall{{userID: 7, generation: 2, sessionID: "session-app-scope"}}, auth.completeAppApprovalCalls)
	require.Equal(t, 1, dispatcher.calls, "app approval must use the one Task12 dispatcher")

	result, err = svc.Resume(context.Background(), 7, operationID, ResumeActionUserCompleted)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	require.Len(t, auth.completeAppApprovalCalls, 1, "a duplicate acknowledgement must not repeat the CLI write path")
	require.Equal(t, 1, dispatcher.calls, "a succeeded operation must not be resumed again")
}

func TestWorkspaceLifecycleResumeKeepsPendingUserAuthWaitingWithoutDispatch(t *testing.T) {
	operationID := "op-user-auth"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationWaitingUserAuth,
		ResultSummaryJSON: lifecycleRecoverySummary(t, model.FeishuOperationWaitingUserAuth, "session-user-auth", model.FeishuAuthPhaseUserAuth, RecoveryUserScope),
	}
	svc, _, workspace, _, dispatcher, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)
	workspace.activeSession = &model.FeishuAuthSession{
		ID: "session-user-auth", UserID: 7, Generation: 2, OperationID: &operationID,
		Phase: model.FeishuAuthPhaseUserAuth, State: model.FeishuAuthSessionPending,
		ExpiresAt: time.Now().Add(time.Minute),
	}

	result, err := svc.Resume(context.Background(), 7, operationID, ResumeActionUserCompleted)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, result.State)
	require.Nil(t, result.Action, "a pending session must not reconstruct or persist an authorization URL")
	require.Zero(t, dispatcher.calls, "only the completed auth worker may resume the original operation")
}

func TestWorkspaceLifecycleResumeRejectsUnrelatedAppScopeSession(t *testing.T) {
	operationID := "op-app-scope"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationWaitingAppScope,
		ResultSummaryJSON: lifecycleRecoverySummary(t, model.FeishuOperationWaitingAppScope, "session-owned", model.FeishuAuthPhaseAppScope, RecoveryAppScope),
	}
	svc, _, workspace, auth, dispatcher, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)
	otherOperationID := "op-other"
	workspace.activeSession = &model.FeishuAuthSession{
		ID: "session-other", UserID: 7, Generation: 2, OperationID: &otherOperationID,
		Phase: model.FeishuAuthPhaseAppScope, State: model.FeishuAuthSessionPending,
	}

	_, err := svc.Resume(context.Background(), 7, operationID, ResumeActionUserCompleted)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.Empty(t, auth.completeAppApprovalCalls)
	require.Zero(t, dispatcher.calls)
}

func lifecycleRecoverySummary(t *testing.T, status, sessionID, phase string, kind RecoveryKind) []byte {
	t.Helper()
	encoded, err := json.Marshal(persistedOperationSummary{
		Status: status, SessionID: sessionID, Phase: phase, RecoveryKind: kind,
	})
	require.NoError(t, err)
	return encoded
}

func TestWorkspaceLifecycleResumeUserCompletedRequiresRecoverableWait(t *testing.T) {
	op := &model.FeishuOperation{ID: "op-1", UserID: 7, Generation: 2, State: model.FeishuOperationNotStarted}
	svc, _, _, _, dispatcher, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 2}, op)

	_, err := svc.Resume(context.Background(), 7, "op-1", ResumeActionUserCompleted)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleInvalid)
	require.Zero(t, dispatcher.calls, "an arbitrary operation must not be resumed by a browser acknowledgement")
}

func TestWorkspaceLifecycleResumeConfirmationActionsRequireWaitingConfirmation(t *testing.T) {
	op := &model.FeishuOperation{ID: "op-1", UserID: 7, Generation: 2, State: model.FeishuOperationWaitingUserAuth}
	svc, _, _, _, dispatcher, operations, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 2}, op)

	_, err := svc.Resume(context.Background(), 7, "op-1", ResumeActionConfirmed)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleInvalid)
	require.Zero(t, dispatcher.calls)
	require.Zero(t, operations.confirmed)

	op.State = model.FeishuOperationWaitingConfirmation
	result, err := svc.Resume(context.Background(), 7, "op-1", ResumeActionConfirmed)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationExecuting, result.State)
	require.Equal(t, 1, operations.confirmed)
}

func TestWorkspaceLifecycleResumeCollapsesCrossUserToNotFound(t *testing.T) {
	svc, _, workspace, _, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 8, Provider: ProviderLark, Generation: 1}, nil)
	workspace.operationErr = gorm.ErrRecordNotFound

	_, err := svc.Resume(context.Background(), 8, "op-owned-by-7", ResumeActionUserCompleted)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
}

func TestWorkspaceLifecycleRefreshUsesCurrentGenerationAndReturnsNewLiveAction(t *testing.T) {
	svc, _, _, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 9}, nil)

	action, err := svc.RefreshAction(context.Background(), 7, "session-1")
	require.NoError(t, err)
	require.Equal(t, "session-1", action.SessionID)
	require.Equal(t, 1, auth.refreshCalls)
}

func TestWorkspaceLifecycleResumeAndRefreshMapAccountStoreFailureToUnavailable(t *testing.T) {
	svc, accounts, _, _, dispatcher, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, &model.FeishuOperation{ID: "op-1", UserID: 7, Generation: 2, State: model.FeishuOperationWaitingUserAuth})
	accounts.getErr = errors.New("simulated account store outage")

	_, err := svc.Resume(context.Background(), 7, "op-1", ResumeActionUserCompleted)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.Zero(t, dispatcher.calls)

	_, err = svc.RefreshAction(context.Background(), 7, "session-1")
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
}

func TestWorkspaceLifecycleUnbindRetiresGenerationStopsWorkersDeletesVaultAndKeepsRemoteApp(t *testing.T) {
	svc, accounts, workspace, auth, _, _, teardown := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 4, AppID: "cli_keep_remote", Connected: true,
		ConnectionState: model.FeishuConnectionConnected,
	}, nil)

	result, err := svc.Unbind(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionNone, result.State)
	require.False(t, result.Connected)
	require.Contains(t, result.Message, "飞书侧个人自建应用仍保留")
	require.Equal(t, 1, accounts.retireCalls)
	require.Equal(t, 1, accounts.finalizeCalls)
	require.Equal(t, 1, workspace.deleteVaultCalls)
	require.Equal(t, []uint64{4}, auth.stopped)
	require.Equal(t, []uint64{4}, teardown.calls)
}

func TestWorkspaceLifecycleUnbindWaitsForWorkerExitBeforeDeletingRetiredVault(t *testing.T) {
	svc, accounts, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 4, Connected: true,
		ConnectionState: model.FeishuConnectionConnected,
	}, nil)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	auth.stopWait = func(ctx context.Context) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	resultCh := make(chan *UnbindResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := svc.Unbind(context.Background(), 7)
		resultCh <- result
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("unbind did not request a worker join")
	}
	require.Zero(t, workspace.deleteVaultCalls, "vault deletion must wait for worker/temp HOME cleanup")
	require.Zero(t, accounts.finalizeCalls, "unbind must not report none before worker exit")
	close(release)
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("unbind did not finish after worker exit")
	}
	result := <-resultCh
	require.Equal(t, model.FeishuConnectionNone, result.State)
	require.Equal(t, 1, workspace.deleteVaultCalls)
	require.Equal(t, 1, accounts.finalizeCalls)
}

func TestWorkspaceLifecycleUnbindWorkerJoinTimeoutLeavesDisconnectingForSafeRetry(t *testing.T) {
	svc, accounts, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 4, Connected: true,
		ConnectionState: model.FeishuConnectionConnected,
	}, nil)
	svc.cleanupTimeout = 25 * time.Millisecond
	auth.stopWait = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	_, err := svc.Unbind(context.Background(), 7)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.Equal(t, model.FeishuConnectionDisconnecting, accounts.account.ConnectionState)
	require.False(t, accounts.account.Connected)
	require.Zero(t, workspace.deleteVaultCalls)
	require.Zero(t, accounts.finalizeCalls)
}

func TestWorkspaceLifecycleUnbindLeavesRetiredGenerationDisconnectingWhenTeardownCannotCleanHome(t *testing.T) {
	svc, accounts, workspace, auth, _, _, teardown := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 4, Connected: true,
		ConnectionState: model.FeishuConnectionConnected,
	}, nil)
	teardown.err = errRetiredWorkspaceCleanup

	_, err := svc.Unbind(context.Background(), 7)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.Equal(t, model.FeishuConnectionDisconnecting, accounts.account.ConnectionState)
	require.False(t, accounts.account.Connected)
	require.Equal(t, uint64(5), accounts.account.Generation)
	require.Equal(t, uint64(4), accounts.retiredGeneration)
	require.Equal(t, []uint64{4}, auth.stopped)
	require.Equal(t, []uint64{4}, teardown.calls)
	require.Zero(t, workspace.deleteVaultCalls, "critical retired HOME cleanup failure must keep the encrypted vault for a safe retry")
	require.Zero(t, accounts.finalizeCalls, "a connection must not claim local deletion while a readable retired HOME may remain")

	teardown.err = nil
	_, err = svc.Unbind(context.Background(), 7)
	require.NoError(t, err, "retry reuses the same retired generation after teardown cleanup succeeds")
	require.Equal(t, []uint64{4, 4}, auth.stopped)
	require.Equal(t, []uint64{4, 4}, teardown.calls)
	require.Equal(t, 1, workspace.deleteVaultCalls)
	require.Equal(t, 1, accounts.finalizeCalls)
	require.Equal(t, model.FeishuConnectionNone, accounts.account.ConnectionState)
}

func TestNewWorkspaceLifecycleServiceRejectsIncompleteGraph(t *testing.T) {
	_, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrWorkspaceLifecycleNotFound))
}
