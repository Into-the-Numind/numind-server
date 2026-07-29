package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

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
	return "Execute controlled lark-cli argv for Docs/Base/Wiki/Drive. " +
		"argv may copy the official skill command verbatim with a leading `lark-cli`, or omit that one executable token. " +
		"JSON bodies must be one complete inline argv value after --json; stdin and file indirection are not supported. " +
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
			"argv":{"type":"array","minItems":1,"items":{"type":"string"},"description":"Controlled lark-cli argv; a single leading literal lark-cli from official skill examples is optional; JSON bodies must be one complete inline value after --json; no shell, stdin, or file indirection."}
		},
		"required":["argv"],
		"additionalProperties":false
	}`)
}

type larkExecuteInput struct {
	Argv      []string
	StdinJSON json.RawMessage
}

var errLarkExecuteStdinUnsupported = errors.New("stdin_json unsupported")

func (t *larkExecuteTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	if t == nil || t.executor == nil || ctx == nil {
		return larkWorkspaceSoftError(larkWorkspaceErrorUnavailable)
	}
	userID, ok := middleware.UserIDFromCtx(ctx)
	runID := RunIDFromContext(ctx)
	toolCallID := ToolCallIDFromContext(ctx)
	if !ok || userID == 0 || runID == 0 || strings.TrimSpace(toolCallID) == "" {
		return larkWorkspaceSoftError(larkWorkspaceErrorIdentity)
	}
	decoded, decodeErr := decodeLarkExecuteInput(input)
	var normalizedCommand *feishu.NormalizedCommand
	var normalizeErr error
	if decodeErr == nil {
		normalizedCommand, normalizeErr = feishu.NewCommandCatalog().Normalize(decoded.Argv, nil)
	}
	commandClass := "invalid"
	if decodeErr == nil {
		commandClass = feishu.SafeCommandClass(decoded.Argv)
	}
	span := startSafePipelineToolSpan(ctx, "tool.lark_execute.execute", map[string]any{
		"run_id":        runID,
		"command_class": commandClass,
	})
	spanOutput := map[string]any{
		"attempt":       0,
		"max_attempts":  larkExecuteMaxCorrectableAttempts,
		"feishu_called": false,
	}
	spanErrorClass := "pre_execution_error"
	defer func() { span.End(spanOutput, spanErrorClass) }()

	writeFenceKey := larkExecuteWriteFenceKey(normalizedCommand)
	retryState, retryAttempt, blockedReason, allowed := larkExecuteRetryBegin(runID, writeFenceKey)
	attemptsBeforeCall, _ := larkExecuteRetryProgress(retryState)
	spanOutput["attempt"] = attemptsBeforeCall
	if allowed && attemptsBeforeCall < larkExecuteMaxCorrectableAttempts {
		spanOutput["attempt"] = attemptsBeforeCall + 1
	}
	if !allowed {
		switch blockedReason {
		case larkRetryBlockedTerminal:
			spanErrorClass = "execution_stopped"
			return larkWorkspaceSoftError(larkWorkspaceErrorExecuteStopped)
		case larkRetryBlockedInFlight:
			spanErrorClass = "command_in_flight"
			return larkWorkspaceSoftError(larkWorkspaceErrorExecuteInFlight)
		default:
			spanErrorClass = "correction_exhausted"
			return larkWorkspaceSoftError(larkWorkspaceErrorExecuteRetryExhausted)
		}
	}
	if decodeErr != nil {
		code := "invalid_execute_input"
		message := "飞书工作区参数无效；lark_execute 只接受有效的 argv。"
		hint := "lark_execute 只接受有效的 argv"
		if errors.Is(decodeErr, errLarkExecuteStdinUnsupported) {
			code = "unsupported_stdin_json"
			message = "有数托管环境不支持 stdin_json；请把完整 JSON 作为 --json 后的一个内联 argv。"
			hint = "有数托管环境不支持 stdin_json，请使用完整内联 --json"
		}
		exhausted := larkExecuteRetryRejected(retryState, retryAttempt)
		attempts, remaining := larkExecuteRetryProgress(retryState)
		spanOutput["attempt"] = attempts
		spanErrorClass = code
		if exhausted {
			spanErrorClass = "correction_exhausted"
			return larkWorkspaceCorrectionExhausted(hint)
		}
		return larkWorkspaceCorrectableCommandError(
			code,
			message,
			attempts,
			remaining,
		)
	}
	if hint, code, ok := larkExecuteLocalCatalogRejectionHint(decoded.Argv, normalizeErr); ok {
		exhausted := larkExecuteRetryRejected(retryState, retryAttempt)
		attempts, remaining := larkExecuteRetryProgress(retryState)
		spanOutput["attempt"] = attempts
		spanErrorClass = code
		if exhausted {
			spanErrorClass = "correction_exhausted"
			return larkWorkspaceCorrectionExhausted(hint)
		}
		message := "飞书命令参数校验失败：" + hint + "。本次尚未访问飞书，请按该原因修正命令。"
		if code == "command_rejected" {
			message = "飞书业务命令不在托管目录内：" + hint + " 请使用技能列出的 Docs/Base/Wiki/Drive 业务命令。本次尚未访问飞书，也不代表连接异常。"
		}
		return larkWorkspaceCorrectableCommandError(
			code,
			message,
			attempts,
			remaining,
		)
	}
	if larkExecuteTopicGuardBlocks(runID, normalizedCommand) {
		exhausted := larkExecuteRetryRejected(retryState, retryAttempt)
		attempts, remaining := larkExecuteRetryProgress(retryState)
		spanOutput["attempt"] = attempts
		spanErrorClass = "topic_customer_mismatch"
		if exhausted {
			spanErrorClass = "correction_exhausted"
			return larkWorkspaceCorrectionExhausted("选题规划目标文档与已读取客户画像不一致")
		}
		return larkWorkspaceCorrectableCommandError(
			"topic_customer_mismatch",
			"选题规划目标文档与已读取客户画像不一致，本次尚未访问飞书。请重新读取目标选题文档，确认客户名称一致；若不一致，请询问用户提供正确链接或创建客户专属文档。",
			attempts,
			remaining,
		)
	}

	result, err := t.executor.Execute(ctx, feishu.ExecuteRequest{
		UserID:         userID,
		AgentRunID:     runID,
		ToolCallID:     toolCallID,
		IdempotencyKey: fmt.Sprintf("%d:%s", runID, toolCallID),
		Argv:           append([]string(nil), decoded.Argv...),
		StdinJSON:      nil,
	})
	if err != nil {
		if errors.Is(err, feishu.ErrOperationRequestRejected) {
			hint, hasHint := feishu.SafeOperationRequestValidation(err)
			exhausted := larkExecuteRetryRejected(retryState, retryAttempt)
			attempts, remaining := larkExecuteRetryProgress(retryState)
			spanOutput["attempt"] = attempts
			spanErrorClass = "command_rejected"
			if exhausted {
				spanErrorClass = "correction_exhausted"
				return larkWorkspaceCorrectionExhausted(hint)
			}
			if hasHint {
				spanErrorClass = "command_validation"
				return larkWorkspaceCorrectableCommandError(
					"command_validation",
					"飞书命令参数校验失败："+hint+"。本次尚未访问飞书，请按该原因修正命令。",
					attempts,
					remaining,
				)
			}
			return larkWorkspaceCorrectableCommandError(
				"command_rejected",
				"飞书业务命令不符合平台策略，本次尚未访问飞书，也不代表连接异常。请按技能说明修正 Docs/Base/Wiki/Drive 命令；不要执行 auth/config/whoami，也不要要求用户提供 App ID/App Secret。",
				attempts,
				remaining,
			)
		}
		spanOutput["feishu_called"] = "unknown"
		spanErrorClass = "operation_error"
		larkExecuteRetryFailed(retryState, retryAttempt)
		return larkWorkspaceSoftError(larkWorkspaceErrorExecute)
	}
	if result == nil || strings.TrimSpace(result.OperationID) == "" || strings.TrimSpace(result.State) == "" {
		spanOutput["feishu_called"] = "unknown"
		spanErrorClass = "invalid_result"
		larkExecuteRetryFailed(retryState, retryAttempt)
		return larkWorkspaceSoftError(larkWorkspaceErrorExecute)
	}

	if isLarkWaitingState(result.State) {
		spanOutput["feishu_called"] = false
		spanErrorClass = pipelineToolTraceNoError
		waitingResult, waitingErr := larkWaitingYield(result, toolCallID)
		if waitingErr != nil {
			spanErrorClass = "invalid_wait"
			larkExecuteRetryCompleted(retryState, retryAttempt)
		} else {
			larkExecuteRetryFailed(retryState, retryAttempt)
		}
		return waitingResult, waitingErr
	}
	if !isLarkTerminalState(result.State) {
		spanOutput["feishu_called"] = "unknown"
		spanErrorClass = "invalid_result"
		larkExecuteRetryFailed(retryState, retryAttempt)
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidResult)
	}
	if result.State == model.FeishuOperationSucceeded {
		spanOutput["feishu_called"] = normalizedCommand != nil && !normalizedCommand.LocalOnly
		spanErrorClass = pipelineToolTraceNoError
		larkExecuteTopicGuardObserve(runID, normalizedCommand, result.Data)
	} else if result.Failure != nil {
		spanOutput["feishu_called"] = result.Failure.BusinessStarted
		spanErrorClass = larkExecuteTerminalTraceErrorClass(result.Failure.Category)
	} else {
		spanOutput["feishu_called"] = "unknown"
		spanErrorClass = "terminal_failure"
	}
	// The operation layer intentionally knows nothing about Agent-run replay
	// state. Attach only the opaque exact-command digest at this final trusted
	// boundary, without mutating a shared executor result.
	if result.State == model.FeishuOperationUnknown && result.Failure != nil && writeFenceKey != "" {
		resultClone := *result
		failureClone := *result.Failure
		failureClone.RequiredScopes = append([]string(nil), result.Failure.RequiredScopes...)
		failureClone.WriteFenceKey = writeFenceKey
		resultClone.Failure = &failureClone
		result = &resultClone
	}
	output, err := feishu.MarshalLarkToolResult(result)
	if err != nil {
		spanErrorClass = "invalid_result"
		larkExecuteRetryFailed(retryState, retryAttempt)
		return larkWorkspaceSoftError(larkWorkspaceErrorInvalidResult)
	}
	if result.State == model.FeishuOperationSucceeded {
		larkExecuteRetryCompleted(retryState, retryAttempt)
	} else if larkExecuteRetryTerminalOutcome(retryState, retryAttempt, result.Failure) {
		spanErrorClass = "correction_exhausted"
		return larkWorkspaceSoftError(larkWorkspaceErrorExecuteRetryExhausted)
	}
	return ToolResult(output), nil
}

func larkExecuteTerminalTraceErrorClass(category string) string {
	switch category {
	case "connection_required",
		"scope_required",
		"reauth_required",
		"validation",
		"not_found",
		"resource_denied",
		"rate_limited",
		"temporary",
		"unknown_result",
		"failed":
		return category
	default:
		return "terminal_failure"
	}
}

func larkExecuteLocalCatalogRejectionHint(argv []string, err error) (string, string, bool) {
	if err == nil {
		return "", "", false
	}
	hint, ok := feishu.SafeCommandValidationHint(argv, err)
	if !ok {
		return "", "", false
	}
	if errors.Is(err, feishu.ErrCommandDenied) {
		return hint, "command_rejected", true
	}
	if errors.Is(err, feishu.ErrCommandInvalidArgument) {
		return hint, "command_validation", true
	}
	return "", "", false
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
	// Rolling-upgrade compatibility only: old conversations may still include
	// skill_receipts. The value is deliberately not parsed or copied. Receipts are
	// not user identity, Feishu authorization, command policy, scope evidence, or
	// idempotency proof; asking an LLM to reproduce them made valid commands fail.
	// Every security decision remains server-owned below the tool boundary.
	if stdinRaw, ok := fields["stdin_json"]; ok && !bytes.Equal(bytes.TrimSpace(stdinRaw), []byte("null")) {
		return larkExecuteInput{}, errLarkExecuteStdinUnsupported
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

const (
	larkTopicGuardProfileBegin = "[有数AI受管区：客户画像｜契约 profile/v1｜开始]"
	larkTopicGuardTopicsPrefix = "[有数AI受管文档：选题规划｜契约 topics"
)

var (
	larkTopicGuardRuns          sync.Map // map[uint64]*larkTopicGuardState
	larkTopicGuardPlanningTitle = regexp.MustCompile(`(?m)^#\s+(.+?)\s+选题规划\s*$`)
)

type larkTopicGuardState struct {
	mu              sync.Mutex
	profileCustomer string
	topicsDocuments map[string]struct{}
	topicsCustomers map[string]string
}

func larkExecuteTopicGuardObserve(runID uint64, command *feishu.NormalizedCommand, data json.RawMessage) {
	if runID == 0 || command == nil || command.Path != "docs +fetch" || len(data) == 0 || !json.Valid(data) {
		return
	}
	docArg := larkTopicGuardArgValue(command.Argv, "--doc")
	docID, content := larkTopicGuardFetchDocument(data)
	if content == "" {
		return
	}
	state := larkTopicGuardStateForRun(runID)
	if customer := larkTopicGuardProfileCustomer(content); customer != "" {
		state.mu.Lock()
		state.profileCustomer = customer
		state.mu.Unlock()
	}
	if strings.Contains(content, larkTopicGuardTopicsPrefix) {
		state.mu.Lock()
		larkTopicGuardRecordTopicDocument(state, docArg, docID)
		customer := larkTopicGuardTopicsCustomer(content)
		if docArg != "" {
			larkTopicGuardRecordTopicCustomer(state, docArg, customer)
		}
		if docID != "" {
			larkTopicGuardRecordTopicCustomer(state, docID, customer)
		}
		state.mu.Unlock()
	}
}

func larkExecuteTopicGuardBlocks(runID uint64, command *feishu.NormalizedCommand) bool {
	if runID == 0 || command == nil || command.Path != "docs +update" {
		return false
	}
	updateCommand := larkTopicGuardArgValue(command.Argv, "--command")
	if updateCommand != "append" && updateCommand != "str_replace" &&
		updateCommand != "block_replace" && updateCommand != "overwrite" {
		return false
	}
	content := larkTopicGuardArgValue(command.Argv, "--content")
	if !strings.Contains(content, larkTopicGuardTopicsPrefix) && !strings.Contains(content, "[有数AI轮次：") {
		return false
	}
	value, ok := larkTopicGuardRuns.Load(runID)
	if !ok {
		return false
	}
	state := value.(*larkTopicGuardState)
	target := larkTopicGuardArgValue(command.Argv, "--doc")
	state.mu.Lock()
	defer state.mu.Unlock()
	profileCustomer := state.profileCustomer
	_, targetFetched := state.topicsDocuments[target]
	targetCustomer := state.topicsCustomers[target]
	if profileCustomer == "" || !targetFetched {
		return false
	}
	if targetCustomer == "" {
		return true
	}
	return profileCustomer != targetCustomer
}

func larkExecuteTopicGuardClearRun(runID uint64) {
	if runID != 0 {
		larkTopicGuardRuns.Delete(runID)
	}
}

func larkTopicGuardStateForRun(runID uint64) *larkTopicGuardState {
	value, _ := larkTopicGuardRuns.LoadOrStore(runID, &larkTopicGuardState{})
	return value.(*larkTopicGuardState)
}

// larkTopicGuardRecordTopicDocument requires state.mu to be held.
func larkTopicGuardRecordTopicDocument(state *larkTopicGuardState, docArg, docID string) {
	if state.topicsDocuments == nil {
		state.topicsDocuments = make(map[string]struct{})
	}
	if docArg != "" {
		state.topicsDocuments[docArg] = struct{}{}
	}
	if docID != "" {
		state.topicsDocuments[docID] = struct{}{}
	}
}

// larkTopicGuardRecordTopicCustomer requires state.mu to be held.
func larkTopicGuardRecordTopicCustomer(state *larkTopicGuardState, doc string, customer string) {
	if doc == "" || customer == "" {
		return
	}
	if state.topicsCustomers == nil {
		state.topicsCustomers = make(map[string]string)
	}
	state.topicsCustomers[doc] = customer
}

func larkTopicGuardFetchDocument(data json.RawMessage) (docID, content string) {
	var nested struct {
		Document struct {
			DocumentID string `json:"document_id"`
			Content    string `json:"content"`
		} `json:"document"`
		DocumentID string `json:"document_id"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal(data, &nested); err != nil {
		return "", ""
	}
	docID = strings.TrimSpace(nested.Document.DocumentID)
	content = nested.Document.Content
	if docID == "" {
		docID = strings.TrimSpace(nested.DocumentID)
	}
	if content == "" {
		content = nested.Content
	}
	if !utf8.ValidString(content) {
		return "", ""
	}
	return docID, content
}

func larkTopicGuardProfileCustomer(content string) string {
	if !strings.Contains(content, larkTopicGuardProfileBegin) {
		return ""
	}
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "客户名称") && !strings.HasPrefix(strings.TrimSpace(line), "客户") {
			continue
		}
		if index := strings.Index(line, "："); index >= 0 {
			return larkTopicGuardNormalizeCustomer(line[index+len("："):])
		}
		if index := strings.Index(line, ":"); index >= 0 {
			return larkTopicGuardNormalizeCustomer(line[index+1:])
		}
	}
	return ""
}

func larkTopicGuardTopicsCustomer(content string) string {
	if !strings.Contains(content, larkTopicGuardTopicsPrefix) {
		return ""
	}
	match := larkTopicGuardPlanningTitle.FindStringSubmatch(content)
	if len(match) != 2 {
		return ""
	}
	return larkTopicGuardNormalizeCustomer(match[1])
}

func larkTopicGuardNormalizeCustomer(value string) string {
	value = strings.TrimSpace(strings.Trim(value, "*"))
	for _, separator := range []string{"（", "(", "，", ",", "：", ":"} {
		if index := strings.Index(value, separator); index >= 0 {
			value = value[:index]
		}
	}
	return strings.TrimSpace(strings.Trim(value, "*"))
}

func larkTopicGuardArgValue(argv []string, flag string) string {
	for index := 0; index < len(argv); index++ {
		if argv[index] == flag && index+1 < len(argv) {
			return strings.TrimSpace(argv[index+1])
		}
		prefix := flag + "="
		if strings.HasPrefix(argv[index], prefix) {
			return strings.TrimSpace(strings.TrimPrefix(argv[index], prefix))
		}
	}
	return ""
}
