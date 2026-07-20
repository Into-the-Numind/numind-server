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
	"time"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/google/uuid"
	"github.com/spf13/viper"
)

const feishuDeviceAuthStartupCleanupTimeout = 5 * time.Second

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
	deviceAuthFlow     *feishu.DeviceAuthFlow
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
	// scopePreflight is a test seam. Production always derives the strict
	// fixed-version adapter from runner.
	scopePreflight feishu.ScopePreflight
	// receiptVerifier is a test seam for exercising operation persistence
	// without invoking the controlled skill reader. Production always uses the
	// SkillReader built from tokenKey.
	receiptVerifier feishu.ReceiptVerifier

	// verifyVersion is a test seam. Production leaves it nil so the fixed
	// ControlledLarkCLIRunner probe is always performed.
	verifyVersion func(context.Context) error
	// deviceAuthFactory is a fail-closed composition seam. Production always
	// uses NewDeviceAuthFlow; tests can prove a nil flow is never published.
	deviceAuthFactory func(feishu.DeviceAuthFlowDeps) (*feishu.DeviceAuthFlow, error)
}

type deviceAuthObservationSink func(string, ...interface{})

type operationObservationSink func(string, ...interface{})

type productionOperationObserver struct {
	sink operationObservationSink
}

func newProductionOperationObserver() productionOperationObserver {
	return productionOperationObserver{sink: log.Infow}
}

func (o productionOperationObserver) ObserveOperation(event feishu.OperationObservation) {
	if o.sink == nil || event.UserID == 0 ||
		!validDeviceAuthObservationID(event.OperationID) || event.OperationID == "" ||
		!validOperationObservation(event) ||
		(event.CLIVersion != "" && event.CLIVersion != feishu.LarkCLIVersion) ||
		event.ExitCode < -1 || event.ExitCode > 255 {
		return
	}
	duration := event.Duration
	if duration < 0 {
		duration = 0
	}
	o.sink("feishu business operation",
		"user_id", event.UserID,
		"generation", event.Generation,
		"operation_id", event.OperationID,
		"phase", event.Phase,
		"outcome_class", event.OutcomeClass,
		"risk", event.Risk,
		"invocation_started", event.InvocationStarted,
		"exit_code", event.ExitCode,
		"cli_version", event.CLIVersion,
		"duration", duration,
		"cli_error_type", event.CLIErrorType,
		"cli_error_subtype", event.CLIErrorSubtype,
		"cli_error_code", event.CLIErrorCode,
		"failure_source", event.FailureSource,
	)
}

func validOperationObservation(event feishu.OperationObservation) bool {
	switch event.Phase {
	case "invoke":
		if event.Generation == 0 || !validOperationObservationOutcome(event.OutcomeClass) ||
			!validOperationObservationRisk(event.Risk) ||
			!validOperationFailureSource(event.OutcomeClass, event.FailureSource) {
			return false
		}
		if event.CLIErrorType == "" && event.CLIErrorSubtype == "" && event.CLIErrorCode == "" {
			return true
		}
		return feishu.ValidOperationDiagnosticTuple(
			event.OutcomeClass, event.CLIErrorType, event.CLIErrorSubtype, event.CLIErrorCode,
		)
	case "handoff":
		return event.Generation == 0 && event.Risk == "" && event.CLIVersion == "" &&
			event.ExitCode == -1 && event.CLIErrorType == "" &&
			event.CLIErrorSubtype == "" && event.CLIErrorCode == "" && event.FailureSource == "" &&
			validOperationHandoffOutcome(event.OutcomeClass)
	default:
		return false
	}
}

func validOperationFailureSource(outcome, source string) bool {
	if outcome == "succeeded" {
		return source == ""
	}
	switch source {
	case "structured_cli_error", "timeout", "malformed_output", "output_limit",
		"transport", "vault", "not_started", "unclassified":
		return true
	default:
		return false
	}
}

func validOperationHandoffOutcome(outcome string) bool {
	switch outcome {
	case "continuation_succeeded", "continuation_retry", "terminal_finalized", "terminal_finalize_retry":
		return true
	default:
		return false
	}
}

func validOperationObservationRisk(risk feishu.RiskLevel) bool {
	return risk == feishu.RiskRead || risk == feishu.RiskWrite || risk == feishu.RiskHigh
}

func validOperationObservationOutcome(outcome string) bool {
	switch outcome {
	case "succeeded",
		feishu.PublicCodeConnectionRequired, feishu.PublicCodeScopeRequired,
		feishu.PublicCodeReauthRequired, feishu.PublicCodeResourceDenied,
		feishu.PublicCodeRateLimited, feishu.PublicCodeTemporaryError,
		feishu.PublicCodeValidationError, feishu.PublicCodeNotFound,
		feishu.PublicCodeUnknownResult, feishu.PublicCodeFailed,
		feishu.PublicCodeCancelled:
		return true
	default:
		return false
	}
}

// productionDeviceAuthObserver is the only bridge from device authorization
// observations to the application logger. It validates every enum-like field
// again at the sink boundary so future callers cannot turn the telemetry seam
// into a raw logging channel.
type productionDeviceAuthObserver struct {
	sink deviceAuthObservationSink
}

func newProductionDeviceAuthObserver() productionDeviceAuthObserver {
	return productionDeviceAuthObserver{sink: log.Infow}
}

func (o productionDeviceAuthObserver) ObserveDeviceAuth(event feishu.DeviceAuthObservation) {
	if o.sink == nil || !validDeviceAuthObservationPhase(event.Phase) ||
		!validDeviceAuthObservationOutcome(event.OutcomeClass) ||
		(event.CLIVersion != "" && event.CLIVersion != feishu.LarkCLIVersion) ||
		!validDeviceAuthObservationID(event.OperationID) || !validDeviceAuthObservationID(event.SessionID) {
		return
	}
	duration := event.Duration
	if duration < 0 {
		duration = 0
	}
	o.sink("feishu device authorization",
		"user_id", event.UserID,
		"generation", event.Generation,
		"operation_id", event.OperationID,
		"session_id", event.SessionID,
		"phase", event.Phase,
		"outcome_class", event.OutcomeClass,
		"cli_version", event.CLIVersion,
		"duration", duration,
	)
}

func validDeviceAuthObservationID(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == strings.ToLower(value)
}

func validDeviceAuthObservationPhase(phase string) bool {
	switch phase {
	case "start", "complete", "lease_claim", "lease_renew", "candidate", "replacement", "dispatch",
		"binding", "cli_complete", "reconcile_status", "reconcile_app":
		return true
	default:
		return false
	}
}

func validDeviceAuthObservationOutcome(outcome string) bool {
	switch outcome {
	case "succeeded", "unavailable", "processing", "conflict", "dependency",
		"claimed", "contended", "lost", "retry", "verified",
		"available", "matched", "mismatch",
		string(feishu.DeviceAuthCompleted), string(feishu.DeviceAuthPending),
		string(feishu.DeviceAuthRejected), string(feishu.DeviceAuthExpired),
		string(feishu.DeviceAuthRetryableDependency), string(feishu.DeviceAuthProtocolFailure),
		string(feishu.DeviceAuthAmbiguous), string(feishu.DeviceAuthPollingPendingTimeout),
		string(feishu.DeviceAuthPollingNetworkFailure), string(feishu.DeviceAuthPollingReadFailure),
		string(feishu.DeviceAuthPollingParseFailure), string(feishu.DeviceAuthPollingSlowDown),
		string(feishu.AuthorizationPending), string(feishu.AuthorizationProcessing),
		string(feishu.AuthorizationRejected), string(feishu.AuthorizationExpired),
		string(feishu.AuthorizationUpdated):
		return true
	default:
		return false
	}
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
	deviceAuthCipher, err := feishu.NewDeviceAuthCredentialCipher(ciphers, deps.keyVersion)
	if err != nil {
		return nil, fmt.Errorf("feishu: build device authorization cipher: %w", err)
	}
	skillReader, err := feishu.NewSkillReader(deps.tokenKey)
	if err != nil {
		return nil, fmt.Errorf("feishu: build skill reader: %w", err)
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
	scopePreflight := deps.scopePreflight
	if scopePreflight == nil {
		scopePreflight = feishu.NewControlledScopePreflight(runner)
	}
	operationObserver := newProductionOperationObserver()
	operationService, err := feishu.NewFeishuOperationService(feishu.OperationServiceDeps{
		Accounts:           deps.dataStore.ThirdPartyAccounts(),
		Operations:         deps.dataStore.FeishuWorkspace(),
		Catalog:            catalog,
		Receipts:           receiptVerifier,
		Recovery:           recovery,
		Vault:              vault,
		Preflight:          scopePreflight,
		Runner:             runner,
		Cipher:             operationCipher,
		VerifiedCLIVersion: feishu.LarkCLIVersion,
		Observer:           operationObserver,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build operation service: %w", err)
	}
	resumer := agent.NewAgentRunResumer(deps.resumeStore, deps.studentRuns, deps.supervisor)
	dispatcher := NewWorkspaceResumeDispatcher(operationService, resumer, operationObserver)
	deviceAuthFactory := deps.deviceAuthFactory
	if deviceAuthFactory == nil {
		deviceAuthFactory = feishu.NewDeviceAuthFlow
	}
	deviceAuthFlow, err := deviceAuthFactory(feishu.DeviceAuthFlowDeps{
		Accounts: deps.dataStore.ThirdPartyAccounts(), Sessions: deps.dataStore.FeishuWorkspace(),
		Vault: vault, CLI: runner, Cipher: deviceAuthCipher, Dispatcher: dispatcher,
		Owner: deps.authOwner, Observer: newProductionDeviceAuthObserver(),
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build device authorization flow: %w", err)
	}
	if deviceAuthFlow == nil {
		return nil, fmt.Errorf("feishu: build device authorization flow: unavailable")
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), feishuDeviceAuthStartupCleanupTimeout)
	_, cleanupErr := deviceAuthFlow.CleanupExpiredCredentials(cleanupCtx, 100)
	cleanupCancel()
	if cleanupErr != nil {
		return nil, fmt.Errorf("feishu: cleanup device authorization credentials: %w", cleanupErr)
	}
	authService, err = feishu.NewAuthSessionService(feishu.AuthSessionServiceDeps{
		Accounts:           deps.dataStore.ThirdPartyAccounts(),
		Sessions:           deps.dataStore.FeishuWorkspace(),
		Vault:              vault,
		CLI:                runner,
		DeviceAuth:         deviceAuthFlow,
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
		AgentWaits: resumer,
		Teardown:   teardown,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build lifecycle service: %w", err)
	}

	return &feishuPersonalWorkspace{
		runner: runner, vault: vault, catalog: catalog, skillReader: skillReader,
		operationService: operationService, authSessionService: authService, deviceAuthFlow: deviceAuthFlow,
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
	var resumeVersions []string
	if err := dataStore.DB().Model(&model.FeishuAuthSession{}).
		Where("resume_key_version IS NOT NULL AND resume_key_version <> ''").
		Distinct().Pluck("resume_key_version", &resumeVersions).Error; err != nil {
		return fmt.Errorf("persistent key version store unavailable")
	}
	for _, version := range resumeVersions {
		if err := verifyFeishuPersistedKeyVersion(version, ciphers); err != nil {
			return err
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
