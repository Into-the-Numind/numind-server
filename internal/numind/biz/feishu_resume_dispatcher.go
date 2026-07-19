package biz

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/externalaction"
	"numind-server/internal/pkg/model"
)

type operationResumeService interface {
	Resume(context.Context, uint, string) (*feishu.OperationResult, error)
}

type agentExternalResultResumer interface {
	Resume(context.Context, agent.ExternalToolResult) error
	FinalizeExternalToolWait(context.Context, uint, uint64, string, string, externalaction.TerminalOutcome) (bool, error)
}

type workspaceResumeCall struct {
	done chan struct{}
	err  error
}

// WorkspaceResumeDispatcher is the sole outer-package bridge from a completed
// Feishu operation to Task11's durable Agent continuation. It deliberately owns
// no lease: AgentRunResumer's tokenized claim remains the cross-process fence.
type WorkspaceResumeDispatcher struct {
	operations   operationResumeService
	agentResumer agentExternalResultResumer

	mu       sync.Mutex
	inFlight map[string]*workspaceResumeCall
	joined   func() // test hook: invoked after a callback joins an in-flight call
}

// NewWorkspaceResumeDispatcher constructs the shared authorization and
// confirmation completion dispatcher. Missing dependencies fail closed when
// DispatchResume is called, so a half-built composition cannot backfill a run.
func NewWorkspaceResumeDispatcher(operations operationResumeService, agentResumer agentExternalResultResumer) *WorkspaceResumeDispatcher {
	return &WorkspaceResumeDispatcher{
		operations: operations, agentResumer: agentResumer,
		inFlight: make(map[string]*workspaceResumeCall),
	}
}

// DispatchResume advances one existing operation and, after any terminal
// outcome, backfills the original external tool call. Concurrent callbacks for the same
// operation share one in-process attempt; Task11's durable tokenized lease
// makes retries and callbacks from other application instances idempotent.
func (d *WorkspaceResumeDispatcher) DispatchResume(ctx context.Context, userID uint, operationID string) error {
	if d == nil || d.operations == nil || d.agentResumer == nil || userID == 0 || strings.TrimSpace(operationID) == "" {
		return fmt.Errorf("feishu workspace resume dispatcher is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	d.mu.Lock()
	if d.inFlight == nil {
		d.inFlight = make(map[string]*workspaceResumeCall)
	}
	if existing := d.inFlight[operationID]; existing != nil {
		joined := d.joined
		d.mu.Unlock()
		if joined != nil {
			joined()
		}
		select {
		case <-existing.done:
			return existing.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	call := &workspaceResumeCall{done: make(chan struct{})}
	d.inFlight[operationID] = call
	d.mu.Unlock()

	err := d.dispatch(ctx, userID, operationID)
	d.mu.Lock()
	call.err = err
	delete(d.inFlight, operationID)
	close(call.done)
	d.mu.Unlock()
	return err
}

func (d *WorkspaceResumeDispatcher) dispatch(ctx context.Context, userID uint, operationID string) error {
	result, err := d.operations.Resume(ctx, userID, operationID)
	if err != nil {
		return fmt.Errorf("resume feishu operation: %w", err)
	}
	if result == nil {
		return fmt.Errorf("resume feishu operation: empty result")
	}
	switch result.State {
	case model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation:
		return nil
	case model.FeishuOperationSucceeded:
		// handled below
	case model.FeishuOperationFailed, model.FeishuOperationUnknown, model.FeishuOperationCancelled:
		if result.OperationID != operationID || result.AgentRunID == 0 || strings.TrimSpace(result.ToolCallID) == "" {
			return fmt.Errorf("resume feishu operation: terminal result identity is invalid")
		}
		outcome, ok := terminalAgentOutcome(result.State)
		if !ok {
			return fmt.Errorf("resume feishu operation: invalid terminal state")
		}
		if _, err := d.agentResumer.FinalizeExternalToolWait(
			ctx, userID, result.AgentRunID, result.OperationID, result.ToolCallID, outcome,
		); err != nil {
			return fmt.Errorf("finalize agent external wait: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("resume feishu operation: invalid state")
	}
	if result.OperationID != operationID || result.AgentRunID == 0 || strings.TrimSpace(result.ToolCallID) == "" {
		return fmt.Errorf("resume feishu operation: terminal result identity is invalid")
	}
	toolResult, err := feishu.MarshalLarkToolResult(result)
	if err != nil {
		return fmt.Errorf("resume feishu operation: terminal result is invalid: %w", err)
	}
	if err := d.agentResumer.Resume(ctx, agent.ExternalToolResult{
		RunID:       result.AgentRunID,
		ToolCallID:  result.ToolCallID,
		OperationID: result.OperationID,
		Result:      toolResult,
	}); err != nil {
		return fmt.Errorf("resume agent run: %w", err)
	}
	return nil
}

func terminalAgentOutcome(state string) (externalaction.TerminalOutcome, bool) {
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
