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
	return "Read-only inspection of the current user's Feishu connection or one controlled Docs/Base/Wiki/Drive command. " +
		`Minimum valid examples: {"mode":"connection"} or {"mode":"command","argv":["lark-cli","docs","+fetch","--doc","<文档URL或token>"]}. ` +
		"Use only when the user asks what the current connection state is or after a structured failure. " +
		"If the user asks to connect, reconnect, authorize, or link Feishu, call lark_connect immediately instead of this tool."
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
			"argv":{"type":"array","minItems":1,"items":{"type":"string"}}
		},
		"required":["mode"],
		"additionalProperties":false
	}`)
}

func (t *larkInspectTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	if t == nil || t.inspector == nil || ctx == nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorUnavailable)
	}
	userID, ok := middleware.UserIDFromCtx(ctx)
	runID := RunIDFromContext(ctx)
	if !ok || userID == 0 || runID == 0 {
		return larkWorkspaceSoftError(larkWorkspaceErrorIdentity)
	}
	request, err := decodeLarkInspectInput(input)
	if err != nil {
		code, message, hint, fix := larkInspectInputProtocolSpec(err)
		return larkWorkspaceToolInputProtocolError(runID, t.Name(), code, message, hint, fix)
	}
	larkToolInputProtocolClearRun(runID)
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

var (
	errLarkInspectMissingMode     = errors.New("inspect mode missing")
	errLarkInspectInvalidMode     = errors.New("inspect mode invalid")
	errLarkInspectConnectionArgv  = errors.New("inspect connection argv rejected")
	errLarkInspectMissingArgv     = errors.New("inspect command argv missing")
	errLarkInspectInvalidCommand  = errors.New("inspect command argv invalid")
	errLarkInspectInvalidEnvelope = errors.New("inspect envelope invalid")
)

func decodeLarkInspectInput(input ToolInput) (feishu.InspectionRequest, error) {
	fields, err := decodeStrictLarkToolObject(input, "mode", "argv", "skill_receipts")
	if err != nil {
		return feishu.InspectionRequest{}, errLarkInspectInvalidEnvelope
	}
	var mode string
	raw, ok := fields["mode"]
	if !ok {
		return feishu.InspectionRequest{}, errLarkInspectMissingMode
	}
	if json.Unmarshal(raw, &mode) != nil {
		return feishu.InspectionRequest{}, errLarkInspectInvalidMode
	}
	request := feishu.InspectionRequest{Mode: mode}
	switch mode {
	case feishu.InspectionModeConnection:
		if _, hasArgv := fields["argv"]; hasArgv {
			return feishu.InspectionRequest{}, errLarkInspectConnectionArgv
		}
	case feishu.InspectionModeCommand:
		argvRaw, hasArgv := fields["argv"]
		if !hasArgv {
			return feishu.InspectionRequest{}, errLarkInspectMissingArgv
		}
		if json.Unmarshal(argvRaw, &request.Argv) != nil || len(request.Argv) == 0 {
			return feishu.InspectionRequest{}, errLarkInspectInvalidCommand
		}
		if request.Argv[0] == "lark-cli" {
			request.Argv = request.Argv[1:]
		}
		if len(request.Argv) == 0 || strings.TrimSpace(request.Argv[0]) == "" {
			return feishu.InspectionRequest{}, errLarkInspectInvalidCommand
		}
	default:
		return feishu.InspectionRequest{}, errLarkInspectInvalidMode
	}
	return request, nil
}

func larkInspectInputProtocolSpec(err error) (code, message, hint, fix string) {
	fix = "lark_inspect 的 mode/argv 工具参数"
	switch {
	case errors.Is(err, errLarkInspectMissingMode):
		return "missing_inspect_mode",
			"lark_inspect 缺少 mode；失败发生在大模型到后端的工具参数层，不是飞书端失败。检查连接状态请传 {\"mode\":\"connection\"}；检查一条命令请传 {\"mode\":\"command\",\"argv\":[\"lark-cli\",\"docs\",\"+fetch\",\"--doc\",\"<文档URL或token>\"]}。",
			"lark_inspect 缺少 mode",
			fix
	case errors.Is(err, errLarkInspectMissingArgv):
		return "missing_inspect_argv",
			"lark_inspect command 模式缺少 argv；失败发生在大模型到后端的工具参数层，不是飞书端失败。下一次必须传 mode 和 argv，例如 {\"mode\":\"command\",\"argv\":[\"lark-cli\",\"docs\",\"+fetch\",\"--doc\",\"<文档URL或token>\"]}。如果只检查连接状态，请传 {\"mode\":\"connection\"}。",
			"lark_inspect command 模式缺少 argv",
			fix
	case errors.Is(err, errLarkInspectConnectionArgv):
		return "invalid_inspect_input",
			"lark_inspect connection 模式不接受 argv；失败发生在大模型到后端的工具参数层，不是飞书端失败。检查连接状态只传 {\"mode\":\"connection\"}。",
			"lark_inspect connection 模式不接受 argv",
			fix
	default:
		return "invalid_inspect_input",
			"lark_inspect 参数无效；失败发生在大模型到后端的工具参数层，不是飞书端失败。检查连接状态请传 {\"mode\":\"connection\"}；检查命令请传 mode=command 和 argv 数组。",
			"lark_inspect 参数无效",
			fix
	}
}
