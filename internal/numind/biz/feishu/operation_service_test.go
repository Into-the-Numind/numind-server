package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
	mu      sync.Mutex
	action  *OperationAction
	actions []*OperationAction
	err     error
	calls   []RecoveryRequest
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
	mu       sync.Mutex
	changed  []bool
	sealed   int
	afterRun func(userID uint, generation uint64, changed bool) error
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
	now, leaseUntil time.Time,
) (bool, error) {
	s.mu.Lock()
	s.owners = append(s.owners, owner)
	s.mu.Unlock()
	return s.OperationStore.ClaimOperation(ctx, userID, generation, id, owner, now, leaseUntil)
}

func (s *recordingOperationStore) snapshotOwners() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.owners...)
}

func (f *operationVaultFake) WithHome(
	_ context.Context,
	userID uint,
	generation uint64,
	callback func(home string) (bool, error),
) error {
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

type operationHarness struct {
	t            *testing.T
	ctx          context.Context
	db           *gorm.DB
	dataStore    store.IStore
	receipts     *operationReceiptFake
	recovery     *operationRecoveryFake
	confirmation *operationConfirmationFake
	runner       *operationRunnerFake
	vault        *operationVaultFake
	cipher       *OperationCipherKeyring
	service      *FeishuOperationService
}

func newOperationHarness(t *testing.T) *operationHarness {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared&_busy_timeout=5000"
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
	))
	dataStore := store.NewTestStore(db)
	receipts := &operationReceiptFake{}
	recovery := &operationRecoveryFake{action: &OperationAction{Provider: ProviderLark, Phase: "create_app", SessionID: "session-1"}}
	confirmation := &operationConfirmationFake{action: &OperationAction{Provider: ProviderLark, Phase: "confirmation", SessionID: "confirmation-1"}}
	runner := &operationRunnerFake{steps: []operationRunnerStep{{result: operationOKResult(`{"document_id":"doc1"}`)}}}
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
		Runner:        runner,
		Cipher:        cipher,
		Now:           func() time.Time { return time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC) },
		LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	return &operationHarness{
		t: t, ctx: context.Background(), db: db, dataStore: dataStore,
		receipts: receipts, recovery: recovery, confirmation: confirmation,
		runner: runner, vault: vault, cipher: cipher, service: service,
	}
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
}

func TestOperationService_StrictInputAndServerOwnedReceiptDomain(t *testing.T) {
	t.Run("normalize happens before receipt verification", func(t *testing.T) {
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

	t.Run("wiki content requires three-skill domain", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		req := operationDocsFetchRequest(10, "tc-wiki")
		req.Argv = []string{"docs", "+fetch", "--doc", "https://acme.feishu.cn/wiki/wikcnABCDEFG123"}
		_, err := h.service.Execute(h.ctx, req)
		require.NoError(t, err)
		domains, runs := h.receipts.snapshot()
		require.Equal(t, []string{SkillDomainWikiContent}, domains)
		require.Equal(t, []uint64{10}, runs)
	})

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
		domains, _ := h.receipts.snapshot()
		require.Equal(t, []string{SkillDomainDocs}, domains)
	})

	for _, testCase := range []struct {
		name   string
		domain string
		argv   []string
	}{
		{name: "docs", domain: SkillDomainDocs, argv: []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123"}},
		{name: "base", domain: SkillDomainBase, argv: []string{"base", "+base-get", "--base-token", "bascnABCDEFG123"}},
		{name: "wiki", domain: SkillDomainWiki, argv: []string{"wiki", "+node-get", "--node-token", "wikcnABCDEFG123"}},
	} {
		t.Run("server domain "+testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
			req := operationDocsFetchRequest(100, "tc-domain-"+testCase.name)
			req.Argv = testCase.argv
			_, err := h.service.Execute(h.ctx, req)
			require.NoError(t, err)
			domains, _ := h.receipts.snapshot()
			require.Equal(t, []string{testCase.domain}, domains)
		})
	}

	t.Run("invalid receipt never creates operation", func(t *testing.T) {
		h := newOperationHarness(t)
		h.receipts.err = errors.New("invalid receipt detail")
		_, err := h.service.Execute(h.ctx, operationDocsFetchRequest(101, "tc-receipt-invalid"))
		require.ErrorIs(t, err, ErrOperationRequestRejected)
		var count int64
		require.NoError(t, h.db.Model(&model.FeishuOperation{}).Count(&count).Error)
		require.Zero(t, count)
		_, accountErr := h.dataStore.ThirdPartyAccounts().Get(h.ctx, 7, ProviderLark)
		require.ErrorIs(t, accountErr, gorm.ErrRecordNotFound)
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
	})
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

func TestOperationService_StructuredRecoveryUsesExactScopesAndSealsVault(t *testing.T) {
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
	require.Equal(t, model.FeishuOperationWaitingUserAuth, got.State)
	require.Equal(t, ProviderLark, got.Action.Provider)
	require.Equal(t, "user_auth", got.Action.Phase)
	require.Equal(t, []string{"docx:document:create"}, got.Action.Scopes)
	require.Equal(t, "https://verification.example/sensitive", got.Action.URL)
	calls := h.recovery.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, RecoveryUserScope, calls[0].Kind)
	require.Equal(t, []string{"docx:document:create"}, calls[0].Scopes)
	require.Equal(t, []bool{true}, h.vault.changed)

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, stored.State)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
	require.NotContains(t, string(stored.ResultSummaryJSON), "verification.example")
	require.NotContains(t, string(stored.ResultSummaryJSON), "raw CLI")
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
}

func TestOperationService_HighRiskOnlyCreatesConfirmationWaiting(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.confirmation.action = &OperationAction{
		Provider: ProviderLark, Phase: "confirmation", SessionID: "confirm-1",
		URL: "https://confirmation.example/transient", Scopes: []string{"must-copy-defensively"},
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

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, got.OperationID)
	require.NoError(t, err)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
	require.Empty(t, stored.ErrorType)
	require.Empty(t, stored.ErrorSubtype)
	require.NotContains(t, string(stored.ResultSummaryJSON), "confirmation.example")

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
			Identity: "user", MissingScopes: []string{"docx:document:create"},
		}},
	}
	h.runner.steps = []operationRunnerStep{
		{result: missingScope, err: errors.New("business error one")},
		{result: missingScope, err: errors.New("business error two")},
	}
	req := ExecuteRequest{
		UserID: 7, AgentRunID: 19, ToolCallID: "tc-repeat-recovery",
		IdempotencyKey: "19:tc-repeat-recovery",
		Argv:           []string{"docs", "+create", "--title", "original-title"},
		SkillReceipts:  []string{"shared-receipt", "doc-receipt"},
	}

	waiting, err := h.service.Execute(h.ctx, req)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)
	req.Argv[3] = "mutated-after-persist"

	resumed, err := h.service.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationFailed, resumed.State)
	calls, argv := h.runner.snapshot()
	require.Equal(t, 2, calls)
	require.Equal(t, argv[0], argv[1])
	require.Contains(t, argv[1], "original-title")
	require.NotContains(t, argv[1], "mutated-after-persist")
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
		Confirmation: h.confirmation, Vault: h.vault, Runner: h.runner,
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
			"request_fingerprint": operationFingerprint(nonCanonical),
			"key_version":         keyVersion,
		}).Error)

		_, err = h.service.Resume(h.ctx, 7, stored.ID)
		require.ErrorIs(t, err, ErrOperationIntegrity)
	})
}

func TestOperationService_GenerationBumpAfterRunnerStartCannotCommitOrSeal(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"document_id":"remote-maybe-created"}`)}}
	h.vault.afterRun = func(userID uint, generation uint64, changed bool) error {
		require.Equal(t, uint(7), userID)
		require.EqualValues(t, 1, generation)
		require.True(t, changed)
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
	claimed, err := h.dataStore.FeishuWorkspace().ClaimOperation(h.ctx, 7, 1, stored.ID, "new-owner", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
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
	require.Equal(t, []bool{false}, h.vault.changed)
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
		Confirmation: h.confirmation, Vault: h.vault, Runner: h.runner, Cipher: h.cipher,
		Now: func() time.Time { return time.Date(2026, 7, 13, 12, 2, 0, 0, time.UTC) }, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	h.recovery.action = nil
	h.recovery.actions = []*OperationAction{{Provider: ProviderLark, Phase: "user_auth", SessionID: "session-concurrent"}}
	missingScope := &CLIResult{
		InvocationStarted: true, ExitCode: 1,
		Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
			Type: "authorization", Subtype: "missing_scope", Code: json.RawMessage(`99991672`),
			Identity: "user", MissingScopes: []string{"docx:document:create"},
		}},
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h.runner.steps = []operationRunnerStep{
		{result: missingScope, err: errors.New("missing scope")},
		{result: operationOKResult(`{"document_id":"resumed-once"}`), started: started, release: release},
	}
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
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, resumeErr := service.Resume(h.ctx, 7, waiting.OperationID)
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
	for resumeErr := range errs {
		require.NoError(t, resumeErr)
	}
	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls)
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
		RequestCiphertext: ciphertext, KeyVersion: keyVersion, RequestFingerprint: operationFingerprint(plaintext),
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
