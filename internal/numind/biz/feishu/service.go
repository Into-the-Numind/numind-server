// Package feishu exposes the server-owned lifecycle surface for a personal
// Lark workspace. HTTP callers provide only an intent/action and opaque IDs;
// account ownership, generation, scopes, argv, application identity, and the
// one Agent continuation path all remain inside the composed workspace graph.
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

var (
	// ErrWorkspaceLifecycleNotFound deliberately collapses a missing, stale, or
	// cross-tenant operation/session to one public result.
	ErrWorkspaceLifecycleNotFound = errors.New("feishu workspace resource not found")
	// ErrWorkspaceLifecycleInvalid is a safe public validation failure. It never
	// includes client-supplied identifiers or command data.
	ErrWorkspaceLifecycleInvalid = errors.New("feishu workspace lifecycle request rejected")
	// ErrWorkspaceLifecycleUnavailable is used for dependency or persistence
	// failures without exposing internal runner/vault details to HTTP callers.
	ErrWorkspaceLifecycleUnavailable = errors.New("feishu workspace lifecycle unavailable")
)

const (
	ResumeActionUserCompleted        = "user_completed"
	ResumeActionConfirmed            = "confirmed"
	ResumeActionCancelled            = "cancelled"
	workspaceLifecycleCleanupTimeout = 5 * time.Second
)

// CapabilityStatus is the durable, last-known status of one supported domain.
// It is cache metadata only; real operation results remain authoritative.
type CapabilityStatus struct {
	State         string     `json:"state"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
}

// StatusAction intentionally excludes URL and device_code. A live URL is only
// emitted by connect/refresh in the same process that owns the auth worker.
type StatusAction struct {
	OperationID   string    `json:"operation_id,omitempty"`
	SessionID     string    `json:"session_id"`
	Phase         string    `json:"phase"`
	ExpiresAt     time.Time `json:"expires_at"`
	LinkAvailable bool      `json:"link_available"`
}

// StatusResult is the GET /v1/feishu/status DTO. It contains no live URL,
// device code, raw app id, requested scopes, or credential material.
type StatusResult struct {
	State        string                      `json:"state"`
	Connected    bool                        `json:"connected"`
	AppIDMasked  string                      `json:"app_id_masked,omitempty"`
	CLIVersion   string                      `json:"cli_version,omitempty"`
	Capabilities map[string]CapabilityStatus `json:"capabilities"`
	ActiveAction *StatusAction               `json:"active_action,omitempty"`
}

// ConnectResult is returned only by the explicit manual settings action. The
// action's URL is transient and is never written to an account/session DTO.
type ConnectResult struct {
	State  string           `json:"state"`
	Action *OperationAction `json:"action,omitempty"`
}

// UnbindResult explicitly describes the local-only deletion guarantee.
type UnbindResult struct {
	State     string `json:"state"`
	Connected bool   `json:"connected"`
	Message   string `json:"message"`
}

// IFeishuService is the narrow lifecycle API exposed by biz to HTTP routing.
// It has no parameter that can carry an app id, scopes, argv, user id, or
// client-generated authorization URL.
type IFeishuService interface {
	Connect(context.Context, uint) (*ConnectResult, error)
	Status(context.Context, uint) (*StatusResult, error)
	Resume(context.Context, uint, string, string) (*OperationResult, error)
	RefreshAction(context.Context, uint, string) (*OperationAction, error)
	Unbind(context.Context, uint) (*UnbindResult, error)
}

// WorkspaceLifecycleAccountStore is the account-only lifecycle subset. Its
// RetireGeneration operation is atomic with cancellation/unknown closure of
// the retired generation in the store implementation.
type WorkspaceLifecycleAccountStore interface {
	Get(context.Context, uint, string) (*model.UserThirdPartyAccount, error)
	RetireGeneration(context.Context, uint, string) (uint64, uint64, error)
	FinalizeDisconnect(context.Context, uint, string, uint64) error
}

// WorkspaceLifecycleStore is the tenant-fenced persistence surface needed by
// resume and teardown. It intentionally has no broad query or raw DB API.
type WorkspaceLifecycleStore interface {
	GetOperationForUser(context.Context, uint, uint64, string) (*model.FeishuOperation, error)
	GetSessionForUser(context.Context, uint, uint64, string) (*model.FeishuAuthSession, error)
	FindActiveSessionForUser(context.Context, uint, uint64) (*model.FeishuAuthSession, error)
	DeleteVault(context.Context, uint, uint64) error
}

// WorkspaceLifecycleAuth owns in-memory worker cancellation and creates a
// replacement URL only after it supersedes the old server-owned session.
type WorkspaceLifecycleAuth interface {
	ConnectManual(context.Context, uint) (*OperationAction, error)
	RefreshAction(context.Context, uint, uint64, string) (*OperationAction, error)
	CompleteAppApproval(context.Context, uint, uint64, string) error
	StopGenerationAndWait(context.Context, uint, uint64) error
}

// WorkspaceLifecycleDispatcher is implemented by the Task12-composed outer
// dispatcher. It is intentionally the same interface used by auth workers;
// no HTTP-specific resumer may be constructed.
type WorkspaceLifecycleDispatcher interface {
	DispatchResume(context.Context, uint, string) error
}

// WorkspaceLifecycleOperations supplies the only two confirmation transitions
// that a browser is allowed to request. The service validates ownership/state
// before invoking either transition.
type WorkspaceLifecycleOperations interface {
	Confirm(context.Context, uint, string) (*OperationResult, error)
	Cancel(context.Context, uint, string) (*OperationResult, error)
}

// WorkspaceLifecycleExecutions is the narrow app-lifecycle boundary for
// locally running CLI callbacks and the durable account-wide execution gate.
// It intentionally exposes neither arbitrary cancellation nor a runner: the
// lifecycle service can only retire and join one already-fenced generation.
type WorkspaceLifecycleExecutions interface {
	StopGenerationAndWait(context.Context, uint, uint64) error
}

// WorkspaceLifecycleTeardown runs only a fixed logout command after the
// account generation has been retired. Its result records advisory CLI logout
// status. A non-nil error means retired HOME materialization/cleanup failed,
// so Unbind must retain the encrypted vault and disconnecting generation.
// It receives no browser-controlled data.
type WorkspaceLifecycleTeardown interface {
	LogoutRetired(context.Context, uint, uint64) (RetiredWorkspaceTeardownResult, error)
}

// WorkspaceLifecycleDeps is the complete lifecycle graph. All dependencies
// must come from the one Task12 composition root.
type WorkspaceLifecycleDeps struct {
	Accounts   WorkspaceLifecycleAccountStore
	Workspace  WorkspaceLifecycleStore
	Auth       WorkspaceLifecycleAuth
	Dispatcher WorkspaceLifecycleDispatcher
	Operations WorkspaceLifecycleOperations
	Executions WorkspaceLifecycleExecutions
	Teardown   WorkspaceLifecycleTeardown
}

// WorkspaceLifecycleService is the stateful personal-workspace HTTP service.
// It does not construct a runner, vault, auth session service, or Agent
// resumer; those must be the already-validated Task12 instances.
type WorkspaceLifecycleService struct {
	accounts       WorkspaceLifecycleAccountStore
	workspace      WorkspaceLifecycleStore
	auth           WorkspaceLifecycleAuth
	dispatcher     WorkspaceLifecycleDispatcher
	operations     WorkspaceLifecycleOperations
	executions     WorkspaceLifecycleExecutions
	teardown       WorkspaceLifecycleTeardown
	cleanupTimeout time.Duration
}

var _ IFeishuService = (*WorkspaceLifecycleService)(nil)

// NewWorkspaceLifecycleService validates that the endpoint cannot publish a
// half-composed lifecycle path.
func NewWorkspaceLifecycleService(deps WorkspaceLifecycleDeps) (*WorkspaceLifecycleService, error) {
	if deps.Accounts == nil || deps.Workspace == nil || deps.Auth == nil || deps.Dispatcher == nil || deps.Operations == nil || deps.Executions == nil || deps.Teardown == nil {
		return nil, fmt.Errorf("%w: incomplete workspace graph", ErrWorkspaceLifecycleUnavailable)
	}
	return &WorkspaceLifecycleService{
		accounts: deps.Accounts, workspace: deps.Workspace, auth: deps.Auth,
		dispatcher: deps.Dispatcher, operations: deps.Operations, executions: deps.Executions, teardown: deps.Teardown,
		cleanupTimeout: workspaceLifecycleCleanupTimeout,
	}, nil
}

// Status is strictly read-only. In particular it must never call ConnectManual,
// AuthStatus, RefreshAction, or create an authorization worker.
func (s *WorkspaceLifecycleService) Status(ctx context.Context, userID uint) (*StatusResult, error) {
	if s == nil || userID == 0 {
		return nil, ErrWorkspaceLifecycleInvalid
	}
	account, err := s.accounts.Get(ctx, userID, ProviderLark)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return emptyWorkspaceStatus(), nil
	}
	if err != nil || !lifecycleAccountOwned(account, userID) {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	status := workspaceStatusFromAccount(account)
	session, sessionErr := s.workspace.FindActiveSessionForUser(ctx, userID, account.Generation)
	if errors.Is(sessionErr, gorm.ErrRecordNotFound) {
		return status, nil
	}
	if sessionErr != nil || session == nil || session.UserID != userID || session.Generation != account.Generation ||
		session.State != model.FeishuAuthSessionPending || !validAuthPhase(session.Phase) {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	status.ActiveAction = &StatusAction{
		SessionID: session.ID, Phase: session.Phase, ExpiresAt: session.ExpiresAt.UTC(), LinkAvailable: false,
	}
	if session.OperationID != nil {
		status.ActiveAction.OperationID = *session.OperationID
	}
	return status, nil
}

// Connect starts only the server-owned manual flow. AuthSessionService enforces
// its fixed offline_access scope set and creates an app only where necessary.
func (s *WorkspaceLifecycleService) Connect(ctx context.Context, userID uint) (*ConnectResult, error) {
	if s == nil || userID == 0 {
		return nil, ErrWorkspaceLifecycleInvalid
	}
	action, err := s.auth.ConnectManual(ctx, userID)
	if err != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	result := &ConnectResult{Action: cloneOperationAction(action)}
	if action != nil {
		result.State = connectionStateForAction(action.Phase)
	}
	return result, nil
}

// Resume processes one of the three fixed browser actions. The operation is
// first loaded through the caller's current account generation, making stale
// and cross-user IDs indistinguishable from a missing resource.
func (s *WorkspaceLifecycleService) Resume(ctx context.Context, userID uint, operationID, action string) (*OperationResult, error) {
	if s == nil || userID == 0 || strings.TrimSpace(operationID) == "" || !validResumeAction(action) {
		return nil, ErrWorkspaceLifecycleInvalid
	}
	account, err := s.currentAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	operation, err := s.workspace.GetOperationForUser(ctx, userID, account.Generation, operationID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkspaceLifecycleNotFound
	}
	if err != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if operation == nil || operation.UserID != userID || operation.Generation != account.Generation {
		return nil, ErrWorkspaceLifecycleNotFound
	}

	switch action {
	case ResumeActionUserCompleted:
		if operation.State == model.FeishuOperationSucceeded {
			if err := s.compensateSucceededOperation(ctx, userID, operation); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		}
		if !recoveryWaitingState(operation.State) {
			return nil, ErrWorkspaceLifecycleInvalid
		}
		session, sessionErr := s.recoverySession(ctx, userID, account.Generation, operation)
		if sessionErr != nil {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		if operation.State == model.FeishuOperationWaitingAppScope {
			// App-scope approval has no local auth worker. The browser's fixed
			// acknowledgement is therefore completed through AuthSessionService,
			// which owns the session lease and the same Task12 dispatcher used by
			// workers. Do not dispatch again here: that would create a second
			// continuation path for the original Agent tool call.
			if err := s.auth.CompleteAppApproval(ctx, userID, account.Generation, session.ID); err != nil {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			updated, updateErr := s.workspace.GetOperationForUser(ctx, userID, account.Generation, operationID)
			if updateErr != nil || updated == nil || updated.UserID != userID || updated.Generation != account.Generation {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			return lifecycleStoredOperationSummary(updated), nil
		}
		// Create-app and user-auth recovery are completed by the server-owned
		// auth worker. A premature browser acknowledgement simply returns the
		// durable waiting state; it never reconstructs a URL or starts a second
		// worker. Once a completed session is observed, the shared dispatcher is
		// safe to retry after a worker-side dispatch interruption.
		if session.State == model.FeishuAuthSessionPending {
			return lifecycleStoredOperationSummary(operation), nil
		}
		if session.State != model.FeishuAuthSessionCompleted {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		// The exact Task12 dispatcher performs OperationService.Resume and, only
		// on success, the durable Task11 tool-result continuation. It is shared
		// with the auth worker, so concurrent user/worker callbacks cannot invoke
		// the original CLI command a second time.
		if err := s.dispatcher.DispatchResume(ctx, userID, operationID); err != nil {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		updated, updateErr := s.workspace.GetOperationForUser(ctx, userID, account.Generation, operationID)
		if updateErr != nil || updated == nil || updated.UserID != userID || updated.Generation != account.Generation {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		return lifecycleStoredOperationSummary(updated), nil
	case ResumeActionConfirmed:
		// A confirmation may have durably completed its one Feishu write before
		// the Task11 handoff was interrupted. Retrying the same acknowledgement
		// must therefore compensate through the shared dispatcher, not call
		// Confirm again (which would be the only path that can invoke the CLI).
		if operation.State == model.FeishuOperationSucceeded {
			if err := s.compensateSucceededOperation(ctx, userID, operation); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		}
		if operation.State != model.FeishuOperationWaitingConfirmation {
			return nil, ErrWorkspaceLifecycleInvalid
		}
		result, confirmErr := s.operations.Confirm(ctx, userID, operationID)
		if confirmErr != nil || result == nil {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		if result.State == model.FeishuOperationSucceeded {
			// Confirm has committed the terminal transition before returning its
			// result. Preserve the immutable Agent linkage from the browser-verified
			// row while reflecting that committed state for the compensation helper.
			operation.State = model.FeishuOperationSucceeded
			if err := s.compensateSucceededOperation(ctx, userID, operation); err != nil {
				return nil, err
			}
		}
		return lifecycleResultSummary(result), nil
	case ResumeActionCancelled:
		// Cancellation is idempotent after the write has succeeded, but it must
		// never become a second continuation trigger. A user can retry
		// user_completed/confirmed to compensate Task11 instead.
		if operation.State == model.FeishuOperationSucceeded {
			return lifecycleStoredOperationSummary(operation), nil
		}
		if operation.State != model.FeishuOperationWaitingConfirmation {
			return nil, ErrWorkspaceLifecycleInvalid
		}
		result, cancelErr := s.operations.Cancel(ctx, userID, operationID)
		if cancelErr != nil || result == nil {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		return lifecycleResultSummary(result), nil
	default:
		return nil, ErrWorkspaceLifecycleInvalid
	}
}

// compensateSucceededOperation retries only the durable Task11 continuation
// for a committed operation. It deliberately does not call Confirm or any CLI
// runner: WorkspaceResumeDispatcher's OperationService.Resume sees succeeded
// and returns the encrypted result without replaying the Feishu operation.
//
// All normal lark_execute operations carry both AgentRunID and ToolCallID. A
// legacy/non-Agent operation carries neither and has no continuation to
// dispatch. A partially populated link is corrupt, so fail closed instead of
// silently dropping an original Agent tool result.
func (s *WorkspaceLifecycleService) compensateSucceededOperation(
	ctx context.Context,
	userID uint,
	operation *model.FeishuOperation,
) error {
	if s == nil || operation == nil || operation.State != model.FeishuOperationSucceeded ||
		operation.UserID != userID || userID == 0 {
		return ErrWorkspaceLifecycleUnavailable
	}
	if operation.AgentRunID == 0 && strings.TrimSpace(operation.ToolCallID) == "" {
		return nil
	}
	if operation.AgentRunID == 0 || !validStableIdentifier(operation.ToolCallID, operationMaxToolCallIDBytes) {
		return ErrWorkspaceLifecycleUnavailable
	}
	if err := s.dispatcher.DispatchResume(ctx, userID, operation.ID); err != nil {
		return ErrWorkspaceLifecycleUnavailable
	}
	return nil
}

// recoverySession resolves an authorization session only through the durable
// operation summary. The route exposes an operation ID, never a session ID;
// checking the stored session's tenant, generation, phase, and operation
// linkage prevents an unrelated active session from authorizing this replay.
func (s *WorkspaceLifecycleService) recoverySession(
	ctx context.Context,
	userID uint,
	generation uint64,
	operation *model.FeishuOperation,
) (*model.FeishuAuthSession, error) {
	if s == nil || operation == nil || operation.UserID != userID || operation.Generation != generation {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	expectedPhase := lifecycleRecoveryPhase(operation.State)
	if expectedPhase == "" {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	summary, err := decodeOperationSummary(operation.ResultSummaryJSON)
	if err != nil || summary.Status != operation.State || summary.SessionID == "" || summary.Phase != expectedPhase ||
		phaseForRecovery(summary.RecoveryKind) != expectedPhase {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	session, err := s.workspace.GetSessionForUser(ctx, userID, generation, summary.SessionID)
	if err != nil || session == nil || session.UserID != userID || session.Generation != generation ||
		session.Phase != expectedPhase || session.OperationID == nil || *session.OperationID != operation.ID {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	return session, nil
}

func lifecycleRecoveryPhase(operationState string) string {
	switch operationState {
	case model.FeishuOperationWaitingConnection:
		return model.FeishuAuthPhaseCreateApp
	case model.FeishuOperationWaitingAppScope:
		return model.FeishuAuthPhaseAppScope
	case model.FeishuOperationWaitingUserAuth:
		return model.FeishuAuthPhaseUserAuth
	default:
		return ""
	}
}

// RefreshAction validates the caller's current generation before delegating to
// AuthSessionService, which supersedes the old worker/session and returns a
// newly-created live URL. It never accepts or returns a device code.
func (s *WorkspaceLifecycleService) RefreshAction(ctx context.Context, userID uint, sessionID string) (*OperationAction, error) {
	if s == nil || userID == 0 || strings.TrimSpace(sessionID) == "" {
		return nil, ErrWorkspaceLifecycleInvalid
	}
	account, err := s.currentAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Authenticate the browser-controlled opaque ID against the caller's
	// current account generation before asking AuthSessionService to retire its
	// worker and create a fresh URL. A missing or stale ID is intentionally
	// indistinguishable from an unowned one; a persistence failure must not be
	// misreported as a 404 because that would hide an unavailable dependency.
	session, sessionErr := s.workspace.GetSessionForUser(ctx, userID, account.Generation, sessionID)
	if errors.Is(sessionErr, gorm.ErrRecordNotFound) {
		return nil, ErrWorkspaceLifecycleNotFound
	}
	if sessionErr != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if session == nil || session.ID != sessionID || session.UserID != userID || session.Generation != account.Generation ||
		session.State != model.FeishuAuthSessionPending {
		return nil, ErrWorkspaceLifecycleNotFound
	}
	if !validAuthPhase(session.Phase) {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	action, refreshErr := s.auth.RefreshAction(ctx, userID, account.Generation, sessionID)
	if refreshErr != nil || action == nil || strings.TrimSpace(action.SessionID) == "" {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	return cloneOperationAction(action), nil
}

// Unbind first retires the active generation atomically. That database fence
// cancels waiting work and converts executing work to unknown before local
// worker cancellation, vault deletion, and final metadata clearing. The remote
// self-built Feishu app is intentionally not claimed as deleted.
func (s *WorkspaceLifecycleService) Unbind(ctx context.Context, userID uint) (*UnbindResult, error) {
	if s == nil || userID == 0 {
		return nil, ErrWorkspaceLifecycleInvalid
	}
	retiredGeneration, nextGeneration, err := s.accounts.RetireGeneration(ctx, userID, ProviderLark)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return unboundWorkspaceResult(), nil
	}
	if err != nil || retiredGeneration == 0 || nextGeneration <= retiredGeneration {
		return nil, ErrWorkspaceLifecycleUnavailable
	}

	// Do not delete the retired vault while a local worker may still be running
	// against a materialized HOME. The store fence is cross-instance safety; this
	// bounded local join makes the deletion claim truthful. On failure the row
	// intentionally stays disconnecting and RetireGeneration will reuse the same
	// retired generation on a later retry.
	cleanupCtx, cleanupCancel := workspaceLifecycleCleanupContext(ctx, s.cleanupTimeout)
	defer cleanupCancel()
	if err := s.auth.StopGenerationAndWait(cleanupCtx, userID, retiredGeneration); err != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	// RetireGeneration makes old operation results durable-unknown, but it does
	// not itself terminate a local CLI process. Join all local callbacks and
	// wait for the account-wide durable execution gate (including another
	// service instance) to release or expire before materializing/deleting a
	// retired HOME. Failure deliberately leaves this same generation in
	// disconnecting for a safe retry.
	if err := s.executions.StopGenerationAndWait(cleanupCtx, userID, retiredGeneration); err != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	// lark-cli auth logout only clears the local user login; it cannot revoke or
	// delete the remote self-built app. Its command failure is deliberately
	// advisory, but LogoutRetired structurally returns an error only when its
	// retired HOME could not be materialized or removed. In that case deleting
	// the vault would falsely claim local credential removal, so leave the row
	// disconnecting for the same-generation retry.
	if _, err := s.teardown.LogoutRetired(cleanupCtx, userID, retiredGeneration); err != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if err := s.workspace.DeleteVault(cleanupCtx, userID, retiredGeneration); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if err := s.accounts.FinalizeDisconnect(cleanupCtx, userID, ProviderLark, nextGeneration); err != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	return unboundWorkspaceResult(), nil
}

func workspaceLifecycleCleanupContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = workspaceLifecycleCleanupTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (s *WorkspaceLifecycleService) currentAccount(ctx context.Context, userID uint) (*model.UserThirdPartyAccount, error) {
	account, err := s.accounts.Get(ctx, userID, ProviderLark)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkspaceLifecycleNotFound
	}
	if err != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if !lifecycleAccountOwned(account, userID) || account.ConnectionState == model.FeishuConnectionDisconnecting {
		return nil, ErrWorkspaceLifecycleNotFound
	}
	return account, nil
}

func lifecycleAccountOwned(account *model.UserThirdPartyAccount, userID uint) bool {
	return account != nil && account.UserID == userID && account.Provider == ProviderLark && account.Generation != 0
}

func validResumeAction(action string) bool {
	return action == ResumeActionUserCompleted || action == ResumeActionConfirmed || action == ResumeActionCancelled
}

func emptyWorkspaceStatus() *StatusResult {
	return &StatusResult{State: model.FeishuConnectionNone, Connected: false, Capabilities: defaultWorkspaceCapabilities()}
}

func workspaceStatusFromAccount(account *model.UserThirdPartyAccount) *StatusResult {
	status := &StatusResult{
		State: account.ConnectionState, Connected: account.ConnectionState == model.FeishuConnectionConnected && account.Connected,
		AppIDMasked: maskWorkspaceAppID(account.AppID), CLIVersion: account.LarkCLIVersion,
		Capabilities: defaultWorkspaceCapabilities(),
	}
	if status.State == "" {
		status.State = model.FeishuConnectionNone
	}
	var raw map[string]struct {
		State         string     `json:"state"`
		LastSuccessAt *time.Time `json:"last_success_at"`
	}
	if len(account.CapabilityStateJSON) > 0 && json.Unmarshal(account.CapabilityStateJSON, &raw) == nil {
		for _, domain := range []string{"docs", "base", "wiki"} {
			if value, found := raw[domain]; found && validCapabilityState(value.State) {
				status.Capabilities[domain] = CapabilityStatus{State: value.State, LastSuccessAt: value.LastSuccessAt}
			}
		}
	}
	return status
}

func defaultWorkspaceCapabilities() map[string]CapabilityStatus {
	return map[string]CapabilityStatus{
		"docs": {State: model.FeishuCapabilityUnknown},
		"base": {State: model.FeishuCapabilityUnknown},
		"wiki": {State: model.FeishuCapabilityUnknown},
	}
}

func validCapabilityState(state string) bool {
	switch state {
	case model.FeishuCapabilityUnknown, model.FeishuCapabilityAvailable, model.FeishuCapabilityNeedsAppScope,
		model.FeishuCapabilityNeedsUserScope, model.FeishuCapabilityRevoked, model.FeishuCapabilityResourceDenied:
		return true
	default:
		return false
	}
}

func validAuthPhase(phase string) bool {
	return phase == model.FeishuAuthPhaseCreateApp || phase == model.FeishuAuthPhaseAppScope || phase == model.FeishuAuthPhaseUserAuth
}

func connectionStateForAction(phase string) string {
	switch phase {
	case model.FeishuAuthPhaseCreateApp:
		return model.FeishuConnectionCreatingApp
	case model.FeishuAuthPhaseAppScope:
		return model.FeishuConnectionWaitingAppApproval
	case model.FeishuAuthPhaseUserAuth:
		return model.FeishuConnectionWaitingUserAuth
	default:
		return model.FeishuConnectionError
	}
}

func maskWorkspaceAppID(appID string) string {
	if len(appID) <= 4 {
		return ""
	}
	if len(appID) <= 8 {
		return appID[:2] + "****"
	}
	return appID[:4] + "****" + appID[len(appID)-4:]
}

func lifecycleStoredOperationSummary(source *model.FeishuOperation) *OperationResult {
	if source == nil {
		return nil
	}
	return &OperationResult{OperationID: source.ID, State: source.State}
}

func lifecycleResultSummary(source *OperationResult) *OperationResult {
	if source == nil {
		return nil
	}
	return &OperationResult{OperationID: source.OperationID, State: source.State}
}

func unboundWorkspaceResult() *UnbindResult {
	return &UnbindResult{
		State: model.FeishuConnectionNone, Connected: false,
		Message: "有数侧连接已删除；飞书侧个人自建应用仍保留，可在飞书开放平台自行删除",
	}
}
