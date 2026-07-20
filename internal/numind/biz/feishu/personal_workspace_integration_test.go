package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

type personalWorkspaceRestartDispatcher struct {
	mu       sync.Mutex
	delegate *reentrantOperationResumeDispatcher
	calls    []string
}

func (d *personalWorkspaceRestartDispatcher) DispatchResume(
	ctx context.Context,
	userID uint,
	operationID string,
) error {
	d.mu.Lock()
	d.calls = append(d.calls, operationID)
	delegate := d.delegate
	d.mu.Unlock()
	if delegate == nil {
		return errors.New("restart dispatcher unavailable")
	}
	return delegate.DispatchResume(ctx, userID, operationID)
}

func (d *personalWorkspaceRestartDispatcher) setService(service *FeishuOperationService) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.delegate = &reentrantOperationResumeDispatcher{service: service}
}

func (d *personalWorkspaceRestartDispatcher) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// newPersonalWorkspaceIntegrationOperationService intentionally recreates the
// operation coordinator against the same durable stores. The integration tests
// use it at every recovery boundary to prove that a process restart cannot
// change the encrypted command or the owning user/generation.
func newPersonalWorkspaceIntegrationOperationService(
	t *testing.T,
	h *operationHarness,
	recovery RecoveryStarter,
) *FeishuOperationService {
	t.Helper()
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: recovery,
		Confirmation: h.confirmation, Vault: h.vault, Preflight: h.preflight, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	return service
}

func newPersonalWorkspaceRestartOperationService(
	t *testing.T,
	h *operationHarness,
	recovery RecoveryStarter,
	vault OperationHomeVault,
	runner OperationRunner,
) *FeishuOperationService {
	t.Helper()
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
		Catalog: NewCommandCatalog(), Receipts: &operationReceiptFake{}, Recovery: recovery,
		Confirmation: &operationConfirmationFake{}, Vault: vault, Preflight: h.preflight, Runner: runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	return service
}

func setPersonalWorkspaceIntegrationDispatcher(
	dispatcher *reentrantOperationResumeDispatcher,
	service *FeishuOperationService,
) {
	dispatcher.mu.Lock()
	dispatcher.service = service
	dispatcher.mu.Unlock()
}

func newPersonalWorkspaceIntegrationAuthService(
	t *testing.T,
	h *operationHarness,
	cli *authSessionCLIFake,
	dispatcher OperationResumeDispatcher,
	owner string,
) *AuthSessionService {
	t.Helper()
	vault, err := NewEncryptedCLIHomeVault(
		h.dataStore.ThirdPartyAccounts(), h.dataStore.FeishuWorkspace(),
		h.cipher.currentCipher, h.cipher.currentVersion, t.TempDir(),
	)
	require.NoError(t, err)
	deviceAuth, err := NewDeviceAuthFlow(DeviceAuthFlowDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: vault, CLI: cli, Cipher: newDeviceAuthFlowCredentialCipher(t), Dispatcher: dispatcher,
		Owner: owner + "-device-auth", Now: h.service.now,
		LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second, CompletionTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	auth, err := NewAuthSessionService(AuthSessionServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: vault, CLI: cli, DeviceAuth: deviceAuth, Dispatcher: dispatcher, Owner: owner,
		Now: h.service.now, LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second,
	})
	require.NoError(t, err)
	return auth
}

func TestPersonalWorkspaceIntegration_PhaseRecoveriesSurviveCoordinatorRestarts(t *testing.T) {
	h := newOperationHarness(t)
	createRelease := make(chan struct{})
	releaseCreate := releaseAuthSessionCLIFake(t, createRelease)
	cli := &authSessionCLIFake{
		appID:           "cli_personal_workspace",
		completeOutcome: DeviceAuthCompleted,
		status:          true,
		urls: []string{
			"https://open.feishu.cn/page/cli?user_code=CREATE_PERSONAL",
			"https://open.feishu.cn/suite/passport/oauth/device?user_code=INITIAL_USER_SCOPE",
		},
		releases: []<-chan struct{}{createRelease},
	}
	dispatcher := &reentrantOperationResumeDispatcher{}
	authBeforeCreate := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "personal-workspace-create")

	appScopeRequired := &CLIResult{
		InvocationStarted: true,
		ExitCode:          1,
		Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
			Type: "authorization", Subtype: "app_scope_not_applied", Identity: "user",
			ConsoleURL:    "https://open.feishu.cn/app/cli_personal_workspace/auth",
			MissingScopes: []string{"docx:document:readonly"}, PermissionViolations: json.RawMessage(`[{"level":"app"}]`),
		}},
	}
	h.runner.steps = []operationRunnerStep{
		{result: appScopeRequired, err: errors.New("app permission has not been approved")},
		{result: operationOKResult(`{"document_id":"replayed-exactly-once"}`)},
	}

	firstCoordinator := newPersonalWorkspaceIntegrationOperationService(t, h, authBeforeCreate)
	setPersonalWorkspaceIntegrationDispatcher(dispatcher, firstCoordinator)
	request := operationDocsFetchRequest(900, "tool-personal-workspace")
	waitingCreate, err := firstCoordinator.Execute(h.ctx, request)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConnection, waitingCreate.State)
	require.Equal(t, model.FeishuAuthPhaseCreateApp, waitingCreate.Action.Phase)

	require.Eventually(t, func() bool {
		argv, _ := cli.snapshot()
		return len(argv) == 1
	}, time.Second, 10*time.Millisecond, "creating the app must begin after the operation is durably waiting")

	// Hold the old worker after its durable completion, rebuild both services,
	// then let the new process perform the replay.
	createDispatchRelease := make(chan struct{})
	releaseCreateDispatch := releaseAuthSessionCLIFake(t, createDispatchRelease)
	createDispatchEntered := dispatcher.blockNextDispatch(createDispatchRelease)
	releaseCreate()
	select {
	case <-createDispatchEntered:
	case <-time.After(time.Second):
		t.Fatal("create-app completion did not reach the restart boundary")
	}
	authAfterCreate := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "personal-workspace-after-create")
	afterCreateRestart := newPersonalWorkspaceIntegrationOperationService(t, h, authAfterCreate)
	setPersonalWorkspaceIntegrationDispatcher(dispatcher, afterCreateRestart)
	releaseCreateDispatch()

	// A newly created app has no user identity yet. Completing that first
	// account-level authorization replays the exact operation, whose structured
	// result can then ask for app approval without treating an ACL failure as an
	// OAuth failure.
	var initialUserAuthSession model.FeishuAuthSession
	require.Eventually(t, func() bool {
		return h.db.Where("operation_id = ? AND phase = ? AND state = ?", waitingCreate.OperationID,
			model.FeishuAuthPhaseUserAuth, model.FeishuAuthSessionPending).
			Order("created_at DESC").Take(&initialUserAuthSession).Error == nil
	}, 2*time.Second, 10*time.Millisecond, "app creation must lead to the first exact user authorization")
	require.JSONEq(t, `["docx:document:readonly"]`, string(initialUserAuthSession.RequestedScopesJSON))
	require.Eventually(t, func() bool {
		argv, _ := cli.snapshot()
		return len(argv) == 2
	}, time.Second, 10*time.Millisecond, "initial user authorization must be started once")

	userDispatchRelease := make(chan struct{})
	releaseUserDispatch := releaseAuthSessionCLIFake(t, userDispatchRelease)
	userDispatchEntered := dispatcher.blockNextDispatch(userDispatchRelease)
	initialUserCompleted := make(chan error, 1)
	go func() {
		_, completeErr := authAfterCreate.CompleteUserAuthorization(
			h.ctx, 7, 1, initialUserAuthSession.ID,
		)
		initialUserCompleted <- completeErr
	}()
	select {
	case <-userDispatchEntered:
	case completeErr := <-initialUserCompleted:
		t.Fatalf("initial user authorization returned before dispatch: %v", completeErr)
	case <-time.After(time.Second):
		t.Fatal("user authorization completion did not reach the restart boundary")
	}
	authAfterUserAuth := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "personal-workspace-after-user-auth")
	afterInitialUserAuthRestart := newPersonalWorkspaceIntegrationOperationService(t, h, authAfterUserAuth)
	setPersonalWorkspaceIntegrationDispatcher(dispatcher, afterInitialUserAuthRestart)
	releaseUserDispatch()
	require.NoError(t, <-initialUserCompleted)

	var appScopeSession model.FeishuAuthSession
	require.Eventually(t, func() bool {
		return h.db.Where("operation_id = ? AND phase = ? AND state = ?", waitingCreate.OperationID,
			model.FeishuAuthPhaseAppScope, model.FeishuAuthSessionPending).
			Order("created_at DESC").Take(&appScopeSession).Error == nil
	}, 2*time.Second, 10*time.Millisecond, "the exact operation must move to app-scope approval after the structured app error")

	// The browser acknowledgement is durable too. Pause it after completion,
	// rebuild the whole composition, and only then deliver the exact replay.
	appScopeDispatchRelease := make(chan struct{})
	releaseAppScopeDispatch := releaseAuthSessionCLIFake(t, appScopeDispatchRelease)
	appScopeDispatchEntered := dispatcher.blockNextDispatch(appScopeDispatchRelease)
	appScopeCompleted := make(chan error, 1)
	go func() {
		appScopeCompleted <- authAfterUserAuth.CompleteAppApproval(h.ctx, 7, 1, appScopeSession.ID)
	}()
	select {
	case <-appScopeDispatchEntered:
	case <-time.After(time.Second):
		t.Fatal("app-scope completion did not reach the restart boundary")
	}
	authAfterAppScope := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "personal-workspace-after-app-scope")
	afterAppScopeRestart := newPersonalWorkspaceIntegrationOperationService(t, h, authAfterAppScope)
	setPersonalWorkspaceIntegrationDispatcher(dispatcher, afterAppScopeRestart)
	releaseAppScopeDispatch()
	require.NoError(t, <-appScopeCompleted)

	var completed model.FeishuOperation
	require.Eventually(t, func() bool {
		return h.db.Where("id = ?", waitingCreate.OperationID).Take(&completed).Error == nil &&
			completed.State == model.FeishuOperationSucceeded
	}, 2*time.Second, 10*time.Millisecond, "completed authorization must replay the original operation")
	result, err := afterAppScopeRestart.Resume(h.ctx, 7, waitingCreate.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, result.State)
	require.JSONEq(t, `{"document_id":"replayed-exactly-once"}`, string(result.Data))

	calls, argv := h.runner.snapshot()
	require.Equal(t, 2, calls, "app-scope recovery must replay the exact original business operation once")
	for _, invocation := range argv {
		require.Equal(t, []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--format", "json", "--as", "user"}, invocation)
	}
	require.Equal(t, 3, dispatcher.callCount(), "each completed phase dispatches one exact-operation continuation")
}

func TestPersonalWorkspaceIntegration_UserScopeRecoveryReplaysExactRequestAfterRestart(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_personal_workspace")
	cli := &authSessionCLIFake{
		urls:            []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=RECOVER_USER_SCOPE"},
		completeOutcome: DeviceAuthCompleted,
		status:          true,
		appID:           "cli_personal_workspace",
	}
	dispatcher := &reentrantOperationResumeDispatcher{}
	authBeforeRestart := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "personal-workspace-user-scope")
	h.runner.steps = []operationRunnerStep{
		{result: userScopeRequiredCLIResult(), err: errors.New("user permission has not been granted")},
		{result: operationOKResult(`{"document_id":"replayed-after-user-scope"}`)},
	}

	firstCoordinator := newPersonalWorkspaceIntegrationOperationService(t, h, authBeforeRestart)
	setPersonalWorkspaceIntegrationDispatcher(dispatcher, firstCoordinator)
	request := operationDocsFetchRequest(905, "tool-user-scope-recovery")
	waiting, err := firstCoordinator.Execute(h.ctx, request)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)
	request.Argv[3] = "mutated-after-persist"

	userDispatchRelease := make(chan struct{})
	releaseUserDispatch := releaseAuthSessionCLIFake(t, userDispatchRelease)
	userDispatchEntered := dispatcher.blockNextDispatch(userDispatchRelease)
	userCompleted := make(chan error, 1)
	go func() {
		_, completeErr := authBeforeRestart.CompleteUserAuthorization(
			h.ctx, 7, 1, waiting.Action.SessionID,
		)
		userCompleted <- completeErr
	}()
	select {
	case <-userDispatchEntered:
	case completeErr := <-userCompleted:
		t.Fatalf("user-scope authorization returned before dispatch: %v", completeErr)
	case <-time.After(time.Second):
		t.Fatal("user-scope completion did not reach the restart boundary")
	}
	authAfterRestart := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "personal-workspace-user-scope-restarted")
	afterUserScopeRestart := newPersonalWorkspaceIntegrationOperationService(t, h, authAfterRestart)
	setPersonalWorkspaceIntegrationDispatcher(dispatcher, afterUserScopeRestart)
	releaseUserDispatch()
	require.NoError(t, <-userCompleted)

	var completed model.FeishuOperation
	require.Eventually(t, func() bool {
		return h.db.Where("id = ?", waiting.OperationID).Take(&completed).Error == nil &&
			completed.State == model.FeishuOperationSucceeded
	}, 2*time.Second, 10*time.Millisecond, "completed user authorization must resume the persisted request")
	duplicate, err := afterUserScopeRestart.Resume(h.ctx, 7, waiting.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, duplicate.State)
	calls, argv := h.runner.snapshot()
	require.Equal(t, 2, calls)
	for _, invocation := range argv {
		require.Equal(t, []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--format", "json", "--as", "user"}, invocation)
	}
}

func TestPersonalWorkspaceIntegration_UserAuthRecoveryIsOperationIndependentAcrossDomains(t *testing.T) {
	tests := []struct {
		name      string
		inputArgv []string
		argv      []string
		scopes    []string
	}{
		{
			name:      "docs create",
			inputArgv: []string{"docs", "+create", "--title", "Recovery report"},
			argv:      []string{"docs", "+create", "--title", "Recovery report", "--format", "json", "--as", "user"},
			scopes:    []string{"docx:document:create"},
		},
		{
			name: "base read",
			inputArgv: []string{
				"base", "+record-get", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--record-id", "recABCDEFG123",
			},
			argv: []string{
				"base", "+record-get", "--base-token", "bascnABCDEFG123", "--table-id", "Tasks", "--record-id", "recABCDEFG123",
				"--format", "json", "--as", "user",
			},
			scopes: []string{"base:record:read"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			h := newOperationHarness(t)
			h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_domain_independent")
			cli := &authSessionCLIFake{
				urls:            []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=DOMAIN_RECOVERY"},
				completeOutcome: DeviceAuthCompleted,
				status:          true,
				appID:           "cli_domain_independent",
			}
			dispatcher := &reentrantOperationResumeDispatcher{}
			auth := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "domain-independent")
			if testCase.name == "docs create" {
				h.preflight.steps = []operationScopePreflightStep{
					{result: &ScopeCheckResult{Missing: append([]string(nil), testCase.scopes...)}},
					{result: &ScopeCheckResult{Granted: append([]string(nil), testCase.scopes...)}},
				}
				h.runner.steps = []operationRunnerStep{{result: operationOKResult(`{"ok":true}`)}}
			} else {
				h.runner.steps = []operationRunnerStep{
					{result: userScopeRequiredCLIResultFor(testCase.scopes), err: errors.New("user permission has not been granted")},
					{result: operationOKResult(`{"ok":true}`)},
				}
			}

			beforeRestart := newPersonalWorkspaceIntegrationOperationService(t, h, auth)
			setPersonalWorkspaceIntegrationDispatcher(dispatcher, beforeRestart)
			request := ExecuteRequest{
				UserID: 7, AgentRunID: 950, ToolCallID: "tool-domain-recovery",
				IdempotencyKey: "950:tool-domain-recovery",
				Argv:           append([]string(nil), testCase.inputArgv...), SkillReceipts: []string{"shared", "doc", "base"},
			}
			waiting, err := beforeRestart.Execute(h.ctx, request)
			require.NoError(t, err)
			require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)
			require.NotNil(t, waiting.Action)
			storedWaiting, err := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, waiting.OperationID)
			require.NoError(t, err)
			for _, secret := range []string{"restart-safe-device-code", "DOMAIN_RECOVERY", "?user_code="} {
				require.NotContains(t, string(storedWaiting.ResultSummaryJSON), secret,
					"durable operation summaries must never persist device credentials or live URL queries")
			}

			// Mutating caller memory after persistence must not affect recovery.
			request.Argv[0] = "im"
			afterRestart := newPersonalWorkspaceIntegrationOperationService(t, h, auth)
			setPersonalWorkspaceIntegrationDispatcher(dispatcher, afterRestart)
			completed, err := auth.CompleteUserAuthorization(h.ctx, 7, 1, waiting.Action.SessionID)
			require.NoError(t, err)
			require.True(t, completed.Completed)

			calls, invocations := h.runner.snapshot()
			if testCase.name == "docs create" {
				require.Equal(t, 1, calls, "write preflight must recover before any business invocation")
				require.Equal(t, testCase.argv, invocations[0])
			} else {
				require.Equal(t, 2, calls)
				require.Equal(t, testCase.argv, invocations[0], "initial execution must use the full canonical argv")
				require.Equal(t, testCase.argv, invocations[1], "authorization must replay the full exact encrypted argv")
			}
		})
	}

	t.Run("wiki update resolves obj token before docs update", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_wiki_update")
		cli := &authSessionCLIFake{
			urls: []string{
				"https://open.feishu.cn/suite/passport/oauth/device?user_code=WIKI_RESOLVE",
				"https://open.feishu.cn/suite/passport/oauth/device?user_code=WIKI_UPDATE",
			},
			completeOutcome: DeviceAuthCompleted,
			status:          true,
			appID:           "cli_wiki_update",
		}
		dispatcher := &reentrantOperationResumeDispatcher{}
		auth := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "wiki-update")
		const resolvedObjToken = "doxcnWikiObject123"
		h.runner.steps = []operationRunnerStep{
			{result: userScopeRequiredCLIResultFor([]string{"wiki:node:retrieve"}), err: errors.New("wiki user scope missing")},
			{result: operationOKResult(`{"obj_token":"` + resolvedObjToken + `","obj_type":"docx"}`)},
			{result: operationOKResult(`{"document_id":"` + resolvedObjToken + `","updated":true}`)},
		}

		nodeInput := []string{"wiki", "+node-get", "--node-token", "wikcnABCDEFG123"}
		nodeArgv := []string{"wiki", "+node-get", "--node-token", "wikcnABCDEFG123", "--format", "json", "--as", "user"}
		nodeCoordinator := newPersonalWorkspaceIntegrationOperationService(t, h, auth)
		setPersonalWorkspaceIntegrationDispatcher(dispatcher, nodeCoordinator)
		nodeWaiting, err := nodeCoordinator.Execute(h.ctx, ExecuteRequest{
			UserID: 7, AgentRunID: 951, ToolCallID: "tool-wiki-node-get",
			IdempotencyKey: "951:tool-wiki-node-get", Argv: nodeInput,
			SkillReceipts: []string{"shared", "wiki"},
		})
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingUserAuth, nodeWaiting.State)
		require.NotNil(t, nodeWaiting.Action)
		require.Equal(t, []string{"wiki:node:retrieve"}, nodeWaiting.Action.Scopes)

		afterNodeRestart := newPersonalWorkspaceIntegrationOperationService(t, h, auth)
		setPersonalWorkspaceIntegrationDispatcher(dispatcher, afterNodeRestart)
		nodeCompleted, err := auth.CompleteUserAuthorization(h.ctx, 7, 1, nodeWaiting.Action.SessionID)
		require.NoError(t, err)
		require.True(t, nodeCompleted.Completed)
		nodeResult, err := afterNodeRestart.Resume(h.ctx, 7, nodeWaiting.OperationID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, nodeResult.State)
		var resolved struct {
			ObjToken string `json:"obj_token"`
			ObjType  string `json:"obj_type"`
		}
		require.NoError(t, json.Unmarshal(nodeResult.Data, &resolved))
		require.Equal(t, resolvedObjToken, resolved.ObjToken)
		require.Equal(t, "docx", resolved.ObjType)

		const updateContent = "Recovered Wiki content"
		updateInput := []string{
			"docs", "+update", "--doc", resolved.ObjToken, "--command", "append", "--content", updateContent,
		}
		updateArgv := []string{
			"docs", "+update", "--doc", resolvedObjToken, "--command", "append", "--content", updateContent,
			"--format", "json", "--as", "user",
		}
		h.preflight.steps = []operationScopePreflightStep{
			{result: &ScopeCheckResult{Missing: []string{"docx:document:readonly", "docx:document:write_only"}}},
			{result: &ScopeCheckResult{Granted: []string{"docx:document:readonly", "docx:document:write_only"}}},
		}
		updateWaiting, err := afterNodeRestart.Execute(h.ctx, ExecuteRequest{
			UserID: 7, AgentRunID: 952, ToolCallID: "tool-wiki-doc-update",
			IdempotencyKey: "952:tool-wiki-doc-update", Argv: updateInput,
			SkillReceipts: []string{"shared", "doc"},
		})
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationWaitingUserAuth, updateWaiting.State)
		require.NotNil(t, updateWaiting.Action)
		require.ElementsMatch(t,
			[]string{"docx:document:write_only", "docx:document:readonly"},
			updateWaiting.Action.Scopes,
		)

		afterUpdateRestart := newPersonalWorkspaceIntegrationOperationService(t, h, auth)
		setPersonalWorkspaceIntegrationDispatcher(dispatcher, afterUpdateRestart)
		updateCompleted, err := auth.CompleteUserAuthorization(h.ctx, 7, 1, updateWaiting.Action.SessionID)
		require.NoError(t, err)
		require.True(t, updateCompleted.Completed)
		updateResult, err := afterUpdateRestart.Resume(h.ctx, 7, updateWaiting.OperationID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationSucceeded, updateResult.State)

		calls, invocations := h.runner.snapshot()
		require.Equal(t, 3, calls)
		require.Equal(t, nodeArgv, invocations[0])
		require.Equal(t, nodeArgv, invocations[1])
		require.Equal(t, updateArgv, invocations[2])
		require.Equal(t, resolved.ObjToken, invocations[2][3], "docs update token must come from node-get result")
		for _, invocation := range invocations {
			require.NotEqual(t, []string{"wiki", "+update"}, invocation[:2], "pinned lark-cli has no wiki update verb")
		}

		authArgv, _ := cli.snapshot()
		require.Equal(t, [][]string{
			{"auth", "login", "--scope", "wiki:node:retrieve", "--no-wait", "--json"},
			{"auth", "login", "--scope", "docx:document:readonly docx:document:write_only", "--no-wait", "--json"},
		}, authArgv)
	})
}

func TestPersonalWorkspaceIntegration_UserAuthResumeSurvivesServiceRestart(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_personal_workspace")
	type instanceAResult struct {
		waiting     *OperationResult
		runner      *operationRunnerFake
		runtimeBase string
	}
	startInstanceA := func() instanceAResult {
		vaultA, err := NewEncryptedCLIHomeVault(
			h.dataStore.ThirdPartyAccounts(), h.dataStore.FeishuWorkspace(),
			h.cipher.currentCipher, h.cipher.currentVersion, t.TempDir(),
		)
		require.NoError(t, err)
		instanceARelease := make(chan struct{})
		releaseInstanceA := releaseAuthSessionCLIFake(t, instanceARelease)
		instanceACLI := &authSessionCLIFake{
			urls:    []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=INSTANCE_A"},
			release: instanceARelease,
		}
		instanceADispatcher := &personalWorkspaceRestartDispatcher{}
		instanceADeviceAuth, err := NewDeviceAuthFlow(DeviceAuthFlowDeps{
			Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
			Vault: vaultA, CLI: instanceACLI, Cipher: newDeviceAuthFlowCredentialCipher(t), Dispatcher: instanceADispatcher,
			Owner: "instance-a-device-auth", Now: h.service.now,
			LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
			HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second, CompletionTimeout: 30 * time.Second,
		})
		require.NoError(t, err)
		instanceAAuth, err := NewAuthSessionService(AuthSessionServiceDeps{
			Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
			Vault: vaultA, CLI: instanceACLI, DeviceAuth: instanceADeviceAuth,
			Dispatcher: instanceADispatcher, Owner: "instance-a",
			Now: h.service.now, LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
			HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second,
		})
		require.NoError(t, err)
		instanceARunner := &operationRunnerFake{steps: []operationRunnerStep{
			{result: userScopeRequiredCLIResult(), err: errors.New("user permission has not been granted")},
		}}
		instanceAOperations := newPersonalWorkspaceRestartOperationService(t, h, instanceAAuth, vaultA, instanceARunner)
		instanceADispatcher.setService(instanceAOperations)
		waiting, executeErr := instanceAOperations.Execute(h.ctx, operationDocsFetchRequest(907, "tool-device-auth-restart"))
		require.NoError(t, executeErr)
		require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)
		require.NotNil(t, waiting.Action)
		require.Equal(t, waiting.OperationID, waiting.Action.OperationID)
		assert.Zero(t, instanceACLI.ActiveRuns(),
			"split user authorization start must not depend on an instance-local blocking worker")
		releaseInstanceA()
		return instanceAResult{waiting: waiting, runner: instanceARunner, runtimeBase: vaultA.RuntimeBase()}
	}
	instanceA := startInstanceA()
	waiting := instanceA.waiting
	instanceARunner := instanceA.runner

	// A hard process loss cannot gracefully stop its graph first: doing so would
	// replace the restart boundary with a teardown boundary. The helper scope
	// deliberately drops every strong reference to A's vault/auth/dispatcher;
	// only the durable waiting result and a read-only runner counter escape.
	vaultB, err := NewEncryptedCLIHomeVault(
		h.dataStore.ThirdPartyAccounts(), h.dataStore.FeishuWorkspace(),
		h.cipher.currentCipher, h.cipher.currentVersion, t.TempDir(),
	)
	require.NoError(t, err)
	require.NotEqual(t, instanceA.runtimeBase, vaultB.RuntimeBase(),
		"instance B must reconstruct its vault with a fresh process-local runtime base")

	instanceBCLI := &authSessionCLIFake{
		completeOutcome: DeviceAuthCompleted,
		status:          true,
		appID:           "cli_personal_workspace",
	}
	instanceBDispatcher := &personalWorkspaceRestartDispatcher{}
	instanceBDeviceAuth, err := NewDeviceAuthFlow(DeviceAuthFlowDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: vaultB, CLI: instanceBCLI, Cipher: newDeviceAuthFlowCredentialCipher(t), Dispatcher: instanceBDispatcher,
		Owner: "instance-b-device-auth", Now: h.service.now,
		LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second, CompletionTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	instanceBAuth, err := NewAuthSessionService(AuthSessionServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Sessions: h.dataStore.FeishuWorkspace(),
		Vault: vaultB, CLI: instanceBCLI, DeviceAuth: instanceBDeviceAuth,
		Dispatcher: instanceBDispatcher, Owner: "instance-b",
		Now: h.service.now, LeaseDuration: time.Minute, SessionDuration: 10 * time.Minute,
		HeartbeatInterval: 30 * time.Second, StartTimeout: time.Second,
	})
	require.NoError(t, err)
	instanceBAuthForCleanup := instanceBAuth
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if stopErr := instanceBAuthForCleanup.StopGenerationAndWait(stopCtx, 7, 1); stopErr != nil {
			t.Errorf("join instance B authorization worker: %v", stopErr)
		}
	})
	instanceBRunner := &operationRunnerFake{steps: []operationRunnerStep{{
		result: operationOKResult(`{"document_id":"completed-after-service-restart"}`),
	}}}
	instanceBOperations := newPersonalWorkspaceRestartOperationService(t, h, instanceBAuth, vaultB, instanceBRunner)
	instanceBDispatcher.setService(instanceBOperations)
	instanceBLifecycle, err := NewWorkspaceLifecycleService(WorkspaceLifecycleDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Workspace: h.dataStore.FeishuWorkspace(),
		Auth: instanceBAuth, Dispatcher: instanceBDispatcher, Operations: instanceBOperations,
		Executions: instanceBOperations, AgentWaits: &lifecycleAgentWaitFake{}, Teardown: &lifecycleTeardownFake{},
		Now: h.service.now,
	})
	require.NoError(t, err)

	result, err := instanceBLifecycle.Resume(h.ctx, 7, waiting.OperationID, waiting.Action.SessionID, ResumeActionUserCompleted)
	if assert.NoError(t, err) && assert.NotNil(t, result) {
		assert.Equal(t, model.FeishuOperationSucceeded, result.State)
		assert.Equal(t, waiting.OperationID, result.OperationID)
	}
	assert.Equal(t, []string{waiting.OperationID}, instanceBDispatcher.snapshot(),
		"instance B must dispatch the exact durable operation once")
	instanceACalls, instanceAArgv := instanceARunner.snapshot()
	instanceBCalls, instanceBArgv := instanceBRunner.snapshot()
	assert.Equal(t, 1, instanceACalls)
	assert.Equal(t, 1, instanceBCalls, "the original business operation must complete exactly once after authorization")
	if len(instanceAArgv) == 1 && len(instanceBArgv) == 1 {
		assert.Equal(t, instanceAArgv[0], instanceBArgv[0],
			"restart recovery must replay the persisted exact operation")
	}
}

func TestPersonalWorkspaceIntegration_DispatcherRestartReadsStoredResult(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_personal_workspace")
	h.runner.steps = []operationRunnerStep{{
		result: operationOKResult(`{"document_id":"durable-dispatch-result"}`),
	}}
	completed, err := h.service.Execute(h.ctx, operationDocsFetchRequest(908, "tool-dispatch-restart"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, completed.State)

	restartRunner := &operationRunnerFake{steps: []operationRunnerStep{{
		err: errors.New("stored result must not invoke a business CLI after restart"),
	}}}
	restarted := newPersonalWorkspaceRestartOperationService(t, h, h.recovery, h.vault, restartRunner)
	dispatcher := &personalWorkspaceRestartDispatcher{}
	dispatcher.setService(restarted)

	require.NoError(t, dispatcher.DispatchResume(h.ctx, 7, completed.OperationID))
	stored, err := restarted.Resume(h.ctx, 7, completed.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, stored.State)
	require.Equal(t, []string{completed.OperationID}, dispatcher.snapshot())
	restartCalls, _ := restartRunner.snapshot()
	require.Zero(t, restartCalls, "succeeded dispatch must read the encrypted stored result without replay")
}

func TestPersonalWorkspaceIntegration_RevokedRefreshRecoversOriginalOperationAfterRestart(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_personal_workspace")
	cli := &authSessionCLIFake{
		urls:            []string{"https://open.feishu.cn/suite/passport/oauth/device?user_code=REAUTH_REVOKED"},
		completeOutcome: DeviceAuthCompleted,
		status:          true,
		appID:           "cli_personal_workspace",
	}
	dispatcher := &reentrantOperationResumeDispatcher{}
	authBeforeRestart := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "personal-workspace-reauth")
	h.runner.steps = []operationRunnerStep{
		{result: &CLIResult{InvocationStarted: true, ExitCode: 1, Envelope: &CLIEnvelope{
			OK: false, Identity: "user", Error: &CLIError{
				Type: "authorization", Subtype: "refresh_token_revoked", Identity: "user",
			},
		}}, err: errors.New("refresh token was revoked")},
		{result: operationOKResult(`{"document_id":"replayed-after-reauth"}`)},
	}

	firstCoordinator := newPersonalWorkspaceIntegrationOperationService(t, h, authBeforeRestart)
	setPersonalWorkspaceIntegrationDispatcher(dispatcher, firstCoordinator)
	waiting, err := firstCoordinator.Execute(h.ctx, operationDocsFetchRequest(906, "tool-reauth-recovery"))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingUserAuth, waiting.State)
	require.Equal(t, model.FeishuAuthPhaseUserAuth, waiting.Action.Phase)

	userDispatchRelease := make(chan struct{})
	releaseUserDispatch := releaseAuthSessionCLIFake(t, userDispatchRelease)
	userDispatchEntered := dispatcher.blockNextDispatch(userDispatchRelease)
	userCompleted := make(chan error, 1)
	go func() {
		_, completeErr := authBeforeRestart.CompleteUserAuthorization(
			h.ctx, 7, 1, waiting.Action.SessionID,
		)
		userCompleted <- completeErr
	}()
	select {
	case <-userDispatchEntered:
	case completeErr := <-userCompleted:
		t.Fatalf("reauthorization returned before dispatch: %v", completeErr)
	case <-time.After(time.Second):
		t.Fatal("reauthorization completion did not reach the restart boundary")
	}
	authAfterRestart := newPersonalWorkspaceIntegrationAuthService(t, h, cli, dispatcher, "personal-workspace-reauth-restarted")
	afterReauthRestart := newPersonalWorkspaceIntegrationOperationService(t, h, authAfterRestart)
	setPersonalWorkspaceIntegrationDispatcher(dispatcher, afterReauthRestart)
	releaseUserDispatch()
	require.NoError(t, <-userCompleted)
	require.Eventually(t, func() bool {
		operation, getErr := h.dataStore.FeishuWorkspace().GetOperationForUser(h.ctx, 7, 1, waiting.OperationID)
		return getErr == nil && operation.State == model.FeishuOperationSucceeded
	}, 2*time.Second, 10*time.Millisecond, "reauthorization must replay the original encrypted operation")
	calls, argv := h.runner.snapshot()
	require.Equal(t, 2, calls)
	for _, invocation := range argv {
		require.Equal(t, []string{"docs", "+fetch", "--doc", "doxcnABCDEFG123", "--format", "json", "--as", "user"}, invocation)
	}
}

func userScopeRequiredCLIResult() *CLIResult {
	return userScopeRequiredCLIResultFor([]string{"docx:document:readonly"})
}

func userScopeRequiredCLIResultFor(scopes []string) *CLIResult {
	return &CLIResult{
		InvocationStarted: true,
		ExitCode:          1,
		Envelope: &CLIEnvelope{OK: false, Identity: "user", Error: &CLIError{
			Type: "authorization", Subtype: "missing_scope", Code: json.RawMessage(`99991672`),
			Identity: "user", MissingScopes: append([]string(nil), scopes...),
		}},
	}
}

func TestPersonalWorkspaceIntegration_TenantAndGenerationFencesRejectStaleRecovery(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_user_seven")
	h.createAccount(8, model.FeishuConnectionConnected, 1, "cli_user_eight")
	h.runner.steps = []operationRunnerStep{
		{result: operationOKResult(`{"document_id":"user-seven"}`)},
		{result: operationOKResult(`{"document_id":"user-eight"}`)},
	}

	userSevenRequest := operationDocsFetchRequest(901, "tool-user-seven")
	userSeven, err := h.service.Execute(h.ctx, userSevenRequest)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, userSeven.State)

	_, err = h.service.Resume(h.ctx, 8, userSeven.OperationID)
	require.ErrorIs(t, err, ErrOperationUnavailable, "another account must not observe or replay this operation")

	retiredGeneration, nextGeneration, err := h.dataStore.ThirdPartyAccounts().RetireGeneration(h.ctx, 7, ProviderLark)
	require.NoError(t, err)
	require.EqualValues(t, 1, retiredGeneration)
	require.EqualValues(t, 2, nextGeneration)
	_, err = h.service.Resume(h.ctx, 7, userSeven.OperationID)
	require.ErrorIs(t, err, ErrOperationUnavailable, "unbinding must invalidate all old-generation operation IDs")

	userEightRequest := operationDocsFetchRequest(902, "tool-user-eight")
	userEightRequest.UserID = 8
	userEightRequest.IdempotencyKey = "902:tool-user-eight"
	userEight, err := h.service.Execute(h.ctx, userEightRequest)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationSucceeded, userEight.State)

	calls, _ := h.runner.snapshot()
	require.Equal(t, 2, calls, "stale and cross-tenant resumes must never invoke lark-cli")
}

func TestPersonalWorkspaceIntegration_ResourceACLAndUnknownWriteNeverBecomeOAuthReplay(t *testing.T) {
	t.Run("resource ACL is a sharing failure, not OAuth", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		h.runner.steps = []operationRunnerStep{{
			result: &CLIResult{InvocationStarted: true, ExitCode: 1, Envelope: &CLIEnvelope{
				OK: false, Identity: "user", Error: &CLIError{
					Type: "api", Subtype: "permission_denied", Code: json.RawMessage(`"RESOURCE_ACCESS_DENIED"`), Identity: "user",
				},
			}},
			err: errors.New("the document is not shared with this user"),
		}}

		got, err := h.service.Execute(h.ctx, operationDocsFetchRequest(903, "tool-resource-acl"))
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationFailed, got.State)
		require.Empty(t, h.recovery.snapshot())
	})

	t.Run("started write with unknown result is terminal", func(t *testing.T) {
		h := newOperationHarness(t)
		h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
		h.runner.steps = []operationRunnerStep{{
			result: &CLIResult{InvocationStarted: true, ExitCode: -1},
			err:    errors.New("transport ended after command start"),
		}}
		request := operationDocsCreateRequest(7, 904, "tool-unknown-write", "不可重试", nil)
		got, err := h.service.Execute(h.ctx, request)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationUnknown, got.State)

		resumed, err := h.service.Resume(h.ctx, 7, got.OperationID)
		require.NoError(t, err)
		require.Equal(t, model.FeishuOperationUnknown, resumed.State)
		calls, _ := h.runner.snapshot()
		require.Equal(t, 1, calls, "an unknown write must never be retried automatically")
	})
}
