// v2 #2 agent-mode-v2-skill-invocation:
// use_skill platform tool — 让 Agent 的 LLM 主动调用已装载的 Skill。
//
// 设计权威：docs/superpowers/specs/2026-05-24-agent-mode-v2-skill-invocation-design.md §3.1 + §3.7
//
// T02 范围（本文件）：types + ctx helpers + tool skeleton（Execute 是 stub）。
// T03 在此基础上替换 Execute 为完整 Invoke 逻辑（11 步）。
//
// 重要：T01 验证 v2 #1 实装时发现 spec 假设有调整（详见 manifest S4-D26）：
//   - 无 Service.GetByName / GetByIDs — 改用 runner-cached SkillByName + SkillByID 双 map
//   - ID 类型 uint（非 uint64）
//   - 无 Name UNIQUE 约束 — runner defensive check 是同名唯一防线（S1-D13）
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"numind-server/internal/pkg/model"
)

const (
	// UseSkillToolName 是平台 tool 的标识，注册到 AgentToolRegistry。
	UseSkillToolName = "use_skill"

	// UseSkillTurnCapDefault 是单 turn 内 use_skill 最大调用次数 (S0-D6)。
	UseSkillTurnCapDefault = 3
)

// ── ctx keys (typed struct{}, 避免 string-key 冲突) ──────────────────────────

// ctxKeyUseSkillTurnT — runner 注入 *UseSkillTurnState 的 ctx key 类型
type ctxKeyUseSkillTurnT struct{}

// CtxKeyUseSkillTurn — 由 runner.Run 在装载 binding 成功后注入；
// use_skill tool 和 UseSkillTurnScope permission validator 都从 ctx 读
// 这个 key 取 *UseSkillTurnState。
var CtxKeyUseSkillTurn = ctxKeyUseSkillTurnT{}

// ctxKeyAgentBaseToolNamesT — runner 注入 Agent 基础工具白名单 []string 的 ctx key 类型
type ctxKeyAgentBaseToolNamesT struct{}

// CtxKeyAgentBaseToolNames — 由 runner.Run 在装载工具后注入，
// UseSkillTurnScope validator 读取做 "是不是 Agent 原生白名单工具" 的判断。
// 值类型: []string
var CtxKeyAgentBaseToolNames = ctxKeyAgentBaseToolNamesT{}

// ctxKeySkillBindingsT — runner 注入 binding 列表的 ctx key 类型（预留）
type ctxKeySkillBindingsT struct{}

// CtxKeySkillBindings — 由 runner.Run 注入，spec §3.7 预留扩展用；
// 主路径走 turn.SkillByID / SkillByName，不需要从 ctx 取 binding 列表。
// TODO(T03/T06): 评估实际是否注入；若 W3/W4 编码未触达，删此 ctx key + 配套 helpers。
// 值类型: []model.AgentSkillBinding
var CtxKeySkillBindings = ctxKeySkillBindingsT{}

// ── UseSkillTurnState ────────────────────────────────────────────────────────

// UseSkillTurnState 是一次 runner.Run() 调用的 turn-scope 状态。
//
// Lifetime: runner.Run 在装载 binding 成功后构造，注入 ctx，Eino 内层多轮
// ReAct 共享同一实例。下次 runner.Run 调用（= 新 user input）重新构造新 state。
//
// 字段：
//   - InvocationCount: 本 turn 已 use_skill 调用次数，每次 +1，超 Cap 拒绝
//   - AllowedTools: turn 内由 use_skill 累计添加的 Skill 工具白名单（去重 set）
//   - Cap: use_skill 调用上限（默认 UseSkillTurnCapDefault = 3）
//   - PendingBody/Name/Version: use_skill Execute 写入；runner 在下次 LLM
//     Generate 前消费，把 body 包装在 <system-reminder> user msg 注入 messages
//   - SkillByID: runner 启动时 batchGet 缓存（spec §3.6 解 N+1）
//     use_skill 内部做 bound check（O(1) map lookup）
//   - SkillByName: 由 SkillByID 派生的反向索引（spec §S4-D26 调整 — 无 GetByName API）
//     use_skill 通过 name 找 Skill 用此 map，避免每次遍历 SkillByID
//
// 同名防御：runner 在 init 此 state 前已做 checkDuplicateSkillNames，
// 保证 SkillByName 1:1 不冲突（S1-D13）。
type UseSkillTurnState struct {
	InvocationCount     int
	AllowedTools        map[string]struct{}
	Cap                 int
	PendingBody         string
	PendingSkillName    string
	PendingSkillVersion int
	SkillByID           map[uint]*model.Skill
	SkillByName         map[string]*model.Skill
}

// NewUseSkillTurnState 工厂——零值字段不会让 runner 启动崩 nil-deref。
// runner 调用方紧接着填 SkillByID / SkillByName，并通过 WithUseSkillTurn 注入 ctx。
func NewUseSkillTurnState(cap int) *UseSkillTurnState {
	if cap <= 0 {
		cap = UseSkillTurnCapDefault
	}
	return &UseSkillTurnState{
		InvocationCount: 0,
		AllowedTools:    make(map[string]struct{}),
		Cap:             cap,
		SkillByID:       make(map[uint]*model.Skill),
		SkillByName:     make(map[string]*model.Skill),
	}
}

// ── ctx helpers ──────────────────────────────────────────────────────────────

// WithUseSkillTurn 把 turn state 写入 ctx。
func WithUseSkillTurn(ctx context.Context, s *UseSkillTurnState) context.Context {
	return context.WithValue(ctx, CtxKeyUseSkillTurn, s)
}

// UseSkillTurnFromCtx 从 ctx 取 turn state；不存在或 nil 时 ok=false。
func UseSkillTurnFromCtx(ctx context.Context) (*UseSkillTurnState, bool) {
	s, ok := ctx.Value(CtxKeyUseSkillTurn).(*UseSkillTurnState)
	return s, ok && s != nil
}

// WithAgentBaseToolNames 把 Agent 基础工具白名单写入 ctx（v2 #2 permission validator 用）。
func WithAgentBaseToolNames(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, CtxKeyAgentBaseToolNames, names)
}

// AgentBaseToolNamesFromCtx 从 ctx 取基础工具白名单；不存在时 ok=false。
func AgentBaseToolNamesFromCtx(ctx context.Context) ([]string, bool) {
	n, ok := ctx.Value(CtxKeyAgentBaseToolNames).([]string)
	return n, ok
}

// WithSkillBindings 把 binding 列表写入 ctx（spec §3.7 预留，主路径未使用）。
func WithSkillBindings(ctx context.Context, bindings []model.AgentSkillBinding) context.Context {
	return context.WithValue(ctx, CtxKeySkillBindings, bindings)
}

// SkillBindingsFromCtx 从 ctx 取 binding 列表；不存在时 ok=false。
func SkillBindingsFromCtx(ctx context.Context) ([]model.AgentSkillBinding, bool) {
	b, ok := ctx.Value(CtxKeySkillBindings).([]model.AgentSkillBinding)
	return b, ok
}

// ── useSkillTool skeleton (T02 — Execute 是 stub，T03 替换为完整 Invoke) ────────

// useSkillTool 实现 FullTool interface（嵌入 BaseTool 用默认行为）。
type useSkillTool struct {
	BaseTool
	// T03 会注入 skillService *artifact.Service / bindingService *artifact.BindingService
	// T02 阶段只放 stub，Execute 返回 "not implemented yet"
}

// NewUseSkillTool constructs the use_skill FullTool (T02 skeleton).
// T03 will replace the signature to accept skillService / bindingService deps;
// Execute will switch from stub to the full 11-step Invoke logic per spec §3.1.
func NewUseSkillTool() FullTool {
	return &useSkillTool{}
}

// 编译期断言
var _ FullTool = (*useSkillTool)(nil)

func (t *useSkillTool) Name() string { return UseSkillToolName }

func (t *useSkillTool) Description() string {
	return "Call a Skill that is bound to this Agent. The Skill's detailed guide will be loaded into the conversation context, and the Skill's required tools will be temporarily enabled for this turn. Input: {\"name\": string} — the Skill name (must be one listed in the '可用技能' section of system prompt)."
}

func (t *useSkillTool) UserFacingName() string { return "调用技能" }

func (t *useSkillTool) NarrationVerb() string { return "调用技能" }

func (t *useSkillTool) IsReadOnly() bool { return true } // 不改 DB，只 read Skill + 改 turn state

func (t *useSkillTool) IsDestructive() bool { return false }

func (t *useSkillTool) AlwaysLoad() bool { return false } // 只在 Agent 有 binding 时由 runner 显式注入

func (t *useSkillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "要调用的技能名称（必须是已装载的技能之一）"
    }
  },
  "required": ["name"]
}`)
}

// Execute — T02 阶段是 stub；T03 替换为完整 Invoke 逻辑。
// 当前 stub 永远返回 error tool result（status=error，让 LLM 知道未就绪）。
func (t *useSkillTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	stub := map[string]string{
		"status": "error",
		"error":  "use_skill not implemented yet (T02 stub — T03 will replace with full Invoke)",
	}
	out, _ := json.Marshal(stub)
	return ToolResult(out), nil
}

// ── 辅助函数（T03 也会用）──────────────────────────────────────────────────

// jsonErr 构造统一的 error tool result JSON。
func jsonErr(format string, args ...interface{}) string {
	msg := fmt.Sprintf(format, args...)
	b, _ := json.Marshal(map[string]string{"status": "error", "error": msg})
	return string(b)
}
