package agent

// tool_feishu_connect.go — the feishu_connect agent tool (feishu-agent-connect
// R3-connect-tool). It models the 飞书 CONNECTION as an agent tool: the agent
// drives the conversation — it hands the user a link and the run resumes on its
// own — instead of a separate HTTP-driven settings flow.
//
// Orchestration (all secret-free at this boundary):
//
//	phase create_app  → run lark-cli, yield an AUTH pause carrying the device-code
//	                    page URL; the agent shows the user the link. The user
//	                    creates the app in a browser, then resumes the run.
//	phase authorize   → mint a signed OAuth state + the 飞书 authorize URL, yield
//	                    an AUTH pause carrying it; the user grants scopes, 飞书
//	                    redirects to our callback, which exchanges + stores the
//	                    token and resumes the run via the answer key.
//	phase done        → already connected; return a non-yield success result.
//
// On EVERY call the tool first polls+persists any finished app-create (the
// create_app → authorize bridge), so a re-call after the user finishes the
// device-code step advances to authorize without a separate trigger. The DB row
// is the durable phase source of truth — nothing is carried across the yield.
//
// Security (CLAUDE.md / .claude/rules/ai-service.md): the tool NEVER sees or
// returns appSecret / access_token / refresh_token — the connector returns only a
// phase + URL. The LLM therefore only ever sees a link + status, never a secret.
//
// Failure policy (design.md §8; CLAUDE.md "tool 硬错误杀 run" lesson): EVERY
// failure path returns a SOFT tool result (nil Go error). A non-nil Go error is
// reserved EXCLUSIVELY for the yield pause sentinel — a real failure must never
// kill the agent run.

import (
	"context"
	"encoding/json"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
)

// feishuConnectPromptText is the FIXED prompt text the auth-pause carries as its
// single question. It doubles as the answer KEY: the OAuth callback resumes the
// run via biz.Answer keyed by this exact text (state.question_text == this), and
// the create_app→authorize re-call after a manual resume answers the same key.
// It MUST stay stable — changing it would orphan in-flight paused runs.
const feishuConnectPromptText = "飞书连接链接已生成，请在浏览器中完成后回到这里继续"

// feishuConnector is the narrow seam onto biz/feishu's ConnectOrchestrator. It is
// an interface (not the concrete *feishu.ConnectOrchestrator) so the tool is unit
// tested with a fake and so biz/agent does not hard-depend on the orchestrator's
// construction. It returns ONLY non-sensitive info (phase + URL).
type feishuConnector interface {
	// PollAndPersistApp checks whether a pending device-code app-create finished
	// and, if so, persists the app creds (no token). Idempotent; (false,nil) when
	// not ready.
	PollAndPersistApp(ctx context.Context, userID uint) (persisted bool, err error)
	// NextConnectStep returns the next non-sensitive step (phase + URL). runID +
	// questionText are signed into the OAuth state so the callback resumes the run.
	NextConnectStep(ctx context.Context, userID uint, runID uint64, questionText string) (*feishu.ConnectStep, error)
}

// feishuConnectTool implements FullTool for feishu_connect.
type feishuConnectTool struct {
	BaseTool
	connector feishuConnector // nil → 飞书 integration off (soft error at Execute)
}

var _ FullTool = (*feishuConnectTool)(nil)

// Compile-time guard: the concrete biz/feishu orchestrator satisfies the seam, so
// the factory can pass a *feishu.ConnectOrchestrator straight in.
var _ feishuConnector = (*feishu.ConnectOrchestrator)(nil)

func (t *feishuConnectTool) Name() string { return "feishu_connect" }
func (t *feishuConnectTool) Description() string {
	return "Connect the current user's 飞书 (Lark) account so other 飞书 tools " +
		"(lark_create_doc / lark_send_message / lark_read_bitable) can act on their behalf. " +
		"Call this BEFORE using any 飞书 tool when the user is not yet connected, or when a " +
		"飞书 tool reports 尚未连接/授权已过期. Takes no arguments. " +
		"It returns a link for the user to open; the run pauses until they finish, then resumes " +
		"automatically. If already connected it returns immediately. " +
		"You never see or handle any credential — only a link and the connection status."
}
func (t *feishuConnectTool) UserFacingName() string { return "连接飞书" }
func (t *feishuConnectTool) NarrationVerb() string  { return "连接飞书" }

// Connecting writes credentials → not read-only / not concurrency-safe.
func (t *feishuConnectTool) IsReadOnly() bool                   { return false }
func (t *feishuConnectTool) IsConcurrencySafe(_ ToolInput) bool { return false }

// AlwaysLoad keeps feishu_connect available in the agent's full-open loop (like
// ask_user_question) so the model can always offer to connect 飞书.
func (t *feishuConnectTool) AlwaysLoad() bool { return true }

func (t *feishuConnectTool) InputSchema() json.RawMessage {
	// No parameters: the tool acts on the run initiator (from ctx). An empty
	// object schema is the OpenAI-function-calling way to say "no args".
	return json.RawMessage(`{"type": "object", "properties": {}}`)
}

// feishuConnectOutput is the success (done) result the LLM reads. It carries only
// non-sensitive status — never a secret/token. Soft failures use larkSoftError's
// {"error":...} shape instead (a Go-error-free recoverable result), so this
// struct has no error field.
type feishuConnectOutput struct {
	Status  string `json:"status"`  // "connected"
	Message string `json:"message"` // human-readable, secret-free
}

// Execute drives the connect flow. On a create_app/authorize step it returns a
// *yieldError (PAUSE — the only non-nil Go error path). On the done step it
// returns a success ToolResult. On ANY failure it returns a SOFT error (a
// ToolResult with a nil Go error) so a misconfig / outage never kills the run.
func (t *feishuConnectTool) Execute(ctx context.Context, _ ToolInput) (ToolResult, error) {
	const label = "连接飞书"

	if t.connector == nil {
		return larkSoftError("飞书集成未启用，无法%s。", label)
	}
	userID, _ := middleware.UserIDFromCtx(ctx)
	if userID == 0 {
		return larkSoftError("无法识别当前用户身份，无法%s。", label)
	}
	// runID lets the OAuth callback resume THIS run (signed into the state). It may
	// be 0 in degenerate contexts; the callback simply won't have a run to resume
	// then, but the connection still completes — so we do not hard-require it.
	runID := RunIDFromContext(ctx)

	endSpan := larkStartSpan(ctx, "connect", userID, map[string]any{"run_id": runID})

	// 1. Bridge create_app → authorize: persist any just-finished device-code app
	// (idempotent; a no-op when nothing is pending). A failure here is soft.
	if _, err := t.connector.PollAndPersistApp(ctx, userID); err != nil {
		endSpan(map[string]any{"outcome": "poll_error"}, err.Error())
		return larkSoftError("%s时出错：%s。可稍后重试。", label, err.Error())
	}

	// 2. Decide the next step from the (now up-to-date) DB row.
	step, err := t.connector.NextConnectStep(ctx, userID, runID, feishuConnectPromptText)
	if err != nil {
		endSpan(map[string]any{"outcome": "step_error"}, err.Error())
		return larkSoftError("%s时出错：%s。可稍后重试。", label, err.Error())
	}

	switch step.Phase {
	case feishu.ConnectPhaseDone:
		endSpan(map[string]any{"outcome": "done"}, "")
		out, _ := json.Marshal(feishuConnectOutput{
			Status:  "connected",
			Message: "已连接飞书，可以直接使用飞书相关工具了。",
		})
		return ToolResult(out), nil

	case feishu.ConnectPhaseCreateApp, feishu.ConnectPhaseAuthorize:
		if step.URL == "" {
			// Defensive: a create_app/authorize step with no URL is unusable — never
			// yield an empty auth card; soft-error so the model can retry.
			endSpan(map[string]any{"outcome": "empty_url", "phase": step.Phase}, "empty step URL")
			return larkSoftError("%s失败：未能生成有效的连接链接，请稍后重试。", label)
		}
		endSpan(map[string]any{"outcome": "yield", "phase": step.Phase}, "")
		// YIELD: pause the run on an AUTH-class card carrying the link + the fixed
		// resume-key question. The runner persists this; the callback (authorize) or
		// a manual resume (create_app) answers the same key to continue.
		return nil, &yieldError{Payload: YieldPayload{
			PauseType: PauseTypeAuth,
			AuthURL:   step.URL,
			Questions: []YieldQuestion{{Question: feishuConnectPromptText}},
		}}

	default:
		// Unknown phase (forward-compat guard) — soft-error, never crash the run.
		endSpan(map[string]any{"outcome": "unknown_phase", "phase": step.Phase}, "unknown phase")
		return larkSoftError("%s失败：未知的连接状态 %q。", label, step.Phase)
	}
}
