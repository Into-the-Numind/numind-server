package biz

// feishu_adapter.go is the sole production composition root for the personal
// workspace. It deliberately has the only imports that bridge biz/agent and
// biz/feishu; the lower Feishu package never imports Agent.

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/crypto"

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
	resumer            *agent.AgentRunResumer
	dispatcher         *WorkspaceResumeDispatcher
	// authWorkerDispatcher records the exact dispatcher handed to the
	// authorization worker. Keeping this immutable composition edge explicit
	// prevents a later HTTP adapter from accidentally constructing a second
	// callback path.
	authWorkerDispatcher feishu.OperationResumeDispatcher
	supervisor           *agent.ExternalContinuationSupervisor
}

type feishuCompositionDeps struct {
	enabled     bool
	dataStore   store.IStore
	tokenKey    string
	keyVersion  string
	runtimeBase string
	authOwner   string

	studentRuns *agent.StudentRunService
	resumeStore store.IExternalToolResumeLease
	supervisor  *agent.ExternalContinuationSupervisor
	runner      *feishu.ControlledLarkCLIRunner

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

	cipher, err := crypto.NewCipher(deps.tokenKey)
	if err != nil {
		return nil, fmt.Errorf("feishu: build token cipher: %w", err)
	}
	ciphers := map[string]*crypto.Cipher{deps.keyVersion: cipher}
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
	operationService, err := feishu.NewFeishuOperationService(feishu.OperationServiceDeps{
		Accounts:     deps.dataStore.ThirdPartyAccounts(),
		Operations:   deps.dataStore.FeishuWorkspace(),
		Catalog:      catalog,
		Receipts:     skillReader,
		Recovery:     recovery,
		Confirmation: confirmation,
		Vault:        vault,
		Runner:       runner,
		Cipher:       operationCipher,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build operation service: %w", err)
	}
	resumer := agent.NewAgentRunResumer(deps.resumeStore, deps.studentRuns, deps.supervisor)
	dispatcher := NewWorkspaceResumeDispatcher(operationService, resumer)
	authService, err = feishu.NewAuthSessionService(feishu.AuthSessionServiceDeps{
		Accounts:   deps.dataStore.ThirdPartyAccounts(),
		Sessions:   deps.dataStore.FeishuWorkspace(),
		Vault:      vault,
		CLI:        runner,
		Dispatcher: dispatcher,
		Owner:      deps.authOwner,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: build authorization service: %w", err)
	}

	return &feishuPersonalWorkspace{
		runner: runner, vault: vault, catalog: catalog, skillReader: skillReader,
		operationService: operationService, authSessionService: authService,
		resumer: resumer, dispatcher: dispatcher, authWorkerDispatcher: dispatcher,
		supervisor: deps.supervisor,
	}, nil
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
	return buildFeishuService(feishuCompositionDeps{
		enabled:     viper.GetBool("features.feishu_integration.enabled"),
		dataStore:   dataStore,
		tokenKey:    viper.GetString("security.thirdparty_token_key"),
		keyVersion:  viper.GetString("feishu.key_version"),
		runtimeBase: viper.GetString("feishu.runtime_base"),
		authOwner:   viper.GetString("feishu.auth_owner"),
		studentRuns: studentRuns,
		resumeStore: resumeStore,
		supervisor:  supervisor,
	})
}
