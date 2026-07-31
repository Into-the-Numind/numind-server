package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

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
		`Minimum valid examples, not the full Lark guide: {"skill":"lark-doc"} or {"skill":"lark-base","reference":"lark-base-record-batch-create.md"}. ` +
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

var larkHostedExecutionPolicy = "有数托管规则（优先于下方针对本地电脑的 CLI 说明）：" +
	"不要执行 auth/config/whoami/qrcode，也不要要求用户提供 App ID/App Secret。" +
	"先执行 Docs/Base/Wiki/Drive 业务命令，不要每次先检查权限；写操作由平台在真正写入前自动做只读 scope check，连接或权限不足时平台会生成授权卡片并恢复原任务。" +
	"用户明确要求连接、重新连接或授权飞书且没有业务任务时，必须立即调用 lark_connect；不要只检查状态后让用户再描述任务。" +
	"只有用户明确询问连接状态，或 lark_execute 已返回结构化失败时才调用 lark_inspect；lark_inspect 是独立工具，不是 lark_execute 里的 drive +inspect/wiki +inspect/docs +inspect 命令。" +
	"用户只提供资源标题而没有 URL/token 时，先读取 lark-drive，再执行 drive +search --query <标题> --only-title --doc-types docx,wiki,bitable；" +
	"如果结果 has_more=true，必须保持相同 query/only-title/doc-types/page-size，用 page_token 继续搜索，按 URL/token 去重，最多 5 页或 100 条；只有穷尽分页后才能判断唯一、多个或没有精确匹配。达到上限仍有更多结果时，不得宣称唯一或未找到，应让用户缩小标题范围或提供链接。" +
	"精确匹配优先使用结果的原始 title；若只能使用 title_highlighted，先剥离 <h>/<hb> 高亮标签再比较。唯一精确匹配时按结果类型和 URL 路由到 Docs/Base/Wiki；" +
	"结果 URL 是 /wiki/ 时，先用 shared+wiki 执行 wiki +node-get --node-token <URL>，再按 obj_type/obj_token 路由：仅 docx 可换 shared+doc 后 docs fetch、bitable 可换 shared+base 后 base 读取；其余 obj_type（doc、sheet、mindnote、slides、file）本期不支持。" +
	"非 wiki 结果随后读取目标业务技能：docx 用 lark-doc，bitable 用 lark-base。技能读取只提供命令说明；身份、权限与恢复由平台负责。" +
	"多个精确匹配时列出候选让用户选择，每个编号候选必须保留标题、类型和完整原始 URL，不得只展示 token 后缀；用户随后按编号选择时必须使用对话中保留的完整 URL。如果当前上下文只剩 token 后缀，必须重新执行相同的受限搜索恢复完整 URL，禁止猜测或拼接地址。" +
	"没有精确匹配时说明未找到并请求链接，不要猜测 token，也不要说成连接未就绪。" +
	"lark_execute 必须串行：一次只调用一个并等待结构化结果后再决定下一步，禁止在同一轮并发调用或在前一个结果返回前重复同一命令。" +
	"官方命令中的固定 --as user 与 --format json 可以保留，平台会安全规范化。" +
	"有数托管环境的 JSON 必须作为对应 --json 参数后的一个完整内联 argv；不支持 `stdin_json`、`@file`、`-` 或本地文件/stdin 间接引用。官方技能同时展示内联与 @file 时，只采用内联示例。" +
	fmt.Sprintf("policy_rejected 或 validation 每个连续纠错窗口最多允许 %d 次总尝试；每次必须根据结构化原因修正业务命令，不能原样重复；not_found 或 resource_denied 应向用户确认资源，不要自动重试；unknown_result 只禁止原样重复那一条结果不确定的写命令。应优先使用 Catalog 只读 list/get/fetch 命令核验；一次核验失败可改用其他合规只读方式，且不得阻止用户要求中的其他 Docs/Base/Wiki/Drive 操作。未核验成功时必须如实说明，不能宣称写入成功。", larkExecuteMaxCorrectableAttempts) +
	"rate_limited/temporary 仅在结构化结果 retryable=true 时最多重试一次；不要改跑本地初始化命令。"

func (t *larkSkillReadTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	if t == nil || t.executor == nil || ctx == nil || RunIDFromContext(ctx) == 0 {
		return larkWorkspaceSoftError(larkWorkspaceErrorUnavailable)
	}

	runID := RunIDFromContext(ctx)
	request, err := decodeLarkSkillReadInput(input, runID)
	if err != nil {
		code, message, hint, fix := larkSkillReadInputProtocolSpec(err)
		recordLarkSkillReadInvalidInputSpan(ctx, runID, code)
		return larkWorkspaceToolInputProtocolError(runID, t.Name(), code, message, hint, fix)
	}
	larkToolInputProtocolClearRun(runID)
	var span *safePipelineToolSpan
	spanOutput := map[string]any{
		"ok":            false,
		"resolved_path": "",
		"page_count":    0,
	}
	spanErrorClass := "skill_read_error"
	defer func() { span.End(spanOutput, spanErrorClass) }()
	startSpan := func(skill, requestedReference string) {
		if span != nil {
			return
		}
		span = startSafePipelineToolSpan(ctx, "tool.lark_skill_read.execute", map[string]any{
			"run_id":              request.AgentRunID,
			"skill":               skill,
			"requested_reference": requestedReference,
		})
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
			startSpan("invalid", "invalid")
			if pageIndex == 0 && errors.Is(readErr, feishu.ErrSkillReadInvalid) {
				spanErrorClass = "invalid_skill_input"
				return larkWorkspaceSoftError(larkWorkspaceErrorInvalidSkillInput)
			}
			return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
		}
		spanOutput["page_count"] = pageIndex + 1
		if pageIndex == 0 {
			if page.Skill != request.Skill {
				startSpan("invalid", "invalid")
				spanErrorClass = "invalid_skill_result"
				return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
			}
			startSpan(page.Skill, request.Reference)
			resultSkill = page.Skill
			resultPath = page.Path
			resultReferences = append([]string(nil), page.References...)
			spanOutput["resolved_path"] = resultPath
		} else if page.Skill != resultSkill || page.Path != resultPath || !slices.Equal(page.References, resultReferences) {
			spanErrorClass = "invalid_skill_result"
			return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
		}

		_, _ = content.WriteString(page.Content)
		hostedPolicy := larkHostedExecutionPolicy + feishu.HostedCommandContract(resultSkill)
		output, marshalErr := json.Marshal(larkSkillReadOutput{
			OK:           true,
			Skill:        resultSkill,
			Path:         resultPath,
			HostedPolicy: hostedPolicy,
			Content:      content.String(),
			References:   append([]string(nil), resultReferences...),
			CLIVersion:   feishu.LarkCLIVersion,
		})
		if marshalErr != nil || len(output) > larkSkillReadAtomicOutputLimit {
			spanErrorClass = "invalid_skill_result"
			return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
		}
		if page.Cursor == "" {
			spanOutput["ok"] = true
			spanErrorClass = pipelineToolTraceNoError
			return ToolResult(output), nil
		}
		if _, duplicate := seenCursors[page.Cursor]; duplicate || pageIndex+1 >= larkSkillReadMaxPages {
			spanErrorClass = "invalid_skill_pagination"
			return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
		}
		seenCursors[page.Cursor] = struct{}{}
		request.Cursor = page.Cursor
		if request.Reference == "" && page.Path != "SKILL.md" {
			request.Reference = page.Path
		}
	}
	spanErrorClass = "invalid_skill_pagination"
	return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
}

var (
	errLarkSkillReadMissingSkill = errors.New("skill missing")
	errLarkSkillReadInvalidSkill = errors.New("skill invalid")
)

func decodeLarkSkillReadInput(input ToolInput, runID uint64) (feishu.SkillReadRequest, error) {
	fields, err := decodeStrictLarkToolObject(input, "skill", "reference", "cursor")
	if err != nil {
		return feishu.SkillReadRequest{}, errLarkSkillReadInvalidSkill
	}
	var request feishu.SkillReadRequest
	request.AgentRunID = runID
	if _, ok := fields["skill"]; !ok {
		return feishu.SkillReadRequest{}, errLarkSkillReadMissingSkill
	}
	if err := decodeRequiredNonEmptyString(fields, "skill", &request.Skill); err != nil {
		return feishu.SkillReadRequest{}, errLarkSkillReadInvalidSkill
	}
	if !slices.Contains([]string{"lark-shared", "lark-doc", "lark-base", "lark-wiki", "lark-drive"}, request.Skill) {
		return feishu.SkillReadRequest{}, errLarkSkillReadInvalidSkill
	}
	if err := decodeOptionalString(fields, "reference", &request.Reference); err != nil {
		return feishu.SkillReadRequest{}, errLarkSkillReadInvalidSkill
	}
	if err := decodeOptionalString(fields, "cursor", &request.Cursor); err != nil {
		return feishu.SkillReadRequest{}, errLarkSkillReadInvalidSkill
	}
	return request, nil
}

func larkSkillReadInputProtocolSpec(err error) (code, message, hint, fix string) {
	fix = "lark_skill_read 的 skill/reference 工具参数"
	if errors.Is(err, errLarkSkillReadMissingSkill) {
		return "missing_skill",
			"lark_skill_read 缺少 skill；失败发生在大模型到后端的工具参数层，不是飞书端失败。下一次必须传 skill，例如 {\"skill\":\"lark-doc\"}。可选 skill：lark-shared、lark-doc、lark-base、lark-wiki、lark-drive。",
			"lark_skill_read 缺少 skill",
			fix
	}
	return "invalid_skill_input",
		"lark_skill_read 参数无效；失败发生在大模型到后端的工具参数层，不是飞书端失败。必须传 skill，reference 必须来自该技能返回的 references。",
		"lark_skill_read 参数无效",
		fix
}

func recordLarkSkillReadInvalidInputSpan(ctx context.Context, runID uint64, errorClass string) {
	span := startSafePipelineToolSpan(ctx, "tool.lark_skill_read.execute", map[string]any{
		"run_id":              runID,
		"skill":               "invalid",
		"requested_reference": "invalid",
	})
	span.End(map[string]any{
		"ok":            false,
		"resolved_path": "",
		"page_count":    0,
	}, errorClass)
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
	larkWorkspaceErrorExecuteInFlight
	larkWorkspaceErrorConnectionInProgress
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
		message = "飞书工作区参数无效，请仅使用 argv。"
		publicCode, recoverable = "invalid_execute_input", true
	case larkWorkspaceErrorIdentity:
		message = "无法验证当前飞书工作区操作身份。"
		publicCode = "identity_unavailable"
	case larkWorkspaceErrorExecuteRejected:
		message = fmt.Sprintf("飞书业务命令不符合平台策略，本次尚未访问飞书，也不代表连接异常。请按技能说明修正 Docs/Base/Wiki/Drive 命令；最多允许 %d 次总尝试。不要执行 auth/config/whoami，也不要要求用户提供 App ID/App Secret。", larkExecuteMaxCorrectableAttempts)
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
		message = "这条完全相同的飞书写命令此前返回 unknown_result，因此不再原样重复，以免重复写入。请用 Catalog 只读 list/get/fetch 命令核验；一次读取失败可换另一种合规只读方式。其他不同的飞书操作仍可继续。"
		publicCode = "execution_stopped"
	case larkWorkspaceErrorExecuteInFlight:
		message = "已有一项飞书工作区操作正在执行。请等待当前工具结果返回后，再按顺序执行下一项 lark_execute；不要并行调用。"
		publicCode, recoverable, retryable = "command_in_flight", true, true
	case larkWorkspaceErrorConnectionInProgress:
		message = "飞书连接已在进行中。请继续当前页面或已有授权卡片，不要重复创建连接；完成后原任务会自动继续。"
		publicCode, recoverable = "connection_in_progress", true
	}
	output, _ := json.Marshal(map[string]any{
		"error":       "ERROR: " + message,
		"code":        publicCode,
		"recoverable": recoverable,
		"retryable":   retryable,
	})
	return ToolResult(output), nil
}

type larkToolInputProtocolState struct {
	mu       sync.Mutex
	failures map[string]uint8
}

var larkToolInputProtocolRuns sync.Map // map[uint64]*larkToolInputProtocolState

func larkToolInputProtocolClearRun(runID uint64) {
	if runID != 0 {
		larkToolInputProtocolRuns.Delete(runID)
	}
}

func larkToolInputProtocolRejected(runID uint64, toolName, code string) (sameErrorAttempts, remaining int, exhausted bool) {
	if runID == 0 {
		return larkExecuteMaxSameInputProtocolAttempts, 0, true
	}
	value, _ := larkToolInputProtocolRuns.LoadOrStore(runID, &larkToolInputProtocolState{})
	state := value.(*larkToolInputProtocolState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failures == nil {
		state.failures = make(map[string]uint8)
	}
	key := toolName + "\x00" + code
	state.failures[key]++
	sameErrorAttempts = int(state.failures[key])
	remaining = larkExecuteMaxSameInputProtocolAttempts - sameErrorAttempts
	if remaining < 0 {
		remaining = 0
	}
	return sameErrorAttempts, remaining, sameErrorAttempts >= larkExecuteMaxSameInputProtocolAttempts
}

func larkWorkspaceToolInputProtocolError(
	runID uint64,
	toolName, code, message, hint, fix string,
) (ToolResult, error) {
	if code == "" {
		code = "invalid_tool_input"
	}
	if message == "" {
		message = toolName + " 参数无效；失败发生在大模型到后端的工具参数层，不是飞书端失败。"
	}
	sameErrorAttempts, remaining, exhausted := larkToolInputProtocolRejected(runID, toolName, code)
	if exhausted {
		return larkWorkspaceToolInputProtocolExhausted(code, hint, fix, sameErrorAttempts, remaining)
	}
	output, _ := json.Marshal(map[string]any{
		"error":                   "ERROR: " + message,
		"code":                    code,
		"category":                "input_protocol",
		"layer":                   "model_to_backend",
		"stage":                   "pre_execution",
		"attempt":                 sameErrorAttempts,
		"max_attempts":            larkExecuteMaxSameInputProtocolAttempts,
		"same_error_attempt":      sameErrorAttempts,
		"same_error_max_attempts": larkExecuteMaxSameInputProtocolAttempts,
		"remaining_attempts":      remaining,
		"feishu_called":           false,
		"recoverable":             true,
		"retryable":               false,
	})
	return ToolResult(output), nil
}

func larkWorkspaceToolInputProtocolExhausted(
	code, hint, fix string,
	sameErrorAttempts, remaining int,
) (ToolResult, error) {
	message := "连续工具参数错误，已停止当前任务后续的飞书辅助工具调用。失败发生在大模型到后端的工具参数层，尚未调用飞书。"
	if code != "" {
		message += "错误类型：" + code + "。"
	}
	if hint != "" {
		message += "最后一次校验原因：" + hint + "。"
	}
	if fix == "" {
		fix = "当前飞书工具参数"
	}
	message += "不要猜测标题、内容长度、飞书权限、连接状态或限流；只修正 " + fix + "。"
	output, _ := json.Marshal(map[string]any{
		"error":                   "ERROR: " + message,
		"code":                    "input_protocol_exhausted",
		"category":                "input_protocol",
		"layer":                   "model_to_backend",
		"stage":                   "pre_execution",
		"attempt":                 sameErrorAttempts,
		"max_attempts":            larkExecuteMaxSameInputProtocolAttempts,
		"same_error_attempt":      sameErrorAttempts,
		"same_error_max_attempts": larkExecuteMaxSameInputProtocolAttempts,
		"remaining_attempts":      remaining,
		"feishu_called":           false,
		"recoverable":             false,
		"retryable":               false,
	})
	return ToolResult(output), nil
}

func larkWorkspaceCorrectableCommandError(code, message string, attempts, remaining int) (ToolResult, error) {
	if code == "" {
		code = "command_rejected"
	}
	if message == "" {
		message = "飞书业务命令不符合平台策略，请按当前技能说明修正命令。"
	}
	output, _ := json.Marshal(map[string]any{
		"error":              "ERROR: " + message,
		"code":               code,
		"category":           "validation",
		"stage":              "pre_execution",
		"attempt":            attempts,
		"max_attempts":       larkExecuteMaxCorrectableAttempts,
		"remaining_attempts": remaining,
		"feishu_called":      false,
		"recoverable":        true,
		"retryable":          false,
	})
	return ToolResult(output), nil
}

func larkWorkspaceCorrectionExhausted(hint string) (ToolResult, error) {
	message := fmt.Sprintf("飞书命令连续 %d 次未通过执行前校验，已停止当前任务后续的飞书命令。", larkExecuteMaxCorrectableAttempts)
	if hint != "" {
		message += "最后一次校验原因：" + hint + "。"
	}
	output, _ := json.Marshal(map[string]any{
		"error":              "ERROR: " + message,
		"code":               "correction_exhausted",
		"category":           "validation",
		"stage":              "pre_execution",
		"attempt":            larkExecuteMaxCorrectableAttempts,
		"max_attempts":       larkExecuteMaxCorrectableAttempts,
		"remaining_attempts": 0,
		"feishu_called":      false,
		"recoverable":        false,
		"retryable":          false,
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
