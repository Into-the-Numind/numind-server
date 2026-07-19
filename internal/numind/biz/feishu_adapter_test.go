package biz

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
	numindconfig "numind-server/internal/numind/config"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"

	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newFeishuCompositionDeps(t *testing.T) feishuCompositionDeps {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "composition.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.UserThirdPartyAccount{},
		&model.FeishuCLIVault{},
		&model.FeishuAuthSession{},
		&model.FeishuOperation{},
	))
	ds := store.NewTestStore(db)
	resumeStore, ok := ds.AgentRuns().(store.IExternalToolResumeLease)
	require.True(t, ok)
	tokenKey := feishuCompositionKey(0)
	return feishuCompositionDeps{
		enabled:        true,
		dataStore:      ds,
		tokenKey:       tokenKey,
		cipherKeys:     []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("test-v1", tokenKey)},
		keyVersion:     "test-v1",
		runtimeBase:    filepath.Join(t.TempDir(), "runtime"),
		authOwner:      "test-feishu-auth-worker",
		studentRuns:    agent.NewStudentRunService(nil, ds.AgentRuns(), ds.AgentDefinitions(), nil, nil, nil),
		resumeStore:    resumeStore,
		supervisor:     agent.NewExternalContinuationSupervisor(agent.ExternalContinuationLimit),
		verifyVersion:  func(context.Context) error { return nil },
		scopePreflight: compositionScopePreflight{},
	}
}

type feishuCompositionWorkspaceStore struct {
	store.IFeishuWorkspaceStore
	sweep func(context.Context, time.Time, string, int) (store.FeishuDeviceAuthCleanupPage, error)
}

func (s *feishuCompositionWorkspaceStore) SweepDeviceAuthCredentials(
	ctx context.Context,
	before time.Time,
	afterSessionID string,
	scanLimit int,
) (store.FeishuDeviceAuthCleanupPage, error) {
	if s.sweep != nil {
		return s.sweep(ctx, before, afterSessionID, scanLimit)
	}
	return s.IFeishuWorkspaceStore.SweepDeviceAuthCredentials(ctx, before, afterSessionID, scanLimit)
}

type feishuCompositionStore struct {
	store.IStore
	workspace store.IFeishuWorkspaceStore
}

func (s *feishuCompositionStore) FeishuWorkspace() store.IFeishuWorkspaceStore { return s.workspace }

func wrapFeishuCompositionSweep(
	deps *feishuCompositionDeps,
	sweep func(context.Context, time.Time, string, int) (store.FeishuDeviceAuthCleanupPage, error),
) *feishuCompositionWorkspaceStore {
	workspace := &feishuCompositionWorkspaceStore{IFeishuWorkspaceStore: deps.dataStore.FeishuWorkspace(), sweep: sweep}
	deps.dataStore = &feishuCompositionStore{IStore: deps.dataStore, workspace: workspace}
	return workspace
}

func feishuCompositionKey(seed byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{seed}, crypto.KeyLen))
}

func feishuCompositionKeyringEntry(version, key string) feishuCipherKeyringEntry {
	return feishuCipherKeyringEntry{Version: version, Key: key}
}

func prepareHistoricalCompositionVault(t *testing.T, deps feishuCompositionDeps, keyVersion, key string) {
	t.Helper()
	require.NoError(t, deps.dataStore.DB().Create(&model.UserThirdPartyAccount{
		UserID: 7, Provider: feishu.ProviderLark, AppID: "app-7",
		ConnectionState: model.FeishuConnectionConnected, Connected: true, Generation: 1,
	}).Error)

	historicalCipher, err := crypto.NewCipher(key)
	require.NoError(t, err)
	historicalVault, err := feishu.NewEncryptedCLIHomeVaultWithKeyring(
		deps.dataStore.ThirdPartyAccounts(), deps.dataStore.FeishuWorkspace(),
		map[string]*crypto.Cipher{keyVersion: historicalCipher}, keyVersion, deps.runtimeBase,
	)
	require.NoError(t, err)
	require.NoError(t, historicalVault.WithHome(context.Background(), 7, 1, func(home string) (bool, error) {
		return true, os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"key":"historical"}`), 0o600)
	}))
}

func prepareHistoricalCompositionAuthSession(t *testing.T, deps feishuCompositionDeps, keyVersion string) {
	t.Helper()
	resumeExpiry := time.Date(2026, 7, 17, 6, 0, 0, 0, time.UTC)
	require.NoError(t, deps.dataStore.DB().Create(&model.FeishuAuthSession{
		ID: "00000000-0000-4000-8000-000000000071", UserID: 7, Generation: 1,
		Phase: model.FeishuAuthPhaseUserAuth, RequestedScopesJSON: []byte(`["docx:document:readonly"]`),
		State: model.FeishuAuthSessionPending, ProtocolVersion: 2,
		ResumeCredentialCiphertext: []byte("historical-device-code"), ResumeKeyVersion: keyVersion,
		ResumeExpiresAt: &resumeExpiry, ScopeHash: strings.Repeat("a", 64), ExpiresAt: resumeExpiry,
	}).Error)
}

type compositionReceiptVerifier struct{}

func (compositionReceiptVerifier) VerifyRequired([]string, uint64, string) error { return nil }

type compositionScopePreflight struct{}

func (compositionScopePreflight) Check(_ context.Context, _ string, scopes []string) (*feishu.ScopeCheckResult, error) {
	return &feishu.ScopeCheckResult{Granted: append([]string(nil), scopes...)}, nil
}

func TestBuildFeishuService_FeatureFlagOffReturnsNil(t *testing.T) {
	composition, err := buildFeishuService(feishuCompositionDeps{enabled: false})
	require.NoError(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_VersionMismatchFailsClosed(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.verifyVersion = func(context.Context) error { return errors.New("wrong CLI version") }

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_ComposesCompleteWorkspaceBeforePublishing(t *testing.T) {
	composition, err := buildFeishuService(newFeishuCompositionDeps(t))
	require.NoError(t, err)
	require.NotNil(t, composition)
	require.NotNil(t, composition.skillReader)
	require.NotNil(t, composition.operationService)
	require.NotNil(t, composition.authSessionService)
	require.NotNil(t, composition.deviceAuthFlow)
	require.NotNil(t, composition.resumer)
	require.NotNil(t, composition.dispatcher)
	require.NotNil(t, composition.lifecycleService, "Task13 must publish the same Task12 graph to HTTP")
	require.Same(t, composition.dispatcher, composition.authWorkerDispatcher)
	require.NotNil(t, composition.supervisor)
}

func TestBuildFeishuService_DeviceAuthCompositionFailsClosed(t *testing.T) {
	t.Run("missing cipher", func(t *testing.T) {
		deps := newFeishuCompositionDeps(t)
		deps.cipherKeys = nil
		composition, err := buildFeishuService(deps)
		require.Error(t, err)
		require.Nil(t, composition)
	})

	t.Run("missing flow", func(t *testing.T) {
		deps := newFeishuCompositionDeps(t)
		deps.deviceAuthFactory = func(feishu.DeviceAuthFlowDeps) (*feishu.DeviceAuthFlow, error) {
			return nil, nil
		}
		composition, err := buildFeishuService(deps)
		require.Error(t, err)
		require.Nil(t, composition)
	})
}

func TestFeishuAdapter_DeviceAuthStartupCleanupFailsClosed(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	cleanupCalls := 0
	wrapFeishuCompositionSweep(&deps, func(_ context.Context, _ time.Time, cursor string, limit int) (store.FeishuDeviceAuthCleanupPage, error) {
		cleanupCalls++
		require.Empty(t, cursor)
		require.Equal(t, 100, limit)
		return store.FeishuDeviceAuthCleanupPage{}, errors.New("credential cleanup unavailable")
	})

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.ErrorContains(t, err, "cleanup device authorization credentials")
	require.Nil(t, composition, "no lifecycle/tool surface may be published after startup cleanup fails")
	require.Equal(t, 1, cleanupCalls)
}

func TestFeishuAdapter_DeviceAuthStartupCleanupUsesFiveSecondBudget(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	var cleanupCtx context.Context
	var cleanupDeadline time.Time
	var hasDeadline bool
	wrapFeishuCompositionSweep(&deps, func(ctx context.Context, _ time.Time, cursor string, limit int) (store.FeishuDeviceAuthCleanupPage, error) {
		cleanupCtx = ctx
		cleanupDeadline, hasDeadline = ctx.Deadline()
		require.Empty(t, cursor)
		require.Equal(t, 100, limit)
		return store.FeishuDeviceAuthCleanupPage{Done: true}, nil
	})
	startedAt := time.Now()

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	require.NotNil(t, composition)
	require.True(t, hasDeadline)
	require.WithinDuration(t, startedAt.Add(5*time.Second), cleanupDeadline, 500*time.Millisecond)
	require.ErrorIs(t, cleanupCtx.Err(), context.Canceled, "the independent startup budget must be released after cleanup")
}

func TestFeishuAdapter_DeviceAuthStartupCleanupPreservesWaitingOperation(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	operationID := "00000000-0000-4000-8000-000000000091"
	sessionID := "00000000-0000-4000-8000-000000000092"
	expiredAt := time.Now().UTC().Add(-time.Minute)
	recoverySummary := []byte(`{"status":"waiting_user_auth","phase":"user_auth","session_id":"00000000-0000-4000-8000-000000000092","recovery_kind":"user_scope","recovery_scopes":["docx:document:readonly"]}`)
	require.NoError(t, deps.dataStore.DB().Create(&model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 1, State: model.FeishuOperationWaitingUserAuth,
		AgentRunID: 1, ToolCallID: "cleanup-wait", IdempotencyKey: "cleanup-wait",
		CommandPath: "docs +fetch", Domain: "docs", RiskLevel: "read",
		RequestCiphertext: []byte("encrypted-request"), KeyVersion: deps.keyVersion,
		RequestFingerprint: strings.Repeat("b", 64), ResultSummaryJSON: recoverySummary,
	}).Error)
	require.NoError(t, deps.dataStore.DB().Create(&model.FeishuAuthSession{
		ID: sessionID, UserID: 7, Generation: 1, OperationID: &operationID,
		Phase: model.FeishuAuthPhaseUserAuth, State: model.FeishuAuthSessionPending,
		RequestedScopesJSON: []byte(`["docx:document:readonly"]`), ProtocolVersion: 2,
		ResumeCredentialCiphertext: []byte("encrypted-device-code"), ResumeKeyVersion: deps.keyVersion,
		ResumeExpiresAt: &expiredAt, ScopeHash: strings.Repeat("a", 64), ExpiresAt: expiredAt,
	}).Error)

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	require.NotNil(t, composition)
	var operation model.FeishuOperation
	require.NoError(t, deps.dataStore.DB().Where("id = ?", operationID).Take(&operation).Error)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, operation.State)
	var session model.FeishuAuthSession
	require.NoError(t, deps.dataStore.DB().Where("id = ?", sessionID).Take(&session).Error)
	require.Empty(t, session.ResumeCredentialCiphertext)
	require.Empty(t, session.ResumeKeyVersion)
	require.Nil(t, session.ResumeExpiresAt)
	require.Empty(t, session.LeaseOwner)
	require.Nil(t, session.LeaseUntil)
}

func TestFeishuAdapter_DeviceAuthCompositionSharesVaultAndDispatcher(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	var order []string
	deps.verifyVersion = func(context.Context) error {
		order = append(order, "verify-pinned-"+feishu.LarkCLIVersion)
		return nil
	}
	workspace := wrapFeishuCompositionSweep(&deps, func(_ context.Context, _ time.Time, _ string, _ int) (store.FeishuDeviceAuthCleanupPage, error) {
		order = append(order, "cleanup")
		return store.FeishuDeviceAuthCleanupPage{Done: true}, nil
	})
	var captured feishu.DeviceAuthFlowDeps
	var builtFlow *feishu.DeviceAuthFlow
	deps.deviceAuthFactory = func(flowDeps feishu.DeviceAuthFlowDeps) (*feishu.DeviceAuthFlow, error) {
		captured = flowDeps
		var err error
		builtFlow, err = feishu.NewDeviceAuthFlow(flowDeps)
		return builtFlow, err
	}

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	require.NotNil(t, composition)
	require.Equal(t, []string{"verify-pinned-" + feishu.LarkCLIVersion, "cleanup"}, order)
	require.Same(t, builtFlow, composition.deviceAuthFlow)
	require.Same(t, composition.vault, captured.Vault)
	require.Same(t, composition.dispatcher, captured.Dispatcher)
	require.Same(t, composition.authWorkerDispatcher, captured.Dispatcher)
	require.Same(t, workspace, captured.Sessions)
	require.NotNil(t, captured.Cipher)
	require.NotNil(t, composition.lifecycleService, "the lifecycle is exposed only after version validation and cleanup")
}

func TestBuildFeishuService_ComposesAllowlistedDeviceAuthObserver(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	var captured feishu.DeviceAuthObserver
	deps.deviceAuthFactory = func(flowDeps feishu.DeviceAuthFlowDeps) (*feishu.DeviceAuthFlow, error) {
		captured = flowDeps.Observer
		return feishu.NewDeviceAuthFlow(flowDeps)
	}

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	require.NotNil(t, composition)
	require.NotNil(t, captured, "production composition must not silently discard device-auth observations")

	type logEntry struct {
		message string
		fields  []interface{}
	}
	var entries []logEntry
	observer := productionDeviceAuthObserver{sink: func(message string, fields ...interface{}) {
		entries = append(entries, logEntry{message: message, fields: append([]interface{}(nil), fields...)})
	}}
	operationID := "00000000-0000-4000-8000-000000000007"
	sessionID := "00000000-0000-4000-8000-000000000008"
	observer.ObserveDeviceAuth(feishu.DeviceAuthObservation{
		UserID: 7, Generation: 3, OperationID: operationID, SessionID: sessionID,
		Phase: "dispatch", OutcomeClass: "succeeded", CLIVersion: feishu.LarkCLIVersion, Duration: 12 * time.Millisecond,
	})
	observer.ObserveDeviceAuth(feishu.DeviceAuthObservation{
		UserID: 7, Generation: 3, OperationID: operationID, SessionID: sessionID,
		Phase: "cli_complete", OutcomeClass: string(feishu.DeviceAuthPollingNetworkFailure),
		CLIVersion: feishu.LarkCLIVersion, Duration: 30 * time.Second,
	})
	observer.ObserveDeviceAuth(feishu.DeviceAuthObservation{
		UserID: 7, Phase: "raw-secret-phase", OutcomeClass: "token-private-value", CLIVersion: "private-cli-output",
	})
	observer.ObserveDeviceAuth(feishu.DeviceAuthObservation{
		UserID: 7, Phase: "dispatch", OutcomeClass: "token-private-value", CLIVersion: feishu.LarkCLIVersion,
	})
	observer.ObserveDeviceAuth(feishu.DeviceAuthObservation{
		UserID: 7, Phase: "dispatch", OutcomeClass: "succeeded", CLIVersion: "private-cli-output",
	})
	require.Len(t, entries, 2, "only fixed safe phases, outcomes, and CLI versions may reach the production sink")
	require.Equal(t, "feishu device authorization", entries[0].message)
	require.Equal(t, []interface{}{
		"user_id", uint(7), "generation", uint64(3), "operation_id", operationID, "session_id", sessionID,
		"phase", "dispatch", "outcome_class", "succeeded", "cli_version", feishu.LarkCLIVersion,
		"duration", 12 * time.Millisecond,
	}, entries[0].fields)
	flattened, err := json.Marshal(entries)
	require.NoError(t, err)
	require.NotContains(t, string(flattened), "token-private-value")
	require.NotContains(t, string(flattened), "private-cli-output")
}

func TestProductionDeviceAuthObserver_DropsNonCanonicalIdentifiers(t *testing.T) {
	entries := 0
	observer := productionDeviceAuthObserver{sink: func(string, ...interface{}) { entries++ }}
	valid := feishu.DeviceAuthObservation{
		UserID: 7, Generation: 3, Phase: "dispatch", OutcomeClass: "succeeded", CLIVersion: feishu.LarkCLIVersion,
	}

	withPrivateOperationID := valid
	withPrivateOperationID.OperationID = "token-private-value"
	observer.ObserveDeviceAuth(withPrivateOperationID)
	withPrivateSessionID := valid
	withPrivateSessionID.SessionID = "Docs Base Wiki private content"
	observer.ObserveDeviceAuth(withPrivateSessionID)
	for _, invalidID := range []string{
		" 00000000-0000-4000-8000-000000000017 ",
		"urn:uuid:00000000-0000-4000-8000-000000000017",
		"{00000000-0000-4000-8000-000000000017}",
		"00000000-0000-4000-8000-000000000017-too-long",
	} {
		invalid := valid
		invalid.OperationID = invalidID
		observer.ObserveDeviceAuth(invalid)
	}

	require.Zero(t, entries, "non-UUID identifiers must drop the entire event before the logger")
	observer.ObserveDeviceAuth(valid)
	withCanonicalIDs := valid
	withCanonicalIDs.OperationID = "00000000-0000-4000-8000-000000000017"
	withCanonicalIDs.SessionID = "00000000-0000-4000-8000-000000000018"
	observer.ObserveDeviceAuth(withCanonicalIDs)
	require.Equal(t, 2, entries, "empty identifiers and canonical UUIDs remain observable")
}

func TestProductionOperationObserver_AllowsOnlyFixedCredentialFreeFields(t *testing.T) {
	type logEntry struct {
		message string
		fields  []interface{}
	}
	var entries []logEntry
	observer := productionOperationObserver{sink: func(message string, fields ...interface{}) {
		entries = append(entries, logEntry{message: message, fields: append([]interface{}(nil), fields...)})
	}}
	operationID := "00000000-0000-4000-8000-000000000027"
	valid := feishu.OperationObservation{
		UserID: 7, Generation: 3, OperationID: operationID,
		Phase: "invoke", OutcomeClass: feishu.PublicCodeValidationError, Risk: feishu.RiskWrite,
		InvocationStarted: true, ExitCode: 1, CLIVersion: feishu.LarkCLIVersion, Duration: 5 * time.Second,
	}
	observer.ObserveOperation(valid)

	invalidOutcome := valid
	invalidOutcome.OutcomeClass = "token-private-value"
	observer.ObserveOperation(invalidOutcome)
	invalidID := valid
	invalidID.OperationID = "private Base content"
	observer.ObserveOperation(invalidID)
	invalidVersion := valid
	invalidVersion.CLIVersion = "private-cli-output"
	observer.ObserveOperation(invalidVersion)

	require.Len(t, entries, 1)
	require.Equal(t, "feishu business operation", entries[0].message)
	require.Equal(t, []interface{}{
		"user_id", uint(7), "generation", uint64(3), "operation_id", operationID,
		"phase", "invoke", "outcome_class", feishu.PublicCodeValidationError,
		"risk", feishu.RiskWrite, "invocation_started", true, "exit_code", 1,
		"cli_version", feishu.LarkCLIVersion, "duration", 5 * time.Second,
	}, entries[0].fields)
	flattened, err := json.Marshal(entries)
	require.NoError(t, err)
	require.NotContains(t, string(flattened), "token-private-value")
	require.NotContains(t, string(flattened), "private Base content")
	require.NotContains(t, string(flattened), "private-cli-output")
}

func TestBuildFeishuService_KeyRotationReadsHistoricalVault(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("v1", feishuCompositionKey(1)),
		feishuCompositionKeyringEntry("v2", feishuCompositionKey(2)),
	}
	prepareHistoricalCompositionVault(t, deps, "v1", feishuCompositionKey(1))

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	require.NoError(t, composition.vault.WithHome(context.Background(), 7, 1, func(home string) (bool, error) {
		contents, readErr := os.ReadFile(filepath.Join(home, "config.json"))
		require.NoError(t, readErr)
		require.JSONEq(t, `{"key":"historical"}`, string(contents))
		return false, nil
	}))
}

func TestBuildFeishuService_MissingHistoricalKeyFailsClosedBeforePublication(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("v2", feishuCompositionKey(2))}
	prepareHistoricalCompositionVault(t, deps, "v1", feishuCompositionKey(1))

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_KeyRotationRetainsHistoricalAuthSessionKey(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("v1", feishuCompositionKey(1)),
		feishuCompositionKeyringEntry("v2", feishuCompositionKey(2)),
	}
	prepareHistoricalCompositionAuthSession(t, deps, "v1")

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	require.NotNil(t, composition)
}

func TestBuildFeishuService_MissingHistoricalAuthSessionKeyFailsClosedBeforePublication(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("v2", feishuCompositionKey(2))}
	prepareHistoricalCompositionAuthSession(t, deps, "v1")

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_KeyRotationReadsHistoricalSucceededOperation(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("v1", feishuCompositionKey(1)),
		feishuCompositionKeyringEntry("v2", feishuCompositionKey(2)),
	}
	prepareHistoricalCompositionVault(t, deps, "v1", feishuCompositionKey(1))

	historicalCipher, err := crypto.NewCipher(feishuCompositionKey(1))
	require.NoError(t, err)
	historicalKeyring, err := feishu.NewOperationCipherKeyring(map[string]*crypto.Cipher{"v1": historicalCipher}, "v1")
	require.NoError(t, err)
	operationID := uuid.NewString()
	owner := feishu.OperationCipherOwner{UserID: 7, Generation: 1, OperationID: operationID}
	ciphertext, keyVersion, err := historicalKeyring.Seal(feishu.OperationCipherPurposeResult, owner, []byte(`{"document_id":"historical"}`))
	require.NoError(t, err)
	resultBlob, err := json.Marshal(struct {
		KeyVersion string `json:"key_version"`
		Ciphertext []byte `json:"ciphertext"`
	}{KeyVersion: keyVersion, Ciphertext: ciphertext})
	require.NoError(t, err)
	require.NoError(t, deps.dataStore.DB().Create(&model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 1, AgentRunID: 91, ToolCallID: "call-historical",
		IdempotencyKey: "historical-result", CommandPath: "docs +fetch", Domain: "docs", RiskLevel: "read",
		RequestCiphertext: []byte("unread-terminal-request"), KeyVersion: keyVersion,
		RequestFingerprint: "historical-request", State: model.FeishuOperationSucceeded, ResultCiphertext: resultBlob,
	}).Error)

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	resumer := &dispatcherAgentResumerFake{}
	composition.dispatcher.agentResumer = resumer
	require.NoError(t, composition.dispatcher.DispatchResume(context.Background(), 7, operationID))
	backfills := resumer.snapshot()
	require.Len(t, backfills, 1)
	require.Equal(t, uint64(91), backfills[0].RunID)
	require.Equal(t, "call-historical", backfills[0].ToolCallID)
	require.JSONEq(t, `{
		"ok":true,"state":"succeeded","operation_id":"`+operationID+`",
		"data":{"document_id":"historical"}
	}`, string(backfills[0].Result))
}

func TestBuildFeishuService_MissingRetainedResultKeyFailsClosedBeforePublication(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v3"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("v1", feishuCompositionKey(1)),
		feishuCompositionKeyringEntry("v3", feishuCompositionKey(3)),
	}
	prepareHistoricalCompositionVault(t, deps, "v1", feishuCompositionKey(1))

	operationID := uuid.NewString()
	owner := feishu.OperationCipherOwner{UserID: 7, Generation: 1, OperationID: operationID}
	v1Cipher, err := crypto.NewCipher(feishuCompositionKey(1))
	require.NoError(t, err)
	v1Keyring, err := feishu.NewOperationCipherKeyring(map[string]*crypto.Cipher{"v1": v1Cipher}, "v1")
	require.NoError(t, err)
	requestCiphertext, requestKeyVersion, err := v1Keyring.Seal(feishu.OperationCipherPurposeRequest, owner, []byte(`{"argv":["docs","+fetch"]}`))
	require.NoError(t, err)

	v2Cipher, err := crypto.NewCipher(feishuCompositionKey(2))
	require.NoError(t, err)
	v2Keyring, err := feishu.NewOperationCipherKeyring(map[string]*crypto.Cipher{"v2": v2Cipher}, "v2")
	require.NoError(t, err)
	resultCiphertext, resultKeyVersion, err := v2Keyring.Seal(feishu.OperationCipherPurposeResult, owner, []byte(`{"document_id":"rotated"}`))
	require.NoError(t, err)
	resultBlob, err := json.Marshal(struct {
		KeyVersion string `json:"key_version"`
		Ciphertext []byte `json:"ciphertext"`
	}{KeyVersion: resultKeyVersion, Ciphertext: resultCiphertext})
	require.NoError(t, err)
	require.NoError(t, deps.dataStore.DB().Create(&model.FeishuOperation{
		ID: operationID, UserID: 7, Generation: 1, AgentRunID: 91, ToolCallID: "call-rotated",
		IdempotencyKey: "rotated-result", CommandPath: "docs +fetch", Domain: "docs", RiskLevel: "read",
		RequestCiphertext: requestCiphertext, KeyVersion: requestKeyVersion,
		RequestFingerprint: "rotated-request", State: model.FeishuOperationSucceeded, ResultCiphertext: resultBlob,
	}).Error)

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)

	deps.cipherKeys = append(deps.cipherKeys, feishuCompositionKeyringEntry("v2", feishuCompositionKey(2)))
	composition, err = buildFeishuService(deps)
	require.NoError(t, err)
	require.NotNil(t, composition)
	resumer := &dispatcherAgentResumerFake{}
	composition.dispatcher.agentResumer = resumer
	require.NoError(t, composition.dispatcher.DispatchResume(context.Background(), 7, operationID))
	backfills := resumer.snapshot()
	require.Len(t, backfills, 1)
	require.Equal(t, uint64(91), backfills[0].RunID)
	require.Equal(t, "call-rotated", backfills[0].ToolCallID)
	require.JSONEq(t, `{
		"ok":true,"state":"succeeded","operation_id":"`+operationID+`",
		"data":{"document_id":"rotated"}
	}`, string(backfills[0].Result))
}

func TestBuildFeishuService_InvalidRetainedResultBlobFailsClosedBeforePublication(t *testing.T) {
	for name, blob := range map[string][]byte{
		"missing result blob":  nil,
		"missing key version":  []byte(`{"ciphertext":"AQ=="}`),
		"empty ciphertext":     []byte(`{"key_version":"v1","ciphertext":""}`),
		"unknown field":        []byte(`{"key_version":"v1","ciphertext":"AQ==","unexpected":true}`),
		"noncanonical version": []byte(`{"key_version":"V1","ciphertext":"AQ=="}`),
	} {
		t.Run(name, func(t *testing.T) {
			deps := newFeishuCompositionDeps(t)
			deps.keyVersion = "v1"
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("v1", feishuCompositionKey(1))}
			deps.verifyVersion = func(context.Context) error { return nil }
			require.NoError(t, deps.dataStore.DB().Create(&model.FeishuOperation{
				ID: uuid.NewString(), UserID: 7, Generation: 1, AgentRunID: 91, ToolCallID: "call-invalid-result",
				IdempotencyKey: "invalid-result-" + name, CommandPath: "docs +fetch", Domain: "docs", RiskLevel: "read",
				RequestCiphertext: []byte("unread-terminal-request"), KeyVersion: "v1",
				RequestFingerprint: "invalid-result", State: model.FeishuOperationSucceeded, ResultCiphertext: blob,
			}).Error)

			composition, err := buildFeishuService(deps)
			require.Error(t, err)
			require.Nil(t, composition)
		})
	}
}

func TestBuildFeishuService_KeyRotationNewVaultWriteUsesCurrentVersion(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("v1", feishuCompositionKey(1)),
		feishuCompositionKeyringEntry("v2", feishuCompositionKey(2)),
	}
	prepareHistoricalCompositionVault(t, deps, "v1", feishuCompositionKey(1))

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	require.NoError(t, composition.vault.WithHome(context.Background(), 7, 1, func(home string) (bool, error) {
		return true, os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"key":"current"}`), 0o600)
	}))
	stored, err := deps.dataStore.FeishuWorkspace().GetVault(context.Background(), 7, 1)
	require.NoError(t, err)
	require.Equal(t, "v2", stored.KeyVersion)
}

func TestBuildFeishuService_KeyRotationNewOperationWriteUsesCurrentVersion(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("v1", feishuCompositionKey(1)),
		feishuCompositionKeyringEntry("v2", feishuCompositionKey(2)),
	}
	deps.receiptVerifier = compositionReceiptVerifier{}
	prepareHistoricalCompositionVault(t, deps, "v1", feishuCompositionKey(1))

	composition, err := buildFeishuService(deps)
	require.NoError(t, err)
	result, err := composition.operationService.Execute(context.Background(), feishu.ExecuteRequest{
		UserID: 7, AgentRunID: 92, ToolCallID: "call-current", IdempotencyKey: "92:call-current",
		Argv:          []string{"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "overwrite", "--content", "current"},
		SkillReceipts: []string{"test-receipt"},
	})
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, result.State)
	stored, err := deps.dataStore.FeishuWorkspace().GetOperationForUser(context.Background(), 7, 1, result.OperationID)
	require.NoError(t, err)
	require.Equal(t, "v2", stored.KeyVersion)
}

func TestBuildFeishuService_RejectsInvalidEncryptionKeyring(t *testing.T) {
	valid := feishuCompositionKey(7)
	for name, mutate := range map[string]func(*feishuCompositionDeps){
		"missing keyring": func(deps *feishuCompositionDeps) { deps.cipherKeys = nil },
		"invalid base64": func(deps *feishuCompositionDeps) {
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("test-v1", "not-base64")}
		},
		"noncanonical base64": func(deps *feishuCompositionDeps) {
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("test-v1", valid+"\n")}
		},
		"wrong decoded length": func(deps *feishuCompositionDeps) {
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("test-v1", base64.StdEncoding.EncodeToString([]byte("short")))}
		},
		"invalid version": func(deps *feishuCompositionDeps) {
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("bad/version", valid)}
		},
		"uppercase current and configured version": func(deps *feishuCompositionDeps) {
			deps.keyVersion = "V1"
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("V1", valid)}
		},
		"noncanonical current version": func(deps *feishuCompositionDeps) {
			deps.keyVersion = " v2"
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry(" v2", valid)}
		},
		"current version absent": func(deps *feishuCompositionDeps) {
			deps.keyVersion = "v2"
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("v1", valid)}
		},
		"duplicate material": func(deps *feishuCompositionDeps) {
			deps.keyVersion = "v2"
			deps.cipherKeys = []feishuCipherKeyringEntry{
				feishuCompositionKeyringEntry("v1", valid),
				feishuCompositionKeyringEntry("v2", valid),
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			deps := newFeishuCompositionDeps(t)
			mutate(&deps)

			composition, err := buildFeishuService(deps)
			require.Error(t, err)
			require.Nil(t, composition)
			require.NotContains(t, err.Error(), valid)
		})
	}
}

func TestBuildFeishuService_NoncanonicalPersistedKeyVersionsFailClosed(t *testing.T) {
	for name, persist := range map[string]func(*testing.T, feishuCompositionDeps){
		"vault": func(t *testing.T, deps feishuCompositionDeps) {
			t.Helper()
			require.NoError(t, deps.dataStore.DB().Create(&model.FeishuCLIVault{
				UserID: 7, Generation: 1, Ciphertext: []byte("vault-ciphertext"), KeyVersion: "V1",
				Checksum: "checksum", Revision: 1,
			}).Error)
		},
		"operation request": func(t *testing.T, deps feishuCompositionDeps) {
			t.Helper()
			require.NoError(t, deps.dataStore.DB().Create(&model.FeishuOperation{
				ID: uuid.NewString(), UserID: 7, Generation: 1, AgentRunID: 91, ToolCallID: "call-noncanonical-request",
				IdempotencyKey: "noncanonical-request", CommandPath: "docs +fetch", Domain: "docs", RiskLevel: "read",
				RequestCiphertext: []byte("request-ciphertext"), KeyVersion: "V1", RequestFingerprint: "request",
				State: model.FeishuOperationWaitingConfirmation,
			}).Error)
		},
	} {
		t.Run(name, func(t *testing.T) {
			deps := newFeishuCompositionDeps(t)
			deps.keyVersion = "v1"
			deps.cipherKeys = []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("v1", feishuCompositionKey(1))}
			deps.verifyVersion = func(context.Context) error { return nil }
			persist(t, deps)

			composition, err := buildFeishuService(deps)
			require.Error(t, err)
			require.Nil(t, composition)
		})
	}
}

func TestBuildFeishuCipherKeyring_RejectsNoncanonicalViperVersion(t *testing.T) {
	key := feishuCompositionKey(7)
	config := viper.New()
	config.SetConfigType("yaml")
	require.NoError(t, config.ReadConfig(strings.NewReader("feishu:\n  key_version: V1\n  keyring:\n    - version: V1\n      key: "+key+"\n")))

	require.Equal(t, "V1", config.GetString("feishu.key_version"))
	entries, err := readFeishuCipherKeyring(config)
	require.NoError(t, err)
	require.Equal(t, []feishuCipherKeyringEntry{feishuCompositionKeyringEntry("V1", key)}, entries)
	_, err = buildFeishuCipherKeyring(config.GetString("feishu.key_version"), entries)
	require.Error(t, err)
	require.NotContains(t, err.Error(), key)
}

func TestBuildConfiguredFeishuService_RejectsAmbiguousLegacyViperKeyringMap(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	firstKey := feishuCompositionKey(1)
	secondKey := feishuCompositionKey(2)
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader("features:\n  feishu_integration:\n    enabled: true\nsecurity:\n  thirdparty_token_key: "+feishuCompositionKey(9)+"\nfeishu:\n  key_version: v1\n  keyring:\n    V1: "+firstKey+"\n    v1: "+secondKey+"\n  runtime_base: "+filepath.Join(t.TempDir(), "runtime")+"\n  auth_owner: test-feishu-auth-worker\n")))

	deps := newFeishuCompositionDeps(t)
	workspace, err := buildConfiguredFeishuService(deps.dataStore, deps.studentRuns, deps.resumeStore, deps.supervisor)
	require.Error(t, err)
	require.ErrorContains(t, err, "keyring configuration")
	require.Nil(t, workspace)
	require.NotContains(t, err.Error(), firstKey)
	require.NotContains(t, err.Error(), secondKey)
}

func TestBuildConfiguredFeishuService_FeatureFlagOffDoesNotRequireKeyring(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader("features:\n  feishu_integration:\n    enabled: false\n")))

	deps := newFeishuCompositionDeps(t)
	workspace, err := buildConfiguredFeishuService(deps.dataStore, deps.studentRuns, deps.resumeStore, deps.supervisor)
	require.NoError(t, err)
	require.Nil(t, workspace)
}

func TestBuildFeishuCipherKeyring_RejectsDuplicateCanonicalViperListVersions(t *testing.T) {
	firstKey := feishuCompositionKey(1)
	secondKey := feishuCompositionKey(2)
	config := viper.New()
	config.SetConfigType("yaml")
	require.NoError(t, config.ReadConfig(strings.NewReader("feishu:\n  keyring:\n    - version: v1\n      key: "+firstKey+"\n    - version: v1\n      key: "+secondKey+"\n")))

	entries, err := readFeishuCipherKeyring(config)
	require.NoError(t, err)
	require.Equal(t, []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("v1", firstKey),
		feishuCompositionKeyringEntry("v1", secondKey),
	}, entries)

	_, err = buildFeishuCipherKeyring("v1", entries)
	require.Error(t, err)
	require.NotContains(t, err.Error(), firstKey)
	require.NotContains(t, err.Error(), secondKey)
}

func TestBuildFeishuCipherKeyring_RejectsNoncanonicalViperListVersions(t *testing.T) {
	uppercaseKey := feishuCompositionKey(1)
	canonicalKey := feishuCompositionKey(2)
	config := viper.New()
	config.SetConfigType("yaml")
	require.NoError(t, config.ReadConfig(strings.NewReader("feishu:\n  keyring:\n    - version: V1\n      key: "+uppercaseKey+"\n    - version: v1\n      key: "+canonicalKey+"\n")))

	entries, err := readFeishuCipherKeyring(config)
	require.NoError(t, err)
	require.Equal(t, []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("V1", uppercaseKey),
		feishuCompositionKeyringEntry("v1", canonicalKey),
	}, entries)

	_, err = buildFeishuCipherKeyring("v1", entries)
	require.Error(t, err)
	require.NotContains(t, err.Error(), uppercaseKey)
	require.NotContains(t, err.Error(), canonicalKey)
}

func TestBuildFeishuCipherKeyring_ReadsOrderedViperList(t *testing.T) {
	v1Key := feishuCompositionKey(1)
	v2Key := feishuCompositionKey(2)
	config := viper.New()
	config.SetConfigType("yaml")
	require.NoError(t, config.ReadConfig(strings.NewReader("feishu:\n  key_version: v2\n  keyring:\n    - version: v1\n      key: "+v1Key+"\n    - version: v2\n      key: "+v2Key+"\n")))

	entries, err := readFeishuCipherKeyring(config)
	require.NoError(t, err)
	require.Equal(t, []feishuCipherKeyringEntry{
		feishuCompositionKeyringEntry("v1", v1Key),
		feishuCompositionKeyringEntry("v2", v2Key),
	}, entries)

	ciphers, err := buildFeishuCipherKeyring(config.GetString("feishu.key_version"), entries)
	require.NoError(t, err)
	require.Len(t, ciphers, 2)
	require.Contains(t, ciphers, "v1")
	require.Contains(t, ciphers, "v2")
}

func TestReadFeishuCipherKeyring_RuntimeViperEnvironmentIsStrictJSON(t *testing.T) {
	firstKey := feishuCompositionKey(1)
	secondKey := feishuCompositionKey(2)

	for _, testCase := range []struct {
		name     string
		raw      string
		wantOK   bool
		wantKeys []string
	}{
		{
			name:     "ordered list",
			raw:      `[{"version":"v1","key":"` + firstKey + `"},{"version":"v2","key":"` + secondKey + `"}]`,
			wantOK:   true,
			wantKeys: []string{"v1", "v2"},
		},
		{name: "object map", raw: `{"v1":"` + firstKey + `"}`},
		{name: "unknown entry field", raw: `[{"version":"v1","key":"` + firstKey + `","extra":true}]`},
		{name: "uppercase entry field", raw: `[{"Version":"v1","key":"` + firstKey + `"}]`},
		{name: "duplicate entry field", raw: `[{"version":"v1","version":"v2","key":"` + firstKey + `"}]`},
		{name: "trailing data", raw: `[{"version":"v1","key":"` + firstKey + `"}] {}`},
		{name: "duplicate version", raw: `[{"version":"v1","key":"` + firstKey + `"},{"version":"v1","key":"` + secondKey + `"}]`},
		{name: "uppercase version", raw: `[{"version":"V1","key":"` + firstKey + `"}]`},
		{name: "invalid base64", raw: `[{"version":"v1","key":"not-base64"}]`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := viper.New()
			config.SetConfigType("yaml")
			// A legacy map in file configuration must not bypass the environment
			// value. The environment is the only runtime secret entry point.
			require.NoError(t, config.ReadConfig(strings.NewReader("feishu:\n  keyring:\n    V1: ignored-file-value\n")))
			numindconfig.SetupViperEnvBindings(config)
			t.Setenv("NUMIND_SECURITY_THIRDPARTY_TOKEN_KEY", feishuCompositionKey(9))
			t.Setenv("NUMIND_FEISHU_KEYRING", testCase.raw)
			t.Setenv("NUMIND_FEISHU_KEY_VERSION", "v2")
			t.Setenv("NUMIND_FEISHU_RUNTIME_BASE", filepath.Join(t.TempDir(), "runtime"))
			t.Setenv("NUMIND_FEISHU_AUTH_OWNER", "test-feishu-auth-worker")
			t.Setenv("NUMIND_FEATURES_FEISHU_INTEGRATION_ENABLED", "true")

			require.Equal(t, feishuCompositionKey(9), config.GetString("security.thirdparty_token_key"))
			require.Equal(t, "v2", config.GetString("feishu.key_version"))
			require.NotEmpty(t, config.GetString("feishu.runtime_base"))
			require.Equal(t, "test-feishu-auth-worker", config.GetString("feishu.auth_owner"))
			require.True(t, config.GetBool("features.feishu_integration.enabled"))

			entries, err := readFeishuCipherKeyring(config)
			if !testCase.wantOK && err == nil {
				_, err = buildFeishuCipherKeyring(config.GetString("feishu.key_version"), entries)
			}
			if !testCase.wantOK {
				require.Error(t, err)
				require.NotContains(t, err.Error(), firstKey)
				require.NotContains(t, err.Error(), secondKey)
				return
			}
			require.NoError(t, err)

			ciphers, err := buildFeishuCipherKeyring(config.GetString("feishu.key_version"), entries)
			require.NoError(t, err)
			for _, version := range testCase.wantKeys {
				require.Contains(t, ciphers, version)
			}
		})
	}
}

func TestFeishuWorkspacePublication_UsesOneComposedGraph(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	composition, err := buildFeishuService(deps)
	require.NoError(t, err)

	b := &biz{}
	b.publishFeishuPersonalWorkspace(composition, deps.resumeStore)

	// Task 13's future HTTP service has one private source, while the auth
	// worker and process reclaimer keep the exact dispatcher/resumer graph that
	// composition created. No second service construction is necessary.
	require.Same(t, composition, b.feishuWorkspace)
	require.Same(t, composition.supervisor, b.externalResumeSupervisor)
	require.NotNil(t, b.externalResumeReclaimer)
	require.Same(t, composition.dispatcher, composition.authWorkerDispatcher)
}

func TestFeishuWorkspacePublication_FailedCompositionLeavesBizUnpublished(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.verifyVersion = func(context.Context) error { return errors.New("wrong CLI version") }
	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)

	b := &biz{}
	b.publishFeishuPersonalWorkspace(composition, deps.resumeStore)
	require.Nil(t, b.feishuWorkspace)
	require.Nil(t, b.externalResumeSupervisor)
	require.Nil(t, b.externalResumeReclaimer)
}

func TestBuildFeishuService_MissingExplicitRuntimeRootFailsClosed(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.runtimeBase = ""

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_CleanupFailurePreventsPublication(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	outside := t.TempDir()
	link := filepath.Join(t.TempDir(), "runtime-link")
	require.NoError(t, os.Symlink(outside, link))
	deps.runtimeBase = link

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}
