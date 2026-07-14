package feishu

import (
	"context"
	"errors"
	"strings"
	"time"

	"numind-server/internal/numind/store"

	"github.com/google/uuid"
)

const operationConfirmationLifetime = 15 * time.Minute

// OperationConfirmationRequester supplies the durable action identity that the
// operation state machine persists under its existing user/generation/lease
// fence. Task13 will consume this stored action for confirmed/cancelled input.
// It intentionally does not mutate operations itself because it is not given
// the operation lease owner.
type OperationConfirmationRequester struct {
	workspace store.IFeishuWorkspaceStore
	now       func() time.Time
	newID     func() string
}

// NewOperationConfirmationRequester creates the non-nil confirmation adapter
// required by FeishuOperationService. A nil datastore is rejected so callers
// cannot silently wire a confirmation bypass.
func NewOperationConfirmationRequester(dataStore store.IStore) (*OperationConfirmationRequester, error) {
	if dataStore == nil || dataStore.FeishuWorkspace() == nil {
		return nil, errors.New("feishu operation confirmation requester dependencies rejected")
	}
	return &OperationConfirmationRequester{
		workspace: dataStore.FeishuWorkspace(),
		now:       func() time.Time { return time.Now().UTC() },
		newID:     uuid.NewString,
	}, nil
}

// RequestConfirmation returns a durable, server-owned confirmation action for
// high-risk operations. The caller persists waiting_confirmation atomically
// while it still holds the operation lease; nil is never a successful result.
func (r *OperationConfirmationRequester) RequestConfirmation(
	ctx context.Context,
	operationID string,
	summary ConfirmationSummary,
) (*OperationAction, error) {
	if r == nil || r.workspace == nil || ctx == nil || ctx.Err() != nil ||
		strings.TrimSpace(operationID) == "" || summary.Risk != RiskHigh ||
		strings.TrimSpace(summary.CommandPath) == "" || strings.TrimSpace(summary.Domain) == "" ||
		strings.TrimSpace(summary.Action) == "" || r.now == nil || r.newID == nil {
		return nil, errors.New("feishu operation confirmation unavailable")
	}
	now := r.now().UTC()
	sessionID := strings.TrimSpace(r.newID())
	if sessionID == "" {
		return nil, errors.New("feishu operation confirmation unavailable")
	}
	return &OperationAction{
		Provider:    ProviderLark,
		OperationID: operationID,
		SessionID:   sessionID,
		Phase:       "confirmation",
		ExpiresAt:   now.Add(operationConfirmationLifetime),
	}, nil
}

var _ ConfirmationRequester = (*OperationConfirmationRequester)(nil)

// RecoveryStarterFunc is the small closure bridge used by the outer composition
// root to let OperationService refer to the subsequently-created
// AuthSessionService without importing the Agent package or forming a package
// cycle. Missing callbacks fail closed.
type RecoveryStarterFunc struct {
	StartRecoveryFunc func(context.Context, RecoveryRequest) (*OperationAction, error)
	ActivateFunc      func(context.Context, string) error
	AbortFunc         func(string)
}

// StartRecovery implements RecoveryStarter.
func (f RecoveryStarterFunc) StartRecovery(ctx context.Context, request RecoveryRequest) (*OperationAction, error) {
	if f.StartRecoveryFunc == nil {
		return nil, errors.New("feishu recovery starter unavailable")
	}
	return f.StartRecoveryFunc(ctx, request)
}

// Activate implements RecoveryStarter.
func (f RecoveryStarterFunc) Activate(ctx context.Context, sessionID string) error {
	if f.ActivateFunc == nil {
		return errors.New("feishu recovery starter unavailable")
	}
	return f.ActivateFunc(ctx, sessionID)
}

// Abort implements RecoveryStarter.
func (f RecoveryStarterFunc) Abort(sessionID string) {
	if f.AbortFunc != nil {
		f.AbortFunc(sessionID)
	}
}

var _ RecoveryStarter = RecoveryStarterFunc{}
