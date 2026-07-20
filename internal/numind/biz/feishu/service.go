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
	"sync"
	"time"

	"github.com/google/uuid"

	"numind-server/internal/pkg/externalaction"
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
	// ErrWorkspaceLifecycleConflict reports a current lifecycle race for which
	// no safe replacement action could be returned.
	ErrWorkspaceLifecycleConflict = errors.New("feishu workspace lifecycle conflict")
	// ErrWorkspaceLifecycleDependency reports a temporary CLI, Feishu, or
	// persistence dependency failure that is safe for the caller to retry.
	ErrWorkspaceLifecycleDependency = errors.New("feishu workspace lifecycle dependency unavailable")
	// ErrWorkspaceLifecycleUnavailable is the fail-closed category for internal
	// invariants and legacy lifecycle failures that cannot safely be classified.
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

// RefreshActionResult is a tagged refresh response. Exactly one branch is
// populated: Action for a new live URL, or Terminal when the historical
// operation can no longer be refreshed safely.
type RefreshActionResult struct {
	Action   *OperationAction       `json:"action,omitempty"`
	Terminal *RefreshTerminalResult `json:"terminal,omitempty"`
}

// RefreshTerminalResult deliberately contains no authorization material.
// The browser only needs the exact operation identity and its stored state to
// settle the stale external-action card without replaying the operation.
type RefreshTerminalResult struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
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
	Resume(context.Context, uint, string, string, string) (*OperationResult, error)
	RefreshAction(context.Context, uint, string) (*RefreshActionResult, error)
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
	ListTerminalOperationsForGeneration(context.Context, uint, uint64, []string) ([]model.FeishuOperation, error)
	GetSessionForUser(context.Context, uint, uint64, string) (*model.FeishuAuthSession, error)
	FindActiveSessionForUser(context.Context, uint, uint64) (*model.FeishuAuthSession, error)
	ClaimRetiredTeardown(context.Context, uint, uint64, uint64, string, time.Time, time.Time) (bool, error)
	RenewRetiredTeardown(context.Context, uint, uint64, uint64, string, time.Time, time.Time) (bool, error)
	ReleaseRetiredTeardown(context.Context, uint, uint64, string, time.Time) error
	CompleteRetiredTeardown(context.Context, uint, uint64, uint64, string, time.Time) (bool, error)
}

// WorkspaceLifecycleAuth owns in-memory worker cancellation and creates a
// replacement URL only after it supersedes the old server-owned session.
type WorkspaceLifecycleAuth interface {
	ConnectManual(context.Context, uint) (*OperationAction, error)
	RefreshAction(context.Context, uint, uint64, string) (*OperationAction, error)
	RefreshOperationAction(context.Context, uint, uint64, string, string, string, []byte) (*OperationAction, error)
	RecoverOperationRefreshAction(context.Context, uint, uint64, string, string, string, []byte) (*OperationAction, error)
	CompleteAppApproval(context.Context, uint, uint64, string) error
	CompleteUserAuthorization(context.Context, uint, uint64, string) (*DeviceAuthCompletion, error)
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

// WorkspaceLifecycleAgentWaits is the only bridge from a terminal Feishu
// operation to its exact Agent external wait. Unlike the Task11 success
// dispatcher it never starts a model continuation: it persists a fixed tool
// error and makes the Agent run terminal.
type WorkspaceLifecycleAgentWaits interface {
	FinalizeExternalToolWait(context.Context, uint, uint64, string, string, externalaction.TerminalOutcome) (bool, error)
	HandoffExternalToolWait(context.Context, uint, uint64, externalaction.Payload, []string) (bool, error)
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
	AgentWaits WorkspaceLifecycleAgentWaits
	Teardown   WorkspaceLifecycleTeardown
	Now        func() time.Time
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
	agentWaits     WorkspaceLifecycleAgentWaits
	teardown       WorkspaceLifecycleTeardown
	now            func() time.Time
	cleanupTimeout time.Duration
	// unbinds serializes destructive local teardown per user without holding a
	// global mutex while a database, vault, or lark-cli dependency is running.
	// A concurrent caller joins the owner and observes its one durable result;
	// it never starts a second generation retirement or vault deletion.
	unbindMu sync.Mutex
	unbinds  map[uint]*workspaceUnbindFlight
}

type workspaceUnbindFlight struct {
	done   chan struct{}
	result *UnbindResult
	err    error
}

// retiredTeardownLease supervises one short durable teardown lease while the
// bounded local cleanup runs. The heartbeat deliberately stops at the cleanup
// context deadline: a stuck runner or os.RemoveAll must not preserve ownership
// forever. Once ownership is lost, callers must not perform another
// destructive step; CompleteRetiredTeardown independently enforces the same
// owner/generation fence in the database.
type retiredTeardownLease struct {
	workspace               WorkspaceLifecycleStore
	userID                  uint
	retiredGeneration       uint64
	disconnectingGeneration uint64
	owner                   string
	leaseDuration           time.Duration
	cleanupCtx              context.Context

	mu      sync.Mutex
	lost    bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

var _ IFeishuService = (*WorkspaceLifecycleService)(nil)

// NewWorkspaceLifecycleService validates that the endpoint cannot publish a
// half-composed lifecycle path.
func NewWorkspaceLifecycleService(deps WorkspaceLifecycleDeps) (*WorkspaceLifecycleService, error) {
	if deps.Accounts == nil || deps.Workspace == nil || deps.Auth == nil || deps.Dispatcher == nil || deps.Operations == nil || deps.Executions == nil || deps.AgentWaits == nil || deps.Teardown == nil {
		return nil, fmt.Errorf("%w: incomplete workspace graph", ErrWorkspaceLifecycleUnavailable)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &WorkspaceLifecycleService{
		accounts: deps.Accounts, workspace: deps.Workspace, auth: deps.Auth,
		dispatcher: deps.Dispatcher, operations: deps.Operations, executions: deps.Executions, agentWaits: deps.AgentWaits, teardown: deps.Teardown,
		now:            now,
		cleanupTimeout: workspaceLifecycleCleanupTimeout,
		unbinds:        make(map[uint]*workspaceUnbindFlight),
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
func (s *WorkspaceLifecycleService) Resume(ctx context.Context, userID uint, operationID, sessionID, action string) (*OperationResult, error) {
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
	if currentSessionID, waiting := lifecycleOperationSessionID(operation); waiting {
		if strings.TrimSpace(currentSessionID) == "" {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		if strings.TrimSpace(sessionID) != currentSessionID {
			return lifecycleCurrentOperationObservation(operation)
		}
	}

	switch action {
	case ResumeActionUserCompleted:
		if operation.State == model.FeishuOperationSucceeded {
			if err := s.compensateSucceededOperation(ctx, userID, operation); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		}
		switch operation.State {
		case model.FeishuOperationExecuting:
			return lifecycleStoredOperationSummary(operation), nil
		case model.FeishuOperationFailed:
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, externalaction.TerminalOutcomeFailed); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		case model.FeishuOperationUnknown:
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, externalaction.TerminalOutcomeUnknown); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		case model.FeishuOperationCancelled:
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, externalaction.TerminalOutcomeCancelled); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		}
		if operation.State == model.FeishuOperationWaitingConfirmation {
			// Rolling-upgrade compatibility only. New operation services no longer
			// create this state, but an authorization card from an older server may
			// still acknowledge after the durable operation has advanced. Resume the
			// encrypted operation through the shared dispatcher; never interpret the
			// stale browser phase as business input.
			dispatchCtx, dispatchCancel := authSessionDispatchContext(ctx)
			defer dispatchCancel()
			if err := s.dispatcher.DispatchResume(dispatchCtx, userID, operationID); err != nil {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			updated, updateErr := s.workspace.GetOperationForUser(ctx, userID, account.Generation, operationID)
			if updateErr != nil || updated == nil || updated.UserID != userID || updated.Generation != account.Generation {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			if _, finalizeErr := s.finalizeTerminalOperationIfNeeded(ctx, userID, updated); finalizeErr != nil {
				return nil, finalizeErr
			}
			return lifecycleStoredOperationSummary(updated), nil
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
			if _, finalizeErr := s.finalizeTerminalOperationIfNeeded(ctx, userID, updated); finalizeErr != nil {
				return nil, finalizeErr
			}
			return lifecycleStoredOperationSummary(updated), nil
		}
		if session.State == model.FeishuAuthSessionCompleted {
			dispatchCtx, dispatchCancel := authSessionDispatchContext(ctx)
			defer dispatchCancel()
			if err := s.dispatcher.DispatchResume(dispatchCtx, userID, operationID); err != nil {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			updated, updateErr := s.workspace.GetOperationForUser(ctx, userID, account.Generation, operationID)
			if updateErr != nil || updated == nil || updated.UserID != userID || updated.Generation != account.Generation {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			if _, finalizeErr := s.finalizeTerminalOperationIfNeeded(ctx, userID, updated); finalizeErr != nil {
				return nil, finalizeErr
			}
			return lifecycleStoredOperationSummary(updated), nil
		}
		if session.Phase == model.FeishuAuthPhaseUserAuth {
			if session.ProtocolVersion == 2 && session.State == model.FeishuAuthSessionPending && !session.ExpiresAt.After(s.now().UTC()) {
				action, refreshErr := s.auth.RefreshOperationAction(
					ctx, userID, account.Generation, session.ID, operation.ID, operation.State, operation.ResultSummaryJSON,
				)
				if refreshErr != nil || action == nil || action.OperationID != operation.ID ||
					strings.TrimSpace(action.SessionID) == "" || action.Phase != model.FeishuAuthPhaseUserAuth {
					return nil, ErrWorkspaceLifecycleUnavailable
				}
				if err := s.handoffRefreshedOperationAction(ctx, userID, operation, action); err != nil {
					return nil, err
				}
				return lifecycleAuthorizationNoticeResult(operation, &DeviceAuthCompletion{
					NoticeCode: AuthorizationExpired,
					Action:     action,
				})
			}
			completion, completeErr := s.auth.CompleteUserAuthorization(
				ctx, userID, account.Generation, session.ID,
			)
			if errors.Is(completeErr, ErrDeviceAuthProcessing) {
				return lifecycleAuthorizationNoticeResult(operation, &DeviceAuthCompletion{NoticeCode: AuthorizationProcessing})
			}
			if errors.Is(completeErr, ErrDeviceAuthConflict) {
				return nil, ErrWorkspaceLifecycleConflict
			}
			if errors.Is(completeErr, ErrDeviceAuthDependency) {
				return nil, ErrWorkspaceLifecycleDependency
			}
			if completeErr != nil || completion == nil {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			if completion.NoticeCode != "" || completion.Action != nil {
				if completion.Action != nil {
					if err := s.handoffRefreshedOperationAction(ctx, userID, operation, completion.Action); err != nil {
						return nil, err
					}
				}
				return lifecycleAuthorizationNoticeResult(operation, completion)
			}
			if !completion.Completed {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			updated, updateErr := s.workspace.GetOperationForUser(ctx, userID, account.Generation, operationID)
			if updateErr != nil || updated == nil || updated.UserID != userID || updated.Generation != account.Generation {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			if _, finalizeErr := s.finalizeTerminalOperationIfNeeded(ctx, userID, updated); finalizeErr != nil {
				return nil, finalizeErr
			}
			return lifecycleStoredOperationSummary(updated), nil
		}
		// Create-app remains owned by its blocking server worker. A pending
		// acknowledgement does not reconstruct or restart that worker.
		if session.State == model.FeishuAuthSessionPending {
			return lifecycleStoredOperationSummary(operation), nil
		}
		return nil, ErrWorkspaceLifecycleUnavailable
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
		if outcome, ok := terminalOutcomeForOperationState(operation.State); ok {
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, outcome); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		}
		// A legacy confirmation callback may race with the automatic migration.
		// Once another request has moved the same encrypted operation forward,
		// observing its current state is the only idempotent response: dispatching
		// or confirming again could replay a Feishu write.
		if operation.State == model.FeishuOperationExecuting || recoveryWaitingState(operation.State) {
			return lifecycleStoredOperationSummary(operation), nil
		}
		if operation.State != model.FeishuOperationWaitingConfirmation {
			return nil, ErrWorkspaceLifecycleInvalid
		}
		result, confirmErr := s.operations.Confirm(ctx, userID, operationID)
		if confirmErr != nil || result == nil || result.OperationID != operation.ID {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		operation.State = result.State
		if result.State == model.FeishuOperationSucceeded {
			// Confirm has committed the terminal transition before returning its
			// result. Preserve the immutable Agent linkage from the browser-verified
			// row while reflecting that committed state for the compensation helper.
			if err := s.compensateSucceededOperation(ctx, userID, operation); err != nil {
				return nil, err
			}
		} else if outcome, ok := terminalOutcomeForOperationState(result.State); ok {
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, outcome); err != nil {
				return nil, err
			}
		}
		return lifecycleResultSummary(result), nil
	case ResumeActionCancelled:
		// Cancellation cannot undo a committed Feishu write. Once the operation
		// succeeded, every acknowledgement is therefore a safe Task11 repair:
		// the shared dispatcher returns the stored result and never replays the
		// Feishu command.
		if operation.State == model.FeishuOperationSucceeded {
			if err := s.compensateSucceededOperation(ctx, userID, operation); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		}
		// Operation.Cancel commits its terminal operation transition before this
		// service can terminalize the linked Agent wait. If that second durable
		// step was temporarily unavailable, a retry must finish exactly that
		// wait; returning "invalid" here would strand the Agent run forever.
		// Neither terminal branch invokes Cancel nor the CLI again.
		if outcome, ok := terminalOutcomeForOperationState(operation.State); ok {
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, outcome); err != nil {
				return nil, err
			}
			return lifecycleStoredOperationSummary(operation), nil
		}
		if operation.State != model.FeishuOperationWaitingConfirmation {
			return nil, ErrWorkspaceLifecycleInvalid
		}
		result, cancelErr := s.operations.Cancel(ctx, userID, operationID)
		if cancelErr != nil || result == nil {
			updated, updateErr := s.workspace.GetOperationForUser(ctx, userID, account.Generation, operationID)
			if updateErr != nil || updated == nil || updated.UserID != userID || updated.Generation != account.Generation {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			settled, settleErr := s.settleCommittedOperation(ctx, userID, updated)
			if settleErr != nil {
				return nil, settleErr
			}
			if settled || updated.State == model.FeishuOperationExecuting {
				return lifecycleStoredOperationSummary(updated), nil
			}
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		if result.OperationID != operation.ID {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		operation.State = result.State
		settled, settleErr := s.settleCommittedOperation(ctx, userID, operation)
		if settleErr != nil {
			return nil, settleErr
		}
		if !settled && result.State != model.FeishuOperationExecuting {
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

// finalizeRetiredGenerationAgentWaits consumes only exact Agent links from
// operations that RetireGeneration has already atomically closed. It queries a
// single user/generation/state set, never a broad agent_run list. A teardown
// retry repeats the call safely because AgentRunResumer's terminal store path
// is idempotent for the same operation/tool-result tuple.
func (s *WorkspaceLifecycleService) finalizeRetiredGenerationAgentWaits(
	ctx context.Context,
	userID uint,
	retiredGeneration uint64,
) error {
	if s == nil || userID == 0 || retiredGeneration == 0 {
		return ErrWorkspaceLifecycleUnavailable
	}
	operations, err := s.workspace.ListTerminalOperationsForGeneration(ctx, userID, retiredGeneration, []string{
		model.FeishuOperationCancelled,
		model.FeishuOperationUnknown,
	})
	if err != nil {
		return ErrWorkspaceLifecycleUnavailable
	}
	for index := range operations {
		operation := &operations[index]
		if operation.UserID != userID || operation.Generation != retiredGeneration {
			return ErrWorkspaceLifecycleUnavailable
		}
		var outcome externalaction.TerminalOutcome
		switch operation.State {
		case model.FeishuOperationCancelled:
			outcome = externalaction.TerminalOutcomeCancelled
		case model.FeishuOperationUnknown:
			outcome = externalaction.TerminalOutcomeUnknown
		default:
			return ErrWorkspaceLifecycleUnavailable
		}
		if err := s.finalizeTerminalOperationWait(ctx, userID, operation, outcome); err != nil {
			return err
		}
	}
	return nil
}

// finalizeTerminalOperationWait is deliberately terminal-only. A successful
// operation remains owned by Task12's shared dispatcher and Task11 resumer;
// this path is for failed, cancelled, and unknown operation states, where automatic
// continuation would be both misleading and unsafe.
func (s *WorkspaceLifecycleService) finalizeTerminalOperationWait(
	ctx context.Context,
	userID uint,
	operation *model.FeishuOperation,
	outcome externalaction.TerminalOutcome,
) error {
	if s == nil || s.agentWaits == nil || operation == nil || operation.UserID != userID || userID == 0 ||
		strings.TrimSpace(operation.ID) == "" {
		return ErrWorkspaceLifecycleUnavailable
	}
	expectedOutcome, ok := terminalOutcomeForOperationState(operation.State)
	if !ok || expectedOutcome != outcome {
		return ErrWorkspaceLifecycleUnavailable
	}
	if operation.AgentRunID == 0 && strings.TrimSpace(operation.ToolCallID) == "" {
		// Manual/non-Agent operations intentionally have no Agent transcript to
		// modify. This is a safe no-op, not a missing continuation.
		return nil
	}
	if operation.AgentRunID == 0 || !validStableIdentifier(operation.ToolCallID, operationMaxToolCallIDBytes) {
		return ErrWorkspaceLifecycleUnavailable
	}
	if _, err := s.agentWaits.FinalizeExternalToolWait(
		ctx, userID, operation.AgentRunID, operation.ID, operation.ToolCallID, outcome,
	); err != nil {
		return ErrWorkspaceLifecycleUnavailable
	}
	return nil
}

func (s *WorkspaceLifecycleService) finalizeTerminalOperationIfNeeded(
	ctx context.Context,
	userID uint,
	operation *model.FeishuOperation,
) (bool, error) {
	if operation == nil {
		return false, ErrWorkspaceLifecycleUnavailable
	}
	outcome, ok := terminalOutcomeForOperationState(operation.State)
	if !ok {
		return false, nil
	}
	if err := s.finalizeTerminalOperationWait(ctx, userID, operation, outcome); err != nil {
		return false, err
	}
	return true, nil
}

func (s *WorkspaceLifecycleService) settleCommittedOperation(
	ctx context.Context,
	userID uint,
	operation *model.FeishuOperation,
) (bool, error) {
	if operation != nil && operation.State == model.FeishuOperationSucceeded {
		if err := s.compensateSucceededOperation(ctx, userID, operation); err != nil {
			return false, err
		}
		return true, nil
	}
	return s.finalizeTerminalOperationIfNeeded(ctx, userID, operation)
}

func terminalOutcomeForOperationState(state string) (externalaction.TerminalOutcome, bool) {
	switch state {
	case model.FeishuOperationFailed:
		return externalaction.TerminalOutcomeFailed, true
	case model.FeishuOperationUnknown:
		return externalaction.TerminalOutcomeUnknown, true
	case model.FeishuOperationCancelled:
		return externalaction.TerminalOutcomeCancelled, true
	default:
		return "", false
	}
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

// RefreshAction validates the caller's current generation before either
// delegating to AuthSessionService for a new live URL or returning the linked
// operation's stored terminal state. It never accepts or returns a device code.
func (s *WorkspaceLifecycleService) RefreshAction(ctx context.Context, userID uint, sessionID string) (*RefreshActionResult, error) {
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
	if session == nil || session.ID != sessionID || session.UserID != userID || session.Generation != account.Generation {
		return nil, ErrWorkspaceLifecycleNotFound
	}
	if !validAuthPhase(session.Phase) {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if session.OperationID == nil {
		var action *OperationAction
		var refreshErr error
		switch session.State {
		case model.FeishuAuthSessionPending:
			action, refreshErr = s.auth.RefreshAction(ctx, userID, account.Generation, sessionID)
		case model.FeishuAuthSessionRejected, model.FeishuAuthSessionExpired:
			if session.Phase != model.FeishuAuthPhaseUserAuth || session.ProtocolVersion != 2 ||
				len(session.ResumeCredentialCiphertext) != 0 || session.ResumeKeyVersion != "" ||
				session.ResumeExpiresAt != nil || session.LeaseOwner != "" || session.LeaseUntil != nil {
				return nil, ErrWorkspaceLifecycleNotFound
			}
			action, refreshErr = s.auth.ConnectManual(ctx, userID)
		default:
			return nil, ErrWorkspaceLifecycleNotFound
		}
		if refreshErr != nil || action == nil || strings.TrimSpace(action.SessionID) == "" {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		return &RefreshActionResult{Action: cloneOperationAction(action)}, nil
	}
	if strings.TrimSpace(*session.OperationID) == "" {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	operation, operationErr := s.workspace.GetOperationForUser(ctx, userID, account.Generation, *session.OperationID)
	if operationErr != nil || operation == nil || operation.UserID != userID || operation.Generation != account.Generation {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if refreshTerminalOperationState(operation.State) {
		switch operation.State {
		case model.FeishuOperationSucceeded:
			if err := s.compensateSucceededOperation(ctx, userID, operation); err != nil {
				return nil, err
			}
		case model.FeishuOperationFailed:
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, externalaction.TerminalOutcomeFailed); err != nil {
				return nil, err
			}
		case model.FeishuOperationUnknown:
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, externalaction.TerminalOutcomeUnknown); err != nil {
				return nil, err
			}
		case model.FeishuOperationCancelled:
			if err := s.finalizeTerminalOperationWait(ctx, userID, operation, externalaction.TerminalOutcomeCancelled); err != nil {
				return nil, err
			}
		}
		return &RefreshActionResult{Terminal: &RefreshTerminalResult{
			OperationID: operation.ID,
			State:       operation.State,
		}}, nil
	}
	boundSession, bindingErr := s.recoverySession(ctx, userID, account.Generation, operation)
	if bindingErr != nil || boundSession == nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	var action *OperationAction
	var refreshErr error
	if session.Phase == model.FeishuAuthPhaseUserAuth && boundSession.ID != session.ID &&
		(session.State == model.FeishuAuthSessionRejected || session.State == model.FeishuAuthSessionExpired ||
			session.State == model.FeishuAuthSessionSuperseded) {
		action, refreshErr = s.auth.RefreshOperationAction(
			ctx, userID, account.Generation, boundSession.ID, operation.ID, operation.State, operation.ResultSummaryJSON,
		)
	} else {
		switch session.State {
		case model.FeishuAuthSessionPending, model.FeishuAuthSessionFailed:
			if boundSession.ID != session.ID {
				return nil, ErrWorkspaceLifecycleUnavailable
			}
			action, refreshErr = s.auth.RefreshOperationAction(
				ctx, userID, account.Generation, sessionID, operation.ID, operation.State, operation.ResultSummaryJSON,
			)
		case model.FeishuAuthSessionSuperseded:
			if boundSession.ID == session.ID {
				// Compatibility for the pre-atomic-refresh state: its operation
				// summary still names the superseded source session. The downstream
				// transaction rechecks this exact binding before minting a new link.
				action, refreshErr = s.auth.RefreshOperationAction(
					ctx, userID, account.Generation, sessionID, operation.ID, operation.State, operation.ResultSummaryJSON,
				)
				break
			}
			// A failed post-commit compensation may leave the browser's original
			// card on a superseded ID. It can only repair the exact operation's
			// current replacement; it cannot refresh arbitrary historical sessions.
			action, refreshErr = s.auth.RecoverOperationRefreshAction(
				ctx, userID, account.Generation, sessionID, operation.ID, operation.State, operation.ResultSummaryJSON,
			)
		case model.FeishuAuthSessionRejected, model.FeishuAuthSessionExpired:
			if session.Phase != model.FeishuAuthPhaseUserAuth || boundSession.ID != session.ID {
				return nil, ErrWorkspaceLifecycleNotFound
			}
			action, refreshErr = s.auth.RefreshOperationAction(
				ctx, userID, account.Generation, sessionID, operation.ID, operation.State, operation.ResultSummaryJSON,
			)
		default:
			return nil, ErrWorkspaceLifecycleNotFound
		}
	}
	if refreshErr != nil || action == nil || strings.TrimSpace(action.SessionID) == "" {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if err := s.handoffRefreshedOperationAction(ctx, userID, operation, action); err != nil {
		return nil, err
	}
	return &RefreshActionResult{Action: cloneOperationAction(action)}, nil
}

func (s *WorkspaceLifecycleService) handoffRefreshedOperationAction(
	ctx context.Context,
	userID uint,
	operation *model.FeishuOperation,
	action *OperationAction,
) error {
	if s == nil || operation == nil || action == nil || userID == 0 || operation.UserID != userID {
		return ErrWorkspaceLifecycleUnavailable
	}
	if operation.AgentRunID == 0 && strings.TrimSpace(operation.ToolCallID) == "" {
		return nil
	}
	if operation.AgentRunID == 0 || strings.TrimSpace(operation.ToolCallID) == "" || action.OperationID != operation.ID ||
		strings.TrimSpace(action.SessionID) == "" || strings.TrimSpace(action.Phase) == "" || action.ExpiresAt.IsZero() {
		return ErrWorkspaceLifecycleUnavailable
	}
	summary, err := decodeOperationSummary(operation.ResultSummaryJSON)
	if err != nil || summary.Status != operation.State || summary.Phase != action.Phase {
		return ErrWorkspaceLifecycleUnavailable
	}
	summary = advanceOperationSession(summary, action.SessionID)
	transitioned, err := s.agentWaits.HandoffExternalToolWait(ctx, userID, operation.AgentRunID, externalaction.Payload{
		Provider: ProviderLark, OperationID: operation.ID, SessionID: action.SessionID,
		ToolCallID: operation.ToolCallID, Phase: action.Phase, ExpiresAt: action.ExpiresAt,
	}, summary.SupersededSessionIDs)
	if err != nil || !transitioned {
		return ErrWorkspaceLifecycleUnavailable
	}
	return nil
}

func refreshTerminalOperationState(state string) bool {
	switch state {
	case model.FeishuOperationSucceeded,
		model.FeishuOperationFailed,
		model.FeishuOperationUnknown,
		model.FeishuOperationCancelled:
		return true
	default:
		return false
	}
}

// Unbind first retires the active generation atomically. That database fence
// cancels waiting work and converts executing work to unknown before local
// worker cancellation, vault deletion, and final metadata clearing. The remote
// self-built Feishu app is intentionally not claimed as deleted.
func (s *WorkspaceLifecycleService) Unbind(ctx context.Context, userID uint) (*UnbindResult, error) {
	if s == nil || userID == 0 {
		return nil, ErrWorkspaceLifecycleInvalid
	}
	flight, owner := s.beginUnbind(userID)
	if !owner {
		return waitForWorkspaceUnbind(ctx, flight)
	}
	result, err := s.unbindOnce(ctx, userID)
	s.completeUnbind(userID, flight, result, err)
	return result, err
}

// beginUnbind elects exactly one cleanup owner for one user. It never calls a
// dependency while holding the mutex, so a blocked vault or runner cannot
// serialize unrelated users or deadlock a re-entrant completion path.
func (s *WorkspaceLifecycleService) beginUnbind(userID uint) (*workspaceUnbindFlight, bool) {
	s.unbindMu.Lock()
	defer s.unbindMu.Unlock()
	if existing := s.unbinds[userID]; existing != nil {
		return existing, false
	}
	flight := &workspaceUnbindFlight{done: make(chan struct{})}
	s.unbinds[userID] = flight
	return flight, true
}

func (s *WorkspaceLifecycleService) completeUnbind(userID uint, flight *workspaceUnbindFlight, result *UnbindResult, err error) {
	if s == nil || flight == nil {
		return
	}
	s.unbindMu.Lock()
	if s.unbinds[userID] == flight {
		flight.result = cloneUnbindResult(result)
		flight.err = err
		delete(s.unbinds, userID)
		close(flight.done)
	}
	s.unbindMu.Unlock()
}

func waitForWorkspaceUnbind(ctx context.Context, flight *workspaceUnbindFlight) (*UnbindResult, error) {
	if flight == nil || flight.done == nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-flight.done:
		return cloneUnbindResult(flight.result), flight.err
	case <-ctx.Done():
		return nil, ErrWorkspaceLifecycleUnavailable
	}
}

// unbindOnce owns one complete local teardown. A finalized none row is a
// terminal idempotent state: it must not increment generation or reopen any
// retired HOME cleanup work on a repeated DELETE.
func (s *WorkspaceLifecycleService) unbindOnce(ctx context.Context, userID uint) (*UnbindResult, error) {
	account, accountErr := s.accounts.Get(ctx, userID, ProviderLark)
	if errors.Is(accountErr, gorm.ErrRecordNotFound) ||
		(accountErr == nil && account != nil && account.UserID == userID && account.Provider == ProviderLark && account.ConnectionState == model.FeishuConnectionNone) {
		return unboundWorkspaceResult(), nil
	}
	if accountErr != nil || !lifecycleAccountOwned(account, userID) {
		return nil, ErrWorkspaceLifecycleUnavailable
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
	if err := s.finalizeRetiredGenerationAgentWaits(cleanupCtx, userID, retiredGeneration); err != nil {
		return nil, err
	}
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
	teardownOwner := uuid.NewString()
	claimedTeardown, claimErr := s.claimRetiredTeardown(cleanupCtx, userID, retiredGeneration, nextGeneration, teardownOwner)
	if claimErr != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if !claimedTeardown {
		return unboundWorkspaceResult(), nil
	}
	teardownLease := s.startRetiredTeardownLease(cleanupCtx, userID, retiredGeneration, nextGeneration, teardownOwner)
	completedTeardown := false
	defer func() {
		teardownLease.stop()
		if !completedTeardown {
			_ = s.workspace.ReleaseRetiredTeardown(context.Background(), userID, retiredGeneration, teardownOwner, time.Now().UTC())
		}
	}()
	// lark-cli auth logout only clears the local user login; it cannot revoke or
	// delete the remote self-built app. Its command failure is deliberately
	// advisory, but LogoutRetired structurally returns an error only when its
	// retired HOME could not be materialized or removed. In that case deleting
	// the vault would falsely claim local credential removal, so leave the row
	// disconnecting for the same-generation retry.
	if !teardownLease.checkpoint() {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if _, err := s.teardown.LogoutRetired(cleanupCtx, userID, retiredGeneration); err != nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	if !teardownLease.checkpoint() {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	// The vault delete and connection finalization are intentionally one
	// owner-fenced transaction. A teardown worker that lost a short lease while
	// an uncooperative logout/cleanup was blocked cannot commit either half.
	teardownLease.stop()
	completed, err := s.workspace.CompleteRetiredTeardown(cleanupCtx, userID, retiredGeneration, nextGeneration, teardownOwner, time.Now().UTC())
	if err != nil || !completed {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	completedTeardown = true
	return unboundWorkspaceResult(), nil
}

func (s *WorkspaceLifecycleService) claimRetiredTeardown(ctx context.Context, userID uint, retiredGeneration, nextGeneration uint64, owner string) (bool, error) {
	for {
		now := time.Now().UTC()
		claimed, err := s.workspace.ClaimRetiredTeardown(ctx, userID, retiredGeneration, nextGeneration, owner, now, now.Add(s.retiredTeardownLeaseDuration()))
		if err != nil || claimed {
			return claimed, err
		}
		account, getErr := s.accounts.Get(ctx, userID, ProviderLark)
		if errors.Is(getErr, gorm.ErrRecordNotFound) || (getErr == nil && account != nil && account.ConnectionState == model.FeishuConnectionNone) {
			return false, nil
		}
		if getErr != nil || account == nil || account.Generation != nextGeneration || account.ConnectionState != model.FeishuConnectionDisconnecting {
			return false, ErrWorkspaceLifecycleUnavailable
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *WorkspaceLifecycleService) retiredTeardownLeaseDuration() time.Duration {
	timeout := s.cleanupTimeout
	if timeout <= 0 {
		timeout = workspaceLifecycleCleanupTimeout
	}
	lease := timeout / 4
	if lease <= 0 {
		return time.Nanosecond
	}
	return lease
}

func (s *WorkspaceLifecycleService) startRetiredTeardownLease(
	cleanupCtx context.Context,
	userID uint,
	retiredGeneration, disconnectingGeneration uint64,
	owner string,
) *retiredTeardownLease {
	if cleanupCtx == nil {
		cleanupCtx = context.Background()
	}
	heartbeatCtx, cancel := context.WithCancel(cleanupCtx)
	lease := &retiredTeardownLease{
		workspace:               s.workspace,
		userID:                  userID,
		retiredGeneration:       retiredGeneration,
		disconnectingGeneration: disconnectingGeneration,
		owner:                   owner,
		leaseDuration:           s.retiredTeardownLeaseDuration(),
		cleanupCtx:              heartbeatCtx,
		cancel:                  cancel,
		done:                    make(chan struct{}),
	}
	go lease.run()
	return lease
}

func (l *retiredTeardownLease) run() {
	defer close(l.done)
	interval := l.leaseDuration / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-l.cleanupCtx.Done():
			l.mu.Lock()
			l.lost = true
			l.mu.Unlock()
			return
		case <-timer.C:
			l.mu.Lock()
			continueRenewing := !l.stopped && !l.lost && l.renewLocked()
			l.mu.Unlock()
			if !continueRenewing {
				return
			}
			timer.Reset(interval)
		}
	}
}

// checkpoint makes the durable lease freshly live before an irreversible
// boundary. It holds the local heartbeat mutex so a failed renew cannot race a
// caller into continuing after lease loss.
func (l *retiredTeardownLease) checkpoint() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped || l.lost {
		return false
	}
	return l.renewLocked()
}

func (l *retiredTeardownLease) renewLocked() bool {
	if l.cleanupCtx == nil || l.cleanupCtx.Err() != nil || l.workspace == nil {
		l.lost = true
		return false
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(l.leaseDuration)
	if deadline, ok := l.cleanupCtx.Deadline(); ok {
		if !deadline.After(now) {
			l.lost = true
			return false
		}
		if deadline.Before(leaseUntil) {
			leaseUntil = deadline
		}
	}
	callCtx, cancel := retiredTeardownLeaseCallContext(l.cleanupCtx, l.leaseDuration)
	defer cancel()
	renewed, err := l.workspace.RenewRetiredTeardown(
		callCtx, l.userID, l.retiredGeneration, l.disconnectingGeneration, l.owner, now, leaseUntil,
	)
	if err != nil || !renewed {
		l.lost = true
		return false
	}
	return true
}

// retiredTeardownLeaseCallContext detaches the durability check from a client
// request cancellation, while preserving the lifecycle cleanup deadline and a
// short maximum database-call deadline. It prevents a wedged store call from
// keeping the heartbeat goroutine alive indefinitely.
func retiredTeardownLeaseCallContext(cleanupCtx context.Context, leaseDuration time.Duration) (context.Context, context.CancelFunc) {
	limit := leaseDuration
	if limit <= 0 || limit > time.Second {
		limit = time.Second
	}
	if cleanupCtx != nil {
		if deadline, ok := cleanupCtx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining > 0 && remaining < limit {
				limit = remaining
			}
		}
	}
	if limit <= 0 {
		limit = time.Nanosecond
	}
	return context.WithTimeout(context.Background(), limit)
}

// stop joins a bounded in-flight renewal before the caller releases or
// completes the durable lease. It is safe to call more than once.
func (l *retiredTeardownLease) stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	alreadyStopped := l.stopped
	l.stopped = true
	l.mu.Unlock()
	if !alreadyStopped && l.cancel != nil {
		l.cancel()
	}
	if l.done != nil {
		<-l.done
	}
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
		for _, domain := range []string{"docs", "base", "wiki", "drive"} {
			if value, found := raw[domain]; found && validCapabilityState(value.State) {
				status.Capabilities[domain] = CapabilityStatus{State: value.State, LastSuccessAt: value.LastSuccessAt}
			}
		}
	}
	return status
}

func defaultWorkspaceCapabilities() map[string]CapabilityStatus {
	return map[string]CapabilityStatus{
		"docs":  {State: model.FeishuCapabilityUnknown},
		"base":  {State: model.FeishuCapabilityUnknown},
		"wiki":  {State: model.FeishuCapabilityUnknown},
		"drive": {State: model.FeishuCapabilityUnknown},
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

// lifecycleOperationSessionID resolves the browser acknowledgement fence from
// the operation itself. No auth-session lookup or worker action is performed.
func lifecycleOperationSessionID(source *model.FeishuOperation) (string, bool) {
	if source == nil || (!recoveryWaitingState(source.State) && source.State != model.FeishuOperationWaitingConfirmation) {
		return "", false
	}
	summary, err := decodeOperationSummary(source.ResultSummaryJSON)
	if err != nil || summary.Status != source.State || strings.TrimSpace(summary.SessionID) == "" {
		return "", true
	}
	return summary.SessionID, true
}

// lifecycleCurrentOperationObservation is the read-only response for a stale
// or rolling-upgrade browser card. The current opaque session identity may be
// rendered, but its transient URL and scopes are intentionally absent.
func lifecycleCurrentOperationObservation(source *model.FeishuOperation) (*OperationResult, error) {
	if source == nil {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	summary, err := decodeOperationSummary(source.ResultSummaryJSON)
	if err != nil || summary.Status != source.State || strings.TrimSpace(summary.SessionID) == "" || strings.TrimSpace(summary.Phase) == "" {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	result := &OperationResult{
		OperationID: source.ID,
		State:       source.State,
		NoticeCode:  AuthorizationUpdated,
		Action: &OperationAction{
			Provider: ProviderLark, OperationID: source.ID, SessionID: summary.SessionID, Phase: summary.Phase,
		},
	}
	if summary.ExpiresAt != nil {
		result.Action.ExpiresAt = summary.ExpiresAt.UTC()
	}
	return result, nil
}

func lifecycleAuthorizationNoticeResult(source *model.FeishuOperation, completion *DeviceAuthCompletion) (*OperationResult, error) {
	if source == nil || completion == nil || source.State != model.FeishuOperationWaitingUserAuth || completion.Completed {
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	result := &OperationResult{
		OperationID: source.ID,
		State:       source.State,
		NoticeCode:  completion.NoticeCode,
	}
	switch completion.NoticeCode {
	case AuthorizationPending, AuthorizationProcessing:
		if completion.Action != nil {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
	case AuthorizationRejected, AuthorizationExpired, AuthorizationUpdated:
		action := completion.Action
		if action == nil || action.OperationID != source.ID || strings.TrimSpace(action.SessionID) == "" ||
			action.Phase != model.FeishuAuthPhaseUserAuth || strings.TrimSpace(action.URL) == "" {
			return nil, ErrWorkspaceLifecycleUnavailable
		}
		result.Action = cloneOperationAction(action)
	default:
		return nil, ErrWorkspaceLifecycleUnavailable
	}
	return result, nil
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

func cloneUnbindResult(source *UnbindResult) *UnbindResult {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}
