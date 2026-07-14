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

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
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
		&model.FeishuOperation{},
	))
	ds := store.NewTestStore(db)
	resumeStore, ok := ds.AgentRuns().(store.IExternalToolResumeLease)
	require.True(t, ok)
	tokenKey := feishuCompositionKey(0)
	return feishuCompositionDeps{
		enabled:       true,
		dataStore:     ds,
		tokenKey:      tokenKey,
		cipherKeys:    map[string]string{"test-v1": tokenKey},
		keyVersion:    "test-v1",
		runtimeBase:   filepath.Join(t.TempDir(), "runtime"),
		authOwner:     "test-feishu-auth-worker",
		studentRuns:   agent.NewStudentRunService(nil, ds.AgentRuns(), ds.AgentDefinitions(), nil, nil, nil),
		resumeStore:   resumeStore,
		supervisor:    agent.NewExternalContinuationSupervisor(agent.ExternalContinuationLimit),
		verifyVersion: func(context.Context) error { return nil },
	}
}

func feishuCompositionKey(seed byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{seed}, crypto.KeyLen))
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

type compositionReceiptVerifier struct{}

func (compositionReceiptVerifier) VerifyRequired([]string, uint64, string) error { return nil }

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
	require.NotNil(t, composition.resumer)
	require.NotNil(t, composition.dispatcher)
	require.Same(t, composition.dispatcher, composition.authWorkerDispatcher)
	require.NotNil(t, composition.supervisor)
}

func TestBuildFeishuService_KeyRotationReadsHistoricalVault(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = map[string]string{"v1": feishuCompositionKey(1), "v2": feishuCompositionKey(2)}
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
	deps.cipherKeys = map[string]string{"v2": feishuCompositionKey(2)}
	prepareHistoricalCompositionVault(t, deps, "v1", feishuCompositionKey(1))

	composition, err := buildFeishuService(deps)
	require.Error(t, err)
	require.Nil(t, composition)
}

func TestBuildFeishuService_KeyRotationReadsHistoricalSucceededOperation(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v2"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = map[string]string{"v1": feishuCompositionKey(1), "v2": feishuCompositionKey(2)}
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
	require.JSONEq(t, `{"document_id":"historical"}`, string(backfills[0].Result))
}

func TestBuildFeishuService_MissingRetainedResultKeyFailsClosedBeforePublication(t *testing.T) {
	deps := newFeishuCompositionDeps(t)
	deps.keyVersion = "v3"
	deps.tokenKey = feishuCompositionKey(9)
	deps.cipherKeys = map[string]string{"v1": feishuCompositionKey(1), "v3": feishuCompositionKey(3)}
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

	deps.cipherKeys["v2"] = feishuCompositionKey(2)
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
	require.JSONEq(t, `{"document_id":"rotated"}`, string(backfills[0].Result))
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
			deps.cipherKeys = map[string]string{"v1": feishuCompositionKey(1)}
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
	deps.cipherKeys = map[string]string{"v1": feishuCompositionKey(1), "v2": feishuCompositionKey(2)}
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
	deps.cipherKeys = map[string]string{"v1": feishuCompositionKey(1), "v2": feishuCompositionKey(2)}
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
		"invalid base64":  func(deps *feishuCompositionDeps) { deps.cipherKeys = map[string]string{"test-v1": "not-base64"} },
		"noncanonical base64": func(deps *feishuCompositionDeps) {
			deps.cipherKeys = map[string]string{"test-v1": valid + "\n"}
		},
		"wrong decoded length": func(deps *feishuCompositionDeps) {
			deps.cipherKeys = map[string]string{"test-v1": base64.StdEncoding.EncodeToString([]byte("short"))}
		},
		"invalid version": func(deps *feishuCompositionDeps) { deps.cipherKeys = map[string]string{"bad/version": valid} },
		"uppercase current and configured version": func(deps *feishuCompositionDeps) {
			deps.keyVersion = "V1"
			deps.cipherKeys = map[string]string{"V1": valid}
		},
		"noncanonical current version": func(deps *feishuCompositionDeps) {
			deps.keyVersion = " v2"
			deps.cipherKeys = map[string]string{" v2": valid}
		},
		"current version absent": func(deps *feishuCompositionDeps) {
			deps.keyVersion = "v2"
			deps.cipherKeys = map[string]string{"v1": valid}
		},
		"duplicate material": func(deps *feishuCompositionDeps) {
			deps.keyVersion = "v2"
			deps.cipherKeys = map[string]string{"v1": valid, "v2": valid}
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
			deps.cipherKeys = map[string]string{"v1": feishuCompositionKey(1)}
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
	require.NoError(t, config.ReadConfig(strings.NewReader("feishu:\n  key_version: V1\n  keyring:\n    V1: "+key+"\n")))

	require.Equal(t, "V1", config.GetString("feishu.key_version"))
	require.Equal(t, map[string]string{"v1": key}, config.GetStringMapString("feishu.keyring"))
	_, err := buildFeishuCipherKeyring(config.GetString("feishu.key_version"), config.GetStringMapString("feishu.keyring"))
	require.Error(t, err)
	require.NotContains(t, err.Error(), key)
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
