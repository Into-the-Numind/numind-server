package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
)

// LarkInspector is the narrow read-only dependency used by lark_inspect.
type LarkInspector interface {
	Inspect(context.Context, feishu.InspectionRequest) (*feishu.InspectionResult, error)
}

type larkInspectTool struct {
	BaseTool
	inspector LarkInspector
}

var _ FullTool = (*larkInspectTool)(nil)

func (t *larkInspectTool) Name() string { return "lark_inspect" }
func (t *larkInspectTool) Description() string {
	return "Inspect the current user's Feishu connection or one controlled Docs/Base/Wiki/Drive command without executing the business operation. Use only when the user asks about connection state or after a structured failure."
}
func (t *larkInspectTool) UserFacingName() string { return "检查飞书工作区" }
func (t *larkInspectTool) NarrationVerb() string  { return "检查飞书工作区" }
func (t *larkInspectTool) IsReadOnly() bool       { return true }
func (t *larkInspectTool) IsConcurrencySafe(_ ToolInput) bool {
	// Command inspection materializes the same encrypted per-user HOME as the
	// executor, so serialize it with other workspace operations.
	return false
}

func (t *larkInspectTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"mode":{"type":"string","enum":["connection","command"]},
			"argv":{"type":"array","minItems":1,"items":{"type":"string"}},
			"skill_receipts":{"type":"array","minItems":1,"items":{"type":"string"}}
		},
		"required":["mode"],
		"additionalProperties":false
	}`)
}

func (t *larkInspectTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	if t == nil || t.inspector == nil || ctx == nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorUnavailable)
	}
	request, err := decodeLarkInspectInput(input)
	if err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidInspectInput)
	}
	userID, ok := middleware.UserIDFromCtx(ctx)
	runID := RunIDFromContext(ctx)
	if !ok || userID == 0 || runID == 0 {
		return larkWorkspaceSoftError(larkWorkspaceErrorIdentity)
	}
	request.UserID = userID
	request.AgentRunID = runID
	result, err := t.inspector.Inspect(ctx, request)
	if err != nil {
		if errors.Is(err, feishu.ErrInspectionRejected) {
			return larkWorkspaceSoftError(larkWorkspaceErrorInspectRejected)
		}
		return larkWorkspaceSoftError(larkWorkspaceErrorInspect)
	}
	if result == nil || result.Mode != request.Mode {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidResult)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidResult)
	}
	return ToolResult(encoded), nil
}

func decodeLarkInspectInput(input ToolInput) (feishu.InspectionRequest, error) {
	fields, err := decodeStrictLarkToolObject(input, "mode", "argv", "skill_receipts")
	if err != nil {
		return feishu.InspectionRequest{}, err
	}
	var mode string
	if raw, ok := fields["mode"]; !ok || json.Unmarshal(raw, &mode) != nil {
		return feishu.InspectionRequest{}, errors.New("inspection mode rejected")
	}
	request := feishu.InspectionRequest{Mode: mode}
	switch mode {
	case feishu.InspectionModeConnection:
		if _, hasArgv := fields["argv"]; hasArgv {
			return feishu.InspectionRequest{}, errors.New("connection argv rejected")
		}
		if _, hasReceipts := fields["skill_receipts"]; hasReceipts {
			return feishu.InspectionRequest{}, errors.New("connection receipts rejected")
		}
	case feishu.InspectionModeCommand:
		argvRaw, hasArgv := fields["argv"]
		receiptsRaw, hasReceipts := fields["skill_receipts"]
		if !hasArgv || !hasReceipts || json.Unmarshal(argvRaw, &request.Argv) != nil ||
			json.Unmarshal(receiptsRaw, &request.SkillReceipts) != nil ||
			len(request.Argv) == 0 || len(request.SkillReceipts) == 0 {
			return feishu.InspectionRequest{}, errors.New("command inspection rejected")
		}
		if request.Argv[0] == "lark-cli" {
			request.Argv = request.Argv[1:]
		}
		if len(request.Argv) == 0 || strings.TrimSpace(request.Argv[0]) == "" {
			return feishu.InspectionRequest{}, errors.New("command argv rejected")
		}
	default:
		return feishu.InspectionRequest{}, errors.New("inspection mode rejected")
	}
	return request, nil
}
