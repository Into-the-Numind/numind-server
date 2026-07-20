package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	pkgcrypto "numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"
)

type operationReceiptFake struct {
	mu      sync.Mutex
	err     error
	domains []string
	runs    []uint64
}

func (f *operationReceiptFake) VerifyRequired(_ []string, runID uint64, domain string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domains = append(f.domains, domain)
	f.runs = append(f.runs, runID)
	return f.err
}

func (f *operationReceiptFake) snapshot() ([]string, []uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.domains...), append([]uint64(nil), f.runs...)
}

type operationRecoveryFake struct {
	mu         sync.Mutex
	action     *OperationAction
	actions    []*OperationAction
	err        error
	calls      []RecoveryRequest
	activated  []string
	aborted    []string
	onActivate func(string) error
}

type reentrantOperationResumeDispatcher struct {
	mu                  sync.Mutex
	service             *FeishuOperationService
	active              bool
	calls               int
	nextDispatchBlock   <-chan struct{}
	nextDispatchEntered chan<- struct{}
}

func (d *reentrantOperationResumeDispatcher) DispatchResume(ctx context.Context, userID uint, operationID string) error {
	d.mu.Lock()
	d.calls++
	if d.active {
		d.mu.Unlock()
		return errors.New("recursive operation resume")
	}
	d.active = true
	block := d.nextDispatchBlock
	entered := d.nextDispatchEntered
	d.nextDispatchBlock = nil
	d.nextDispatchEntered = nil
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.active = false
		d.mu.Unlock()
	}()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.mu.Lock()
	service := d.service
	d.mu.Unlock()
	if service == nil {
		return errors.New("operation service unavailable")
	}
	_, err := service.Resume(ctx, userID, operationID)
	return err
}

// blockNextDispatch stops one already-durable phase completion immediately
// before it resumes the operation. Integration tests use this as the precise
// process-restart boundary: the old worker has committed its terminal state,
// then a fresh process constructs its services before processing the replay.
func (d *reentrantOperationResumeDispatcher) blockNextDispatch(block <-chan struct{}) <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	entered := make(chan struct{}, 1)
	d.nextDispatchBlock = block
	d.nextDispatchEntered = entered
	return entered
}

func (d *reentrantOperationResumeDispatcher) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (f *operationRecoveryFake) StartRecovery(_ context.Context, req RecoveryRequest) (*OperationAction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cloneTestRecoveryRequest(req))
	if len(f.actions) > 0 {
		action := f.actions[0]
		f.actions = f.actions[1:]
		return cloneTestOperationAction(action), f.err
	}
	return cloneTestOperationAction(f.action), f.err
}

func (f *operationRecoveryFake) snapshot() []RecoveryRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]RecoveryRequest, len(f.calls))
	for index := range f.calls {
		result[index] = cloneTestRecoveryRequest(f.calls[index])
	}
	return result
}

func (f *operationRecoveryFake) Activate(_ context.Context, sessionID string) error {
	f.mu.Lock()
	f.activated = append(f.activated, sessionID)
	onActivate := f.onActivate
	f.mu.Unlock()
	if onActivate != nil {
		return onActivate(sessionID)
	}
	return nil
}

func (f *operationRecoveryFake) Abort(sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted = append(f.aborted, sessionID)
}

func (f *operationRecoveryFake) activationSnapshot() (activated, aborted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.activated...), append([]string(nil), f.aborted...)
}

type operationConfirmationFake struct {
	mu     sync.Mutex
	action *OperationAction
	err    error
	calls  []ConfirmationSummary
}

func (f *operationConfirmationFake) RequestConfirmation(_ context.Context, _ string, summary ConfirmationSummary) (*OperationAction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, summary)
	return cloneTestOperationAction(f.action), f.err
}

type operationRunnerStep struct {
	result  *CLIResult
	err     error
	started chan struct{}
	release <-chan struct{}
}

type operationRunnerFake struct {
	mu         sync.Mutex
	steps      []operationRunnerStep
	calls      int
	argv       [][]string
	stdin      [][]byte
	authStatus int
}

type operationScopePreflightStep struct {
	result *ScopeCheckResult
	err    error
}

// operationScopePreflightFake grants every requested catalog scope by default.
// Tests can script missing scopes or protocol failures without changing the
// business runner fixture.
type operationScopePreflightFake struct {
	mu     sync.Mutex
	steps  []operationScopePreflightStep
	calls  int
	scopes [][]string
}

func (f *operationScopePreflightFake) Check(
	_ context.Context,
	_ string,
	scopes []string,
) (*ScopeCheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.calls
	f.calls++
	f.scopes = append(f.scopes, append([]string(nil), scopes...))
	if index < len(f.steps) {
		step := f.steps[index]
		if step.result == nil {
			return nil, step.err
		}
		return &ScopeCheckResult{
			Granted: append([]string(nil), step.result.Granted...),
			Missing: append([]string(nil), step.result.Missing...),
		}, step.err
	}
	return &ScopeCheckResult{Granted: append([]string(nil), scopes...)}, nil
}

func (f *operationScopePreflightFake) snapshot() (int, [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([][]string, len(f.scopes))
	for index := range f.scopes {
		result[index] = append([]string(nil), f.scopes[index]...)
	}
	return f.calls, result
}

func (f *operationRunnerFake) Run(_ context.Context, _ string, argv []string, stdinJSON []byte) (*CLIResult, error) {
	f.mu.Lock()
	index := f.calls
	f.calls++
	f.argv = append(f.argv, append([]string(nil), argv...))
	f.stdin = append(f.stdin, append([]byte(nil), stdinJSON...))
	var step operationRunnerStep
	if index < len(f.steps) {
		step = f.steps[index]
	}
	f.mu.Unlock()
	if step.started != nil {
		select {
		case step.started <- struct{}{}:
		default:
		}
	}
	if step.release != nil {
		<-step.release
	}
	return cloneTestCLIResult(step.result), step.err
}

func (f *operationRunnerFake) snapshot() (int, [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	argv := make([][]string, len(f.argv))
	for index := range f.argv {
		argv[index] = append([]string(nil), f.argv[index]...)
	}
	return f.calls, argv
}

type operationVaultFake struct {
	mu             sync.Mutex
	changed        []bool
	sealed         int
	beforeCallback func()
	afterRun       func(userID uint, generation uint64, changed bool) error
}

type recordingOperationStore struct {
	OperationStore
	mu     sync.Mutex
	owners []string
}

func (s *recordingOperationStore) ClaimOperation(
	ctx context.Context,
	userID uint,
	generation uint64,
	id, owner string,
	expectedStates []string,
	now, leaseUntil time.Time,
) (bool, error) {
	s.mu.Lock()
	s.owners = append(s.owners, owner)
	s.mu.Unlock()
	return s.OperationStore.ClaimOperation(ctx, userID, generation, id, owner, expectedStates, now, leaseUntil)
}

func (s *recordingOperationStore) snapshotOwners() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.owners...)
}

type terminalBeforeClaimOperationStore struct {
	OperationStore
	db *gorm.DB
}

type waitingTransitionFailOperationStore struct{ OperationStore }

func (s *waitingTransitionFailOperationStore) TransitionOperation(
	ctx context.Context,
	userID uint,
	generation uint64,
	id, owner string,
	from []string,
	to string,
	now time.Time,
	fields map[string]any,
) error {
	if recoveryWaitingState(to) {
		return errors.New("waiting transition failed")
	}
	return s.OperationStore.TransitionOperation(ctx, userID, generation, id, owner, from, to, now, fields)
}

type disappearingProofOperationStore struct {
	OperationStore
	mu    sync.Mutex
	calls int
}

type proofQueryBarrierOperationStore struct {
	OperationStore
	arrived chan struct{}
	release <-chan struct{}
}

func (s *proofQueryBarrierOperationStore) ListSucceededCreatesForRun(
	ctx context.Context,
	userID uint,
	generation uint64,
	agentRunID uint64,
) ([]model.FeishuOperation, error) {
	s.arrived <- struct{}{}
	<-s.release
	return s.OperationStore.ListSucceededCreatesForRun(ctx, userID, generation, agentRunID)
}

type unboundProofOperationStore struct{ OperationStore }

func (s *unboundProofOperationStore) IsOperationProofUsable(
	context.Context,
	uint,
	uint64,
	uint64,
	string,
	string,
) (bool, error) {
	return false, nil
}

type gateClaimBarrierOperationStore struct {
	store.IFeishuWorkspaceStore
	arrived chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *gateClaimBarrierOperationStore) TryClaimExecutionGate(
	ctx context.Context,
	userID uint,
	generation uint64,
	owner, operationID string,
	now, leaseUntil time.Time,
) (bool, error) {
	s.once.Do(func() {
		s.arrived <- struct{}{}
		<-s.release
	})
	return s.IFeishuWorkspaceStore.TryClaimExecutionGate(
		ctx, userID, generation, owner, operationID, now, leaseUntil,
	)
}

type gateAttemptSignalOperationStore struct {
	store.IFeishuWorkspaceStore
	attempted chan struct{}
}

type releaseFailOperationStore struct{ store.IFeishuWorkspaceStore }

type executionGateRenewCall struct {
	now        time.Time
	leaseUntil time.Time
}

type renewalTrackingOperationStore struct {
	store.IFeishuWorkspaceStore
	mu      sync.Mutex
	calls   []executionGateRenewCall
	failAt  int
	failErr error
	renewed chan struct{}
}

func (s *renewalTrackingOperationStore) RenewExecutionGate(
	ctx context.Context,
	userID uint,
	generation uint64,
	owner, operationID string,
	now, leaseUntil time.Time,
) (bool, error) {
	s.mu.Lock()
	s.calls = append(s.calls, executionGateRenewCall{now: now, leaseUntil: leaseUntil})
	call := len(s.calls)
	renewed := s.renewed
	failAt := s.failAt
	failErr := s.failErr
	s.mu.Unlock()
	if renewed != nil {
		select {
		case renewed <- struct{}{}:
		default:
		}
	}
	if failAt > 0 && call == failAt {
		return false, failErr
	}
	return s.IFeishuWorkspaceStore.RenewExecutionGate(
		ctx, userID, generation, owner, operationID, now, leaseUntil,
	)
}

func (s *renewalTrackingOperationStore) snapshot() []executionGateRenewCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]executionGateRenewCall(nil), s.calls...)
}

type contextBlockingOperationRunner struct {
	started chan struct{}
	mu      sync.Mutex
	calls   int
}

func (r *contextBlockingOperationRunner) Run(
	ctx context.Context,
	_ string,
	_ []string,
	_ []byte,
) (*CLIResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return &CLIResult{InvocationStarted: true, ExitCode: -1}, ctx.Err()
}

func (r *contextBlockingOperationRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type deadlineRecordingOperationRunner struct {
	deadline chan time.Time
}

func (r *deadlineRecordingOperationRunner) Run(
	ctx context.Context,
	_ string,
	_ []string,
	_ []byte,
) (*CLIResult, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	r.deadline <- deadline
	return operationOKResult(`{"document_id":"deadline-bounded"}`), nil
}

type drainingRenewalOperationStore struct {
	store.IFeishuWorkspaceStore
	mu                 sync.Mutex
	calls              int
	heartbeatStarted   chan struct{}
	heartbeatReturned  chan struct{}
	returnOnce         sync.Once
	releaseBeforeDrain bool
}

func (s *drainingRenewalOperationStore) RenewExecutionGate(
	ctx context.Context,
	userID uint,
	generation uint64,
	owner, operationID string,
	now, leaseUntil time.Time,
) (bool, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		return s.IFeishuWorkspaceStore.RenewExecutionGate(
			ctx, userID, generation, owner, operationID, now, leaseUntil,
		)
	}
	select {
	case s.heartbeatStarted <- struct{}{}:
	default:
	}
	<-ctx.Done()
	s.returnOnce.Do(func() { close(s.heartbeatReturned) })
	return false, ctx.Err()
}

func (s *drainingRenewalOperationStore) ReleaseExecutionGate(
	ctx context.Context,
	userID uint,
	generation uint64,
	owner string,
	now time.Time,
) (bool, error) {
	select {
	case <-s.heartbeatReturned:
	default:
		s.mu.Lock()
		s.releaseBeforeDrain = true
		s.mu.Unlock()
	}
	return s.IFeishuWorkspaceStore.ReleaseExecutionGate(ctx, userID, generation, owner, now)
}

func (s *drainingRenewalOperationStore) releasedBeforeHeartbeatDrain() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseBeforeDrain
}

func (s *releaseFailOperationStore) ReleaseExecutionGate(
	context.Context,
	uint,
	uint64,
	string,
	time.Time,
) (bool, error) {
	return false, errors.New("simulated gate release failure")
}

func (s *gateAttemptSignalOperationStore) TryClaimExecutionGate(
	ctx context.Context,
	userID uint,
	generation uint64,
	owner, operationID string,
	now, leaseUntil time.Time,
) (bool, error) {
	claimed, err := s.IFeishuWorkspaceStore.TryClaimExecutionGate(
		ctx, userID, generation, owner, operationID, now, leaseUntil,
	)
	select {
	case s.attempted <- struct{}{}:
	default:
	}
	return claimed, err
}

func (s *disappearingProofOperationStore) ListSucceededCreatesForRun(
	ctx context.Context,
	userID uint,
	generation uint64,
	agentRunID uint64,
) ([]model.FeishuOperation, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call > 1 {
		return nil, nil
	}
	return s.OperationStore.ListSucceededCreatesForRun(ctx, userID, generation, agentRunID)
}

func (s *terminalBeforeClaimOperationStore) ClaimOperation(
	ctx context.Context,
	userID uint,
	generation uint64,
	id, owner string,
	expectedStates []string,
	now, leaseUntil time.Time,
) (bool, error) {
	if err := s.db.WithContext(ctx).Model(&model.FeishuOperation{}).
		Where("id = ? AND user_id = ? AND generation = ?", id, userID, generation).
		Updates(map[string]any{
			"state": model.FeishuOperationFailed, "lease_owner": "", "lease_until": nil,
		}).Error; err != nil {
		return false, err
	}
	return s.OperationStore.ClaimOperation(ctx, userID, generation, id, owner, expectedStates, now, leaseUntil)
}

func (f *operationVaultFake) WithHome(
	_ context.Context,
	userID uint,
	generation uint64,
	callback func(home string) (bool, error),
) error {
	f.mu.Lock()
	beforeCallback := f.beforeCallback
	f.mu.Unlock()
	if beforeCallback != nil {
		beforeCallback()
	}
	changed, err := callback("/tmp/operation-home")
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.changed = append(f.changed, changed)
	after := f.afterRun
	f.mu.Unlock()
	if after != nil {
		if err := after(userID, generation, changed); err != nil {
			return err
		}
	}
	if changed {
		f.mu.Lock()
		f.sealed++
		f.mu.Unlock()
	}
	return nil
}

func (f *operationVaultFake) WithHomeCandidate(
	context.Context,
	uint,
	uint64,
	func(string) error,
) (*CLIHomeCandidate, error) {
	return nil, errors.New("completion candidate is outside operation fixture tests")
}

type operationHarness struct {
	t            *testing.T
	ctx          context.Context
	db           *gorm.DB
	dataStore    store.IStore
	receipts     *operationReceiptFake
	recovery     *operationRecoveryFake
	confirmation *operationConfirmationFake
	runner       *operationRunnerFake
	preflight    *operationScopePreflightFake
	vault        *operationVaultFake
	cipher       *OperationCipherKeyring
	service      *FeishuOperationService
}

type operationObservationCapture struct {
	mu     sync.Mutex
	events []OperationObservation
}

func (c *operationObservationCapture) ObserveOperation(event OperationObservation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *operationObservationCapture) snapshot() []OperationObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]OperationObservation(nil), c.events...)
}

func newOperationHarness(t *testing.T) *operationHarness {
	t.Helper()
	// t.Name() is reused by `go test -count=N`. Give every harness an isolated
	// shared-memory database so a deliberately blocked goroutine in one test
	// cannot share (or outlive cleanup of) a later invocation's SQLite schema.
	dsn := "file:" + t.Name() + "-" + uuid.NewString() + "?mode=memory&cache=shared&_busy_timeout=5000"
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
	dataStore := store.NewTestStore(db)
	receipts := &operationReceiptFake{}
	recovery := &operationRecoveryFake{action: &OperationAction{Provider: ProviderLark, Phase: "create_app", SessionID: "session-1"}}
	confirmation := &operationConfirmationFake{action: &OperationAction{
		Provider: ProviderLark, Phase: "confirmation", SessionID: "confirmation-1",
		ExpiresAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	}}
	runner := &operationRunnerFake{steps: []operationRunnerStep{{result: operationOKResult(`{"document_id":"doc1"}`)}}}
	preflight := &operationScopePreflightFake{}
	vault := &operationVaultFake{}
	cipher := newOperationTestCipherKeyring(t, "v1")
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts:      dataStore.ThirdPartyAccounts(),
		Operations:    dataStore.FeishuWorkspace(),
		Catalog:       NewCommandCatalog(),
		Receipts:      receipts,
		Recovery:      recovery,
		Confirmation:  confirmation,
		Vault:         vault,
		Preflight:     preflight,
		Runner:        runner,
		Cipher:        cipher,
		Now:           func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) },
		LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	return &operationHarness{
		t: t, ctx: context.Background(), db: db, dataStore: dataStore,
		receipts: receipts, recovery: recovery, confirmation: confirmation,
		runner: runner, preflight: preflight, vault: vault, cipher: cipher, service: service,
	}
}

func newHarnessOperationService(t *testing.T, h *operationHarness, operations OperationStore) *FeishuOperationService {
	t.Helper()
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: operations,
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	return service
}

func newHarnessOperationServiceWithRuntime(
	t *testing.T,
	h *operationHarness,
	operations OperationStore,
	runner OperationRunner,
	now func() time.Time,
	heartbeatInterval time.Duration,
) *FeishuOperationService {
	t.Helper()
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: operations,
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Runner: runner, Cipher: h.cipher,
		Preflight: h.preflight,
		Now:       now, LeaseDuration: time.Minute,
		ExecutionGateHeartbeatInterval: heartbeatInterval,
	})
	require.NoError(t, err)
	return service
}

func newOperationTestCipherKeyring(t *testing.T, current string) *OperationCipherKeyring {
	t.Helper()
	ciphers := make(map[string]*pkgcrypto.Cipher)
	for index, version := range []string{"v1", "v2"} {
		raw := make([]byte, pkgcrypto.KeyLen)
		for offset := range raw {
			raw[offset] = byte(index*31 + offset + 1)
		}
		cipher, err := pkgcrypto.NewCipher(base64.StdEncoding.EncodeToString(raw))
		require.NoError(t, err)
		ciphers[version] = cipher
	}
	keyring, err := NewOperationCipherKeyring(ciphers, current)
	require.NoError(t, err)
	return keyring
}

func (h *operationHarness) createAccount(userID uint, state string, generation uint64, appID string) {
	h.t.Helper()
	require.NoError(h.t, h.db.Create(&model.UserThirdPartyAccount{
		UserID: userID, Provider: ProviderLark, AppID: appID,
		ConnectionState: state, Connected: state == model.FeishuConnectionConnected,
		Generation: generation,
	}).Error)
}

func operationDocsFetchRequest(runID uint64, toolCallID string) ExecuteRequest {
	return ExecuteRequest{
		UserID: 7, AgentRunID: runID, ToolCallID: toolCallID,
		IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
		Argv:           []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123"},
		SkillReceipts:  []string{"shared-receipt", "doc-receipt"},
	}
}

func operationDriveSearchRequest(userID uint, runID uint64, toolCallID, query string) ExecuteRequest {
	return ExecuteRequest{
		UserID: userID, AgentRunID: runID, ToolCallID: toolCallID, IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
		Argv:          []string{"drive", "+search", "--query", query, "--only-title", "--doc-types", "docx,wiki,bitable"},
		SkillReceipts: []string{"shared", "drive"},
	}
}

func operationDocsCreateRequest(userID uint, runID uint64, toolCallID, title string, content *string) ExecuteRequest {
	argv := []string{"docs", "+create", "--title", title}
	if content != nil {
		argv = append(argv, "--content", *content)
	}
	return ExecuteRequest{
		UserID: userID, AgentRunID: runID, ToolCallID: toolCallID,
		IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
		Argv:           argv, SkillReceipts: []string{"shared", "doc"},
	}
}

func operationDocsOverwriteRequest(userID uint, runID uint64, toolCallID, docToken string) ExecuteRequest {
	return ExecuteRequest{
		UserID: userID, AgentRunID: runID, ToolCallID: toolCallID,
		IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
		Argv:           []string{"docs", "+update", "--doc", docToken, "--command", "overwrite", "--content", "initial body"},
		SkillReceipts:  []string{"shared", "wiki", "doc"},
	}
}

func operationDocsAppendRequest(userID uint, runID uint64, toolCallID, docToken string) ExecuteRequest {
	return ExecuteRequest{
		UserID: userID, AgentRunID: runID, ToolCallID: toolCallID,
		IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
		Argv: []string{
			"docs", "+update", "--doc", docToken, "--command", "append", "--content", "later body",
		},
		SkillReceipts: []string{"shared", "doc"},
	}
}

func requirePersistedCreateProof(t *testing.T, h *operationHarness, operationID, proofOperationID string) {
	t.Helper()
	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operationID)
	require.NoError(t, err)
	owner := OperationCipherOwner{UserID: stored.UserID, Generation: stored.Generation, OperationID: stored.ID}
	plaintext, _, err := h.service.openOperationBlob(
		OperationCipherPurposeRequest, owner, stored.KeyVersion, stored.RequestCiphertext,
	)
	require.NoError(t, err)
	var audit map[string]any
	require.NoError(t, json.Unmarshal(plaintext, &audit))
	require.Equal(t, true, audit["same_run_empty_create_proof"])
	require.Equal(t, proofOperationID, audit["create_proof_operation_id"])
}

func operationOKResult(data string) *CLIResult {
	return &CLIResult{
		InvocationStarted: true,
		ExitCode:          0,
		Envelope:          &CLIEnvelope{OK: true, Identity: "user", Data: json.RawMessage(data)},
	}
}

func TestOperationService_ConnectedHotPathSkipsAuthStatus(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 3, "cli_existing")
	h.service.verifiedCLIVersion = LarkCLIVersion

	got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(9, "tc1"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, got.State)
	require.JSONEq(t, `{"document_id":"doc1"}`, string(got.Data))
	calls, argv := h.runner.snapshot()
	require.Equal(t, 1, calls)
	require.Equal(t, 0, h.runner.authStatus)
	require.Equal(t, []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--format", "json", "--as", "user"}, argv[0])

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 3, got.OperationID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stored.AttemptCount)
	require.NotEmpty(t, stored.ResultCiphertext)
	require.NotContains(t, string(stored.ResultSummaryJSON), "doc1")
	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.Equal(t, LarkCLIVersion, account.LarkCLIVersion)
	require.NotNil(t, account.LastSuccessAt)
	require.JSONEq(t, `{"docs":{"state":"available","last_success_at":"2026-07-13T12:00:00Z"}}`, string(account.CapabilityStateJSON))
}

func TestOperationService_UnknownWriteEmitsSafeUnderlyingClassificationWithoutReplay(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	capture := &operationObservationCapture{}
	h.service.observer = capture
	h.service.verifiedCLIVersion = LarkCLIVersion
	h.runner.steps = []operationRunnerStep{{
		result: &CLIResult{InvocationStarted: true, ExitCode: 1, Envelope: &CLIEnvelope{
			OK: false, Identity: "user", Error: &CLIError{
				Type: "api", Subtype: "validation_error", Code: json.RawMessage(`400`),
				Identity: "user", Message: "private Base field content must never be logged",
			},
		}},
		err: errors.New("private transport wrapper must never be logged"),
	}}

	got, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 902, "tc-base-like-write", "private title", nil))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationUnknown, got.State)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls, "a started write remains non-replayable")

	events := capture.snapshot()
	require.Len(t, events, 1)
	require.Equal(t, OperationObservation{
		UserID: 7, Generation: 1, OperationID: got.OperationID,
		Phase: "invoke", OutcomeClass: PublicCodeValidationError, Risk: RiskWrite,
		InvocationStarted: true, ExitCode: 1, CLIVersion: LarkCLIVersion,
		CLIErrorType: "api", CLIErrorSubtype: "validation_error", CLIErrorCode: "400",
		FailureSource: "structured_cli_error",
	}, events[0])
	encoded, marshalErr := json.Marshal(events)
	require.NoError(t, marshalErr)
	require.NotContains(t, string(encoded), "private Base field content")
	require.NotContains(t, string(encoded), "private transport wrapper")
	require.NotContains(t, string(encoded), "private title")
}

func TestOperationFailureSourceUsesFixedCredentialFreeClasses(t *testing.T) {
	require.Equal(t, "", operationFailureSource(operationOKResult(`{}`), nil, nil, "succeeded"))
	require.Equal(t, "structured_cli_error", operationFailureSource(&CLIResult{
		InvocationStarted: true,
		Envelope:          &CLIEnvelope{OK: false, Error: &CLIError{Type: "api", Subtype: "validation_error"}},
	}, errors.New("private"), nil, PublicCodeValidationError))
	require.Equal(t, "timeout", operationFailureSource(&CLIResult{InvocationStarted: true}, context.DeadlineExceeded, nil, PublicCodeUnknownResult))
	require.Equal(t, "malformed_output", operationFailureSource(&CLIResult{InvocationStarted: true}, errControlledCLIInvalidJSON, nil, PublicCodeUnknownResult))
	require.Equal(t, "output_limit", operationFailureSource(&CLIResult{InvocationStarted: true}, errControlledCLIOutputLimit, nil, PublicCodeUnknownResult))
	require.Equal(t, "transport", operationFailureSource(&CLIResult{InvocationStarted: true}, errors.New("private network"), nil, PublicCodeUnknownResult))
	require.Equal(t, "vault", operationFailureSource(nil, nil, errors.New("private vault"), PublicCodeFailed))
	require.Equal(t, "not_started", operationFailureSource(nil, nil, nil, PublicCodeFailed))
}

func TestValidOperationDiagnosticTupleRequiresFixedClassifierMatch(t *testing.T) {
	require.True(t, ValidOperationDiagnosticTuple(PublicCodeValidationError, "api", "validation_error", "400"))
	require.True(t, ValidOperationDiagnosticTuple(PublicCodeConnectionRequired, "config", "not_configured", ""))
	require.False(t, ValidOperationDiagnosticTuple(PublicCodeUnknownResult, "api", "validation_error", "400"))
	require.False(t, ValidOperationDiagnosticTuple(PublicCodeValidationError, "api", "private-content", "400"))
	require.False(t, ValidOperationDiagnosticTuple(PublicCodeValidationError, "api", "validation_error", "private-content"))
}

func TestOperationService_StaleNotStartedSnapshotCannotLeaseTerminalOperation(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	racingStore := &terminalBeforeClaimOperationStore{OperationStore: h.dataStore.FeishuWorkspace(), db: h.db}
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: racingStore,
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)

	got, err := service.Execute(h.ctx, operationDocsFetchRequest(901, "tc-terminal-race"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, got.State)
	calls, _ := h.runner.snapshot()
	require.Zero(t, calls)

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, stored.State)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
}

func TestOperationService_LegacyUnknownSummaryPreservesStartedWriteEvidence(t *testing.T) {
	h := newOperationHarness(t)
	for name, summary := range map[string][]byte{
		"malformed":    []byte(`{"unexpected":true}`),
		"stale status": []byte(`{"status":"failed","public_code":"feishu_failed","business_started":false}`),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := h.service.resultFromOperation(&model.FeishuOperation{
				ID: "legacy-unknown", State: model.FeishuOperationUnknown,
				RiskLevel: string(RiskWrite), ResultSummaryJSON: summary,
			})
			require.NoError(t, err)
			require.NotNil(t, got.Failure)
			require.Equal(t, PublicCodeUnknownResult, got.Failure.Code)
			require.True(t, got.Failure.BusinessStarted)
			require.False(t, got.Failure.Retryable)
		})
	}
}

func TestOperationService_StrictInputAndPlatformOwnedPolicy(t *testing.T) {
	t.Run("catalog validation exposes safe correction hint before any operation", func(t *testing.T) {
		h := newOperationHarness(t)
		req := operationDocsFetchRequest(248, "tc-base-schema")
		req.Argv = []string{"base", "+base-create", "--name", "联调", "--table-name", "任务列表"}
		_, err := h.service.Execute(h.ctx, req)
		require.ErrorIs(t, err, ErrOperationRequestRejected)
		hint, ok := SafeOperationRequestValidation(err)
		require.True(t, ok)
		require.Equal(t, "table-name and fields must be supplied together", hint)

		var count int64
		require.NoError(t, h.db.Model(&model.FeishuOperation{}).Count(&count).Error)
		require.Zero(t, count, "catalog rejection must not create or execute a Feishu operation")
	})

	t.Run("normalize rejects forbidden commands before any operation", func(t *testing.T) {
		h := newOperationHarness(t)
		req := operationDocsFetchRequest(9, "tc-invalid")
		req.Argv = []string{"im", "send"}
		_, err := h.service.Execute(h.ctx, req)
		require.ErrorIs(t, err, ErrOperationRequestRejected)
		domains, _ := h.receipts.snapshot()
		require.Empty(t, domains)
		var count int64
		require.NoError(t, h.db.Model(&model.FeishuOperation{}).Count(&count).Error)
		require.Zero(t, count)
	})

	t.Run("legacy receipt verifier cannot block wiki content", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		h.receipts.err = errors.New("legacy verifier unavailable")
		req := operationDocsFetchRequest(10, "tc-wiki")
		req.Argv = []string{"docs", "+fetch", "--doc", "https://acme.feishu.cn/wiki/wikcnABCDEFG123"}
		_, err := h.service.Execute(h.ctx, req)
		require.NoError(t, err)
		domains, runs := h.receipts.snapshot()
		require.Empty(t, domains)
		require.Empty(t, runs)
	})

	for index, testCase := range []struct {
		name string
		argv []string
	}{
		{name: "fetch wikcn token", argv: []string{"docs", "+fetch", "--doc", "wikcnABCDEFG123"}},
		{name: "fetch wikc token", argv: []string{"docs", "+fetch", "--doc", "wikcABCDEFG123"}},
		{name: "update wikcn token", argv: []string{"docs", "+update", "--doc", "wikcnABCDEFG123", "--command", "append", "--content", "hello"}},
		{name: "update wikc token", argv: []string{"docs", "+update", "--doc", "wikcABCDEFG123", "--command", "append", "--content", "hello"}},
	} {
		t.Run("bare wiki token "+testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			runID := uint64(110 + index)
			req := operationDocsFetchRequest(runID, "tc-bare-wiki-"+strconv.Itoa(index))
			req.IdempotencyKey = fmt.Sprintf("%d:%s", runID, req.ToolCallID)
			req.Argv = testCase.argv

			h.receipts.err = errors.New("legacy verifier unavailable")
			_, err := h.service.Execute(h.ctx, req)
			require.NoError(t, err)
			domains, runs := h.receipts.snapshot()
			require.Empty(t, domains)
			require.Empty(t, runs)
		})
	}

	t.Run("wiki-looking document content does not change domain", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		req := ExecuteRequest{
			UserID: 7, AgentRunID: 99, ToolCallID: "tc-content-url", IdempotencyKey: "99:tc-content-url",
			Argv:          []string{"docs", "+create", "--content", "https://acme.feishu.cn/wiki/wikcnABCDEFG123"},
			SkillReceipts: []string{"shared", "doc"},
		}
		_, err := h.service.Execute(h.ctx, req)
		require.NoError(t, err)
		domains, runs := h.receipts.snapshot()
		require.Empty(t, domains)
		require.Empty(t, runs)
	})

	for _, testCase := range []struct {
		name   string
		domain string
		argv   []string
	}{
		{name: "docs", domain: SkillDomainDocs, argv: []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123"}},
		{name: "base", domain: SkillDomainBase, argv: []string{"base", "+base-get", "--base-token", "bascnABCDEFG123"}},
		{name: "wiki", domain: SkillDomainWiki, argv: []string{"wiki", "+node-get", "--node-token", "wikcnABCDEFG123"}},
		{name: "drive", domain: "drive", argv: []string{"drive", "+search", "--query", "有数飞书二次连接测试", "--only-title", "--doc-types", "docx,wiki,bitable"}},
	} {
		t.Run("server domain "+testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			req := operationDocsFetchRequest(100, "tc-domain-"+testCase.name)
			req.Argv = testCase.argv
			_, err := h.service.Execute(h.ctx, req)
			require.NoError(t, err)
			domains, runs := h.receipts.snapshot()
			require.Empty(t, domains)
			require.Empty(t, runs)
			var operation model.FeishuOperation
			require.NoError(t, h.db.Order("created_at desc").First(&operation).Error)
			require.Equal(t, testCase.domain, operation.Domain)
		})
	}

	t.Run("legacy receipt verifier failure is ignored", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		h.receipts.err = errors.New("invalid receipt detail")
		result, err := h.service.Execute(h.ctx, operationDocsFetchRequest(101, "tc-receipt-invalid"))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, result.State)
		var count int64
		require.NoError(t, h.db.Model(&model.FeishuOperation{}).Count(&count).Error)
		require.EqualValues(t, 1, count)
		domains, runs := h.receipts.snapshot()
		require.Empty(t, domains)
		require.Empty(t, runs)
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*ExecuteRequest)
	}{
		{name: "zero user", mutate: func(req *ExecuteRequest) { req.UserID = 0 }},
		{name: "zero run", mutate: func(req *ExecuteRequest) { req.AgentRunID = 0 }},
		{name: "empty tool", mutate: func(req *ExecuteRequest) { req.ToolCallID = "" }},
		{name: "oversized tool", mutate: func(req *ExecuteRequest) { req.ToolCallID = string(make([]byte, 129)) }},
		{name: "non exact idempotency key", mutate: func(req *ExecuteRequest) { req.IdempotencyKey = "prefix:9:tc1" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			req := operationDocsFetchRequest(9, "tc1")
			testCase.mutate(&req)
			_, err := h.service.Execute(h.ctx, req)
			require.ErrorIs(t, err, ErrOperationRequestRejected)
		})
	}
}

func TestOperationService_DriveDiscoveryStaysInsideCurrentUserAccount(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-user-seven")
	h.createAccount(8, model.FeishuConnectionConnected, 1, "cli-user-eight")
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"results":[{"title":"A"}]}`)},
		{result: operationOKResult(`{"results":[{"title":"B"}]}`)},
	}

	first, err := h.service.Execute(h.ctx, operationDriveSearchRequest(7, 211, "drive-user-seven", "A"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, first.State)
	second, err := h.service.Execute(h.ctx, operationDriveSearchRequest(8, 212, "drive-user-eight", "B"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, second.State)

	var operations []model.FeishuOperation
	require.NoError(t, h.db.Where("domain = ?", SkillDomainDrive).Order("user_id ASC").Find(&operations).Error)
	require.Len(t, operations, 2)
	require.Equal(t, uint(7), operations[0].UserID)
	require.Equal(t, uint64(211), operations[0].AgentRunID)
	require.Equal(t, uint(8), operations[1].UserID)
	require.Equal(t, uint64(212), operations[1].AgentRunID)

	for _, userID := range []uint{7, 8} {
		account, getErr := h.dataStore.ThirdPartyAccounts().Get(h.ctx, userID, ProviderLark)
		require.NoError(t, getErr)
		require.JSONEq(t, `{"drive":{"state":"available","last_success_at":"2026-07-13T12:00:00Z"}}`, string(account.CapabilityStateJSON))
	}
}

func TestOperationService_NeverConnectedCreatesPlaceholderWithoutOverwritingExistingAccount(t *testing.T) {
	t.Run("missing row gets generation one placeholder", func(t *testing.T) {
		h := newOperationHarness(t)
		got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(11, "tc-none"))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConnection, got.State)
		account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
		require.NoError(t, err)
		require.Equal(t, model.FeishuConnectionNone, account.ConnectionState)
		require.EqualValues(t, 1, account.Generation)
		require.Empty(t, account.AppID)
		calls, _ := h.runner.snapshot()
		require.Zero(t, calls)
		var gateCount int64
		require.NoError(t, h.db.Model(&model.FeishuOperationExecutionGate{}).Count(&gateCount).Error)
		require.Zero(t, gateCount, "initial connection recovery must not acquire the CLI gate")
	})

	t.Run("existing metadata is untouched", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionReauthRequired, 8, "cli_keep")
		got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(12, "tc-reauth"))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingUserAuth, got.State)
		require.Equal(t, "user_auth", got.Action.Phase)
		require.Equal(t, []string{"docx:document:readonly"}, got.Action.Scopes)
		require.Equal(t, RecoveryReauth, h.recovery.snapshot()[0].Kind)
		account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
		require.NoError(t, err)
		require.Equal(t, "cli_keep", account.AppID)
		require.Equal(t, model.FeishuConnectionReauthRequired, account.ConnectionState)
		require.EqualValues(t, 8, account.Generation)
		stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 8, got.OperationID)
		require.NoError(t, err)
		require.Contains(t, string(stored.ResultSummaryJSON), PublicCodeReauthRequired)
		var gateCount int64
		require.NoError(t, h.db.Model(&model.FeishuOperationExecutionGate{}).Count(&gateCount).Error)
		require.Zero(t, gateCount, "reauth recovery must not acquire the CLI gate")
	})
}

func TestOperationService_LocalBaseURLResolveDoesNotRequireConnectionOrAuthorization(t *testing.T) {
	h := newOperationHarness(t)
	h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"base_token":"ZiXObjsGvahtyAscDJ1cjlRnnLh","table_id":"tblABCDEFG123"}`)}}
	request := ExecuteRequest{
		UserID: 7, AgentRunID: 246, ToolCallID: "base-url-resolve", IdempotencyKey: "246:base-url-resolve",
		Argv: []string{"base", "+url-resolve", "--url", "https://scnb8amlnnek.feishu.cn/base/ZiXObjsGvahtyAscDJ1cjlRnnLh?table=tblABCDEFG123"},
	}

	got, err := h.service.Execute(h.ctx, request)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, got.State)
	require.JSONEq(t, `{
		"base_token":"ZiXObjsGvahtyAscDJ1cjlRnnLh",
		"hint":{"next_step":"use +record-list to list records in the resolved table"},
		"input_type":"base_url",
		"resource_type":"bitable",
		"table_id":"tblABCDEFG123"
	}`, string(got.Data))
	require.Empty(t, h.recovery.snapshot(), "local URL parsing must never start an authorization flow")
	require.Empty(t, h.preflight.calls, "local URL parsing must never inspect Feishu scopes")
	calls, _ := h.runner.snapshot()
	require.Zero(t, calls, "local URL parsing must not start lark-cli")
	require.Empty(t, h.vault.changed, "local URL parsing must not materialize or persist CLI HOME")

	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionNone, account.ConnectionState)
	require.Empty(t, account.CapabilityStateJSON, "local parsing does not prove Base API availability")
	require.Nil(t, account.LastSuccessAt)
	require.Empty(t, account.LarkCLIVersion)
}

func TestOperationService_DisconnectingGenerationRejectsNewAgentOperation(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-app")
	_, nextGeneration, err := h.dataStore.ThirdPartyAccounts().RetireGeneration(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.EqualValues(t, 2, nextGeneration)

	_, err = h.service.Execute(h.ctx, operationDocsFetchRequest(992, "disconnecting-operation"))
	require.ErrorIs(t, err, ErrOperationUnavailable)
	runnerCalls, _ := h.runner.snapshot()
	require.Zero(t, runnerCalls, "disconnecting accounts must not start a lark-cli invocation")

	var count int64
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("user_id = ?", 7).Count(&count).Error)
	require.Zero(t, count, "disconnecting accounts must not create a new-generation operation")
	account, getErr := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, getErr)
	require.Equal(t, model.FeishuConnectionDisconnecting, account.ConnectionState)
	require.EqualValues(t, nextGeneration, account.Generation)
}

func TestOperationService_ActivatesRecoveryOnlyAfterWaitingIsPersisted(t *testing.T) {
	h := newOperationHarness(t)
	h.recovery.onActivate = func(sessionID string) error {
		if sessionID != "session-1" {
			return errors.New("unexpected session")
		}
		var operation model.FeishuOperation
		if err := h.db.Where("user_id = ? AND idempotency_key = ?", 7, "130:tc-activation-order").Take(&operation).Error; err != nil {
			return err
		}
		if operation.State != model.FeishuOperationWaitingConnection {
			return fmt.Errorf("operation state at activation = %s", operation.State)
		}
		return nil
	}

	got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(130, "tc-activation-order"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, got.State)
	activated, aborted := h.recovery.activationSnapshot()
	require.Equal(t, []string{"session-1"}, activated)
	require.Empty(t, aborted)
}

func TestOperationService_WaitingTransitionFailureAbortsRecoveryBarrier(t *testing.T) {
	h := newOperationHarness(t)
	service := newHarnessOperationService(t, h, &waitingTransitionFailOperationStore{
		OperationStore: h.dataStore.FeishuWorkspace(),
	})

	got, err := service.Execute(h.ctx, operationDocsFetchRequest(131, "tc-activation-abort"))
	require.Nil(t, got)
	require.ErrorIs(t, err, ErrOperationUnavailable)
	activated, aborted := h.recovery.activationSnapshot()
	require.Empty(t, activated)
	require.Equal(t, []string{"session-1"}, aborted)
}

func TestOperationService_AppReadyContinuesToExactUserAuthorization(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionAppReady, 1, "cli_app_ready")
	h.recovery.action = &OperationAction{SessionID: "session-user-after-app"}

	got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(13, "tc-app-ready"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, got.State)
	require.Equal(t, model.FeishuAuthPhaseUserAuth, got.Action.Phase)
	require.Equal(t, []string{"docx:document:readonly"}, got.Action.Scopes)
	calls := h.recovery.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, RecoveryReauth, calls[0].Kind)
	require.Equal(t, []string{"docx:document:readonly"}, calls[0].Scopes)
	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.JSONEq(t, `{"docs":{"state":"needs_user_scope"}}`, string(account.CapabilityStateJSON))
}

func TestOperationService_CompletedAuthRecoveryContinuesWithoutRecursiveDispatcher(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionAppReady, 1, "cli_app_ready")
	waiting, err := h.service.Execute(h.ctx, operationDocsFetchRequest(132, "tc-no-recursive-dispatch"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)

	operationID := waiting.OperationID
	require.NoError(t, h.db.Create(&model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-555555555555", UserID: 7, Generation: 1,
		OperationID: &operationID, Phase: model.FeishuAuthPhaseUserAuth,
		RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State:               model.FeishuAuthSessionCompleted, ProtocolVersion: 2,
		ScopeHash: deviceAuthScopeHash([]string{"docx:document:readonly"}),
		ExpiresAt: h.service.now().Add(10 * time.Minute),
	}).Error)
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{"connection_state": model.FeishuConnectionConnected, "connected": true}).Error)

	dispatcher := &reentrantOperationResumeDispatcher{}
	cli := &authSessionCLIFake{}
	deviceAuth, err := NewDeviceAuthFlow(DeviceAuthFlowDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: h.vault, CLI: cli, Cipher: newDeviceAuthFlowCredentialCipher(t), Dispatcher: dispatcher,
		Owner: "recursive-integration-device-auth", Now: h.service.now,
		LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second, CompletionTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	authService, err := NewAuthSessionService(AuthSessionServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: h.vault, CLI: cli, DeviceAuth: deviceAuth, Dispatcher: dispatcher, Owner: "recursive-integration",
		Now: h.service.now, LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second,
	})
	require.NoError(t, err)
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: authService,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	dispatcher.service = service

	result, err := service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	require.Zero(t, dispatcher.callCount(), "StartRecovery must not re-enter Operation.Resume for completed user auth")
}

func TestOperationService_ConcurrentSameKeyRunsOnce(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h.runner.steps = []operationRunnerStep{{
		result: operationOKResult(`{"ok":true}`), started: started, release: release,
	}}

	const workers = 20
	var wg sync.WaitGroup
	results := make(chan *OperationResult, workers)
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(13, "tc-shared"))
			results <- got
			errs <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var operationID string
	for got := range results {
		require.NotNil(t, got)
		if operationID == "" {
			operationID = got.OperationID
		}
		require.Equal(t, operationID, got.OperationID)
	}
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)
	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, operationID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stored.AttemptCount)
}

func TestOperationService_StartedWriteTimeoutIsUnknownAndNeverRetried(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.runner.steps = []operationRunnerStep{{
		result: &CLIResult{InvocationStarted: true, ExitCode: -1},
		err:    context.DeadlineExceeded,
	}}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 14, ToolCallID: "tc-write-timeout",
		IdempotencyKey: "14:tc-write-timeout",
		Argv:           []string{"docs", "+create", "--title", "报告"},
		SkillReceipts:  []string{"shared-receipt", "doc-receipt"},
	}

	got, err := h.service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationUnknown, got.State)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationUnknown, stored.State)
	require.Empty(t, stored.ResultCiphertext)
	require.NotContains(t, string(stored.ResultSummaryJSON), "timeout")
}

func TestOperationService_CodeLessStartedDocsCreateIsUnknownAndNeverAuthorizedOrRetried(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.runner.steps = []operationRunnerStep{{
		result: &CLIResult{
			InvocationStarted: true,
			ExitCode:          3,
			Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
				Type:          "authorization",
				Subtype:       "missing_scope",
				Identity:      "user",
				MissingScopes: []string{"docx:document:create"},
			}},
		},
		err: errors.New("structured code-less scope failure"),
	}}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 140, ToolCallID: "tc-code-less-create",
		IdempotencyKey: "140:tc-code-less-create",
		Argv:           []string{"docs", "+create", "--title", "报告"},
		SkillReceipts:  []string{"shared-receipt", "doc-receipt"},
	}

	got, err := h.service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationUnknown, got.State)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)
	require.Empty(t, h.recovery.snapshot())

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationUnknown, stored.State)
}

func TestOperationService_StartedWriteMissingScopeNeverEntersRecovery(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.recovery.action = &OperationAction{
		Provider: "untrusted-provider", Phase: "untrusted-phase", SessionID: "session-user",
		URL: "https://verification.example/sensitive", Scopes: []string{"untrusted-action-scope"},
	}
	h.runner.steps = []operationRunnerStep{{
		result: &CLIResult{
			InvocationStarted: true,
			ExitCode:          1,
			Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
				Type: "authorization", Subtype: "missing_scope", Code: json.RawMessage(`99991672`),
				Identity: "user", MissingScopes: []string{"docx:document:create"},
			}},
		},
		err: errors.New("raw CLI business error that must not escape"),
	}}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 15, ToolCallID: "tc-scope",
		IdempotencyKey: "15:tc-scope",
		Argv:           []string{"docs", "+create", "--title", "报告"},
		SkillReceipts:  []string{"shared-receipt", "doc-receipt"},
	}

	got, err := h.service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationUnknown, got.State)
	require.Nil(t, got.Action)
	require.Empty(t, h.recovery.snapshot())
	require.Equal(t, []bool{false, true}, h.vault.changed, "preflight is read-only; the business invocation seals")

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationUnknown, stored.State)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
	require.NotContains(t, string(stored.ResultSummaryJSON), "verification.example")
	require.NotContains(t, string(stored.ResultSummaryJSON), "raw CLI")
}

func TestOperationService_AppScopeRecoveryPassesConsoleURLTransientlyOnly(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.recovery.action = &OperationAction{SessionID: "session-app-scope", URL: "https://open.feishu.cn/app/cli_test/auth"}
	h.runner.steps = []operationRunnerStep{{
		result: &CLIResult{
			InvocationStarted: true,
			ExitCode:          1,
			Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
				Type:                 "authorization",
				Subtype:              "app_scope_not_applied",
				Identity:             "user",
				ConsoleURL:           "https://open.feishu.cn/app/cli_test/auth",
				MissingScopes:        []string{"docx:document:readonly"},
				PermissionViolations: json.RawMessage(`[{"level":"app"}]`),
			}},
		},
		err: errors.New("structured app-scope failure"),
	}}

	got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(151, "tc-app-scope"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingAppScope, got.State)
	require.Equal(t, model.FeishuAuthPhaseAppScope, got.Action.Phase)

	calls := h.recovery.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, RecoveryAppScope, calls[0].Kind)
	require.Equal(t, "https://open.feishu.cn/app/cli_test/auth", calls[0].ConsoleURL)

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.NotContains(t, string(stored.ResultSummaryJSON), "open.feishu.cn")
	require.NotContains(t, string(stored.ResultSummaryJSON), "console")
	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.JSONEq(t, `{"docs":{"state":"needs_app_scope"}}`, string(account.CapabilityStateJSON))
}

func TestOperationService_CreateAppAndReauthRecoveriesUseCatalogScopes(t *testing.T) {
	for index, testCase := range []struct {
		name      string
		errorType string
		subtype   string
		kind      RecoveryKind
		waitState string
	}{
		{name: "create app", errorType: "config", subtype: "not_configured", kind: RecoveryCreateApp, waitState: model.FeishuOperationWaitingConnection},
		{name: "reauth", errorType: "authentication", subtype: "token_missing", kind: RecoveryReauth, waitState: model.FeishuOperationWaitingUserAuth},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			h.recovery.action = &OperationAction{SessionID: "catalog-scope-recovery"}
			h.runner.steps = []operationRunnerStep{{
				result: &CLIResult{InvocationStarted: true, ExitCode: 1, Envelope: &CLIEnvelope{
					OK: false, Identity: "user", Error: &CLIError{Type: testCase.errorType, Subtype: testCase.subtype},
				}},
				err: errors.New("structured business failure"),
			}}
			runID := uint64(150 + index)
			toolCallID := "tc-catalog-scope-" + strconv.Itoa(index)
			req := ExecuteRequest{
				UserID: 7, AgentRunID: runID, ToolCallID: toolCallID,
				IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
				Argv:           []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123"}, SkillReceipts: []string{"shared", "doc"},
			}

			got, err := h.service.Execute(h.ctx, req)
			require.NoError(t, err)
			require.Equal(t, testCase.waitState, got.State)
			calls := h.recovery.snapshot()
			require.Len(t, calls, 1)
			require.Equal(t, testCase.kind, calls[0].Kind)
			require.Equal(t, []string{"docx:document:readonly"}, calls[0].Scopes)
			require.Equal(t, calls[0].Scopes, got.Action.Scopes)
		})
	}
}

func TestOperationService_ReadTransientFailureRetriesOnce(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.runner.steps = []operationRunnerStep{
		{result: &CLIResult{InvocationStarted: true, ExitCode: -1}, err: context.DeadlineExceeded},
		{result: operationOKResult(`{"document_id":"after-retry"}`)},
	}

	got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(16, "tc-read-retry"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, got.State)
	require.JSONEq(t, `{"document_id":"after-retry"}`, string(got.Data))
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls)
	require.Equal(t, []bool{true, true}, h.vault.changed)
}

func TestOperationService_ResourceACLDoesNotStartOAuthRecovery(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.runner.steps = []operationRunnerStep{{
		result: &CLIResult{
			InvocationStarted: true,
			ExitCode:          1,
			Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
				Type: "api", Subtype: "permission_denied", Code: json.RawMessage(`"RESOURCE_ACCESS_DENIED"`), Identity: "user",
			}},
		},
		err: errors.New("business error"),
	}}

	got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(17, "tc-acl"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, got.State)
	require.Empty(t, h.recovery.snapshot())
	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Contains(t, string(stored.ResultSummaryJSON), PublicCodeResourceDenied)
	account, err := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.JSONEq(t, `{"docs":{"state":"resource_denied"}}`, string(account.CapabilityStateJSON))
}

func TestOperationService_HighRiskOnlyCreatesConfirmationWaiting(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.confirmation.action = &OperationAction{
		Provider: ProviderLark, Phase: "confirmation", SessionID: "confirm-1",
		URL: "https://confirmation.example/transient", Scopes: []string{"must-copy-defensively"},
		ExpiresAt: h.service.now().Add(10 * time.Minute),
	}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 18, ToolCallID: "tc-high",
		IdempotencyKey: "18:tc-high",
		Argv: []string{
			"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "overwrite", "--content", "replacement",
		},
		SkillReceipts: []string{"shared-receipt", "doc-receipt"},
	}

	got, err := h.service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
	require.Equal(t, "https://confirmation.example/transient", got.Action.URL)
	calls, _ := h.runner.snapshot()
	require.Zero(t, calls)
	require.Len(t, h.confirmation.calls, 1)
	require.Equal(t, RiskHigh, h.confirmation.calls[0].Risk)
	require.False(t, h.confirmation.calls[0].RequiresCLIYes)
	preflightCalls, preflightScopes := h.preflight.snapshot()
	require.Equal(t, 1, preflightCalls)
	require.Equal(t, [][]string{{"docx:document:write_only", "docx:document:readonly"}}, preflightScopes)

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
	require.Empty(t, stored.ErrorType)
	require.Empty(t, stored.ErrorSubtype)
	require.NotContains(t, string(stored.ResultSummaryJSON), "confirmation.example")
	var gateCount int64
	require.NoError(t, h.db.Model(&model.FeishuOperationExecutionGate{}).Count(&gateCount).Error)
	require.Zero(t, gateCount, "ordinary high-risk confirmation must not acquire the CLI gate")

	resumed, err := h.service.Resume(h.ctx, 7, got.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, resumed.State)
	calls, _ = h.runner.snapshot()
	require.Zero(t, calls)

	t.Run("requires CLI yes remains absent before external confirmation", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		req := ExecuteRequest{
			UserID: 7, AgentRunID: 180, ToolCallID: "tc-field-update", IdempotencyKey: "180:tc-field-update",
			Argv: []string{
				"base", "+field-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks",
				"--field-id", "Status", "--json", `{"name":"Status","type":"text"}`,
			},
			SkillReceipts: []string{"shared-receipt", "base-receipt"},
		}
		waiting, err := h.service.Execute(h.ctx, req)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, waiting.State)
		require.Len(t, h.confirmation.calls, 1)
		require.True(t, h.confirmation.calls[0].RequiresCLIYes)
		stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, waiting.OperationID)
		require.NoError(t, err)
		persisted, err := h.service.openPersistedRequest(stored)
		require.NoError(t, err)
		require.NotContains(t, persisted.Argv, "--yes")
		calls, argv := h.runner.snapshot()
		require.Zero(t, calls)
		require.Empty(t, argv)
		var gateCount int64
		require.NoError(t, h.db.Model(&model.FeishuOperationExecutionGate{}).Count(&gateCount).Error)
		require.Zero(t, gateCount)
	})
}

func TestOperationService_NoConfirmationModeExecutesHighRiskWithServerYes(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)

	result, err := service.Execute(h.ctx, ExecuteRequest{
		UserID: 7, AgentRunID: 1801, ToolCallID: "tc-no-confirm-field-update",
		IdempotencyKey: "1801:tc-no-confirm-field-update",
		Argv: []string{
			"base", "+field-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks",
			"--field-id", "Status", "--json", `{"name":"处理状态","type":"text"}`,
		},
	})
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	require.Empty(t, h.confirmation.calls)
	calls, argv := h.runner.snapshot()
	require.Equal(t, 1, calls)
	require.Contains(t, argv[0], "--yes", "lark-cli acknowledgement must remain server-owned")
}

func TestOperationService_NoConfirmationModeMigratesExpiredLegacyWaitExactlyOnce(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	waiting, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(
		7, 1802, "tc-expired-legacy-confirmation", "doxcnABCDEFG123",
	))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, waiting.State)

	expired := h.service.now().Add(-time.Minute)
	summary, err := json.Marshal(persistedOperationSummary{
		Status: model.FeishuOperationWaitingConfirmation, Phase: "confirmation",
		SessionID: "expired-legacy-confirmation", ExpiresAt: &expired,
	})
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", waiting.OperationID).
		Update("result_summary_json", datatypes.JSON(summary)).Error)

	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)

	completed, err := service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, completed.State)
	idempotent, err := service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, idempotent.State)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)
}

func TestOperationService_DocsUpdateMissingScopeIsFoundBeforeBusinessInvocation(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.preflight.steps = []operationScopePreflightStep{{result: &ScopeCheckResult{
		Granted: []string{"docx:document:readonly"}, Missing: []string{"docx:document:write_only"},
	}}}

	got, err := h.service.Execute(h.ctx, operationDocsAppendRequest(7, 181, "preflight-missing", "doxcnABCDEFG123"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, got.State)
	require.Equal(t, []string{"docx:document:write_only"}, got.Action.Scopes)
	preflightCalls, scopes := h.preflight.snapshot()
	require.Equal(t, 1, preflightCalls)
	require.Equal(t, [][]string{{"docx:document:write_only", "docx:document:readonly"}}, scopes)
	businessCalls, _ := h.runner.snapshot()
	require.Zero(t, businessCalls)

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Empty(t, stored.ResultCiphertext)
	require.NotContains(t, string(stored.ResultSummaryJSON), "suggestion")
}

func TestOperationService_WritePreflightRecoveryResumesExactlyOnce(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.preflight.steps = []operationScopePreflightStep{
		{result: &ScopeCheckResult{Granted: []string{"docx:document:readonly"}, Missing: []string{"docx:document:write_only"}}},
		{result: &ScopeCheckResult{Granted: []string{"docx:document:readonly", "docx:document:write_only"}}},
	}
	waiting, err := h.service.Execute(h.ctx, operationDocsAppendRequest(7, 182, "preflight-resume", "doxcnABCDEFG123"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)
	h.recovery.action = nil // The durable authorization phase has completed.

	completed, err := h.service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, completed.State)
	preflightCalls, _ := h.preflight.snapshot()
	require.Equal(t, 2, preflightCalls)
	businessCalls, _ := h.runner.snapshot()
	require.Equal(t, 1, businessCalls)

	idempotent, err := h.service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, idempotent.State)
	preflightCalls, _ = h.preflight.snapshot()
	require.Equal(t, 2, preflightCalls)
	businessCalls, _ = h.runner.snapshot()
	require.Equal(t, 1, businessCalls)
}

func TestOperationService_RepeatedMissingWriteScopeFailsWithoutBusinessInvocation(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	missing := &ScopeCheckResult{
		Granted: []string{"docx:document:readonly"}, Missing: []string{"docx:document:write_only"},
	}
	h.preflight.steps = []operationScopePreflightStep{{result: missing}, {result: missing}}
	waiting, err := h.service.Execute(h.ctx, operationDocsAppendRequest(7, 183, "preflight-still-missing", "doxcnABCDEFG123"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)
	h.recovery.action = nil

	failed, err := h.service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, failed.State)
	preflightCalls, _ := h.preflight.snapshot()
	require.Equal(t, 2, preflightCalls)
	businessCalls, _ := h.runner.snapshot()
	require.Zero(t, businessCalls)
}

func TestOperationService_WritePreflightProtocolFailureNeverStartsBusiness(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.preflight.steps = []operationScopePreflightStep{{err: errors.New("malformed scope response with secret")}}

	failed, err := h.service.Execute(h.ctx, operationDocsAppendRequest(7, 184, "preflight-failed", "doxcnABCDEFG123"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, failed.State)
	businessCalls, _ := h.runner.snapshot()
	require.Zero(t, businessCalls)
	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, failed.OperationID)
	require.NoError(t, err)
	require.Contains(t, string(stored.ResultSummaryJSON), PublicCodeTemporaryError)
	require.NotContains(t, string(stored.ResultSummaryJSON), "secret")
}

func TestOperationService_ConfirmationDecisionExecutesExactlyOnceOrCancelsWithoutRunner(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		waiting, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 188, "confirm-execute", "doxcnABCDEFG123"))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, waiting.State)

		completed, err := h.service.Confirm(h.ctx, 7, waiting.OperationID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, completed.State)
		calls, _ := h.runner.snapshot()
		require.Equal(t, 1, calls)

		idempotent, err := h.service.Confirm(h.ctx, 7, waiting.OperationID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, idempotent.State)
		calls, _ = h.runner.snapshot()
		require.Equal(t, 1, calls, "repeat confirmation must not invoke the write twice")
	})

	t.Run("cancelled", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		waiting, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 189, "confirm-cancel", "doxcnABCDEFG123"))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, waiting.State)

		cancelled, err := h.service.Cancel(h.ctx, 7, waiting.OperationID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationCancelled, cancelled.State)
		calls, _ := h.runner.snapshot()
		require.Zero(t, calls)
	})
}

func TestOperationService_ConfirmationRequiresDurableActionIdentity(t *testing.T) {
	for name, action := range map[string]*OperationAction{
		"missing session": {
			Provider:  ProviderLark,
			Phase:     "confirmation",
			ExpiresAt: time.Date(2026, 7, 13, 12, 10, 0, 0, time.UTC),
		},
		"missing expiry": {
			Provider:  ProviderLark,
			Phase:     "confirmation",
			SessionID: "confirmation-missing-expiry",
		},
		"expired": {
			Provider:  ProviderLark,
			Phase:     "confirmation",
			SessionID: "confirmation-expired",
			ExpiresAt: time.Date(2026, 7, 13, 11, 59, 0, 0, time.UTC),
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			h.confirmation.action = action

			got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(
				7,
				181,
				"tc-invalid-confirmation",
				"doxcnABCDEFG123",
			))
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, model.FeishuOperationFailed, got.State,
				"an unpersistable confirmation action must fail before entering waiting_confirmation")
			require.Nil(t, got.Action)

			stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
			require.NoError(t, err)
			require.Equal(t, model.FeishuOperationFailed, stored.State)
			require.Empty(t, stored.LeaseOwner)
			require.Nil(t, stored.LeaseUntil)
		})
	}
}

func TestOperationService_SameRunEmptyDocsCreateProvesOverwrite(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnCreatedEmpty123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
	}

	created, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 301, "tc-create-empty", "New document", nil))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, created.State)
	overwrite, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 301, "tc-initial-overwrite", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, overwrite.State)
	require.Empty(t, h.confirmation.calls)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls)
	requirePersistedCreateProof(t, h, overwrite.OperationID, created.OperationID)
}

func TestOperationService_EmptyCreateProofCanBeConsumedByOnlyOneDistinctOperation(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnSingleUseProof123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
		{result: operationOKResult(`{"revision_id":3}`)},
	}
	_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 330, "tc-create", "Empty", nil))
	require.NoError(t, err)

	first, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 330, "tc-overwrite-1", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, first.State)
	second, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 330, "tc-overwrite-2", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, second.State)
	require.Len(t, h.confirmation.calls, 1)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls)
}

func TestOperationService_SameMillisecondLowerUUIDUpdateInvalidatesCreateProof(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnSameMillisecondProof123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
	}
	created, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 335, "tc-create", "Empty", nil))
	require.NoError(t, err)
	sameMillisecond := time.Date(2026, 7, 13, 12, 0, 0, 123000000, time.UTC)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", created.OperationID).
		Update("created_at", sameMillisecond).Error)
	intermediate := &model.FeishuOperation{
		ID: "00000000-0000-4000-8000-000000000000", UserID: 7, Generation: 1,
		AgentRunID: 335, ToolCallID: "tc-intermediate", IdempotencyKey: "335:tc-intermediate",
		CommandPath: "docs +update", Domain: "docs", RiskLevel: string(RiskHigh),
		RequestCiphertext: []byte("opaque-intermediate-request"), KeyVersion: "v1",
		RequestFingerprint: "opaque-intermediate-fingerprint", State: model.FeishuOperationSucceeded,
		CreatedAt: sameMillisecond, UpdatedAt: sameMillisecond,
	}
	require.Less(t, intermediate.ID, created.OperationID, "the later UUID must sort before the source UUID")
	require.NoError(t, h.db.Create(intermediate).Error)

	overwrite, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 335, "tc-overwrite", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, overwrite.State)
	require.Len(t, h.confirmation.calls, 1)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)
}

func TestOperationService_ExecutingIntermediateUpdateInvalidatesCreateProof(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnExecutingIntermediate123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
	}
	created, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 336, "tc-create", "Empty", nil))
	require.NoError(t, err)
	storedSource, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, created.OperationID)
	require.NoError(t, err)

	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	barrierStore := &proofQueryBarrierOperationStore{
		OperationStore: h.dataStore.FeishuWorkspace(), arrived: arrived, release: release,
	}
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: barrierStore,
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	resultCh := make(chan *OperationResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, executeErr := service.Execute(h.ctx, operationDocsOverwriteRequest(7, 336, "tc-overwrite", token))
		resultCh <- result
		errCh <- executeErr
	}()
	<-arrived
	intermediate := &model.FeishuOperation{
		ID: "00000000-0000-4000-8000-000000000000", UserID: 7, Generation: 1,
		AgentRunID: 336, ToolCallID: "tc-intermediate", IdempotencyKey: "336:tc-intermediate",
		CommandPath: "docs +update", Domain: "docs", RiskLevel: string(RiskHigh),
		RequestCiphertext: []byte("opaque-intermediate-request"), KeyVersion: "v1",
		RequestFingerprint: "opaque-intermediate-fingerprint", State: model.FeishuOperationExecuting,
		CreatedAt: storedSource.CreatedAt.Add(time.Millisecond), UpdatedAt: storedSource.CreatedAt.Add(time.Millisecond),
	}
	require.NoError(t, h.db.Create(intermediate).Error)
	close(release)

	overwrite := <-resultCh
	require.NoError(t, <-errCh)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, overwrite.State)
	require.Len(t, h.confirmation.calls, 1)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)
}

func TestOperationService_ProofReservationPausedThenAppendSucceedsForcesConfirmation(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnGateRecheck123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
		{result: operationOKResult(`{"revision_id":3}`)},
	}
	_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 337, "tc-create", "Empty", nil))
	require.NoError(t, err)

	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	overwriteStore := &gateClaimBarrierOperationStore{
		IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace(), arrived: arrived, release: release,
	}
	overwriteService := newHarnessOperationService(t, h, overwriteStore)
	appendService := newHarnessOperationService(t, h, h.dataStore.FeishuWorkspace())
	type callResult struct {
		result *OperationResult
		err    error
	}
	overwriteCh := make(chan callResult, 1)
	go func() {
		result, executeErr := overwriteService.Execute(h.ctx, operationDocsOverwriteRequest(7, 337, "tc-overwrite", token))
		overwriteCh <- callResult{result: result, err: executeErr}
	}()
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("overwrite did not pause after proof reservation and before gate claim")
	}
	appendResult, err := appendService.Execute(h.ctx, operationDocsAppendRequest(7, 337, "tc-append", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, appendResult.State)
	close(release)
	released = true
	overwrite := <-overwriteCh
	require.NoError(t, overwrite.err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, overwrite.result.State)
	require.Len(t, h.confirmation.calls, 1)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls, "the overwrite runner must not start after proof invalidation")
}

func TestOperationService_ProofConsumerHoldingGateRunsBeforeLaterAppend(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnGateOrder123"
	overwriteStarted := make(chan struct{}, 1)
	overwriteRelease := make(chan struct{})
	appendStarted := make(chan struct{}, 1)
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`), started: overwriteStarted, release: overwriteRelease},
		{result: operationOKResult(`{"revision_id":3}`), started: appendStarted},
	}
	_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 338, "tc-create", "Empty", nil))
	require.NoError(t, err)
	overwriteService := newHarnessOperationService(t, h, h.dataStore.FeishuWorkspace())
	appendService := newHarnessOperationService(t, h, h.dataStore.FeishuWorkspace())
	type callResult struct {
		result *OperationResult
		err    error
	}
	overwriteCh := make(chan callResult, 1)
	appendCh := make(chan callResult, 1)
	go func() {
		result, executeErr := overwriteService.Execute(h.ctx, operationDocsOverwriteRequest(7, 338, "tc-overwrite", token))
		overwriteCh <- callResult{result: result, err: executeErr}
	}()
	select {
	case <-overwriteStarted:
	case <-time.After(time.Second):
		t.Fatal("proof overwrite runner did not start")
	}
	go func() {
		result, executeErr := appendService.Execute(h.ctx, operationDocsAppendRequest(7, 338, "tc-append", token))
		appendCh <- callResult{result: result, err: executeErr}
	}()
	ranBeforeRelease := false
	select {
	case <-appendStarted:
		ranBeforeRelease = true
	case <-time.After(150 * time.Millisecond):
	}
	close(overwriteRelease)
	overwrite := <-overwriteCh
	appendResult := <-appendCh
	require.False(t, ranBeforeRelease, "append runner must wait for the proof consumer's gate")
	require.NoError(t, overwrite.err)
	require.Equal(t, model.FeishuOperationSucceeded, overwrite.result.State)
	require.NoError(t, appendResult.err)
	require.Equal(t, model.FeishuOperationSucceeded, appendResult.result.State)
}

func TestOperationService_ConnectionWaitThenAppendInvalidatesProofBeforeResume(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnResumeGateRecheck123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
		{result: operationOKResult(`{"revision_id":3}`)},
	}
	_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 339, "tc-create", "Empty", nil))
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{"connection_state": model.FeishuConnectionNone, "connected": false}).Error)
	waiting, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 339, "tc-overwrite", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, waiting.State)
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{"connection_state": model.FeishuConnectionConnected, "connected": true}).Error)
	appendResult, err := h.service.Execute(h.ctx, operationDocsAppendRequest(7, 339, "tc-append", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, appendResult.State)
	h.recovery.action = nil

	restartedService := newHarnessOperationService(t, h, h.dataStore.FeishuWorkspace())
	resumed, err := restartedService.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, resumed.State)
	require.Len(t, h.confirmation.calls, 1)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls)
}

func TestOperationService_UpdateCreatedBeforeCreateButStartedAfterFinishInvalidatesOverwriteProof(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionNone, 1, "")
	const (
		existingToken = "doxcnExistingBeforeCreate123"
		createdToken  = "doxcnCreatedAfterWait123"
	)
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + createdToken + `","url":"https://acme.feishu.cn/docx/` + createdToken + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
		{result: operationOKResult(`{"revision_id":3}`)},
	}

	waiting, err := h.service.Execute(h.ctx, operationDocsAppendRequest(7, 340, "tc-append-waiting", existingToken))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, waiting.State)
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{
			"connection_state": model.FeishuConnectionConnected,
			"connected":        true,
			"app_id":           "cli_existing",
		}).Error)

	created, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 340, "tc-create", "Empty", nil))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, created.State)
	h.recovery.action = nil
	resumed, err := h.service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, resumed.State)

	storedWaiting, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, waiting.OperationID)
	require.NoError(t, err)
	storedCreated, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, created.OperationID)
	require.NoError(t, err)
	require.True(t, storedWaiting.CreatedAt.Before(storedCreated.CreatedAt), "the waiting update must predate the create row")
	require.NotNil(t, storedWaiting.StartedAt)
	require.NotNil(t, storedCreated.FinishedAt)
	require.False(t, storedWaiting.StartedAt.Before(*storedCreated.FinishedAt), "the resumed update starts on or after the create execution boundary")

	overwrite, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 340, "tc-overwrite", createdToken))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, overwrite.State)
	require.Len(t, h.confirmation.calls, 1)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls, "the unsafe overwrite runner must not start")
}

func TestOperationService_ClockSkewedResumedUpdateInvalidatesOverwriteProof(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionNone, 1, "")
	const (
		existingToken = "doxcnClockSkewExisting123"
		createdToken  = "doxcnClockSkewCreated123"
	)
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + createdToken + `","url":"https://acme.feishu.cn/docx/` + createdToken + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
		{result: operationOKResult(`{"revision_id":3}`)},
	}
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	slowService := newHarnessOperationServiceWithRuntime(
		t, h, h.dataStore.FeishuWorkspace(), h.runner,
		func() time.Time { return base.Add(-2 * time.Hour) }, 0,
	)
	fastService := newHarnessOperationServiceWithRuntime(
		t, h, h.dataStore.FeishuWorkspace(), h.runner,
		func() time.Time { return base.Add(2 * time.Hour) }, 0,
	)

	waiting, err := slowService.Execute(h.ctx, operationDocsAppendRequest(7, 351, "tc-skewed-append", existingToken))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, waiting.State)
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).
		Where("id = ?", waiting.OperationID).
		Update("created_at", base.Add(-3*time.Hour)).Error)
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{
			"connection_state": model.FeishuConnectionConnected,
			"connected":        true,
			"app_id":           "cli_existing",
		}).Error)

	created, err := fastService.Execute(h.ctx, operationDocsCreateRequest(7, 351, "tc-skewed-create", "Empty", nil))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, created.State)
	h.recovery.action = nil
	resumed, err := slowService.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, resumed.State)

	storedWaiting, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, waiting.OperationID)
	require.NoError(t, err)
	storedCreated, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, created.OperationID)
	require.NoError(t, err)
	require.NotNil(t, storedWaiting.StartedAt)
	require.NotNil(t, storedCreated.FinishedAt)
	require.True(t, storedWaiting.CreatedAt.Before(*storedCreated.FinishedAt))
	require.True(t, storedWaiting.StartedAt.Before(*storedCreated.FinishedAt), "slow service clock must reproduce the timestamp inversion")

	overwrite, err := fastService.Execute(h.ctx, operationDocsOverwriteRequest(7, 351, "tc-skewed-overwrite", createdToken))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, overwrite.State)
	require.Len(t, h.confirmation.calls, 1)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls, "clock skew must not let the overwrite runner start")
}

func TestOperationService_CancelledGateWaitLeavesOperationUnclaimed(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	now := h.service.now().UTC()
	claimed, err := h.dataStore.FeishuWorkspace().TryClaimExecutionGate(
		h.ctx, 7, 1, "crashed-owner", "crashed-operation", now, now.Add(2*time.Minute),
	)
	require.NoError(t, err)
	require.True(t, claimed)
	attempted := make(chan struct{}, 1)
	waitStore := &gateAttemptSignalOperationStore{
		IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace(), attempted: attempted,
	}
	service := newHarnessOperationService(t, h, waitStore)
	ctx, cancel := context.WithCancel(h.ctx)
	type callResult struct {
		result *OperationResult
		err    error
	}
	resultCh := make(chan callResult, 1)
	go func() {
		result, executeErr := service.Execute(ctx, operationDocsFetchRequest(340, "tc-gate-cancel"))
		resultCh <- callResult{result: result, err: executeErr}
	}()
	select {
	case <-attempted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("operation did not attempt the occupied execution gate")
	}
	cancel()
	got := <-resultCh
	require.Nil(t, got.result)
	require.ErrorIs(t, got.err, context.Canceled)
	var operation model.FeishuOperation
	require.NoError(t, h.db.Where("user_id = ? AND idempotency_key = ?", 7, "340:tc-gate-cancel").Take(&operation).Error)
	require.Equal(t, model.FeishuOperationNotStarted, operation.State)
	require.Zero(t, operation.AttemptCount)
	require.Empty(t, operation.LeaseOwner)
	require.Nil(t, operation.LeaseUntil)
	var gate model.FeishuOperationExecutionGate
	require.NoError(t, h.db.First(&gate, "user_id = ?", 7).Error)
	require.Equal(t, "crashed-owner", gate.LeaseOwner)
	require.Equal(t, "crashed-operation", gate.OperationID)
	calls, _ := h.runner.snapshot()
	require.Zero(t, calls)
}

func TestOperationService_GenerationBumpWhileWaitingRejectsOldOperationBeforeClaim(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	barrierStore := &gateClaimBarrierOperationStore{
		IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace(), arrived: arrived, release: release,
	}
	service := newHarnessOperationService(t, h, barrierStore)
	type callResult struct {
		result *OperationResult
		err    error
	}
	resultCh := make(chan callResult, 1)
	go func() {
		result, executeErr := service.Execute(h.ctx, operationDocsFetchRequest(342, "tc-generation-gate"))
		resultCh <- callResult{result: result, err: executeErr}
	}()
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("operation did not reach execution gate")
	}
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Update("generation", 2).Error)
	close(release)
	got := <-resultCh
	require.Nil(t, got.result)
	require.ErrorIs(t, got.err, ErrOperationUnavailable)
	var operation model.FeishuOperation
	require.NoError(t, h.db.Where("user_id = ? AND idempotency_key = ?", 7, "342:tc-generation-gate").Take(&operation).Error)
	require.Equal(t, model.FeishuOperationNotStarted, operation.State)
	require.Empty(t, operation.LeaseOwner)
	require.Nil(t, operation.LeaseUntil)
	var gateCount int64
	require.NoError(t, h.db.Model(&model.FeishuOperationExecutionGate{}).Count(&gateCount).Error)
	require.Zero(t, gateCount)
	calls, _ := h.runner.snapshot()
	require.Zero(t, calls)
}

func TestOperationServiceRetiredExecutionRegistryRejectsLateOldGenerationRegistration(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 4, "cli_existing")
	retiredGeneration, _, err := h.dataStore.ThirdPartyAccounts().RetireGeneration(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.Equal(t, uint64(4), retiredGeneration)
	require.NoError(t, h.service.StopGenerationAndWait(context.Background(), 7, retiredGeneration))
	h.service.executions.mu.Lock()
	_, retiredStillTracked := h.service.executions.retired[7]
	require.Empty(t, h.service.executions.active)
	require.Empty(t, h.service.executions.starts)
	h.service.executions.mu.Unlock()
	require.False(t, retiredStillTracked, "completed execution joins must reclaim the per-user retired tombstone")

	guard, err := h.service.startExecutionGateGuard(context.Background(), &model.FeishuOperation{
		ID: "late-retired-operation", UserID: 7, Generation: retiredGeneration,
	}, "late-owner")
	require.Nil(t, guard)
	require.ErrorIs(t, err, ErrOperationUnavailable)
	calls, _ := h.runner.snapshot()
	require.Zero(t, calls, "a late old-generation registration must not reach the runner")
}

func TestOperationServiceStopGenerationAcceptsOnlyExpiredCrossInstanceGate(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 4, "cli_existing")
	now := h.service.now().UTC()
	claimed, err := h.dataStore.FeishuWorkspace().TryClaimExecutionGate(
		h.ctx, 7, 4, "other-instance", "remote-operation", now.Add(-2*time.Minute), now.Add(-time.Minute),
	)
	require.NoError(t, err)
	require.True(t, claimed)
	retiredGeneration, _, err := h.dataStore.ThirdPartyAccounts().RetireGeneration(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.NoError(t, h.service.StopGenerationAndWait(context.Background(), 7, retiredGeneration), "an expired remote gate is the bounded crash fallback")
}

func TestOperationService_ReleaseFailureKeepsTerminalResultAndExpiresSafely(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	service := newHarnessOperationService(t, h, &releaseFailOperationStore{
		IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace(),
	})

	result, err := service.Execute(h.ctx, operationDocsFetchRequest(341, "tc-release-failure"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, result.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, stored.State)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
	var gate model.FeishuOperationExecutionGate
	require.NoError(t, h.db.First(&gate, "user_id = ?", 7).Error)
	require.NotEmpty(t, gate.LeaseOwner, "failed release leaves only the bounded gate lease")

	crashRecoveryNow := h.service.now().UTC().Add(operationExecutionGateLeaseDuration + time.Second)
	claimed, err := h.dataStore.FeishuWorkspace().TryClaimExecutionGate(
		h.ctx, 7, 1, "recovery-owner", "recovery-operation",
		crashRecoveryNow, crashRecoveryNow.Add(operationExecutionGateLeaseDuration),
	)
	require.NoError(t, err)
	require.True(t, claimed)
}

func TestOperationService_StaleOverwriteCannotStartRunnerAfterAnotherServiceTakesExpiredGate(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnExpiredGateOverwrite123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"document_id":"service-b"}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
	}

	clockMu := sync.Mutex{}
	clockNow := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clockNow
	}
	advance := func(value time.Time) {
		clockMu.Lock()
		clockNow = value
		clockMu.Unlock()
	}
	newService := func() *FeishuOperationService {
		service, err := NewFeishuOperationService(OperationServiceDeps{
			Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
			Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
			Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
			Now: now, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		return service
	}
	serviceA := newService()
	serviceB := newService()
	_, err := serviceA.Execute(h.ctx, operationDocsCreateRequest(7, 343, "tc-create", "Empty", nil))
	require.NoError(t, err)

	vaultArrived := make(chan struct{}, 1)
	vaultRelease := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(vaultRelease)
		}
	}()
	callbackMu := sync.Mutex{}
	callbackCount := 0
	h.vault.mu.Lock()
	h.vault.beforeCallback = func() {
		callbackMu.Lock()
		callbackCount++
		call := callbackCount
		callbackMu.Unlock()
		if call == 1 {
			vaultArrived <- struct{}{}
			<-vaultRelease
		}
	}
	h.vault.mu.Unlock()

	type callResult struct {
		result *OperationResult
		err    error
	}
	staleResult := make(chan callResult, 1)
	go func() {
		result, executeErr := serviceA.Execute(h.ctx, operationDocsOverwriteRequest(7, 343, "tc-overwrite", token))
		staleResult <- callResult{result: result, err: executeErr}
	}()
	select {
	case <-vaultArrived:
	case <-time.After(time.Second):
		t.Fatal("service A did not reach the unpacked vault before runner start")
	}
	advance(now().Add(operationExecutionGateLeaseDuration + time.Second))

	serviceBResult, err := serviceB.Execute(h.ctx, operationDocsFetchRequest(344, "tc-service-b"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, serviceBResult.State)
	close(vaultRelease)
	released = true
	stale := <-staleResult
	if stale.err == nil {
		require.NotNil(t, stale.result)
		require.NotEqual(t, model.FeishuOperationSucceeded, stale.result.State)
	}
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls, "service A must synchronously revalidate the gate after vault unpack and never start its overwrite")
}

func TestOperationService_ExecutionGateHeartbeatExtendsLeaseWhileRunnerIsActive(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h.runner.steps = []operationRunnerStep{{
		result: operationOKResult(`{"document_id":"heartbeat"}`), started: started, release: release,
	}}
	renewed := make(chan struct{}, 8)
	trackingStore := &renewalTrackingOperationStore{
		IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace(), renewed: renewed,
	}
	service := newHarnessOperationServiceWithRuntime(
		t, h, trackingStore, h.runner, time.Now, 10*time.Millisecond,
	)
	type callResult struct {
		result *OperationResult
		err    error
	}
	resultCh := make(chan callResult, 1)
	go func() {
		result, err := service.Execute(h.ctx, operationDocsFetchRequest(345, "tc-heartbeat"))
		resultCh <- callResult{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start after the synchronous renewal")
	}
	for index := 0; index < 2; index++ {
		select {
		case <-renewed:
		case <-time.After(time.Second):
			t.Fatal("execution gate heartbeat did not renew the active lease")
		}
	}
	close(release)
	got := <-resultCh
	require.NoError(t, got.err)
	require.Equal(t, model.FeishuOperationSucceeded, got.result.State)
	calls := trackingStore.snapshot()
	require.GreaterOrEqual(t, len(calls), 2)
	require.True(t, calls[1].leaseUntil.After(calls[0].leaseUntil), "heartbeat must extend, not merely rewrite, the lease")
}

func TestOperationService_StopsAndDrainsHeartbeatBeforeReleasingGate(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	runnerStarted := make(chan struct{}, 1)
	runnerRelease := make(chan struct{})
	h.runner.steps = []operationRunnerStep{{
		result: operationOKResult(`{"document_id":"drained"}`), started: runnerStarted, release: runnerRelease,
	}}
	drainingStore := &drainingRenewalOperationStore{
		IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace(),
		heartbeatStarted:      make(chan struct{}, 1),
		heartbeatReturned:     make(chan struct{}),
	}
	service := newHarnessOperationServiceWithRuntime(
		t, h, drainingStore, h.runner, time.Now, 10*time.Millisecond,
	)
	type callResult struct {
		result *OperationResult
		err    error
	}
	resultCh := make(chan callResult, 1)
	go func() {
		result, err := service.Execute(h.ctx, operationDocsFetchRequest(350, "tc-heartbeat-drain"))
		resultCh <- callResult{result: result, err: err}
	}()
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	select {
	case <-drainingStore.heartbeatStarted:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not enter the blocking renewal")
	}
	close(runnerRelease)
	got := <-resultCh
	require.NoError(t, got.err)
	require.Equal(t, model.FeishuOperationSucceeded, got.result.State)
	select {
	case <-drainingStore.heartbeatReturned:
	default:
		t.Fatal("service returned before the heartbeat goroutine drained")
	}
	require.False(t, drainingStore.releasedBeforeHeartbeatDrain(), "gate release must happen only after heartbeat join")
}

func TestOperationService_ExecutionGateHeartbeatLossCancelsActiveRunner(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	renewed := make(chan struct{}, 8)
	trackingStore := &renewalTrackingOperationStore{
		IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace(), failAt: 3, renewed: renewed,
	}
	runner := &contextBlockingOperationRunner{started: make(chan struct{}, 1)}
	service := newHarnessOperationServiceWithRuntime(
		t, h, trackingStore, runner, time.Now, 10*time.Millisecond,
	)
	type callResult struct {
		result *OperationResult
		err    error
	}
	resultCh := make(chan callResult, 1)
	go func() {
		result, err := service.Execute(h.ctx, operationDocsCreateRequest(7, 346, "tc-heartbeat-loss", "Report", nil))
		resultCh <- callResult{result: result, err: err}
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start after the first renewal")
	}
	select {
	case got := <-resultCh:
		if got.err == nil {
			require.NotNil(t, got.result)
			require.NotEqual(t, model.FeishuOperationSucceeded, got.result.State)
		}
	case <-time.After(time.Second):
		t.Fatal("lost execution gate did not cancel the active runner")
	}
	require.Equal(t, 1, runner.callCount())
	require.GreaterOrEqual(t, len(trackingStore.snapshot()), 2)
}

func TestOperationService_PreRunRenewFailureNeverStartsRunner(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{name: "lease lost"},
		{name: "store error", err: errors.New("renew failed")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			trackingStore := &renewalTrackingOperationStore{
				IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace(), failAt: 1, failErr: testCase.err,
			}
			service := newHarnessOperationServiceWithRuntime(
				t, h, trackingStore, h.runner, time.Now, 0,
			)

			result, err := service.Execute(h.ctx, operationDocsFetchRequest(347, "tc-prerun-renew"))
			if err == nil {
				require.NotNil(t, result)
				require.NotEqual(t, model.FeishuOperationSucceeded, result.State)
			}
			calls, _ := h.runner.snapshot()
			require.Zero(t, calls)
			require.Len(t, trackingStore.snapshot(), 1)
		})
	}
}

func TestOperationService_ReadRetryRenewsGateBeforeEveryInvocation(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.runner.steps = []operationRunnerStep{
		{result: &CLIResult{InvocationStarted: true, ExitCode: -1}, err: context.DeadlineExceeded},
		{result: operationOKResult(`{"document_id":"after-renewed-retry"}`)},
	}
	trackingStore := &renewalTrackingOperationStore{IFeishuWorkspaceStore: h.dataStore.FeishuWorkspace()}
	service := newHarnessOperationServiceWithRuntime(
		t, h, trackingStore, h.runner, time.Now, 0,
	)

	result, err := service.Execute(h.ctx, operationDocsFetchRequest(348, "tc-renewed-retry"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	require.Len(t, trackingStore.snapshot(), 2, "each read invocation, including the retry, must renew synchronously")
}

func TestOperationService_RunnerDeadlineIsStrictlyInsideRenewedGateLease(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	runner := &deadlineRecordingOperationRunner{deadline: make(chan time.Time, 1)}
	service := newHarnessOperationServiceWithRuntime(
		t, h, h.dataStore.FeishuWorkspace(), runner, time.Now, 0,
	)

	result, err := service.Execute(h.ctx, operationDocsFetchRequest(349, "tc-runner-deadline"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	deadline := <-runner.deadline
	require.False(t, deadline.IsZero(), "service must impose a hard runner deadline")
	remaining := time.Until(deadline)
	require.Positive(t, remaining)
	require.LessOrEqual(t, remaining, ControlledLarkCLITimeout+time.Second)
	require.Less(t, ControlledLarkCLITimeout, operationExecutionGateLeaseDuration)
}

func TestOperationService_ExecutionGateLeaseCoversMaximumReadRuntime(t *testing.T) {
	require.GreaterOrEqual(
		t,
		operationExecutionGateLeaseDuration,
		2*ControlledLarkCLITimeout+operationExecutionGateOverhead,
	)
	require.Greater(t, operationExecutionGateWaitTimeout, operationExecutionGateLeaseDuration)
}

func TestOperationService_ConcurrentDistinctOverwritesAtomicallyConsumeOneProof(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	barrierStore := &proofQueryBarrierOperationStore{
		OperationStore: h.dataStore.FeishuWorkspace(), arrived: arrived, release: release,
	}
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: barrierStore,
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	const token = "doxcnConcurrentProof123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
		{result: operationOKResult(`{"revision_id":3}`)},
	}
	_, err = service.Execute(h.ctx, operationDocsCreateRequest(7, 331, "tc-create", "Empty", nil))
	require.NoError(t, err)

	requests := []ExecuteRequest{
		operationDocsOverwriteRequest(7, 331, "tc-overwrite-a", token),
		operationDocsOverwriteRequest(7, 331, "tc-overwrite-b", token),
	}
	results := make(chan *OperationResult, len(requests))
	errs := make(chan error, len(requests))
	var wg sync.WaitGroup
	for index := range requests {
		request := requests[index]
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, executeErr := service.Execute(h.ctx, request)
			results <- result
			errs <- executeErr
		}()
	}
	<-arrived
	<-arrived
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for executeErr := range errs {
		require.NoError(t, executeErr)
	}
	states := make([]string, 0, len(requests))
	for result := range results {
		require.NotNil(t, result)
		states = append(states, result.State)
	}
	require.ElementsMatch(t, []string{model.FeishuOperationWaitingConfirmation, model.FeishuOperationWaitingConfirmation}, states)
	require.Len(t, h.confirmation.calls, 2)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls, "competing overwrite rows invalidate the proof before either runner starts")
}

func TestOperationService_ConcurrentSameIdempotencyReusesOneProofConsumer(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnSharedConsumer123"
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`), started: started, release: release},
	}
	source, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 333, "tc-create", "Empty", nil))
	require.NoError(t, err)
	request := operationDocsOverwriteRequest(7, 333, "tc-overwrite-shared", token)

	const workers = 20
	results := make(chan *OperationResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, executeErr := h.service.Execute(h.ctx, request)
			results <- result
			errs <- executeErr
		}()
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("proof consumer runner did not start")
	}
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for executeErr := range errs {
		require.NoError(t, executeErr)
	}
	var consumerID string
	for result := range results {
		require.NotNil(t, result)
		if consumerID == "" {
			consumerID = result.OperationID
		}
		require.Equal(t, consumerID, result.OperationID)
	}
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls)
	bound, err := h.dataStore.FeishuWorkspace().IsOperationProofUsable(h.ctx, 7, 1, 333, source.OperationID, consumerID)
	require.NoError(t, err)
	require.True(t, bound)
}

func TestOperationService_ResumeCannotTrustUnboundEncryptedProof(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	storeWithoutBinding := &unboundProofOperationStore{OperationStore: h.dataStore.FeishuWorkspace()}
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: storeWithoutBinding,
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	const token = "doxcnUnboundProof123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
	}
	created, err := service.Execute(h.ctx, operationDocsCreateRequest(7, 332, "tc-create", "Empty", nil))
	require.NoError(t, err)
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{"connection_state": model.FeishuConnectionNone, "connected": false}).Error)

	waiting, err := service.Execute(h.ctx, operationDocsOverwriteRequest(7, 332, "tc-overwrite", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, waiting.State)
	bound, err := h.dataStore.FeishuWorkspace().IsOperationProofUsable(
		h.ctx, 7, 1, 332, created.OperationID, waiting.OperationID,
	)
	require.NoError(t, err)
	require.True(t, bound, "authorization waiting must not release a consumed proof")
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{"connection_state": model.FeishuConnectionConnected, "connected": true}).Error)
	h.recovery.action = nil

	resumed, err := service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, resumed.State)
	require.Len(t, h.confirmation.calls, 1)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)
}

func TestOperationService_FailedProofConsumerDoesNotReleaseProof(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	const token = "doxcnFailedConsumer123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: &CLIResult{InvocationStarted: false, ExitCode: -1}, err: errors.New("runner start failed")},
	}
	created, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 334, "tc-create", "Empty", nil))
	require.NoError(t, err)
	failed, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 334, "tc-overwrite", token))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, failed.State)

	bound, err := h.dataStore.FeishuWorkspace().IsOperationProofUsable(
		h.ctx, 7, 1, 334, created.OperationID, failed.OperationID,
	)
	require.NoError(t, err)
	require.True(t, bound, "terminal failure must not release a consumed proof")
}

func TestOperationService_RepeatedOverwriteUsesPersistedProofWhenCandidateWindowChanges(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	proofStore := &disappearingProofOperationStore{OperationStore: h.dataStore.FeishuWorkspace()}
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: proofStore,
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	const token = "doxcnStableProof123"
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
		{result: operationOKResult(`{"revision_id":2}`)},
	}
	_, err = service.Execute(h.ctx, operationDocsCreateRequest(7, 305, "tc-create", "Empty", nil))
	require.NoError(t, err)
	overwriteRequest := operationDocsOverwriteRequest(7, 305, "tc-overwrite", token)
	first, err := service.Execute(h.ctx, overwriteRequest)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, first.State)

	repeated, err := service.Execute(h.ctx, overwriteRequest)
	require.NoError(t, err)
	require.Equal(t, first.OperationID, repeated.OperationID)
	require.Equal(t, model.FeishuOperationSucceeded, repeated.State)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls)
}

func TestOperationService_SameRunWikiDocxCreateProvesOverwriteByNodeOrObjectToken(t *testing.T) {
	for index, target := range []string{"wikcnCreatedNode123", "doxcnCreatedObject123"} {
		t.Run(target, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			h.runner.steps = []operationRunnerStep{
				{result: operationOKResult(`{"node_token":"wikcnCreatedNode123","obj_token":"doxcnCreatedObject123","obj_type":"docx"}`)},
				{result: operationOKResult(`{"revision_id":2}`)},
			}
			runID := uint64(302 + index)
			createRequest := ExecuteRequest{
				UserID: 7, AgentRunID: runID, ToolCallID: "tc-wiki-create", IdempotencyKey: fmt.Sprintf("%d:tc-wiki-create", runID),
				Argv: []string{
					"wiki", "+node-create", "--space-id", "my_library", "--title", "New wiki document",
					"--node-type", "origin", "--obj-type", "docx",
				},
				SkillReceipts: []string{"shared", "wiki"},
			}
			created, err := h.service.Execute(h.ctx, createRequest)
			require.NoError(t, err)
			require.Equal(t, model.FeishuOperationSucceeded, created.State)

			overwrite, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, runID, "tc-wiki-overwrite", target))
			require.NoError(t, err)
			require.Equal(t, model.FeishuOperationSucceeded, overwrite.State)
			require.Empty(t, h.confirmation.calls)
			calls, _ := h.runner.snapshot()
			require.Equal(t, 2, calls)
			requirePersistedCreateProof(t, h, overwrite.OperationID, created.OperationID)
		})
	}
}

func TestOperationService_SameRunCreateProofAcceptsSupportedDocumentURLs(t *testing.T) {
	t.Run("docs docx URL", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		const token = "doxcnCreatedURL123"
		h.runner.steps = []operationRunnerStep{
			{result: operationOKResult(`{"document":{"document_id":"` + token + `","url":"https://acme.feishu.cn/docx/` + token + `"}}`)},
			{result: operationOKResult(`{"revision_id":2}`)},
		}
		created, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 306, "tc-create", "Empty", nil))
		require.NoError(t, err)
		overwrite, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(
			7, 306, "tc-overwrite", "https://acme.feishu.cn/docx/"+token,
		))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, overwrite.State)
		require.Empty(t, h.confirmation.calls)
		requirePersistedCreateProof(t, h, overwrite.OperationID, created.OperationID)
	})

	t.Run("wiki node URL", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		const nodeToken = "wikcnCreatedURL123"
		h.runner.steps = []operationRunnerStep{
			{result: operationOKResult(`{"node_token":"` + nodeToken + `","obj_token":"doxcnCreatedURLObject123","obj_type":"docx"}`)},
			{result: operationOKResult(`{"revision_id":2}`)},
		}
		createRequest := ExecuteRequest{
			UserID: 7, AgentRunID: 307, ToolCallID: "tc-wiki-create", IdempotencyKey: "307:tc-wiki-create",
			Argv: []string{
				"wiki", "+node-create", "--space-id", "my_library", "--title", "Empty",
				"--node-type", "origin", "--obj-type", "docx",
			},
			SkillReceipts: []string{"shared", "wiki"},
		}
		created, err := h.service.Execute(h.ctx, createRequest)
		require.NoError(t, err)
		overwrite, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(
			7, 307, "tc-overwrite", "https://acme.larksuite.com/wiki/"+nodeToken,
		))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, overwrite.State)
		require.Empty(t, h.confirmation.calls)
		requirePersistedCreateProof(t, h, overwrite.OperationID, created.OperationID)
	})
}

func TestOperationService_OverwriteRejectsMaliciousDocumentURLHosts(t *testing.T) {
	for index, target := range []string{
		"https://evil.example/docx/doxcnTarget123",
		"https://acme.feishu.cn.evil.example/wiki/wikcnTarget123",
	} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			_, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, uint64(308+index), "tc-overwrite", target))
			require.ErrorIs(t, err, ErrOperationRequestRejected)
			require.Empty(t, h.confirmation.calls)
			calls, _ := h.runner.snapshot()
			require.Zero(t, calls)
			var count int64
			require.NoError(t, h.db.Model(&model.FeishuOperation{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestOperationService_OverwriteProofRequiresExactPersistedSucceededCreate(t *testing.T) {
	const token = "doxcnProofTarget123"

	for index, malformedResult := range []string{
		`{"document_id":"` + token + `"}`,
		`{"document":"wrong-type"}`,
		`{"document":{"document_id":123}}`,
	} {
		t.Run("malformed docs result "+strconv.Itoa(index), func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-existing")
			h.runner.steps = []operationRunnerStep{{result: operationOKResult(malformedResult)}}
			runID := uint64(318 + index)
			_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, runID, "tc-create", "Empty", nil))
			require.NoError(t, err)
			got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, runID, "tc-overwrite", token))
			require.NoError(t, err)
			require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
			require.Len(t, h.confirmation.calls, 1)
		})
	}

	t.Run("different user", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-user-7")
		h.createAccount(8, model.FeishuConnectionConnected, 1, "cli-user-8")
		h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"` + token + `"}`)}}
		_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 310, "tc-create", "Empty", nil))
		require.NoError(t, err)
		got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(8, 310, "tc-overwrite", token))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
		require.Len(t, h.confirmation.calls, 1)
	})

	t.Run("different generation", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-existing")
		h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"` + token + `"}`)}}
		_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 311, "tc-create", "Empty", nil))
		require.NoError(t, err)
		require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ?", 7, ProviderLark).Update("generation", 2).Error)
		got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 311, "tc-overwrite", token))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
		require.Len(t, h.confirmation.calls, 1)
	})

	t.Run("different run", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-existing")
		h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"` + token + `"}`)}}
		_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 312, "tc-create", "Empty", nil))
		require.NoError(t, err)
		got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 313, "tc-overwrite", token))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
		require.Len(t, h.confirmation.calls, 1)
	})

	t.Run("non create operation", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-existing")
		h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"` + token + `"}`)}}
		fetch := operationDocsFetchRequest(314, "tc-fetch")
		fetch.Argv = []string{"docs", "+fetch", "--doc", token}
		_, err := h.service.Execute(h.ctx, fetch)
		require.NoError(t, err)
		got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 314, "tc-overwrite", token))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
		require.Len(t, h.confirmation.calls, 1)
	})

	t.Run("create already has content", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-existing")
		h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"` + token + `"}`)}}
		content := "existing body"
		_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 315, "tc-create", "Not empty", &content))
		require.NoError(t, err)
		got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 315, "tc-overwrite", token))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
		require.Len(t, h.confirmation.calls, 1)
	})

	t.Run("token mismatch", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-existing")
		h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"` + token + `"}`)}}
		_, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 316, "tc-create", "Empty", nil))
		require.NoError(t, err)
		got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 316, "tc-overwrite", "doxcnDifferentTarget123"))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
		require.Len(t, h.confirmation.calls, 1)
	})

	t.Run("corrupted create proof is fail closed", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-existing")
		h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"` + token + `"}`)}}
		created, err := h.service.Execute(h.ctx, operationDocsCreateRequest(7, 317, "tc-create", "Empty", nil))
		require.NoError(t, err)
		stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, created.OperationID)
		require.NoError(t, err)
		stored.ResultCiphertext[len(stored.ResultCiphertext)-1] ^= 0xff
		require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", stored.ID).
			Update("result_ciphertext", stored.ResultCiphertext).Error)

		got, err := h.service.Execute(h.ctx, operationDocsOverwriteRequest(7, 317, "tc-overwrite", token))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingConfirmation, got.State)
		require.Len(t, h.confirmation.calls, 1)
	})
}

func TestOperationService_ResumeReplaysEncryptedRequestAndStopsRepeatedRecovery(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	action := &OperationAction{Provider: ProviderLark, Phase: "user_auth", SessionID: "session-repeat"}
	h.recovery.action = nil
	h.recovery.actions = []*OperationAction{action, nil}
	missingScope := &CLIResult{
		InvocationStarted: true,
		ExitCode:          1,
		Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
			Type: "authorization", Subtype: "missing_scope", Code: json.RawMessage(`99991672`),
			Identity: "user", MissingScopes: []string{"docx:document:readonly"},
		}},
	}
	h.runner.steps = []operationRunnerStep{
		{result: missingScope, err: errors.New("business error one")},
		{result: missingScope, err: errors.New("business error two")},
	}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 19, ToolCallID: "tc-repeat-recovery",
		IdempotencyKey: "19:tc-repeat-recovery",
		Argv:           []string{"docs", "+fetch", "--doc", "doxcnOriginalToken123"},
		SkillReceipts:  []string{"shared-receipt", "doc-receipt"},
	}

	waiting, err := h.service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)
	req.Argv[3] = "doxcnMutatedAfterPersist123"

	restartedService := newHarnessOperationService(t, h, h.dataStore.FeishuWorkspace())
	resumed, err := restartedService.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, resumed.State)
	calls, argv := h.runner.snapshot()
	require.Equal(t, 2, calls)
	require.Equal(t, argv[0], argv[1])
	require.Contains(t, argv[1], "doxcnOriginalToken123")
	require.NotContains(t, argv[1], "doxcnMutatedAfterPersist123")
	require.Len(t, h.recovery.snapshot(), 2, "same recovery signature must fail instead of opening a third session")

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, waiting.OperationID)
	require.NoError(t, err)
	require.EqualValues(t, 2, stored.AttemptCount)
	require.Equal(t, model.FeishuOperationFailed, stored.State)
}

func TestOperationService_ResumeWhileRecoveryPendingReturnsExistingAction(t *testing.T) {
	h := newOperationHarness(t)
	h.recovery.action = &OperationAction{
		Provider: ProviderLark, Phase: "create_app", SessionID: "session-pending",
		URL: "https://pending.example/live", Scopes: []string{"scope-copy"},
	}
	waiting, err := h.service.Execute(h.ctx, operationDocsFetchRequest(20, "tc-pending"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, waiting.State)
	waiting.Action.Scopes[0] = "caller-mutated"

	resumed, err := h.service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, resumed.State)
	require.Equal(t, []string{"docx:document:readonly"}, resumed.Action.Scopes)
	calls, _ := h.runner.snapshot()
	require.Zero(t, calls)
	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, waiting.OperationID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stored.AttemptCount)
	require.Empty(t, stored.LeaseOwner)
}

func TestOperationService_TerminalResultIsEncryptedIdempotentAndDefensivelyCopied(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	request := operationDocsFetchRequest(21, "tc-terminal")

	first, err := h.service.Execute(h.ctx, request)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, first.State)
	first.Data[0] = '['

	resumed, err := h.service.Resume(h.ctx, 7, first.OperationID)
	require.NoError(t, err)
	require.JSONEq(t, `{"document_id":"doc1"}`, string(resumed.Data))
	resumed.Data[0] = '['

	repeated, err := h.service.Execute(h.ctx, request)
	require.NoError(t, err)
	require.JSONEq(t, `{"document_id":"doc1"}`, string(repeated.Data))
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)

	v2Service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner,
		Cipher: newOperationTestCipherKeyring(t, "v2"), Now: func() time.Time { return time.Date(2026, 7, 13, 12, 1, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	rotated, err := v2Service.Resume(h.ctx, 7, first.OperationID)
	require.NoError(t, err)
	require.JSONEq(t, `{"document_id":"doc1"}`, string(rotated.Data))
}

func TestOperationService_RejectsIdempotencyReuseWithDifferentCanonicalRequest(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	request := operationDocsFetchRequest(22, "tc-conflict")
	_, err := h.service.Execute(h.ctx, request)
	require.NoError(t, err)
	request.Argv = []string{"docs", "+fetch", "--doc", "doxcnDIFFERENT123"}

	_, err = h.service.Execute(h.ctx, request)
	require.ErrorIs(t, err, ErrOperationIdempotencyConflict)
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)
}

func TestOperationService_RequestFingerprintCannotBeRecomputedFromShortTitleDictionary(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	request := ExecuteRequest{
		UserID: 7, AgentRunID: 221, ToolCallID: "tc-short-title", IdempotencyKey: "221:tc-short-title",
		Argv: []string{"docs", "+create", "--title", "B"}, SkillReceipts: []string{"shared", "doc"},
	}

	result, err := h.service.Execute(h.ctx, request)
	require.NoError(t, err)
	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, result.OperationID)
	require.NoError(t, err)
	require.Equal(t, operationFingerprint(stored.RequestCiphertext), stored.RequestFingerprint)

	for _, title := range []string{"A", "B", "C"} {
		candidate := request
		candidate.Argv = []string{"docs", "+create", "--title", title}
		normalized, err := h.service.catalog.Normalize(candidate.Argv, candidate.StdinJSON)
		require.NoError(t, err)
		plaintext, err := json.Marshal(persistedRequestFromNormalized(candidate, normalized))
		require.NoError(t, err)
		require.NotEqual(t, operationFingerprint(plaintext), stored.RequestFingerprint,
			"a plaintext dictionary must not reproduce the persisted fingerprint")
	}
}

func TestOperationService_TamperedAndNonCanonicalStoredRequestsFailClosed(t *testing.T) {
	t.Run("tampered ciphertext", func(t *testing.T) {
		h := newOperationHarness(t)
		waiting, err := h.service.Execute(h.ctx, operationDocsFetchRequest(23, "tc-tamper"))
		require.NoError(t, err)
		var stored model.FeishuOperation
		require.NoError(t, h.db.First(&stored, "id = ?", waiting.OperationID).Error)
		stored.RequestCiphertext[len(stored.RequestCiphertext)-1] ^= 0xff
		require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", stored.ID).
			Update("request_ciphertext", stored.RequestCiphertext).Error)

		_, err = h.service.Resume(h.ctx, 7, stored.ID)
		require.ErrorIs(t, err, ErrOperationIntegrity)
	})

	t.Run("authenticated trailing garbage", func(t *testing.T) {
		h := newOperationHarness(t)
		waiting, err := h.service.Execute(h.ctx, operationDocsFetchRequest(24, "tc-noncanonical"))
		require.NoError(t, err)
		var stored model.FeishuOperation
		require.NoError(t, h.db.First(&stored, "id = ?", waiting.OperationID).Error)
		persisted, err := h.service.openPersistedRequest(&stored)
		require.NoError(t, err)
		canonical, err := json.Marshal(persisted)
		require.NoError(t, err)
		nonCanonical := append(canonical, []byte(" trailing")...)
		owner := OperationCipherOwner{UserID: stored.UserID, Generation: stored.Generation, OperationID: stored.ID}
		ciphertext, keyVersion, err := h.service.sealOperationBlob(OperationCipherPurposeRequest, owner, nonCanonical)
		require.NoError(t, err)
		require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", stored.ID).Updates(map[string]any{
			"request_ciphertext":  ciphertext,
			"request_fingerprint": operationFingerprint(ciphertext),
			"key_version":         keyVersion,
		}).Error)

		_, err = h.service.Resume(h.ctx, 7, stored.ID)
		require.ErrorIs(t, err, ErrOperationIntegrity)
	})
}

func TestOperationService_OpensHistoricalRequestWithoutDerivedProofFields(t *testing.T) {
	h := newOperationHarness(t)
	request := operationDocsFetchRequest(241, "tc-historical-request")
	normalized, err := h.service.catalog.Normalize(request.Argv, request.StdinJSON)
	require.NoError(t, err)
	legacy := struct {
		AgentRunID            uint64          `json:"agent_run_id"`
		ToolCallID            string          `json:"tool_call_id"`
		IdempotencyKey        string          `json:"idempotency_key"`
		CommandPath           string          `json:"command_path"`
		Domain                string          `json:"domain"`
		Action                string          `json:"action"`
		Risk                  RiskLevel       `json:"risk"`
		RequiresCLIYes        bool            `json:"requires_cli_yes"`
		ReplaySafeOnAuthError bool            `json:"replay_safe_on_auth_error"`
		Scopes                []string        `json:"scopes"`
		Argv                  []string        `json:"argv"`
		StdinJSON             json.RawMessage `json:"stdin_json,omitempty"`
	}{
		AgentRunID: request.AgentRunID, ToolCallID: request.ToolCallID, IdempotencyKey: request.IdempotencyKey,
		CommandPath: normalized.Path, Domain: normalized.Domain, Action: normalized.Action, Risk: normalized.Risk,
		RequiresCLIYes: normalized.RequiresCLIYes, ReplaySafeOnAuthError: normalized.ReplaySafeOnAuthError,
		Scopes: normalized.Scopes, Argv: normalized.Argv, StdinJSON: normalized.StdinJSON,
	}
	plaintext, err := json.Marshal(legacy)
	require.NoError(t, err)
	operationID := uuid.NewString()
	owner := OperationCipherOwner{UserID: request.UserID, Generation: 1, OperationID: operationID}
	ciphertext, keyVersion, err := h.service.sealOperationBlob(OperationCipherPurposeRequest, owner, plaintext)
	require.NoError(t, err)
	operation := &model.FeishuOperation{
		ID: operationID, UserID: request.UserID, Generation: 1,
		AgentRunID: request.AgentRunID, ToolCallID: request.ToolCallID, IdempotencyKey: request.IdempotencyKey,
		CommandPath: normalized.Path, Domain: normalized.Domain, RiskLevel: string(normalized.Risk),
		RequestCiphertext: ciphertext, KeyVersion: keyVersion, RequestFingerprint: operationFingerprint(ciphertext),
		State: model.FeishuOperationNotStarted,
	}

	opened, err := h.service.openPersistedRequest(operation)
	require.NoError(t, err)
	require.Equal(t, normalized.Argv, opened.Argv)
	require.False(t, opened.SameRunEmptyCreateProof)
	require.Empty(t, opened.CreateProofOperationID)
}

func TestOperationService_GenerationBumpAfterRunnerStartCannotCommitOrSeal(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"remote-maybe-created"}`)}}
	h.vault.afterRun = func(userID uint, generation uint64, changed bool) error {
		require.Equal(t, uint(7), userID)
		require.EqualValues(t, 1, generation)
		if !changed {
			return nil // the scope preflight never changes or seals the HOME
		}
		require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
			Where("user_id = ? AND provider = ?", userID, ProviderLark).
			Update("generation", 2).Error)
		return gorm.ErrRecordNotFound
	}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 25, ToolCallID: "tc-generation",
		IdempotencyKey: "25:tc-generation", Argv: []string{"docs", "+create", "--title", "报告"},
		SkillReceipts: []string{"shared-receipt", "doc-receipt"},
	}

	got, err := h.service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationUnknown, got.State)
	require.Zero(t, h.vault.sealed)
	var stored model.FeishuOperation
	require.NoError(t, h.db.First(&stored, "id = ?", got.OperationID).Error)
	require.NotEqual(t, model.FeishuOperationSucceeded, stored.State)
	claimed, err := h.dataStore.FeishuWorkspace().ClaimOperation(
		h.ctx, 7, 1, stored.ID, "new-owner", []string{model.FeishuOperationExecuting},
		time.Now().UTC(), time.Now().UTC().Add(time.Minute),
	)
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestOperationService_RunnerNotStartedDoesNotSealVault(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.runner.steps = []operationRunnerStep{{result: &CLIResult{InvocationStarted: false, ExitCode: -1}, err: errors.New("start failed")}}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 26, ToolCallID: "tc-not-started",
		IdempotencyKey: "26:tc-not-started", Argv: []string{"docs", "+create", "--title", "报告"},
		SkillReceipts: []string{"shared-receipt", "doc-receipt"},
	}

	got, err := h.service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, got.State)
	require.Equal(t, []bool{false, false}, h.vault.changed, "preflight and failed process start are both read-only")
	require.Zero(t, h.vault.sealed)
}

func TestOperationService_StartedWriteIndeterminateFailuresAreUnknownWithoutRetry(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result *CLIResult
		err    error
	}{
		{name: "malformed output", result: &CLIResult{InvocationStarted: true, ExitCode: 0}, err: errControlledCLIInvalidJSON},
		{name: "output limit", result: &CLIResult{InvocationStarted: true, ExitCode: 0, StdoutTruncated: true}, err: errControlledCLIOutputLimit},
		{name: "killed", result: &CLIResult{InvocationStarted: true, ExitCode: -1}, err: context.Canceled},
		{name: "upstream 5xx", result: &CLIResult{
			InvocationStarted: true, ExitCode: 1,
			Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
				Type: "api", Subtype: "upstream_error", Code: json.RawMessage(`"503"`), Identity: "user",
			}},
		}, err: errors.New("business error")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			h.runner.steps = []operationRunnerStep{{result: testCase.result, err: testCase.err}}
			req := ExecuteRequest{
				UserID: 7, AgentRunID: 27, ToolCallID: "tc-unknown-" + strings.ReplaceAll(testCase.name, " ", "-"),
				Argv: []string{"docs", "+create", "--title", "报告"}, SkillReceipts: []string{"shared", "doc"},
			}
			req.IdempotencyKey = fmt.Sprintf("%d:%s", req.AgentRunID, req.ToolCallID)
			got, err := h.service.Execute(h.ctx, req)
			require.NoError(t, err)
			require.Equal(t, model.FeishuOperationUnknown, got.State)
			calls, _ := h.runner.snapshot()
			require.Equal(t, 1, calls)
		})
	}
}

func TestOperationService_WritePreflightIsSharedAcrossDocsBaseAndWiki(t *testing.T) {
	tests := []struct {
		name   string
		argv   []string
		scopes []string
	}{
		{
			name: "docs create", argv: []string{"docs", "+create", "--title", "Report"},
			scopes: []string{"docx:document:create"},
		},
		{
			name: "base record update",
			argv: []string{
				"base", "+record-batch-update", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks",
				"--json", `{"record_id_list":["recABCDEFG123"],"patch":{"Status":"Done"}}`,
			},
			scopes: []string{"base:record:update"},
		},
		{
			name:   "wiki node create",
			argv:   []string{"wiki", "+node-create", "--space-id", "my_library", "--title", "Playbook"},
			scopes: []string{"wiki:node:create", "wiki:node:read", "wiki:space:read"},
		},
	}
	for index, test := range tests {
		t.Run(test.name+" missing", func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			h.preflight.steps = []operationScopePreflightStep{{result: &ScopeCheckResult{
				Missing: append([]string(nil), test.scopes...),
			}}}
			request := ExecuteRequest{
				UserID: 7, AgentRunID: uint64(900 + index), ToolCallID: "domain-missing",
				IdempotencyKey: fmt.Sprintf("%d:domain-missing", 900+index),
				Argv:           append([]string(nil), test.argv...), SkillReceipts: []string{"shared", "domain"},
			}
			got, err := h.service.Execute(h.ctx, request)
			require.NoError(t, err)
			require.Equal(t, model.FeishuOperationWaitingUserAuth, got.State)
			require.Equal(t, test.scopes, got.Action.Scopes)
			businessCalls, _ := h.runner.snapshot()
			require.Zero(t, businessCalls)
			preflightCalls, scopes := h.preflight.snapshot()
			require.Equal(t, 1, preflightCalls)
			require.Equal(t, [][]string{test.scopes}, scopes)
		})

		t.Run(test.name+" granted", func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			request := ExecuteRequest{
				UserID: 7, AgentRunID: uint64(910 + index), ToolCallID: "domain-granted",
				IdempotencyKey: fmt.Sprintf("%d:domain-granted", 910+index),
				Argv:           append([]string(nil), test.argv...), SkillReceipts: []string{"shared", "domain"},
			}
			got, err := h.service.Execute(h.ctx, request)
			require.NoError(t, err)
			require.Equal(t, model.FeishuOperationSucceeded, got.State)
			businessCalls, _ := h.runner.snapshot()
			require.Equal(t, 1, businessCalls)
			preflightCalls, scopes := h.preflight.snapshot()
			require.Equal(t, 1, preflightCalls)
			require.Equal(t, [][]string{test.scopes}, scopes)
		})
	}
}

func TestOperationService_ConnectionsAreSharedAcrossAgentRunsAndIsolatedAcrossUsers(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 3, "cli_user_7")
	h.createAccount(8, model.FeishuConnectionConnected, 5, "cli_user_8")
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document_id":"a"}`)},
		{result: operationOKResult(`{"document_id":"b"}`)},
		{result: operationOKResult(`{"document_id":"c"}`)},
	}
	var mu sync.Mutex
	var homes []struct {
		userID     uint
		generation uint64
	}
	h.vault.afterRun = func(userID uint, generation uint64, _ bool) error {
		mu.Lock()
		homes = append(homes, struct {
			userID     uint
			generation uint64
		}{userID: userID, generation: generation})
		mu.Unlock()
		return nil
	}

	requests := []ExecuteRequest{
		operationDocsFetchRequest(1001, "agent-a"),
		operationDocsFetchRequest(1002, "agent-b"),
		operationDocsFetchRequest(1003, "same-definition-other-user"),
	}
	requests[2].UserID = 8
	for _, request := range requests {
		result, err := h.service.Execute(h.ctx, request)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, result.State)
	}

	var operations []model.FeishuOperation
	require.NoError(t, h.db.Order("agent_run_id asc").Find(&operations).Error)
	require.Len(t, operations, 3)
	require.Equal(t, uint(7), operations[0].UserID)
	require.Equal(t, uint64(3), operations[0].Generation)
	require.Equal(t, uint(7), operations[1].UserID)
	require.Equal(t, uint64(3), operations[1].Generation, "two Agents owned by one user share the account generation")
	require.Equal(t, uint(8), operations[2].UserID)
	require.Equal(t, uint64(5), operations[2].Generation, "the same Agent behavior under another user uses another account")
	mu.Lock()
	require.Equal(t, []struct {
		userID     uint
		generation uint64
	}{{7, 3}, {7, 3}, {8, 5}}, homes)
	mu.Unlock()
	domains, runs := h.receipts.snapshot()
	require.Empty(t, domains, "model-carried receipts are not consulted for any Agent run")
	require.Empty(t, runs, "current-user isolation is enforced by the account and operation stores")
}

func TestOperationService_OKEnvelopeWithoutValidDataFailsClosed(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		argv     []string
		expected string
	}{
		{name: "read", argv: []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123"}, expected: model.FeishuOperationFailed},
		{name: "write", argv: []string{"docs", "+create", "--title", "报告"}, expected: model.FeishuOperationUnknown},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			h.runner.steps = []operationRunnerStep{{result: &CLIResult{
				InvocationStarted: true, ExitCode: 0, Envelope: &CLIEnvelope{OK: true, Identity: "user"},
			}}}
			req := ExecuteRequest{
				UserID: 7, AgentRunID: 32, ToolCallID: "tc-missing-data-" + testCase.name,
				Argv: testCase.argv, SkillReceipts: []string{"shared", "doc"},
			}
			req.IdempotencyKey = fmt.Sprintf("%d:%s", req.AgentRunID, req.ToolCallID)

			got, err := h.service.Execute(h.ctx, req)
			require.NoError(t, err)
			require.Equal(t, testCase.expected, got.State)
			stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
			require.NoError(t, err)
			require.Equal(t, testCase.expected, stored.State)
			require.Empty(t, stored.ResultCiphertext)
		})
	}
}

func TestOperationService_ConcurrentResumeUsesUniqueOwnersAndOneReplay(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	recorded := &recordingOperationStore{OperationStore: h.dataStore.FeishuWorkspace()}
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: recorded,
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: func() time.Time { return time.Date(2026, 7, 13, 12, 2, 0, 0, time.UTC) }, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	h.recovery.action = nil
	h.recovery.actions = []*OperationAction{{Provider: ProviderLark, Phase: "user_auth", SessionID: "session-concurrent"}}
	h.preflight.steps = []operationScopePreflightStep{
		{result: &ScopeCheckResult{Missing: []string{"docx:document:create"}}},
		{result: &ScopeCheckResult{Granted: []string{"docx:document:create"}}},
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h.runner.steps = []operationRunnerStep{{
		result: operationOKResult(`{"document_id":"resumed-once"}`), started: started, release: release,
	}}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 28, ToolCallID: "tc-concurrent-resume",
		IdempotencyKey: "28:tc-concurrent-resume", Argv: []string{"docs", "+create", "--title", "报告"},
		SkillReceipts: []string{"shared", "doc"},
	}
	waiting, err := service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)

	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	results := make(chan *OperationResult, workers)
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, resumeErr := service.Resume(h.ctx, 7, waiting.OperationID)
			results <- result
			errs <- resumeErr
		}()
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("resumed runner did not start")
	}
	close(release)
	wg.Wait()
	close(errs)
	close(results)
	for resumeErr := range errs {
		require.NoError(t, resumeErr)
	}
	for result := range results {
		require.NotNil(t, result)
		require.Contains(t, []string{model.FeishuOperationExecuting, model.FeishuOperationSucceeded}, result.State)
	}
	calls, _ := h.runner.snapshot()
	require.Equal(t, 1, calls)
	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, stored.State)
	require.EqualValues(t, 2, stored.AttemptCount)

	owners := recorded.snapshotOwners()
	require.GreaterOrEqual(t, len(owners), 2)
	seen := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		require.NotEmpty(t, owner)
		_, duplicate := seen[owner]
		require.False(t, duplicate, "every execution attempt must use a fresh lease owner")
		seen[owner] = struct{}{}
	}
}

func TestOperationService_ExpiredExecutingLeaseReclaimsOnlyReads(t *testing.T) {
	t.Run("expired read replays once", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		op := h.insertExecutingOperation(operationDocsFetchRequest(29, "tc-expired-read"), h.service.now().Add(-time.Second))

		got, err := h.service.Resume(h.ctx, 7, op.ID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, got.State)
		calls, _ := h.runner.snapshot()
		require.Equal(t, 1, calls)
		stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, op.ID)
		require.NoError(t, err)
		require.EqualValues(t, 2, stored.AttemptCount)
	})

	t.Run("expired local resolver replays without connection", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionNone, 1, "")
		h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"base_token":"ZiXObjsGvahtyAscDJ1cjlRnnLh"}`)}}
		req := ExecuteRequest{
			UserID: 7, AgentRunID: 247, ToolCallID: "tc-expired-local", IdempotencyKey: "247:tc-expired-local",
			Argv: []string{"base", "+url-resolve", "--url", "https://scnb8amlnnek.feishu.cn/base/ZiXObjsGvahtyAscDJ1cjlRnnLh"},
		}
		op := h.insertExecutingOperation(req, h.service.now().Add(-time.Second))

		got, err := h.service.Resume(h.ctx, 7, op.ID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, got.State)
		calls, _ := h.runner.snapshot()
		require.Zero(t, calls)
		require.Empty(t, h.recovery.snapshot())
	})

	t.Run("expired write becomes unknown without replay", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		req := ExecuteRequest{
			UserID: 7, AgentRunID: 30, ToolCallID: "tc-expired-write", IdempotencyKey: "30:tc-expired-write",
			Argv: []string{"docs", "+create", "--title", "报告"}, SkillReceipts: []string{"shared", "doc"},
		}
		op := h.insertExecutingOperation(req, h.service.now().Add(-time.Second))

		got, err := h.service.Resume(h.ctx, 7, op.ID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationUnknown, got.State)
		calls, _ := h.runner.snapshot()
		require.Zero(t, calls)
		stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, op.ID)
		require.NoError(t, err)
		require.EqualValues(t, 1, stored.AttemptCount)
	})

	t.Run("live lease is never stolen", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		op := h.insertExecutingOperation(operationDocsFetchRequest(31, "tc-live-read"), h.service.now().Add(time.Minute))

		got, err := h.service.Resume(h.ctx, 7, op.ID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationExecuting, got.State)
		calls, _ := h.runner.snapshot()
		require.Zero(t, calls)
	})

	t.Run("repeated execute cannot replace tampered encrypted replay", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		req := operationDocsFetchRequest(33, "tc-expired-tampered")
		op := h.insertExecutingOperation(req, h.service.now().Add(-time.Second))
		op.RequestCiphertext[len(op.RequestCiphertext)-1] ^= 0xff
		require.NoError(t, h.db.Model(&model.FeishuOperation{}).Where("id = ?", op.ID).
			Update("request_ciphertext", op.RequestCiphertext).Error)

		_, err := h.service.Execute(h.ctx, req)
		require.ErrorIs(t, err, ErrOperationIntegrity)
		calls, _ := h.runner.snapshot()
		require.Zero(t, calls)
	})
}

func (h *operationHarness) insertExecutingOperation(request ExecuteRequest, leaseUntil time.Time) *model.FeishuOperation {
	h.t.Helper()
	normalized, err := h.service.catalog.Normalize(request.Argv, request.StdinJSON)
	require.NoError(h.t, err)
	persisted := persistedRequestFromNormalized(request, normalized)
	plaintext, err := json.Marshal(persisted)
	require.NoError(h.t, err)
	operationID := uuid.NewString()
	owner := OperationCipherOwner{UserID: request.UserID, Generation: 1, OperationID: operationID}
	ciphertext, keyVersion, err := h.service.sealOperationBlob(OperationCipherPurposeRequest, owner, plaintext)
	require.NoError(h.t, err)
	startedAt := h.service.now().Add(-time.Minute)
	op := &model.FeishuOperation{
		ID: operationID, UserID: request.UserID, Generation: 1,
		AgentRunID: request.AgentRunID, ToolCallID: request.ToolCallID, IdempotencyKey: request.IdempotencyKey,
		CommandPath: normalized.Path, Domain: normalized.Domain, RiskLevel: string(normalized.Risk),
		RequestCiphertext: ciphertext, KeyVersion: keyVersion, RequestFingerprint: operationFingerprint(ciphertext),
		State: model.FeishuOperationExecuting, AttemptCount: 1, LeaseOwner: "crashed-owner",
		LeaseUntil: &leaseUntil, StartedAt: &startedAt,
	}
	require.NoError(h.t, h.db.Create(op).Error)
	return op
}

func TestOperationCipherKeyring_BindsPurposeAndOwnershipAndReadsHistoricalKeys(t *testing.T) {
	v1 := newOperationTestCipherKeyring(t, "v1")
	v2 := newOperationTestCipherKeyring(t, "v2")
	owner := OperationCipherOwner{UserID: 7, Generation: 3, OperationID: "op-1"}
	ciphertext, keyVersion, err := v1.Seal(OperationCipherPurposeRequest, owner, []byte(`{"safe":true}`))
	require.NoError(t, err)
	require.Equal(t, "v1", keyVersion)

	plain, err := v2.Open(OperationCipherPurposeRequest, owner, keyVersion, ciphertext)
	require.NoError(t, err)
	require.JSONEq(t, `{"safe":true}`, string(plain))

	for _, testCase := range []struct {
		name    string
		purpose OperationCipherPurpose
		owner   OperationCipherOwner
		version string
		mutate  func([]byte) []byte
	}{
		{name: "purpose", purpose: OperationCipherPurposeResult, owner: owner, version: keyVersion},
		{name: "user", purpose: OperationCipherPurposeRequest, owner: OperationCipherOwner{UserID: 8, Generation: 3, OperationID: "op-1"}, version: keyVersion},
		{name: "generation", purpose: OperationCipherPurposeRequest, owner: OperationCipherOwner{UserID: 7, Generation: 4, OperationID: "op-1"}, version: keyVersion},
		{name: "operation", purpose: OperationCipherPurposeRequest, owner: OperationCipherOwner{UserID: 7, Generation: 3, OperationID: "op-2"}, version: keyVersion},
		{name: "unknown key", purpose: OperationCipherPurposeRequest, owner: owner, version: "v404"},
		{name: "tampered", purpose: OperationCipherPurposeRequest, owner: owner, version: keyVersion, mutate: func(value []byte) []byte {
			copyValue := append([]byte(nil), value...)
			copyValue[len(copyValue)-1] ^= 0xff
			return copyValue
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			blob := ciphertext
			if testCase.mutate != nil {
				blob = testCase.mutate(blob)
			}
			_, err := v2.Open(testCase.purpose, testCase.owner, testCase.version, blob)
			require.Error(t, err)
		})
	}
}

func cloneTestCLIResult(result *CLIResult) *CLIResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Stdout = append([]byte(nil), result.Stdout...)
	clone.Stderr = append([]byte(nil), result.Stderr...)
	if result.Envelope != nil {
		envelope := *result.Envelope
		envelope.Data = append(json.RawMessage(nil), result.Envelope.Data...)
		if result.Envelope.Error != nil {
			cliErr := *result.Envelope.Error
			cliErr.Code = append(json.RawMessage(nil), cliErr.Code...)
			cliErr.MissingScopes = append([]string(nil), cliErr.MissingScopes...)
			cliErr.PermissionViolations = append(json.RawMessage(nil), cliErr.PermissionViolations...)
			cliErr.Details = append(json.RawMessage(nil), cliErr.Details...)
			cliErr.Hint = append(json.RawMessage(nil), cliErr.Hint...)
			envelope.Error = &cliErr
		}
		clone.Envelope = &envelope
	}
	return &clone
}

func cloneTestRecoveryRequest(req RecoveryRequest) RecoveryRequest {
	req.Scopes = append([]string(nil), req.Scopes...)
	return req
}

func cloneTestOperationAction(action *OperationAction) *OperationAction {
	if action == nil {
		return nil
	}
	clone := *action
	clone.Scopes = append([]string(nil), action.Scopes...)
	return &clone
}

var _ = errors.New
