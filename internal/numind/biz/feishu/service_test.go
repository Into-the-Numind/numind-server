package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"numind-server/internal/numind/store"
	pkgcrypto "numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/externalaction"
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
	getSession       func(context.Context, uint, uint64, string) (*model.FeishuAuthSession, error)
	deleteVaultCalls int
	completeCalls    int
	renewTeardown    func(context.Context, uint, uint64, uint64, string, time.Time, time.Time) (bool, error)
	completeTeardown func(context.Context, uint, uint64, uint64, string, time.Time) (bool, error)
}

func (f *lifecycleWorkspaceFake) ListTerminalOperationsForGeneration(_ context.Context, userID uint, generation uint64, states []string) ([]model.FeishuOperation, error) {
	if f.operationErr != nil {
		return nil, f.operationErr
	}
	if f.operation == nil {
		return nil, nil
	}
	for _, state := range states {
		if f.operation.UserID == userID && f.operation.Generation == generation && f.operation.State == state {
			return []model.FeishuOperation{*f.operation}, nil
		}
	}
	return nil, nil
}

func (f *lifecycleWorkspaceFake) ClaimRetiredTeardown(_ context.Context, _ uint, _ uint64, _ uint64, _ string, _, _ time.Time) (bool, error) {
	return true, nil
}

func (f *lifecycleWorkspaceFake) RenewRetiredTeardown(ctx context.Context, userID uint, retiredGeneration, disconnectingGeneration uint64, owner string, now, leaseUntil time.Time) (bool, error) {
	if f.renewTeardown != nil {
		return f.renewTeardown(ctx, userID, retiredGeneration, disconnectingGeneration, owner, now, leaseUntil)
	}
	return true, nil
}

func (f *lifecycleWorkspaceFake) ReleaseRetiredTeardown(_ context.Context, _ uint, _ uint64, _ string, _ time.Time) error {
	return nil
}

func (f *lifecycleWorkspaceFake) CompleteRetiredTeardown(ctx context.Context, userID uint, retiredGeneration, disconnectingGeneration uint64, owner string, now time.Time) (bool, error) {
	f.completeCalls++
	if f.completeTeardown != nil {
		return f.completeTeardown(ctx, userID, retiredGeneration, disconnectingGeneration, owner, now)
	}
	f.deleteVaultCalls++
	return true, nil
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

func (f *lifecycleWorkspaceFake) GetSessionForUser(ctx context.Context, userID uint, generation uint64, sessionID string) (*model.FeishuAuthSession, error) {
	if f.getSession != nil {
		return f.getSession(ctx, userID, generation, sessionID)
	}
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
	refreshErr               error
	refresh                  func()
	refreshOperation         func(context.Context, uint, uint64, string, string, string, []byte) (*OperationAction, error)
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
	if f.refresh != nil {
		f.refresh()
	}
	return cloneOperationAction(f.action), f.refreshErr
}

func (f *lifecycleAuthFake) RefreshOperationAction(ctx context.Context, userID uint, generation uint64, sessionID, operationID, waitingState string, summary []byte) (*OperationAction, error) {
	f.refreshCalls++
	if f.refreshOperation != nil {
		return f.refreshOperation(ctx, userID, generation, sessionID, operationID, waitingState, summary)
	}
	if f.refresh != nil {
		f.refresh()
	}
	return cloneOperationAction(f.action), f.refreshErr
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

type lifecycleDispatcherFake struct {
	calls   int
	results []error
}

func (f *lifecycleDispatcherFake) DispatchResume(_ context.Context, _ uint, _ string) error {
	f.calls++
	if len(f.results) > 0 {
		err := f.results[0]
		f.results = f.results[1:]
		return err
	}
	return nil
}

type lifecycleOperationsFake struct {
	confirmed int
	cancelled int
	confirm   func(context.Context, uint, string) (*OperationResult, error)
	cancel    func(context.Context, uint, string) (*OperationResult, error)
}

type lifecycleExecutionsFake struct {
	stopped  []uint64
	stopWait func(context.Context) error
}

type lifecycleAgentWaitCall struct {
	userID      uint
	runID       uint64
	operationID string
	toolCallID  string
	outcome     externalaction.TerminalOutcome
}

type lifecycleAgentWaitFake struct {
	calls    []lifecycleAgentWaitCall
	finalize func(context.Context, uint, uint64, string, string, externalaction.TerminalOutcome) (bool, error)
}

func (f *lifecycleAgentWaitFake) FinalizeExternalToolWait(ctx context.Context, userID uint, runID uint64, operationID, toolCallID string, outcome externalaction.TerminalOutcome) (bool, error) {
	f.calls = append(f.calls, lifecycleAgentWaitCall{
		userID: userID, runID: runID, operationID: operationID, toolCallID: toolCallID, outcome: outcome,
	})
	if f.finalize != nil {
		return f.finalize(ctx, userID, runID, operationID, toolCallID, outcome)
	}
	return true, nil
}

func (f *lifecycleExecutionsFake) StopGenerationAndWait(ctx context.Context, _ uint, generation uint64) error {
	f.stopped = append(f.stopped, generation)
	if f.stopWait != nil {
		return f.stopWait(ctx)
	}
	return nil
}

func (f *lifecycleOperationsFake) Confirm(ctx context.Context, userID uint, id string) (*OperationResult, error) {
	f.confirmed++
	if f.confirm != nil {
		return f.confirm(ctx, userID, id)
	}
	return &OperationResult{OperationID: id, State: model.FeishuOperationExecuting}, nil
}

func (f *lifecycleOperationsFake) Cancel(ctx context.Context, userID uint, id string) (*OperationResult, error) {
	f.cancelled++
	if f.cancel != nil {
		return f.cancel(ctx, userID, id)
	}
	return &OperationResult{OperationID: id, State: model.FeishuOperationCancelled}, nil
}

type lifecycleTeardownFake struct {
	calls []uint64
	err   error
}

type lifecycleTeardownBarrier struct {
	entered chan struct{}
	release <-chan struct{}
	mu      sync.Mutex
	calls   int
	err     error
}

func (t *lifecycleTeardownBarrier) LogoutRetired(ctx context.Context, _ uint, _ uint64) (RetiredWorkspaceTeardownResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	select {
	case t.entered <- struct{}{}:
	default:
	}
	select {
	case <-t.release:
		return RetiredWorkspaceTeardownResult{LogoutAttempted: true, LogoutSucceeded: t.err == nil}, t.err
	case <-ctx.Done():
		return RetiredWorkspaceTeardownResult{}, ctx.Err()
	}
}

func (t *lifecycleTeardownBarrier) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.calls }

func (f *lifecycleTeardownFake) LogoutRetired(_ context.Context, _ uint, generation uint64) (RetiredWorkspaceTeardownResult, error) {
	f.calls = append(f.calls, generation)
	return RetiredWorkspaceTeardownResult{LogoutAttempted: true, LogoutSucceeded: f.err == nil}, f.err
}

// lifecycleUncooperativeTeardown models a misbehaving child cleanup that
// ignores cancellation until its underlying operation eventually returns. The
// lifecycle lease must expire at the application cleanup deadline, so another
// service can safely take over while this stale owner is still blocked; when it
// returns, its owner-fenced checkpoint must prevent a stale completion.
type lifecycleUncooperativeTeardown struct {
	entered     chan struct{}
	deadlineHit chan struct{}
	release     <-chan struct{}
	mu          sync.Mutex
	calls       int
}

func (t *lifecycleUncooperativeTeardown) LogoutRetired(ctx context.Context, _ uint, _ uint64) (RetiredWorkspaceTeardownResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	select {
	case t.entered <- struct{}{}:
	default:
	}
	if t.deadlineHit != nil {
		go func() {
			<-ctx.Done()
			select {
			case t.deadlineHit <- struct{}{}:
			default:
			}
		}()
	}
	<-t.release
	return RetiredWorkspaceTeardownResult{LogoutAttempted: true, LogoutSucceeded: true}, nil
}

// lifecycleTrackingWorkspace records the irreversible delete boundary while
// delegating all tenant-fenced reads to the real workspace store.
type lifecycleTrackingWorkspace struct {
	store.IFeishuWorkspaceStore

	mu               sync.Mutex
	deleteVaultCalls int
}

func (s *lifecycleTrackingWorkspace) DeleteVault(ctx context.Context, userID uint, generation uint64) error {
	s.mu.Lock()
	s.deleteVaultCalls++
	s.mu.Unlock()
	return s.IFeishuWorkspaceStore.DeleteVault(ctx, userID, generation)
}

func (s *lifecycleTrackingWorkspace) CompleteRetiredTeardown(ctx context.Context, userID uint, retiredGeneration, disconnectingGeneration uint64, owner string, now time.Time) (bool, error) {
	completed, err := s.IFeishuWorkspaceStore.CompleteRetiredTeardown(ctx, userID, retiredGeneration, disconnectingGeneration, owner, now)
	if completed {
		s.mu.Lock()
		s.deleteVaultCalls++
		s.mu.Unlock()
	}
	return completed, err
}

func (s *lifecycleTrackingWorkspace) deleteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteVaultCalls
}

// lifecycleCompletionWorkspace keeps the real operation-list query in tests
// while making the final local-vault concern irrelevant to an Agent-wait
// assertion. Production CompleteRetiredTeardown is separately tested for its
// atomic vault/finalization fence.
type lifecycleCompletionWorkspace struct {
	store.IFeishuWorkspaceStore
	complete func(context.Context, uint, uint64) error
}

func (*lifecycleCompletionWorkspace) ClaimRetiredTeardown(context.Context, uint, uint64, uint64, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (*lifecycleCompletionWorkspace) RenewRetiredTeardown(context.Context, uint, uint64, uint64, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (*lifecycleCompletionWorkspace) ReleaseRetiredTeardown(context.Context, uint, uint64, string, time.Time) error {
	return nil
}

func (s *lifecycleCompletionWorkspace) CompleteRetiredTeardown(ctx context.Context, userID uint, _ uint64, disconnectingGeneration uint64, _ string, _ time.Time) (bool, error) {
	if s.complete == nil {
		return false, errors.New("test completion is not configured")
	}
	return true, s.complete(ctx, userID, disconnectingGeneration)
}

// lifecycleBlockingRunner represents a controlled child process that has seen
// cancellation but has not yet exited. It lets the test prove Unbind waits for
// WithHome's deferred temporary-HOME removal instead of only observing a
// context cancellation request.
type lifecycleBlockingRunner struct {
	started   chan struct{}
	cancelled chan struct{}
	release   <-chan struct{}

	mu    sync.Mutex
	home  string
	calls int
}

func (r *lifecycleBlockingRunner) Run(ctx context.Context, home string, _ []string, _ []byte) (*CLIResult, error) {
	r.mu.Lock()
	r.calls++
	r.home = home
	r.mu.Unlock()
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case r.cancelled <- struct{}{}:
	default:
	}
	<-r.release
	return &CLIResult{InvocationStarted: true, ExitCode: -1}, ctx.Err()
}

func (r *lifecycleBlockingRunner) snapshot() (home string, calls int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.home, r.calls
}

func newLifecycleService(t *testing.T, account *model.UserThirdPartyAccount, operation *model.FeishuOperation) (*WorkspaceLifecycleService, *lifecycleAccountFake, *lifecycleWorkspaceFake, *lifecycleAuthFake, *lifecycleDispatcherFake, *lifecycleOperationsFake, *lifecycleTeardownFake) {
	t.Helper()
	accounts := &lifecycleAccountFake{account: account}
	workspace := &lifecycleWorkspaceFake{operation: operation}
	auth := &lifecycleAuthFake{action: &OperationAction{Provider: ProviderLark, SessionID: "session-1", Phase: model.FeishuAuthPhaseUserAuth, URL: "https://open.feishu.cn/suite/passport/oauth/device", ExpiresAt: time.Now().Add(time.Minute)}}
	dispatcher := &lifecycleDispatcherFake{}
	operations := &lifecycleOperationsFake{}
	executions := &lifecycleExecutionsFake{}
	agentWaits := &lifecycleAgentWaitFake{}
	teardown := &lifecycleTeardownFake{}
	svc, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{
		Accounts: accounts, Workspace: workspace, Auth: auth, Dispatcher: dispatcher, Operations: operations,
		Executions: executions, AgentWaits: agentWaits, Teardown: teardown,
	})
	require.NoError(t, err)
	workspace.completeTeardown = func(_ context.Context, _ uint, _ uint64, disconnectingGeneration uint64, _ string, _ time.Time) (bool, error) {
		workspace.deleteVaultCalls++
		return true, accounts.FinalizeDisconnect(context.Background(), 7, ProviderLark, disconnectingGeneration)
	}
	return svc, accounts, workspace, auth, dispatcher, operations, teardown
}

// seedLifecycleExternalWait uses the production Agent-run store so lifecycle
// tests exercise the actual cross-store terminalization contract rather than
// only a hand-written interface fake.
func seedLifecycleExternalWait(t *testing.T, h *operationHarness, userID uint, operationID, toolCallID string) (store.IAgentRunStore, uint64) {
	t.Helper()
	// Match the Agent-store SQLite test DDL. AutoMigrate emits datetime(3),
	// which SQLite returns as text and cannot scan into AgentRun.StartedAt.
	require.NoError(t, h.db.Exec(`
		CREATE TABLE agent_run (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'running',
			state_reason TEXT NOT NULL DEFAULT '',
			terminal_metadata TEXT,
			messages TEXT NOT NULL DEFAULT '[]',
			reservation_id INTEGER,
			started_at DATETIME NOT NULL,
			ended_at DATETIME,
			cancellation_requested_at DATETIME,
			agent_definition_id INTEGER NOT NULL DEFAULT 0,
			pending_question_json TEXT,
			pending_question_at DATETIME,
			pending_external_action_json TEXT,
			pending_external_action_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME,
			use_compact_v2 INTEGER NOT NULL DEFAULT 0,
			is_pinned INTEGER NOT NULL DEFAULT 0,
			session_name TEXT NOT NULL DEFAULT '',
			is_deleted INTEGER NOT NULL DEFAULT 0,
			is_test INTEGER NOT NULL DEFAULT 0
		)`).Error)
	runStore := h.dataStore.AgentRuns()
	run := &model.AgentRun{
		UserID: userID, SessionID: "lifecycle-external-wait", Status: "terminated",
		StateReason: "waiting_for_user_choice", Messages: []byte(`[{"role":"user","content":"写入飞书"}]`),
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	writer, ok := runStore.(store.IExternalActionWriter)
	require.True(t, ok, "production Agent store must accept a durable external wait")
	payload := []byte(`{"provider":"feishu","operation_id":"` + operationID + `","session_id":"auth-lifecycle","tool_call_id":"` + toolCallID + `","phase":"user_auth","expires_at":"2027-01-01T00:00:00Z"}`)
	require.NoError(t, writer.UpdatePendingExternalAction(context.Background(), run.ID, payload))
	return runStore, run.ID
}

func TestWorkspaceLifecycleUnbindTerminalizesOnlyItsLinkedAgentWait(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		outcome    externalaction.TerminalOutcome
		resultCode string
		linked     bool
	}{
		{name: "pending operation becomes cancelled", state: model.FeishuOperationWaitingConfirmation, outcome: externalaction.TerminalOutcomeCancelled, resultCode: "feishu_operation_cancelled", linked: true},
		{name: "executing operation becomes unknown", state: model.FeishuOperationExecuting, outcome: externalaction.TerminalOutcomeUnknown, resultCode: "feishu_operation_result_unknown", linked: true},
		{name: "manual operation without agent link is safe", state: model.FeishuOperationWaitingConfirmation, linked: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 4, "cli-existing")
			operationID := "op-lifecycle-" + strconv.Itoa(len(tc.name))
			toolCallID := "tool-lifecycle-" + strconv.Itoa(len(tc.name))
			var runStore store.IAgentRunStore
			var runID uint64
			if tc.linked {
				runStore, runID = seedLifecycleExternalWait(t, h, 7, operationID, toolCallID)
			} else {
				toolCallID = ""
			}
			op := &model.FeishuOperation{
				ID: operationID, UserID: 7, Generation: 4, State: tc.state,
				AgentRunID: runID, ToolCallID: toolCallID,
				IdempotencyKey: operationID + ":key", CommandPath: "docs +create", Domain: "docs", RiskLevel: "write",
				RequestCiphertext: []byte("cipher"), KeyVersion: "v1", RequestFingerprint: operationID + "-fingerprint",
			}
			require.NoError(t, h.db.Create(op).Error)
			waits := &lifecycleAgentWaitFake{}
			if tc.linked {
				finalizer, ok := runStore.(store.IExternalToolWaitFinalizer)
				require.True(t, ok)
				waits.finalize = finalizer.FinalizeExternalToolWait
			}
			dispatcher := &lifecycleDispatcherFake{}
			operations := &lifecycleOperationsFake{}
			workspace := &lifecycleCompletionWorkspace{IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace()}
			workspace.complete = func(ctx context.Context, userID uint, disconnectingGeneration uint64) error {
				return h.dataStore.ThirdPartyAccounts().FinalizeDisconnect(ctx, userID, ProviderLark, disconnectingGeneration)
			}
			svc, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{
				Accounts: h.dataStore.ThirdPartyAccounts(), Workspace: workspace,
				Auth: &lifecycleAuthFake{}, Dispatcher: dispatcher, Operations: operations,
				Executions: &lifecycleExecutionsFake{}, AgentWaits: waits, Teardown: &lifecycleTeardownFake{},
			})
			require.NoError(t, err)

			result, err := svc.Unbind(context.Background(), 7)
			require.NoError(t, err)
			require.Equal(t, model.FeishuConnectionNone, result.State)
			require.Zero(t, dispatcher.calls, "terminal cancellation must never start a Task11 continuation")
			require.Zero(t, operations.cancelled, "unbind retires durable operations; it must not call the interactive cancel path")

			var storedOperation model.FeishuOperation
			require.NoError(t, h.db.Where("id = ?", operationID).Take(&storedOperation).Error)
			if tc.state == model.FeishuOperationExecuting {
				require.Equal(t, model.FeishuOperationUnknown, storedOperation.State)
			} else {
				require.Equal(t, model.FeishuOperationCancelled, storedOperation.State)
			}
			if !tc.linked {
				require.Empty(t, waits.calls, "a legacy/manual operation must not scan or modify Agent runs")
				return
			}

			require.Len(t, waits.calls, 1)
			require.Equal(t, tc.outcome, waits.calls[0].outcome)
			run, getErr := runStore.Get(context.Background(), runID)
			require.NoError(t, getErr)
			require.Equal(t, "terminated", run.Status)
			require.Equal(t, "aborted_tools", run.StateReason)
			require.Empty(t, run.PendingExternalActionJSON)
			require.NotNil(t, run.CancellationRequestedAt)
			var turns []json.RawMessage
			require.NoError(t, json.Unmarshal(run.Messages, &turns))
			require.Len(t, turns, 2, "retries/races must not append a synthetic user or second tool turn")
			var terminalTurn struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			}
			require.NoError(t, json.Unmarshal(turns[1], &terminalTurn))
			require.Equal(t, "tool", terminalTurn.Role)
			require.Equal(t, toolCallID, terminalTurn.ToolCallID)
			require.Contains(t, terminalTurn.Content, tc.resultCode)

			// A completed disconnect cannot re-run terminalization or any Feishu
			// command when the user repeats DELETE.
			_, err = svc.Unbind(context.Background(), 7)
			require.NoError(t, err)
			require.Len(t, waits.calls, 1)
		})
	}
}

func TestWorkspaceLifecycleStatusIsReadOnlyAndNeverReturnsAuthorizationURL(t *testing.T) {
	appID := "cli_12345678"
	svc, _, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, AppID: appID, Generation: 3,
		ConnectionState:     model.FeishuConnectionWaitingUserAuth,
		LarkCLIVersion:      LarkCLIVersion,
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
	require.Equal(t, LarkCLIVersion, status.CLIVersion)
	require.Equal(t, model.FeishuCapabilityAvailable, status.Capabilities["docs"].State)
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

func TestWorkspaceLifecycleResumeConfirmedRetriesSucceededContinuationWithoutReexecutingOperation(t *testing.T) {
	const operationID = "op-confirmed-continuation"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationWaitingConfirmation,
		AgentRunID: 41, ToolCallID: "tool-confirmed-continuation",
	}
	svc, _, workspace, _, dispatcher, operations, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)
	// This simulates a Task11 handoff failure before it can durably claim the
	// continuation. The operation itself has already committed success, so a
	// retry must only compensate through the existing shared dispatcher.
	dispatcher.results = []error{errors.New("task11 continuation was not claimed"), nil, nil}
	operations.confirm = func(_ context.Context, userID uint, id string) (*OperationResult, error) {
		require.Equal(t, uint(7), userID)
		require.Equal(t, operationID, id)
		workspace.operation.State = model.FeishuOperationSucceeded
		return &OperationResult{OperationID: id, State: model.FeishuOperationSucceeded}, nil
	}

	result, err := svc.Resume(context.Background(), 7, operationID, ResumeActionConfirmed)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.Equal(t, 1, operations.confirmed)
	require.Equal(t, 1, dispatcher.calls)
	require.Equal(t, model.FeishuOperationSucceeded, workspace.operation.State)

	result, err = svc.Resume(context.Background(), 7, operationID, ResumeActionConfirmed)
	require.NoError(t, err)
	require.Equal(t, &OperationResult{OperationID: operationID, State: model.FeishuOperationSucceeded}, result)
	require.Equal(t, 1, operations.confirmed, "retry must not execute the confirmed Feishu operation again")
	require.Equal(t, 2, dispatcher.calls, "retry must use the same Task12 dispatcher for durable compensation")

	result, err = svc.Resume(context.Background(), 7, operationID, ResumeActionConfirmed)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	require.Equal(t, 1, operations.confirmed, "repeated confirmation remains operation-idempotent")
	require.Equal(t, 3, dispatcher.calls, "Task11 owns durable continuation idempotence across compensation attempts")
}

func TestWorkspaceLifecycleResumeUserCompletedRetriesSucceededContinuation(t *testing.T) {
	const operationID = "op-user-completed-continuation"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationSucceeded,
		AgentRunID: 42, ToolCallID: "tool-user-completed-continuation",
	}
	svc, _, _, _, dispatcher, operations, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)
	dispatcher.results = []error{errors.New("task11 continuation was not claimed"), nil}

	result, err := svc.Resume(context.Background(), 7, operationID, ResumeActionUserCompleted)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.Equal(t, 1, dispatcher.calls)
	require.Zero(t, operations.confirmed)
	require.Zero(t, operations.cancelled)

	result, err = svc.Resume(context.Background(), 7, operationID, ResumeActionUserCompleted)
	require.NoError(t, err)
	require.Equal(t, &OperationResult{OperationID: operationID, State: model.FeishuOperationSucceeded}, result)
	require.Equal(t, 2, dispatcher.calls)
	require.Zero(t, operations.confirmed)
	require.Zero(t, operations.cancelled)
}

func TestWorkspaceLifecycleResumeSucceededOperationWithoutAgentContinuationOnlyReturnsSummary(t *testing.T) {
	op := &model.FeishuOperation{ID: "op-no-agent-continuation", UserID: 7, Generation: 2, State: model.FeishuOperationSucceeded}
	svc, _, _, _, dispatcher, operations, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)

	result, err := svc.Resume(context.Background(), 7, op.ID, ResumeActionUserCompleted)
	require.NoError(t, err)
	require.Equal(t, &OperationResult{OperationID: op.ID, State: model.FeishuOperationSucceeded}, result)
	require.Zero(t, dispatcher.calls, "an operation with no original tool call has no Agent continuation to dispatch")
	require.Zero(t, operations.confirmed)
}

func TestWorkspaceLifecycleResumeCancelledSucceededOperationOnlyReturnsSummary(t *testing.T) {
	op := &model.FeishuOperation{
		ID: "op-cancelled-after-success", UserID: 7, Generation: 2, State: model.FeishuOperationSucceeded,
		AgentRunID: 43, ToolCallID: "tool-cancelled-after-success",
	}
	svc, _, _, _, dispatcher, operations, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)

	result, err := svc.Resume(context.Background(), 7, op.ID, ResumeActionCancelled)
	require.NoError(t, err)
	require.Equal(t, &OperationResult{OperationID: op.ID, State: model.FeishuOperationSucceeded}, result)
	require.Zero(t, dispatcher.calls, "a cancellation acknowledgement must never restart a successful continuation")
	require.Zero(t, operations.cancelled, "a completed operation must not be cancelled after its write succeeded")
}

func TestWorkspaceLifecycleResumeCancelledRetriesTerminalAgentWaitWithoutReCancellingOperation(t *testing.T) {
	const operationID = "op-cancelled-terminal-wait-retry"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationWaitingConfirmation,
		AgentRunID: 44, ToolCallID: "tool-cancelled-terminal-wait-retry",
	}
	svc, _, workspace, _, dispatcher, operations, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)
	agentWaits := svc.agentWaits.(*lifecycleAgentWaitFake)
	agentWaits.finalize = func(_ context.Context, _ uint, _ uint64, _ string, _ string, _ externalaction.TerminalOutcome) (bool, error) {
		if len(agentWaits.calls) == 1 {
			return false, errors.New("agent wait store temporarily unavailable")
		}
		return true, nil
	}
	operations.cancel = func(_ context.Context, userID uint, id string) (*OperationResult, error) {
		require.Equal(t, uint(7), userID)
		require.Equal(t, operationID, id)
		workspace.operation.State = model.FeishuOperationCancelled
		return &OperationResult{OperationID: id, State: model.FeishuOperationCancelled}, nil
	}

	// Cancel has committed, but terminating the Agent's external wait has not.
	// The retry must compensate that exact wait instead of rejecting the now
	// terminal operation and leaving the frontend stuck in a running state.
	result, err := svc.Resume(context.Background(), 7, operationID, ResumeActionCancelled)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.Equal(t, model.FeishuOperationCancelled, workspace.operation.State)
	require.Equal(t, 1, operations.cancelled)
	require.Len(t, agentWaits.calls, 1)
	require.Equal(t, externalaction.TerminalOutcomeCancelled, agentWaits.calls[0].outcome)

	result, err = svc.Resume(context.Background(), 7, operationID, ResumeActionCancelled)
	require.NoError(t, err)
	require.Equal(t, &OperationResult{OperationID: operationID, State: model.FeishuOperationCancelled}, result)
	require.Equal(t, 1, operations.cancelled, "retry must not call Operation.Cancel or execute a CLI command again")
	require.Len(t, agentWaits.calls, 2)
	require.Zero(t, dispatcher.calls, "terminalization must never resume the Agent model continuation")

	// An already-unknown result is terminalized with its honest unknown result,
	// never rewritten as a cancelled or successful Feishu write.
	unknown := &model.FeishuOperation{
		ID: "op-unknown-terminal-wait-retry", UserID: 7, Generation: 2, State: model.FeishuOperationUnknown,
		AgentRunID: 45, ToolCallID: "tool-unknown-terminal-wait-retry",
	}
	unknownSvc, _, _, _, unknownDispatcher, unknownOperations, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, unknown)
	unknownWaits := unknownSvc.agentWaits.(*lifecycleAgentWaitFake)
	result, err = unknownSvc.Resume(context.Background(), 7, unknown.ID, ResumeActionCancelled)
	require.NoError(t, err)
	require.Equal(t, &OperationResult{OperationID: unknown.ID, State: model.FeishuOperationUnknown}, result)
	require.Len(t, unknownWaits.calls, 1)
	require.Equal(t, externalaction.TerminalOutcomeUnknown, unknownWaits.calls[0].outcome)
	require.Zero(t, unknownOperations.cancelled)
	require.Zero(t, unknownDispatcher.calls)
}

func TestWorkspaceLifecycleResumeCollapsesCrossUserToNotFound(t *testing.T) {
	svc, _, workspace, _, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 8, Provider: ProviderLark, Generation: 1}, nil)
	workspace.operationErr = gorm.ErrRecordNotFound

	_, err := svc.Resume(context.Background(), 8, "op-owned-by-7", ResumeActionUserCompleted)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
}

func TestWorkspaceLifecycleResumeMapsOperationReadFailureToUnavailableWithoutDispatch(t *testing.T) {
	svc, _, workspace, _, dispatcher, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, nil)
	workspace.operationErr = errors.New("simulated operation store outage")

	result, err := svc.Resume(context.Background(), 7, "op-1", ResumeActionUserCompleted)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.NotErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
	require.Zero(t, dispatcher.calls)
}

func TestWorkspaceLifecycleRefreshUsesCurrentGenerationAndReturnsNewLiveAction(t *testing.T) {
	svc, _, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 9}, nil)
	workspace.activeSession = &model.FeishuAuthSession{
		ID: "session-1", UserID: 7, Generation: 9, Phase: model.FeishuAuthPhaseUserAuth,
		State: model.FeishuAuthSessionPending,
	}

	action, err := svc.RefreshAction(context.Background(), 7, "session-1")
	require.NoError(t, err)
	require.Equal(t, "session-1", action.SessionID)
	require.Equal(t, 1, auth.refreshCalls)
}

func TestWorkspaceLifecycleRefreshRebindsOperationSessionBeforeResume(t *testing.T) {
	operationID := "op-refresh-create-app"
	const oldSessionID = "session-old"
	const replacementSessionID = "session-new"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationWaitingConnection,
		ResultSummaryJSON: lifecycleRecoverySummary(t, model.FeishuOperationWaitingConnection, oldSessionID, model.FeishuAuthPhaseCreateApp, RecoveryCreateApp),
	}
	svc, _, workspace, auth, dispatcher, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)
	oldSession := &model.FeishuAuthSession{
		ID: oldSessionID, UserID: 7, Generation: 2, OperationID: &operationID,
		Phase: model.FeishuAuthPhaseCreateApp, State: model.FeishuAuthSessionPending,
	}
	replacementExpiry := time.Now().Add(time.Minute).UTC()
	replacementSession := &model.FeishuAuthSession{
		ID: replacementSessionID, UserID: 7, Generation: 2, OperationID: &operationID,
		Phase: model.FeishuAuthPhaseCreateApp, State: model.FeishuAuthSessionPending, ExpiresAt: replacementExpiry,
	}
	refreshed := false
	workspace.getSession = func(_ context.Context, _ uint, _ uint64, sessionID string) (*model.FeishuAuthSession, error) {
		if !refreshed && sessionID == oldSessionID {
			copy := *oldSession
			return &copy, nil
		}
		if refreshed && sessionID == replacementSessionID {
			copy := *replacementSession
			return &copy, nil
		}
		return nil, gorm.ErrRecordNotFound
	}
	auth.action = &OperationAction{
		Provider: ProviderLark, SessionID: replacementSessionID, Phase: model.FeishuAuthPhaseCreateApp,
		URL: "https://open.feishu.cn/page/cli", ExpiresAt: replacementExpiry,
	}
	auth.refreshOperation = func(_ context.Context, _ uint, _ uint64, _ string, _ string, _ string, operationSummary []byte) (*OperationAction, error) {
		summary, err := decodeOperationSummary(operationSummary)
		require.NoError(t, err)
		summary.SessionID = replacementSessionID
		summary.ExpiresAt = &replacementExpiry
		replacementSummary, err := json.Marshal(summary)
		require.NoError(t, err)
		workspace.operation.ResultSummaryJSON = replacementSummary
		refreshed = true
		return cloneOperationAction(auth.action), nil
	}

	action, err := svc.RefreshAction(context.Background(), 7, oldSessionID)
	require.NoError(t, err)
	require.Equal(t, replacementSessionID, action.SessionID)
	refreshedSummary, summaryErr := decodeOperationSummary(workspace.operation.ResultSummaryJSON)
	require.NoError(t, summaryErr)
	require.Equal(t, replacementSessionID, refreshedSummary.SessionID)
	require.Equal(t, &replacementExpiry, refreshedSummary.ExpiresAt)

	result, err := svc.Resume(context.Background(), 7, operationID, ResumeActionUserCompleted)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, result.State)
	require.Zero(t, dispatcher.calls, "a pending replacement session must not dispatch the operation")
}

func TestWorkspaceLifecycleRefreshMapsAtomicRefreshFailureToUnavailable(t *testing.T) {
	operationID := "op-refresh-rebind-failure"
	oldSessionID := "session-old"
	op := &model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 2, State: model.FeishuOperationWaitingConnection,
		ResultSummaryJSON: lifecycleRecoverySummary(t, model.FeishuOperationWaitingConnection, oldSessionID, model.FeishuAuthPhaseCreateApp, RecoveryCreateApp),
	}
	svc, _, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 2,
	}, op)
	workspace.getSession = func(_ context.Context, _ uint, _ uint64, sessionID string) (*model.FeishuAuthSession, error) {
		if sessionID == oldSessionID {
			return &model.FeishuAuthSession{
				ID: oldSessionID, UserID: 7, Generation: 2, OperationID: &operationID,
				Phase: model.FeishuAuthPhaseCreateApp, State: model.FeishuAuthSessionPending,
			}, nil
		}
		return nil, gorm.ErrRecordNotFound
	}
	auth.refreshOperation = func(context.Context, uint, uint64, string, string, string, []byte) (*OperationAction, error) {
		return nil, errors.New("simulated durable rebind failure")
	}

	action, err := svc.RefreshAction(context.Background(), 7, oldSessionID)
	require.Nil(t, action)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	summary, summaryErr := decodeOperationSummary(workspace.operation.ResultSummaryJSON)
	require.NoError(t, summaryErr)
	require.Equal(t, oldSessionID, summary.SessionID, "a failed atomic refresh must preserve the old durable binding")
}

func TestWorkspaceLifecycleRefreshClassifiesSessionBeforeCallingAuth(t *testing.T) {
	validSession := func() *model.FeishuAuthSession {
		return &model.FeishuAuthSession{
			ID: "session-1", UserID: 7, Generation: 9, Phase: model.FeishuAuthPhaseUserAuth,
			State: model.FeishuAuthSessionPending,
		}
	}

	t.Run("missing session is uniformly not found", func(t *testing.T) {
		svc, _, _, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 9}, nil)

		action, err := svc.RefreshAction(context.Background(), 7, "session-1")
		require.Nil(t, action)
		require.ErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
		require.Zero(t, auth.refreshCalls)
	})

	t.Run("unowned session is uniformly not found", func(t *testing.T) {
		svc, _, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 9}, nil)
		workspace.getSession = func(context.Context, uint, uint64, string) (*model.FeishuAuthSession, error) {
			return &model.FeishuAuthSession{
				ID: "session-1", UserID: 8, Generation: 9, Phase: model.FeishuAuthPhaseUserAuth,
				State: model.FeishuAuthSessionPending,
			}, nil
		}

		action, err := svc.RefreshAction(context.Background(), 7, "session-1")
		require.Nil(t, action)
		require.ErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
		require.Zero(t, auth.refreshCalls)
	})

	t.Run("wrong generation session is uniformly not found", func(t *testing.T) {
		svc, _, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 9}, nil)
		workspace.getSession = func(context.Context, uint, uint64, string) (*model.FeishuAuthSession, error) {
			return &model.FeishuAuthSession{
				ID: "session-1", UserID: 7, Generation: 8, Phase: model.FeishuAuthPhaseUserAuth,
				State: model.FeishuAuthSessionPending,
			}, nil
		}

		action, err := svc.RefreshAction(context.Background(), 7, "session-1")
		require.Nil(t, action)
		require.ErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
		require.Zero(t, auth.refreshCalls)
	})

	t.Run("disconnecting account is uniformly not found", func(t *testing.T) {
		svc, _, _, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{
			UserID: 7, Provider: ProviderLark, Generation: 9, ConnectionState: model.FeishuConnectionDisconnecting,
		}, nil)

		action, err := svc.RefreshAction(context.Background(), 7, "session-1")
		require.Nil(t, action)
		require.ErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
		require.Zero(t, auth.refreshCalls)
	})

	t.Run("session store failure is unavailable", func(t *testing.T) {
		svc, _, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 9}, nil)
		workspace.activeSessionErr = errors.New("simulated session store outage")

		action, err := svc.RefreshAction(context.Background(), 7, "session-1")
		require.Nil(t, action)
		require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
		require.NotErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
		require.Zero(t, auth.refreshCalls)
	})

	t.Run("auth dependency failure is unavailable and returns no live action", func(t *testing.T) {
		svc, _, workspace, auth, _, _, _ := newLifecycleService(t, &model.UserThirdPartyAccount{UserID: 7, Provider: ProviderLark, Generation: 9}, nil)
		workspace.activeSession = validSession()
		auth.refreshErr = errors.New("simulated auth worker outage")

		action, err := svc.RefreshAction(context.Background(), 7, "session-1")
		require.Nil(t, action)
		require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
		require.NotErrorIs(t, err, ErrWorkspaceLifecycleNotFound)
		require.Equal(t, 1, auth.refreshCalls)
	})
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

func TestWorkspaceLifecycleUnbindConcurrentAndRepeatedCallsShareOneTeardown(t *testing.T) {
	svc, accounts, workspace, auth, _, _, teardown := newLifecycleService(t, &model.UserThirdPartyAccount{
		UserID: 7, Provider: ProviderLark, Generation: 4, Connected: true,
		ConnectionState: model.FeishuConnectionConnected,
	}, nil)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	auth.stopWait = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	type callResult struct {
		result *UnbindResult
		err    error
	}
	first := make(chan callResult, 1)
	second := make(chan callResult, 1)
	go func() {
		result, err := svc.Unbind(context.Background(), 7)
		first <- callResult{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first unbind did not become the cleanup owner")
	}
	go func() {
		result, err := svc.Unbind(context.Background(), 7)
		second <- callResult{result: result, err: err}
	}()
	select {
	case outcome := <-second:
		t.Fatalf("second unbind returned before the owner completed: %+v", outcome)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	for _, resultCh := range []<-chan callResult{first, second} {
		select {
		case outcome := <-resultCh:
			require.NoError(t, outcome.err)
			require.NotNil(t, outcome.result)
			require.Equal(t, model.FeishuConnectionNone, outcome.result.State)
		case <-time.After(time.Second):
			t.Fatal("concurrent unbind did not complete")
		}
	}
	require.Equal(t, 1, accounts.retireCalls)
	require.Equal(t, 1, accounts.finalizeCalls)
	require.Equal(t, 1, workspace.deleteVaultCalls)
	require.Equal(t, []uint64{4}, teardown.calls)

	// A successful local deletion is terminally idempotent: it must not create
	// a second retired generation or repeat destructive cleanup.
	repeated, err := svc.Unbind(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionNone, repeated.State)
	require.Equal(t, 1, accounts.retireCalls)
	require.Equal(t, 1, accounts.finalizeCalls)
	require.Equal(t, 1, workspace.deleteVaultCalls)
}

func TestWorkspaceLifecycleUnbindDurablySerializesTwoServiceInstances(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 4, "cli-existing")
	release := make(chan struct{})
	teardown := &lifecycleTeardownBarrier{entered: make(chan struct{}, 2), release: release}
	makeService := func() *WorkspaceLifecycleService {
		svc, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{Accounts: h.dataStore.ThirdPartyAccounts(), Workspace: h.dataStore.FeishuWorkspace(), Auth: &lifecycleAuthFake{}, Dispatcher: &lifecycleDispatcherFake{}, Operations: &lifecycleOperationsFake{}, Executions: &lifecycleExecutionsFake{}, AgentWaits: &lifecycleAgentWaitFake{}, Teardown: teardown})
		require.NoError(t, err)
		return svc
	}
	firstSvc, secondSvc := makeService(), makeService()
	type outcome struct {
		result *UnbindResult
		err    error
	}
	first, second := make(chan outcome, 1), make(chan outcome, 1)
	go func() { r, e := firstSvc.Unbind(context.Background(), 7); first <- outcome{r, e} }()
	select {
	case <-teardown.entered:
	case <-time.After(time.Second):
		t.Fatal("first instance did not acquire durable teardown lease")
	}
	go func() { r, e := secondSvc.Unbind(context.Background(), 7); second <- outcome{r, e} }()
	select {
	case got := <-second:
		t.Fatalf("loser returned before durable owner finalized: %+v", got)
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	for _, ch := range []<-chan outcome{first, second} {
		select {
		case got := <-ch:
			require.NoError(t, got.err)
			require.Equal(t, model.FeishuConnectionNone, got.result.State)
		case <-time.After(time.Second):
			t.Fatal("instance did not finish")
		}
	}
	require.Equal(t, 1, teardown.count(), "only the durable owner may logout/delete/finalize")
}

func TestWorkspaceLifecycleUnbindRenewsRetiredTeardownLeaseUntilOwnerFailure(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 4, "cli-existing")
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	close(secondRelease)
	firstTeardown := &lifecycleTeardownBarrier{
		entered: make(chan struct{}, 1), release: firstRelease, err: errRetiredWorkspaceCleanup,
	}
	secondTeardown := &lifecycleTeardownBarrier{
		entered: make(chan struct{}, 1), release: secondRelease,
	}
	makeService := func(teardown WorkspaceLifecycleTeardown) *WorkspaceLifecycleService {
		svc, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{
			Accounts: h.dataStore.ThirdPartyAccounts(), Workspace: h.dataStore.FeishuWorkspace(),
			Auth: &lifecycleAuthFake{}, Dispatcher: &lifecycleDispatcherFake{}, Operations: &lifecycleOperationsFake{},
			Executions: &lifecycleExecutionsFake{}, AgentWaits: &lifecycleAgentWaitFake{}, Teardown: teardown,
		})
		require.NoError(t, err)
		// Keep this test's total cleanup deadline materially longer than its
		// short, renewable teardown lease.
		svc.cleanupTimeout = 600 * time.Millisecond
		return svc
	}
	firstSvc, secondSvc := makeService(firstTeardown), makeService(secondTeardown)
	type outcome struct {
		result *UnbindResult
		err    error
	}
	first := make(chan outcome, 1)
	second := make(chan outcome, 1)
	go func() { r, e := firstSvc.Unbind(context.Background(), 7); first <- outcome{r, e} }()
	select {
	case <-firstTeardown.entered:
	case <-time.After(time.Second):
		t.Fatal("first instance did not acquire the retired teardown lease")
	}

	// This crosses multiple initial lease durations. A missing heartbeat would
	// let the second service claim and start a second local logout here.
	time.Sleep(firstSvc.retiredTeardownLeaseDuration() * 2)
	go func() { r, e := secondSvc.Unbind(context.Background(), 7); second <- outcome{r, e} }()
	select {
	case <-secondTeardown.entered:
		t.Fatal("second instance started retired HOME cleanup while first owner was renewing")
	case <-time.After(100 * time.Millisecond):
	}

	close(firstRelease)
	select {
	case got := <-first:
		require.Nil(t, got.result)
		require.ErrorIs(t, got.err, ErrWorkspaceLifecycleUnavailable)
	case <-time.After(time.Second):
		t.Fatal("failed teardown owner did not release its durable lease")
	}
	select {
	case <-secondTeardown.entered:
	case <-time.After(time.Second):
		t.Fatal("second instance did not take over after first owner failure")
	}
	select {
	case got := <-second:
		require.NoError(t, got.err)
		require.Equal(t, model.FeishuConnectionNone, got.result.State)
	case <-time.After(time.Second):
		t.Fatal("second instance did not complete after first owner released")
	}
	require.Equal(t, 1, secondTeardown.count(), "only the replacement owner may complete after failure")
}

func TestWorkspaceLifecycleUnbindLeaseExpiryPreventsStaleOwnerCompletion(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 4, "cli-existing")
	workspace := &lifecycleTrackingWorkspace{IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace()}
	firstRelease := make(chan struct{})
	var firstReleaseOnce sync.Once
	releaseFirst := func() { firstReleaseOnce.Do(func() { close(firstRelease) }) }
	secondRelease := make(chan struct{})
	close(secondRelease)
	firstTeardown := &lifecycleUncooperativeTeardown{entered: make(chan struct{}, 1), deadlineHit: make(chan struct{}, 1), release: firstRelease}
	secondTeardown := &lifecycleTeardownBarrier{entered: make(chan struct{}, 1), release: secondRelease}
	makeService := func(teardown WorkspaceLifecycleTeardown) *WorkspaceLifecycleService {
		svc, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{
			Accounts: h.dataStore.ThirdPartyAccounts(), Workspace: workspace,
			Auth: &lifecycleAuthFake{}, Dispatcher: &lifecycleDispatcherFake{}, Operations: &lifecycleOperationsFake{},
			Executions: &lifecycleExecutionsFake{}, AgentWaits: &lifecycleAgentWaitFake{}, Teardown: teardown,
		})
		require.NoError(t, err)
		svc.cleanupTimeout = 120 * time.Millisecond
		return svc
	}
	firstSvc, secondSvc := makeService(firstTeardown), makeService(secondTeardown)
	type outcome struct {
		result *UnbindResult
		err    error
	}
	first := make(chan outcome, 1)
	second := make(chan outcome, 1)
	firstFinished := make(chan struct{})
	go func() {
		defer close(firstFinished)
		r, e := firstSvc.Unbind(context.Background(), 7)
		first <- outcome{r, e}
	}()
	t.Cleanup(func() {
		releaseFirst()
		select {
		case <-firstFinished:
		case <-time.After(time.Second):
			t.Errorf("uncooperative teardown did not exit during test cleanup")
		}
	})
	select {
	case <-firstTeardown.entered:
	case <-time.After(time.Second):
		t.Fatal("first instance did not enter its controlled uncooperative cleanup")
	}

	// Simulate a crashed/stuck owner. The actual handoff is the observable
	// contract: after the first service's bounded cleanup deadline, the second
	// independently composed service must acquire the durable lease and finish.
	// Avoid polling the single-connection SQLite test DB while the heartbeat is
	// committing, because that only tests test-driver scheduling rather than
	// ownership semantics.
	select {
	case <-firstTeardown.deadlineHit:
	case <-time.After(time.Second):
		t.Fatal("stuck teardown did not receive the bounded cleanup deadline")
	}
	time.Sleep(firstSvc.retiredTeardownLeaseDuration() * 2)

	secondFinished := make(chan struct{})
	go func() {
		defer close(secondFinished)
		r, e := secondSvc.Unbind(context.Background(), 7)
		second <- outcome{r, e}
	}()
	t.Cleanup(func() {
		select {
		case <-secondFinished:
		case <-time.After(time.Second):
			t.Errorf("replacement teardown did not exit during test cleanup")
		}
	})
	select {
	case <-secondTeardown.entered:
	case <-time.After(time.Second):
		t.Fatal("replacement instance did not claim the expired teardown lease")
	}
	select {
	case got := <-second:
		require.NoError(t, got.err)
		require.Equal(t, model.FeishuConnectionNone, got.result.State)
	case <-time.After(time.Second):
		t.Fatal("replacement instance did not atomically delete/finalize")
	}
	require.Equal(t, 1, workspace.deleteCount(), "only the replacement owner may cross the atomic delete/finalize boundary")

	releaseFirst()
	select {
	case got := <-first:
		require.Nil(t, got.result)
		require.ErrorIs(t, got.err, ErrWorkspaceLifecycleUnavailable)
	case <-time.After(time.Second):
		t.Fatal("stale owner did not return after its child cleanup released")
	}
	require.Equal(t, 1, workspace.deleteCount(), "stale owner must not delete/finalize after lease loss")
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

func TestWorkspaceLifecycleUnbindJoinsExecutingCLIAndRuntimeHomeBeforeLocalDeletion(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 4, "cli_existing")
	runtimeBase := t.TempDir()
	vault, err := NewEncryptedCLIHomeVaultWithKeyring(
		h.dataStore.ThirdPartyAccounts(), h.dataStore.FeishuWorkspace(),
		map[string]*pkgcrypto.Cipher{"v1": h.cipher.ciphers["v1"]}, "v1", runtimeBase,
	)
	require.NoError(t, err)
	releaseRunner := make(chan struct{})
	runner := &lifecycleBlockingRunner{
		started: make(chan struct{}, 1), cancelled: make(chan struct{}, 1), release: releaseRunner,
	}
	operationService, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: vault, Runner: runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	workspace := &lifecycleTrackingWorkspace{IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace()}
	auth := &lifecycleAuthFake{}
	teardown := &lifecycleTeardownFake{}
	lifecycle, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Workspace: workspace, Auth: auth,
		Dispatcher: &lifecycleDispatcherFake{}, Operations: operationService,
		Executions: operationService, AgentWaits: &lifecycleAgentWaitFake{}, Teardown: teardown,
	})
	require.NoError(t, err)

	type executeResult struct {
		result *OperationResult
		err    error
	}
	executeDone := make(chan executeResult, 1)
	go func() {
		result, executeErr := operationService.Execute(context.Background(), operationDocsAppendRequest(
			7, 991, "unbind-executing", "doxcnABCDEFG123",
		))
		executeDone <- executeResult{result: result, err: executeErr}
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("operation did not start its controlled runner")
	}
	home, calls := runner.snapshot()
	require.Equal(t, 1, calls)
	require.NotEmpty(t, home)
	_, err = os.Stat(home)
	require.NoError(t, err, "real vault HOME must exist while controlled CLI is still running")

	type unbindResult struct {
		result *UnbindResult
		err    error
	}
	unbound := make(chan unbindResult, 1)
	go func() {
		result, unbindErr := lifecycle.Unbind(context.Background(), 7)
		unbound <- unbindResult{result: result, err: unbindErr}
	}()
	select {
	case <-runner.cancelled:
	case <-time.After(time.Second):
		t.Fatal("unbind did not cancel the registered execution")
	}
	require.Zero(t, workspace.deleteCount(), "encrypted vault deletion must wait for CLI exit and HOME cleanup")
	require.Zero(t, teardown.calls, "retired HOME teardown must not overlap the active runtime HOME")
	_, err = os.Stat(home)
	require.NoError(t, err, "runtime HOME must remain until the controlled CLI exits")
	select {
	case premature := <-unbound:
		t.Fatalf("unbind returned before execution cleanup: %#v", premature)
	default:
	}

	close(releaseRunner)
	select {
	case completed := <-unbound:
		require.NoError(t, completed.err)
		require.Equal(t, model.FeishuConnectionNone, completed.result.State)
	case <-time.After(time.Second):
		t.Fatal("unbind did not finish after controlled CLI cleanup")
	}
	select {
	case completed := <-executeDone:
		require.NoError(t, completed.err)
		require.Equal(t, model.FeishuOperationUnknown, completed.result.State)
	case <-time.After(time.Second):
		t.Fatal("execution did not exit after cancellation")
	}
	_, err = os.Stat(home)
	require.True(t, os.IsNotExist(err), "vault callback must remove the plaintext runtime HOME before unbind succeeds")
	require.Equal(t, 1, workspace.deleteCount())
	require.Equal(t, []uint64{4}, auth.stopped)
	require.Equal(t, []uint64{4}, teardown.calls)
	_, calls = runner.snapshot()
	require.Equal(t, 1, calls, "an interrupted write must close unknown instead of invoking CLI again")

	var stored model.FeishuOperation
	require.NoError(t, h.db.Where("user_id = ? AND idempotency_key = ?", 7, "991:unbind-executing").Take(&stored).Error)
	require.Equal(t, model.FeishuOperationUnknown, stored.State, "retire must keep an interrupted execution unknown")
}

func TestWorkspaceLifecycleUnbindWaitsForCrossInstanceExecutionGateOrLeavesDisconnecting(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 4, "cli_existing")
	now := h.service.now().UTC()
	claimed, err := h.dataStore.FeishuWorkspace().TryClaimExecutionGate(
		context.Background(), 7, 4, "other-instance", "remote-operation", now, now.Add(time.Hour),
	)
	require.NoError(t, err)
	require.True(t, claimed)

	workspace := &lifecycleTrackingWorkspace{IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace()}
	auth := &lifecycleAuthFake{}
	teardown := &lifecycleTeardownFake{}
	lifecycle, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Workspace: workspace, Auth: auth,
		Dispatcher: &lifecycleDispatcherFake{}, Operations: h.service, Executions: h.service, AgentWaits: &lifecycleAgentWaitFake{},
		Teardown: teardown,
	})
	require.NoError(t, err)
	lifecycle.cleanupTimeout = 25 * time.Millisecond

	result, err := lifecycle.Unbind(context.Background(), 7)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrWorkspaceLifecycleUnavailable)
	require.Zero(t, workspace.deleteCount(), "a remote active CLI gate must block local vault deletion")
	require.Empty(t, teardown.calls, "retired HOME cleanup cannot begin while another instance holds the gate")
	account, err := h.dataStore.ThirdPartyAccounts().Get(context.Background(), 7, ProviderLark)
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionDisconnecting, account.ConnectionState)
	require.Equal(t, uint64(5), account.Generation)
	require.Equal(t, []uint64{4}, auth.stopped)

	released, err := h.dataStore.FeishuWorkspace().ReleaseExecutionGate(context.Background(), 7, 4, "other-instance", now)
	require.NoError(t, err)
	require.True(t, released)
	result, err = lifecycle.Unbind(context.Background(), 7)
	require.NoError(t, err, "retry must reuse the same retired generation after the cross-instance gate drains")
	require.Equal(t, model.FeishuConnectionNone, result.State)
	require.Equal(t, 1, workspace.deleteCount())
	require.Equal(t, []uint64{4, 4}, auth.stopped)
	require.Equal(t, []uint64{4}, teardown.calls)
}

func TestNewWorkspaceLifecycleServiceRejectsIncompleteGraph(t *testing.T) {
	_, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrWorkspaceLifecycleNotFound))
}
