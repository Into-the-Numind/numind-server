package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// LarkExecutor is the narrow dependency used by lark_execute.
type LarkExecutor interface {
	Execute(context.Context, feishu.ExecuteRequest) (*feishu.OperationResult, error)
}

type larkExecuteTool struct {
	BaseTool
	executor LarkExecutor
}

var _ FullTool = (*larkExecuteTool)(nil)

func (t *larkExecuteTool) Name() string { return "lark_execute" }
func (t *larkExecuteTool) Description() string {
	return "Execute controlled lark-cli argv for Docs/Base/Wiki/Drive using opaque skill receipts. " +
		"argv may copy the official skill command verbatim with a leading `lark-cli`, or omit that one executable token. " +
		"Identity, authorization, scope, risk, and idempotency come only from server context. " +
		"There is no shell execution and no IM/message capability."
}
func (t *larkExecuteTool) UserFacingName() string { return "执行飞书工作区操作" }
func (t *larkExecuteTool) NarrationVerb() string  { return "操作飞书工作区" }
func (t *larkExecuteTool) IsReadOnly() bool       { return false }
func (t *larkExecuteTool) IsConcurrencySafe(_ ToolInput) bool {
	return false
}

func (t *larkExecuteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"argv":{"type":"array","minItems":1,"items":{"type":"string"},"description":"Controlled lark-cli argv; a single leading literal lark-cli from official skill examples is optional; no shell."},
			"stdin_json":{"description":"Optional JSON value passed as stdin; null is normalized to absent."},
			"skill_receipts":{"type":"array","minItems":1,"items":{"type":"string"},"description":"Opaque receipts returned by lark_skill_read."}
		},
		"required":["argv","skill_receipts"],
		"additionalProperties":false
	}`)
}

type larkExecuteInput struct {
	Argv          []string
	StdinJSON     json.RawMessage
	SkillReceipts []string
}

type larkExecuteOutput struct {
	OK          bool            `json:"ok"`
	State       string          `json:"state"`
	OperationID string          `json:"operation_id"`
	Data        json.RawMessage `json:"data,omitempty"`
}

func (t *larkExecuteTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	if t == nil || t.executor == nil || ctx == nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorUnavailable)
	}
	decoded, err := decodeLarkExecuteInput(input)
	if err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidExecuteInput)
	}

	userID, ok := middleware.UserIDFromCtx(ctx)
	runID := RunIDFromContext(ctx)
	toolCallID := ToolCallIDFromContext(ctx)
	if !ok || userID == 0 || runID == 0 || strings.TrimSpace(toolCallID) == "" {
		return larkWorkspaceSoftError(larkWorkspaceErrorIdentity)
	}
	retryState, retryAttempt, allowed := larkExecuteRetryBegin(runID)
	if !allowed {
		return larkWorkspaceSoftError(larkWorkspaceErrorExecuteRetryExhausted)
	}

	result, err := t.executor.Execute(ctx, feishu.ExecuteRequest{
		UserID:         userID,
		AgentRunID:     runID,
		ToolCallID:     toolCallID,
		IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
		Argv:           append([]string(nil), decoded.Argv...),
		StdinJSON:      append(json.RawMessage(nil), decoded.StdinJSON...),
		SkillReceipts:  append([]string(nil), decoded.SkillReceipts...),
	})
	if err != nil {
		if errors.Is(err, feishu.ErrOperationRequestRejected) {
			if larkExecuteRetryRejected(retryState, retryAttempt) {
				return larkWorkspaceSoftError(larkWorkspaceErrorExecuteRetryExhausted)
			}
			return larkWorkspaceSoftError(larkWorkspaceErrorExecuteRejected)
		}
		larkExecuteRetryFailed(retryState, retryAttempt)
		return larkWorkspaceSoftError(larkWorkspaceErrorExecute)
	}
	if result == nil || strings.TrimSpace(result.OperationID) == "" || strings.TrimSpace(result.State) == "" {
		larkExecuteRetryFailed(retryState, retryAttempt)
		return larkWorkspaceSoftError(larkWorkspaceErrorExecute)
	}

	if isLarkWaitingState(result.State) {
		waitingResult, waitingErr := larkWaitingYield(result, toolCallID)
		if waitingErr != nil {
			larkExecuteRetryCompleted(retryState, retryAttempt)
		} else {
			larkExecuteRetryFailed(retryState, retryAttempt)
		}
		return waitingResult, waitingErr
	}
	if !isLarkTerminalState(result.State) {
		larkExecuteRetryFailed(retryState, retryAttempt)
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidResult)
	}
	if len(result.Data) > 0 && !json.Valid(result.Data) {
		larkExecuteRetryFailed(retryState, retryAttempt)
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidResult)
	}
	larkExecuteRetryCompleted(retryState, retryAttempt)
	output, err := json.Marshal(larkExecuteOutput{
		OK:          result.State == model.FeishuOperationSucceeded,
		State:       result.State,
		OperationID: result.OperationID,
		Data:        append(json.RawMessage(nil), result.Data...),
	})
	if err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidResult)
	}
	return ToolResult(output), nil
}

func decodeLarkExecuteInput(input ToolInput) (larkExecuteInput, error) {
	fields, err := decodeStrictLarkToolObject(input, "argv", "stdin_json", "skill_receipts")
	if err != nil {
		return larkExecuteInput{}, err
	}
	var decoded larkExecuteInput
	argvRaw, ok := fields["argv"]
	if !ok || json.Unmarshal(argvRaw, &decoded.Argv) != nil || len(decoded.Argv) == 0 {
		return larkExecuteInput{}, fmt.Errorf("argv rejected")
	}
	// Official embedded skills intentionally show complete shell commands such as
	// `lark-cli docs +create ...`. lark_execute is not a shell and the controlled
	// catalog expects only command argv, so consume exactly this one pinned
	// executable token at the trust boundary. Paths, aliases, env assignments,
	// repeated prefixes, and every other first token remain untouched and are
	// rejected by the catalog as before.
	if decoded.Argv[0] == "lark-cli" {
		decoded.Argv = decoded.Argv[1:]
		if len(decoded.Argv) == 0 {
			return larkExecuteInput{}, fmt.Errorf("argv rejected")
		}
	}
	receiptsRaw, ok := fields["skill_receipts"]
	if !ok || json.Unmarshal(receiptsRaw, &decoded.SkillReceipts) != nil || len(decoded.SkillReceipts) == 0 {
		return larkExecuteInput{}, fmt.Errorf("skill receipts rejected")
	}
	if stdinRaw, ok := fields["stdin_json"]; ok && !bytes.Equal(bytes.TrimSpace(stdinRaw), []byte("null")) {
		decoded.StdinJSON = append(json.RawMessage(nil), stdinRaw...)
	}
	return decoded, nil
}

func isLarkWaitingState(state string) bool {
	switch state {
	case model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation:
		return true
	default:
		return false
	}
}

func isLarkTerminalState(state string) bool {
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

func larkWaitingYield(result *feishu.OperationResult, toolCallID string) (ToolResult, error) {
	action := result.Action
	if action == nil || strings.TrimSpace(action.Provider) == "" ||
		strings.TrimSpace(action.OperationID) == "" || action.OperationID != result.OperationID ||
		strings.TrimSpace(action.SessionID) == "" || strings.TrimSpace(action.Phase) == "" ||
		action.ExpiresAt.IsZero() {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidWait)
	}
	external := ExternalActionPayload{
		Provider:    action.Provider,
		OperationID: action.OperationID,
		SessionID:   action.SessionID,
		ToolCallID:  toolCallID,
		Phase:       action.Phase,
		URL:         action.URL,
		ExpiresAt:   action.ExpiresAt,
	}
	return nil, &yieldError{Payload: YieldPayload{ExternalAction: &external}}
}
