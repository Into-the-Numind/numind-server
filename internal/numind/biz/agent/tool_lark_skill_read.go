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
	return "Read only the five official embedded lark-cli skills (lark-shared, lark-doc, " +
		"lark-base, lark-wiki, lark-drive) through a controlled reference/cursor. No raw path, user identity, " +
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
			"skill":{"type":"string","enum":["lark-shared","lark-doc","lark-base","lark-wiki","lark-drive"]},
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
	"直接调用 lark_execute 执行 Docs/Base/Wiki/Drive 业务命令；连接或权限不足时，平台会自动生成授权卡片。" +
	"skill_receipts 必须同时包含当前 run 的 lark-shared receipt 和对应业务技能 receipt。" +
	"用户只提供资源标题而没有 URL/token 时，先读取 lark-drive，再执行 drive +search --query <标题> --only-title --doc-types docx,wiki,bitable；" +
	"如果结果 has_more=true，必须保持相同 query/only-title/doc-types/page-size，用 page_token 继续搜索，按 URL/token 去重，最多 5 页或 100 条；只有穷尽分页后才能判断唯一、多个或没有精确匹配。达到上限仍有更多结果时，不得宣称唯一或未找到，应让用户缩小标题范围或提供链接。" +
	"精确匹配优先使用结果的原始 title；若只能使用 title_highlighted，先剥离 <h>/<hb> 高亮标签再比较。唯一精确匹配时按结果类型和 URL 路由到 Docs/Base/Wiki；" +
	"结果 URL 是 /wiki/ 时，先用 shared+wiki 执行 wiki +node-get --node-token <URL>，再按 obj_type/obj_token 路由：仅 docx 可换 shared+doc 后 docs fetch、bitable 可换 shared+base 后 base 读取；其余 obj_type（doc、sheet、mindnote、slides、file）本期不支持。" +
	"非 wiki 结果随后读取目标业务技能并换用目标 exact receipts：docx 用 shared+doc，bitable 用 shared+base；Drive receipt 不得带入后续业务命令。" +
	"多个精确匹配时列出候选让用户选择；没有精确匹配时说明未找到并请求链接，不要猜测 token，也不要说成连接未就绪。" +
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
	larkWorkspaceErrorExecuteRetryExhausted
	larkWorkspaceErrorExecute
	larkWorkspaceErrorInvalidResult
	larkWorkspaceErrorInvalidWait
	larkWorkspaceErrorInvalidInspectInput
	larkWorkspaceErrorInspectRejected
	larkWorkspaceErrorInspect
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
		message = "飞书命令或技能凭证不符合平台策略，本次操作尚未访问飞书，也不代表连接异常。仅可直接执行 Docs/Base/Wiki/Drive 业务命令，并同时使用当前 lark-shared 与对应业务技能的 receipt；最多修正并重试一次。不要执行 auth/config/whoami，也不要要求用户提供 App ID/App Secret。"
	case larkWorkspaceErrorExecuteRetryExhausted:
		message = "飞书命令连续被拒绝，已停止后续飞书命令，本任务不会再调用执行器。不要继续重试、执行 auth/config/whoami，或要求用户提供 App ID/App Secret。请向用户说明本次操作未完成。"
	case larkWorkspaceErrorExecute:
		message = "飞书工作区操作暂时不可用，本次未执行。请停止重复调用，也不要改跑 auth/config/whoami 或要求用户提供 App ID/App Secret。"
	case larkWorkspaceErrorInvalidResult:
		message = "飞书工作区操作返回无效，请稍后重试。"
	case larkWorkspaceErrorInvalidWait:
		message = "飞书工作区等待状态无效，请稍后重试。"
	case larkWorkspaceErrorInvalidInspectInput:
		message = "飞书检查参数无效：connection 模式只使用 mode；command 模式必须提供 argv 和当前技能 receipt。"
	case larkWorkspaceErrorInspectRejected:
		message = "飞书检查命令或技能凭证不符合平台策略；只可检查 Docs/Base/Wiki/Drive 业务命令。"
	case larkWorkspaceErrorInspect:
		message = "暂时无法完成飞书工作区检查；不要改用 auth/config/whoami。"
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
