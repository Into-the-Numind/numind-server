package biz

// feishu_adapter.go is the sole production composition root for the personal
// workspace. It deliberately has the only imports that bridge biz/agent and
// biz/feishu; the lower Feishu package never imports Agent.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"

	"github.com/spf13/viper"
)

// feishuPersonalWorkspace is complete only after every bridge endpoint has
// been assigned. NewBiz injects it into the platform factory only after this
// constructor returns successfully.
type feishuPersonalWorkspace struct {
	runner             *feishu.ControlledLarkCLIRunner
	vault              *feishu.EncryptedCLIHomeVault
	catalog            *feishu.CommandCatalog
	skillReader        *feishu.SkillReader
	operationService   *feishu.FeishuOperationService
	authSessionService *feishu.AuthSessionService
	// lifecycleService is the only HTTP-facing view of this graph. It uses the
	// same auth instance, operation instance, and dispatcher below; it must not
	// reconstruct a legacy orchestrator or an additional Agent resumer.
	lifecycleService feishu.IFeishuService
	resumer          *agent.AgentRunResumer
	dispatcher       *WorkspaceResumeDispatcher
	// authWorkerDispatcher records the exact dispatcher handed to the
	// authorization worker. Keeping this immutable composition edge explicit
	// prevents a later HTTP adapter from accidentally constructing a second
	// callback path.
	authWorkerDispatcher feishu.OperationResumeDispatcher
	supervisor           *agent.ExternalContinuationSupervisor
}

// feishuCipherKeyringEntry is one ordered, explicit keyring configuration
// entry. Version is a value rather than a map key so Viper cannot collapse
// differently-cased labels before validation.
type feishuCipherKeyringEntry struct {
	Version string `json:"version"`
	Key     string `json:"key"`
}

type feishuCompositionDeps struct {
	enabled   bool
	dataStore store.IStore
	// tokenKey is exclusively the SkillReader receipt/cursor HMAC root. It is
	// intentionally separate from the versioned encryption keyring below.
	tokenKey    string
	cipherKeys  []feishuCipherKeyringEntry
	keyVersion  string
	runtimeBase string
	authOwner   string

	studentRuns *agent.StudentRunService
	resumeStore store.IExternalToolResumeLease
	supervisor  *agent.ExternalContinuationSupervisor
	runner      *feishu.ControlledLarkCLIRunner
	// receiptVerifier is a test seam for exercising operation persistence
	// without invoking the controlled skill reader. Production always uses the
	// SkillReader built from tokenKey.
	receiptVerifier feishu.ReceiptVerifier

	// verifyVersion is a test seam. Production leaves it nil so the fixed
	// ControlledLarkCLIRunner probe is always performed.
	verifyVersion func(context.Context) error
}

// buildFeishuService composes the flag-gated personal workspace graph. It
// returns nil for a disabled flag. Every enabled-path dependency is explicit:
// there are no generated keys, shared runtime directory fallbacks, or
// partially-visible services after a startup failure.
func buildFeishuService(deps feishuCompositionDeps) (*feishuPersonalWorkspace, error) {
	if !deps.enabled {
		return nil, nil
	}
	if deps.dataStore == nil || deps.studentRuns == nil || deps.resumeStore == nil || deps.supervisor == nil ||
		strings.TrimSpace(deps.tokenKey) == "" || strings.TrimSpace(deps.keyVersion) == "" ||
		strings.TrimSpace(deps.runtimeBase) == "" || strings.TrimSpace(deps.authOwner) == "" {
		return nil, fmt.Errorf("feishu personal workspace configuration is incomplete")
	}

	ciphers, err := buildFeishuCipherKeyring(deps.keyVersion, deps.cipherKeys)
	if err != nil {
		return nil, fmt.Errorf("feishu: build encryption keyring: %w", err)
	}
	if err := verifyFeishuPersistedKeyVersions(deps.dataStore, ciphers); err != nil {
		return nil, fmt.Errorf("feishu: validate persisted encryption versions: %w", err)
	}
	vault, err := feishu.NewEncryptedCLIHomeVaultWithKeyring(
		deps.dataStore.ThirdPartyAccounts(), deps.dataStore.FeishuWorkspace(),
		ciphers, deps.keyVersion, deps.runtimeBase,
	)
	if err != nil {
		return nil, fmt.Errorf("feishu: build CLI home vault: %w", err)
	}
	// Cleanup is startup-only and must happen before any service, tool, or auth
	// worker can obtain the vault. A cleanup error is intentionally fatal to the
	// whole feature rather than risking an abandoned plaintext HOME.
	if err := vault.CleanupRuntimeHomesAtStartup(); err != nil {
		return nil, fmt.Errorf("feishu: cleanup runtime homes: %w", err)
	}

	runner := deps.runner
	if runner == nil {
		runner = &feishu.ControlledLarkCLIRunner{}
	}
	verifyVersion := deps.verifyVersion
	if verifyVersion == nil {
		verifyVersion = runner.VerifyVersion
	}
	if err := verifyVersion(context.Background()); err != nil {
		return nil, fmt.Errorf("feishu: verify controlled CLI version: %w", err)
	}

	operationCipher, err := feishu.NewOperationCipherKeyring(ciphers, deps.keyVersion)
	if err != nil {
		return nil, fmt.Errorf("feishu: build operation cipher keyring: %w", err)
	}
	skillReader, err := feishu.NewSkillReader(deps.tokenKey)
	if err != nil {
		return nil, fmt.Errorf("feishu: build skill reader: %w", err)
	}
	confirmation, err := feishu.NewOperationConfirmationRequester(deps.dataStore)
	if err != nil {
		return nil, fmt.Errorf("feishu: build confirmation requester: %w", err)
	}
	catalog := feishu.NewCommandCatalog()

	// authService is deliberately assigned only after OperationService has its
	// RecoveryStarter. The closures are never invoked before this function
	// returns, and fail closed if a future refactor violates that invariant.
	var authService *feishu.AuthSessionService
	recovery := feishu.RecoveryStarterFunc{
		StartRecoveryFunc: func(ctx context.Context, request feishu.RecoveryRequest) (*feishu.OperationAction, error) {
			if authService == nil {
				return nil, fmt.Errorf("feishu authorization service is unavailable")
			}
			return authService.StartRecovery(ctx, request)
		},
		ActivateFunc: func(ctx context.Context, sessionID string) error {
			if authService == nil {
				return fmt.Errorf("feishu authorization service is unavailable")
			}
			return authService.Activate(ctx, sessionID)
		},
		AbortFunc: func(sessionID string) {
			if authService != nil {
				authService.Abort(sessionID)
			}
		},
	}
	receiptVerifier := feishu.ReceiptVerifier(skillReader)
	if deps.receiptVerifier != nil {
		receiptVerifier = deps.receiptVerifier
	}
	operationService, err := feishu.NewFeishuOperationService(feishu.OperationServiceDeps{
		Accounts:           deps.dataStore.ThirdPartyAccounts(),
		Operations:         deps.dataStore.FeishuWorkspace(),
		Catalog:            catalog,
		Receipts:           receiptVerifier,
		Recovery:           recovery,
		Confirmation:       confirmation,
		Vault:              vault,
		Runner:             runner,
		Cipher:             operationCipher,
		VerifiedCLIVersion: feishu.LarkCLIVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build operation service: %w", err)
	}
	resumer := agent.NewAgentRunResumer(deps.resumeStore, deps.studentRuns, deps.supervisor)
	dispatcher := NewWorkspaceResumeDispatcher(operationService, resumer)
	authService, err = feishu.NewAuthSessionService(feishu.AuthSessionServiceDeps{
		Accounts:           deps.dataStore.ThirdPartyAccounts(),
		Sessions:           deps.dataStore.FeishuWorkspace(),
		Vault:              vault,
		CLI:                runner,
		Dispatcher:         dispatcher,
		Owner:              deps.authOwner,
		VerifiedCLIVersion: feishu.LarkCLIVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build authorization service: %w", err)
	}
	teardown, err := feishu.NewRetiredWorkspaceTeardown(vault, runner)
	if err != nil {
		return nil, fmt.Errorf("feishu: build workspace teardown: %w", err)
	}
	lifecycleService, err := feishu.NewWorkspaceLifecycleService(feishu.WorkspaceLifecycleDeps{
		Accounts: deps.dataStore.ThirdPartyAccounts(), Workspace: deps.dataStore.FeishuWorkspace(),
		Auth: authService, Dispatcher: dispatcher, Operations: operationService,
		Executions: operationService,
		Teardown:   teardown,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build lifecycle service: %w", err)
	}

	return &feishuPersonalWorkspace{
		runner: runner, vault: vault, catalog: catalog, skillReader: skillReader,
		operationService: operationService, authSessionService: authService,
		lifecycleService: lifecycleService,
		resumer:          resumer, dispatcher: dispatcher, authWorkerDispatcher: dispatcher,
		supervisor: deps.supervisor,
	}, nil
}

// buildFeishuCipherKeyring freezes the configured decryption window. Every
// configured entry must be a canonical key version and a strict base64-encoded
// AES-256 key. The current version is the only writer; historical entries stay
// available only so existing vault snapshots and operation blobs can open.
// Errors intentionally omit key material.
func buildFeishuCipherKeyring(currentVersion string, configured []feishuCipherKeyringEntry) (map[string]*crypto.Cipher, error) {
	if !validFeishuCipherKeyVersion(currentVersion) || len(configured) == 0 {
		return nil, fmt.Errorf("invalid keyring configuration")
	}

	ciphers := make(map[string]*crypto.Cipher, len(configured))
	seenMaterial := make(map[[crypto.KeyLen]byte]struct{}, len(configured))
	seenVersions := make(map[string]struct{}, len(configured))
	for _, entry := range configured {
		version := entry.Version
		encodedKey := entry.Key
		if !validFeishuCipherKeyVersion(version) {
			return nil, fmt.Errorf("invalid keyring configuration")
		}
		if _, duplicate := seenVersions[version]; duplicate {
			return nil, fmt.Errorf("invalid keyring configuration")
		}
		seenVersions[version] = struct{}{}
		decoded, err := base64.StdEncoding.DecodeString(encodedKey)
		if err != nil || len(decoded) != crypto.KeyLen || base64.StdEncoding.EncodeToString(decoded) != encodedKey {
			return nil, fmt.Errorf("invalid keyring configuration")
		}
		var material [crypto.KeyLen]byte
		copy(material[:], decoded)
		if _, duplicate := seenMaterial[material]; duplicate {
			return nil, fmt.Errorf("duplicate keyring material")
		}
		seenMaterial[material] = struct{}{}
		cipher, err := crypto.NewCipher(encodedKey)
		if err != nil {
			return nil, fmt.Errorf("invalid keyring configuration")
		}
		ciphers[version] = cipher
	}
	if _, found := ciphers[currentVersion]; !found {
		return nil, fmt.Errorf("current key version unavailable")
	}
	return ciphers, nil
}

func validFeishuCipherKeyVersion(version string) bool {
	if len(version) == 0 || len(version) > 32 || strings.TrimSpace(version) != version {
		return false
	}
	for _, char := range version {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') &&
			char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

// verifyFeishuPersistedKeyVersions makes an enabled feature fail at startup
// when retained data refers to a key outside the configured decryption window.
// This keeps an incomplete rotation from publishing tools that would otherwise
// fail only after a user callback reaches an old vault or operation result.
func verifyFeishuPersistedKeyVersions(dataStore store.IStore, ciphers map[string]*crypto.Cipher) error {
	if dataStore == nil || dataStore.DB() == nil {
		return fmt.Errorf("persistent key version store unavailable")
	}
	for _, persisted := range []any{&model.FeishuCLIVault{}, &model.FeishuOperation{}} {
		var versions []string
		if err := dataStore.DB().Model(persisted).Distinct().Pluck("key_version", &versions).Error; err != nil {
			return fmt.Errorf("persistent key version store unavailable")
		}
		for _, version := range versions {
			if err := verifyFeishuPersistedKeyVersion(version, ciphers); err != nil {
				return err
			}
		}
	}
	var resultBlobs []struct {
		ResultCiphertext []byte `gorm:"column:result_ciphertext"`
	}
	if err := dataStore.DB().Model(&model.FeishuOperation{}).
		Select("result_ciphertext").
		Where("state = ? OR (result_ciphertext IS NOT NULL AND length(result_ciphertext) > 0)", model.FeishuOperationSucceeded).
		Find(&resultBlobs).Error; err != nil {
		return fmt.Errorf("persistent key version store unavailable")
	}
	for _, resultBlob := range resultBlobs {
		version, err := feishu.OperationSealedBlobKeyVersion(resultBlob.ResultCiphertext)
		if err != nil {
			return fmt.Errorf("persistent result encryption is invalid")
		}
		if err := verifyFeishuPersistedKeyVersion(version, ciphers); err != nil {
			return err
		}
	}
	return nil
}

func verifyFeishuPersistedKeyVersion(version string, ciphers map[string]*crypto.Cipher) error {
	if !validFeishuCipherKeyVersion(version) {
		return fmt.Errorf("persistent key version is invalid")
	}
	if _, found := ciphers[version]; !found {
		return fmt.Errorf("persistent key version is unavailable")
	}
	return nil
}

// readFeishuCipherKeyring reads the only supported, ordered Viper keyring
// form. YAML lists arrive as []any while Viper environment overrides arrive as
// strings, so the latter is accepted only as one strict JSON array. It
// deliberately rejects maps before extracting any material: Viper normalizes
// map keys and would otherwise erase a duplicate version label.
func readFeishuCipherKeyring(config *viper.Viper) ([]feishuCipherKeyringEntry, error) {
	if config == nil {
		return nil, fmt.Errorf("invalid keyring configuration")
	}

	switch raw := config.Get("feishu.keyring").(type) {
	case string:
		return decodeFeishuCipherKeyringJSON(raw)
	case []any:
		return decodeFeishuCipherKeyringViperList(raw)
	default:
		return nil, fmt.Errorf("invalid keyring configuration")
	}
}

// decodeFeishuCipherKeyringJSON accepts the string-only Viper environment
// representation. Decoder strictness and the explicit EOF check make a JSON
// object, unknown entry field, or concatenated second value fail closed.
func decodeFeishuCipherKeyringJSON(raw string) ([]feishuCipherKeyringEntry, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var entries []feishuCipherKeyringEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("invalid keyring configuration")
	}
	if err := requireFeishuJSONEOF(decoder); err != nil || len(entries) == 0 {
		return nil, fmt.Errorf("invalid keyring configuration")
	}
	return entries, nil
}

func requireFeishuJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

// UnmarshalJSON rejects the case-insensitive field matching and duplicate
// field overwrites that encoding/json's default struct decoder would allow.
// Keeping version and key as exact lower-case names prevents two textual
// representations from silently becoming one trusted keyring entry.
func (entry *feishuCipherKeyringEntry) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	first, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid keyring entry")
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("invalid keyring entry")
	}

	var decoded feishuCipherKeyringEntry
	seenVersion, seenKey := false, false
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("invalid keyring entry")
		}
		name, ok := field.(string)
		if !ok {
			return fmt.Errorf("invalid keyring entry")
		}
		switch name {
		case "version":
			if seenVersion || decoder.Decode(&decoded.Version) != nil {
				return fmt.Errorf("invalid keyring entry")
			}
			seenVersion = true
		case "key":
			if seenKey || decoder.Decode(&decoded.Key) != nil {
				return fmt.Errorf("invalid keyring entry")
			}
			seenKey = true
		default:
			return fmt.Errorf("invalid keyring entry")
		}
	}
	last, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("invalid keyring entry")
	}
	if delimiter, ok := last.(json.Delim); !ok || delimiter != '}' || !seenVersion || !seenKey {
		return fmt.Errorf("invalid keyring entry")
	}
	if err := requireFeishuJSONEOF(decoder); err != nil {
		return fmt.Errorf("invalid keyring entry")
	}
	*entry = decoded
	return nil
}

func decodeFeishuCipherKeyringViperList(raw []any) ([]feishuCipherKeyringEntry, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("invalid keyring configuration")
	}
	entries := make([]feishuCipherKeyringEntry, 0, len(raw))
	for _, rawEntry := range raw {
		entry, found := rawEntry.(map[string]any)
		if !found || len(entry) != 2 {
			return nil, fmt.Errorf("invalid keyring configuration")
		}
		version, versionFound := entry["version"].(string)
		key, keyFound := entry["key"].(string)
		if !versionFound || !keyFound {
			return nil, fmt.Errorf("invalid keyring configuration")
		}
		entries = append(entries, feishuCipherKeyringEntry{Version: version, Key: key})
	}
	return entries, nil
}

// buildConfiguredFeishuService reads only explicit production configuration.
// The runtime root and key version intentionally have no defaults: an enabled
// feature without either is safer disabled than pointed at an implicit shared
// directory or unversioned key.
func buildConfiguredFeishuService(
	dataStore store.IStore,
	studentRuns *agent.StudentRunService,
	resumeStore store.IExternalToolResumeLease,
	supervisor *agent.ExternalContinuationSupervisor,
) (*feishuPersonalWorkspace, error) {
	enabled := viper.GetBool("features.feishu_integration.enabled")
	if !enabled {
		return nil, nil
	}
	cipherKeys, err := readFeishuCipherKeyring(viper.GetViper())
	if err != nil {
		return nil, fmt.Errorf("feishu keyring configuration rejected")
	}
	return buildFeishuService(feishuCompositionDeps{
		enabled:     enabled,
		dataStore:   dataStore,
		tokenKey:    viper.GetString("security.thirdparty_token_key"),
		cipherKeys:  cipherKeys,
		keyVersion:  viper.GetString("feishu.key_version"),
		runtimeBase: viper.GetString("feishu.runtime_base"),
		authOwner:   viper.GetString("feishu.auth_owner"),
		studentRuns: studentRuns,
		resumeStore: resumeStore,
		supervisor:  supervisor,
	})
}
