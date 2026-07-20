package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// LarkConnector is the narrow, operation-backed dependency for an explicit
// user request to connect or reconnect Feishu.
type LarkConnector interface {
	Connect(context.Context, feishu.ConnectOperationRequest) (*feishu.OperationResult, error)
}

type larkConnectTool struct {
	BaseTool
	connector LarkConnector
}

var _ FullTool = (*larkConnectTool)(nil)

func (t *larkConnectTool) Name() string { return "lark_connect" }
func (t *larkConnectTool) Description() string {
	return "Immediately start or resume the current user's Feishu connection flow. " +
		"Call this when the user explicitly asks to connect, reconnect, authorize, or link Feishu, even when no Docs/Base/Wiki/Drive business task was provided. " +
		"Do not use it merely to inspect connection status, and do not ask the user to provide App ID or App Secret."
}
func (t *larkConnectTool) UserFacingName() string             { return "连接飞书" }
func (t *larkConnectTool) NarrationVerb() string              { return "连接飞书" }
func (t *larkConnectTool) IsReadOnly() bool                   { return false }
func (t *larkConnectTool) IsConcurrencySafe(_ ToolInput) bool { return false }

func (t *larkConnectTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t *larkConnectTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	if t == nil || t.connector == nil || ctx == nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorUnavailable)
	}
	fields, err := decodeStrictLarkToolObject(input)
	if err != nil || len(fields) != 0 {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidExecuteInput)
	}
	userID, ok := middleware.UserIDFromCtx(ctx)
	runID := RunIDFromContext(ctx)
	toolCallID := ToolCallIDFromContext(ctx)
	if !ok || userID == 0 || runID == 0 || strings.TrimSpace(toolCallID) == "" {
		return larkWorkspaceSoftError(larkWorkspaceErrorIdentity)
	}
	result, err := t.connector.Connect(ctx, feishu.ConnectOperationRequest{
		UserID: userID, AgentRunID: runID, ToolCallID: toolCallID,
		IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
	})
	if errors.Is(err, feishu.ErrOperationConnectionInProgress) {
		return larkWorkspaceSoftError(larkWorkspaceErrorConnectionInProgress)
	}
	if err != nil || result == nil || strings.TrimSpace(result.OperationID) == "" || strings.TrimSpace(result.State) == "" {
		return larkWorkspaceSoftError(larkWorkspaceErrorExecute)
	}
	if isLarkWaitingState(result.State) {
		return larkWaitingYield(result, toolCallID)
	}
	if result.State != model.FeishuOperationSucceeded {
		return larkWorkspaceSoftError(larkWorkspaceErrorExecute)
	}
	output, err := feishu.MarshalLarkToolResult(result)
	if err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidResult)
	}
	return ToolResult(output), nil
}
