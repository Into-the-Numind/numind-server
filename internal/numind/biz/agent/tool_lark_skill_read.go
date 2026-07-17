package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"numind-server/internal/numind/biz/feishu"
)

// SkillReadExecutor is the narrow dependency used by lark_skill_read.
type SkillReadExecutor interface {
	Read(context.Context, feishu.SkillReadRequest) (*feishu.SkillReadPage, error)
}

type larkSkillReadTool struct {
	BaseTool
	executor SkillReadExecutor
}

var _ FullTool = (*larkSkillReadTool)(nil)

func (t *larkSkillReadTool) Name() string { return "lark_skill_read" }
func (t *larkSkillReadTool) Description() string {
	return "Read only the four official embedded lark-cli skills (lark-shared, lark-doc, " +
		"lark-base, lark-wiki) through a controlled reference/cursor. No raw path, user identity, " +
		"connection, or credential is accepted."
}
func (t *larkSkillReadTool) UserFacingName() string { return "读取飞书技能" }
func (t *larkSkillReadTool) NarrationVerb() string  { return "读取飞书技能" }
func (t *larkSkillReadTool) IsReadOnly() bool       { return true }
func (t *larkSkillReadTool) IsConcurrencySafe(_ ToolInput) bool {
	return true
}
func (t *larkSkillReadTool) IsSearchOrReadCommand() bool { return true }

func (t *larkSkillReadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"skill":{"type":"string","enum":["lark-shared","lark-doc","lark-base","lark-wiki"]},
			"reference":{"type":"string","description":"Controlled reference returned by a prior skill page."},
			"cursor":{"type":"string","description":"Opaque cursor returned by a prior skill page."}
		},
		"required":["skill"],
		"additionalProperties":false
	}`)
}

type larkSkillReadOutput struct {
	OK           bool     `json:"ok"`
	Skill        string   `json:"skill"`
	Path         string   `json:"path"`
	HostedPolicy string   `json:"hosted_policy"`
	Content      string   `json:"content"`
	References   []string `json:"references"`
	Cursor       string   `json:"cursor"`
	Receipt      string   `json:"receipt"`
	CLIVersion   string   `json:"cli_version"`
}

const larkHostedExecutionPolicy = "有数托管规则（优先于下方针对本地电脑的 CLI 说明）：" +
	"不要执行 auth/config/whoami/qrcode，也不要要求用户提供 App ID/App Secret。" +
	"直接调用 lark_execute 执行 Docs/Base/Wiki 业务命令；连接或权限不足时，平台会自动生成授权卡片。" +
	"skill_receipts 必须同时包含当前 run 的 lark-shared receipt 和对应业务技能 receipt。" +
	"官方命令中的固定 --as user 与 --format json 可以保留，平台会安全规范化。" +
	"如果命令被拒绝，只修正业务命令或 receipts，最多修正并重试一次；不要改跑本地初始化命令。"

func (t *larkSkillReadTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	if t == nil || t.executor == nil || ctx == nil || RunIDFromContext(ctx) == 0 {
		return larkWorkspaceSoftError(larkWorkspaceErrorUnavailable)
	}

	fields, err := decodeStrictLarkToolObject(input, "skill", "reference", "cursor")
	if err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidSkillInput)
	}
	var request feishu.SkillReadRequest
	request.AgentRunID = RunIDFromContext(ctx)
	if err := decodeRequiredNonEmptyString(fields, "skill", &request.Skill); err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidSkillInput)
	}
	if err := decodeOptionalString(fields, "reference", &request.Reference); err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidSkillInput)
	}
	if err := decodeOptionalString(fields, "cursor", &request.Cursor); err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidSkillInput)
	}

	page, err := t.executor.Read(ctx, request)
	if err != nil || page == nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
	}
	output, err := json.Marshal(larkSkillReadOutput{
		OK:           true,
		Skill:        page.Skill,
		Path:         page.Path,
		HostedPolicy: larkHostedExecutionPolicy,
		Content:      page.Content,
		References:   append([]string(nil), page.References...),
		Cursor:       page.Cursor,
		Receipt:      page.Receipt,
		CLIVersion:   feishu.LarkCLIVersion,
	})
	if err != nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
	}
	return ToolResult(output), nil
}

type larkWorkspaceErrorCode uint8

const (
	larkWorkspaceErrorUnavailable larkWorkspaceErrorCode = iota
	larkWorkspaceErrorInvalidSkillInput
	larkWorkspaceErrorSkillRead
	larkWorkspaceErrorInvalidExecuteInput
	larkWorkspaceErrorIdentity
	larkWorkspaceErrorExecuteRejected
	larkWorkspaceErrorExecute
	larkWorkspaceErrorInvalidResult
	larkWorkspaceErrorInvalidWait
)

// larkWorkspaceSoftError returns only fixed, reviewed messages. It deliberately
// accepts an enum rather than arbitrary text so argv, paths, receipts, provider
// errors, and secrets cannot be interpolated into model-visible output.
func larkWorkspaceSoftError(code larkWorkspaceErrorCode) (ToolResult, error) {
	message := "飞书工作区操作当前不可用，请稍后重试。"
	switch code {
	case larkWorkspaceErrorInvalidSkillInput:
		message = "飞书技能参数无效，请仅使用 skill、reference、cursor。"
	case larkWorkspaceErrorSkillRead:
		message = "读取飞书技能暂时失败，请稍后重试。"
	case larkWorkspaceErrorInvalidExecuteInput:
		message = "飞书工作区参数无效，请仅使用 argv、stdin_json、skill_receipts。"
	case larkWorkspaceErrorIdentity:
		message = "无法验证当前飞书工作区操作身份。"
	case larkWorkspaceErrorExecuteRejected:
		message = "飞书命令或技能凭证无效，本次操作未执行。仅可直接执行 Docs/Base/Wiki 业务命令，并同时使用当前 lark-shared 与对应业务技能的 receipt；最多修正并重试一次。不要执行 auth/config/whoami，也不要要求用户提供 App ID/App Secret。"
	case larkWorkspaceErrorExecute:
		message = "飞书工作区操作暂时不可用，本次未执行。请停止重复调用，也不要改跑 auth/config/whoami 或要求用户提供 App ID/App Secret。"
	case larkWorkspaceErrorInvalidResult:
		message = "飞书工作区操作返回无效，请稍后重试。"
	case larkWorkspaceErrorInvalidWait:
		message = "飞书工作区等待状态无效，请稍后重试。"
	}
	output, _ := json.Marshal(map[string]string{"error": "ERROR: " + message})
	return ToolResult(output), nil
}

func decodeStrictLarkToolObject(input ToolInput, allowedNames ...string) (map[string]json.RawMessage, error) {
	allowed := make(map[string]struct{}, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = struct{}{}
	}

	decoder := json.NewDecoder(bytes.NewReader(input))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid object")
	}
	fields := make(map[string]json.RawMessage, len(allowedNames))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid object key")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("invalid object key")
		}
		if _, ok := allowed[key]; !ok {
			return nil, errors.New("unknown field")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("duplicate field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, errors.New("invalid field value")
		}
		fields[key] = append(json.RawMessage(nil), raw...)
	}
	endToken, err := decoder.Token()
	if err != nil || endToken != json.Delim('}') {
		return nil, errors.New("invalid object end")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON")
	}
	return fields, nil
}

func decodeRequiredNonEmptyString(fields map[string]json.RawMessage, name string, target *string) error {
	raw, ok := fields[name]
	if !ok || json.Unmarshal(raw, target) != nil || strings.TrimSpace(*target) == "" {
		return errors.New("required string missing")
	}
	return nil
}

func decodeOptionalString(fields map[string]json.RawMessage, name string, target *string) error {
	raw, ok := fields[name]
	if !ok {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '"' {
		return errors.New("invalid string")
	}
	if err := json.Unmarshal(trimmed, target); err != nil {
		return errors.New("invalid string")
	}
	return nil
}
