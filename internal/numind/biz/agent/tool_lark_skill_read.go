package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
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
	return "Read complete instructions from only the five official embedded lark-cli skills (lark-shared, " +
		"lark-doc, lark-base, lark-wiki, lark-drive). Select the skill and an optional controlled reference; " +
		"the platform handles pagination. No raw path, user identity, connection, or credential is accepted."
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
			"reference":{"type":"string","description":"Optional reference declared by the selected skill; a unique Markdown filename is accepted."}
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
	CLIVersion   string   `json:"cli_version"`
}

const larkSkillReadMaxPages = 2

const larkHostedExecutionPolicy = "有数托管规则（优先于下方针对本地电脑的 CLI 说明）：" +
	"不要执行 auth/config/whoami/qrcode，也不要要求用户提供 App ID/App Secret。" +
	"先执行 Docs/Base/Wiki/Drive 业务命令，不要每次先检查权限；写操作由平台在真正写入前自动做只读 scope check，连接或权限不足时平台会生成授权卡片并恢复原任务。" +
	"只有用户明确询问连接状态，或 lark_execute 已返回结构化失败时才调用 lark_inspect。" +
	"用户只提供资源标题而没有 URL/token 时，先读取 lark-drive，再执行 drive +search --query <标题> --only-title --doc-types docx,wiki,bitable；" +
	"如果结果 has_more=true，必须保持相同 query/only-title/doc-types/page-size，用 page_token 继续搜索，按 URL/token 去重，最多 5 页或 100 条；只有穷尽分页后才能判断唯一、多个或没有精确匹配。达到上限仍有更多结果时，不得宣称唯一或未找到，应让用户缩小标题范围或提供链接。" +
	"精确匹配优先使用结果的原始 title；若只能使用 title_highlighted，先剥离 <h>/<hb> 高亮标签再比较。唯一精确匹配时按结果类型和 URL 路由到 Docs/Base/Wiki；" +
	"结果 URL 是 /wiki/ 时，先用 shared+wiki 执行 wiki +node-get --node-token <URL>，再按 obj_type/obj_token 路由：仅 docx 可换 shared+doc 后 docs fetch、bitable 可换 shared+base 后 base 读取；其余 obj_type（doc、sheet、mindnote、slides、file）本期不支持。" +
	"非 wiki 结果随后读取目标业务技能：docx 用 lark-doc，bitable 用 lark-base。技能读取只提供命令说明；身份、权限与恢复由平台负责。" +
	"多个精确匹配时列出候选让用户选择；没有精确匹配时说明未找到并请求链接，不要猜测 token，也不要说成连接未就绪。" +
	"官方命令中的固定 --as user 与 --format json 可以保留，平台会安全规范化。" +
	"policy_rejected 或 validation 只可修正业务命令一次；not_found 或 resource_denied 应向用户确认资源，不要自动重试；unknown_result 必须立即停止，禁止换参数重复写入。" +
	"rate_limited/temporary 仅在结构化结果 retryable=true 时最多重试一次；不要改跑本地初始化命令。"

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

	var (
		resultSkill      string
		resultPath       string
		resultReferences []string
		content          strings.Builder
		seenCursors      = make(map[string]struct{}, larkSkillReadMaxPages)
	)
	for pageIndex := 0; pageIndex < larkSkillReadMaxPages; pageIndex++ {
		page, readErr := t.executor.Read(ctx, request)
		if readErr != nil || page == nil {
			if pageIndex == 0 && errors.Is(readErr, feishu.ErrSkillReadInvalid) {
				return larkWorkspaceSoftError(larkWorkspaceErrorInvalidSkillInput)
			}
			return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
		}
		if pageIndex == 0 {
			if page.Skill != request.Skill {
				return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
			}
			resultSkill = page.Skill
			resultPath = page.Path
			resultReferences = append([]string(nil), page.References...)
		} else if page.Skill != resultSkill || page.Path != resultPath || !slices.Equal(page.References, resultReferences) {
			return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
		}

		_, _ = content.WriteString(page.Content)
		output, marshalErr := json.Marshal(larkSkillReadOutput{
			OK:           true,
			Skill:        resultSkill,
			Path:         resultPath,
			HostedPolicy: larkHostedExecutionPolicy,
			Content:      content.String(),
			References:   append([]string(nil), resultReferences...),
			CLIVersion:   feishu.LarkCLIVersion,
		})
		if marshalErr != nil || len(output) > larkSkillReadAtomicOutputLimit {
			return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
		}
		if page.Cursor == "" {
			return ToolResult(output), nil
		}
		if _, duplicate := seenCursors[page.Cursor]; duplicate || pageIndex+1 >= larkSkillReadMaxPages {
			return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
		}
		seenCursors[page.Cursor] = struct{}{}
		request.Cursor = page.Cursor
		if request.Reference == "" && page.Path != "SKILL.md" {
			request.Reference = page.Path
		}
	}
	return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
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
	larkWorkspaceErrorExecuteStopped
)

// larkWorkspaceSoftError returns only fixed, reviewed messages. It deliberately
// accepts an enum rather than arbitrary text so argv, paths, receipts, provider
// errors, and secrets cannot be interpolated into model-visible output.
func larkWorkspaceSoftError(code larkWorkspaceErrorCode) (ToolResult, error) {
	message := "飞书工作区操作当前不可用，请稍后重试。"
	publicCode := "workspace_unavailable"
	recoverable := false
	retryable := false
	switch code {
	case larkWorkspaceErrorInvalidSkillInput:
		message = "飞书技能参数无效，请重新选择 skill 或当前技能声明的 reference。"
		publicCode, recoverable = "invalid_skill_input", true
	case larkWorkspaceErrorSkillRead:
		message = "读取飞书技能暂时失败，请稍后重试。"
		publicCode = "skill_read_unavailable"
	case larkWorkspaceErrorInvalidExecuteInput:
		message = "飞书工作区参数无效，请仅使用 argv、stdin_json。"
		publicCode, recoverable = "invalid_execute_input", true
	case larkWorkspaceErrorIdentity:
		message = "无法验证当前飞书工作区操作身份。"
		publicCode = "identity_unavailable"
	case larkWorkspaceErrorExecuteRejected:
		message = "飞书业务命令不符合平台策略，本次尚未访问飞书，也不代表连接异常。请按技能说明修正 Docs/Base/Wiki/Drive 命令；最多修正并重试一次。不要执行 auth/config/whoami，也不要要求用户提供 App ID/App Secret。"
		publicCode, recoverable = "command_rejected", true
	case larkWorkspaceErrorExecuteRetryExhausted:
		message = "飞书命令连续被拒绝，已停止后续飞书命令，本任务不会再调用执行器。不要继续重试、执行 auth/config/whoami，或要求用户提供 App ID/App Secret。请向用户说明本次操作未完成。"
		publicCode = "correction_exhausted"
	case larkWorkspaceErrorExecute:
		message = "飞书工作区操作暂时不可用，本次未执行。请停止重复调用，也不要改跑 auth/config/whoami 或要求用户提供 App ID/App Secret。"
		publicCode = "workspace_unavailable"
	case larkWorkspaceErrorInvalidResult:
		message = "飞书工作区操作返回无效，请稍后重试。"
		publicCode = "invalid_result"
	case larkWorkspaceErrorInvalidWait:
		message = "飞书工作区等待状态无效，请稍后重试。"
		publicCode = "invalid_wait"
	case larkWorkspaceErrorInvalidInspectInput:
		message = "飞书检查参数无效：connection 模式只使用 mode；command 模式必须提供 argv。"
		publicCode, recoverable = "invalid_inspect_input", true
	case larkWorkspaceErrorInspectRejected:
		message = "飞书检查命令不符合平台策略；只可检查 Docs/Base/Wiki/Drive 业务命令。"
		publicCode, recoverable = "inspect_rejected", true
	case larkWorkspaceErrorInspect:
		message = "暂时无法完成飞书工作区检查；不要改用 auth/config/whoami。"
		publicCode = "inspect_unavailable"
	case larkWorkspaceErrorExecuteStopped:
		message = "上一项飞书操作返回不可自动重试的结构化结果，本任务已停止后续飞书业务执行。不要换参数重复写入；请根据结果向用户说明或等待用户提供新信息。"
		publicCode = "execution_stopped"
	}
	output, _ := json.Marshal(map[string]any{
		"error":       "ERROR: " + message,
		"code":        publicCode,
		"recoverable": recoverable,
		"retryable":   retryable,
	})
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
