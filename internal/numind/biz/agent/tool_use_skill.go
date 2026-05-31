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

	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/model"
)

const (
	// UseSkillToolName 是平台 tool 的标识，注册到 AgentToolRegistry。
	UseSkillToolName = "use_skill"

	// UseSkillTurnCapDefault 是单 turn 内 use_skill 最大调用次数 (S0-D6)。
	UseSkillTurnCapDefault = 3

	// Tool result status enum (Langfuse span output + ack JSON 复用，避免魔法字符串)
	toolStatusLoaded = "loaded"
	toolStatusError  = "error"
)

// ── ctx keys (typed struct{}, 避免 string-key 冲突) ──────────────────────────

// ctxKeyUseSkillTurnT — runner 注入 *UseSkillTurnState 的 ctx key 类型
type ctxKeyUseSkillTurnT struct{}

// CtxKeyUseSkillTurn — 由 runner.Run 在装载 binding 成功后注入；
// use_skill tool 从 ctx 读这个 key 取 *UseSkillTurnState。
var CtxKeyUseSkillTurn = ctxKeyUseSkillTurnT{}

// ctxKeySkillBindingsT — runner 注入 binding 列表的 ctx key 类型（预留）
type ctxKeySkillBindingsT struct{}

// CtxKeySkillBindings — 由 runner.Run 注入，spec §3.7 预留扩展用；
// 主路径走 turn.SkillByID / SkillByName，未来扩展（如 admin debug / observability）
// 可直接从 ctx 取已 join 的 Skill 列表（与 BindingService.ListByAgent 返回类型一致）。
// 值类型: []model.Skill（S4-D26 校正 — 不是 spec 原假设的 []AgentSkillBinding）
var CtxKeySkillBindings = ctxKeySkillBindingsT{}

// ── UseSkillTurnState ────────────────────────────────────────────────────────

// PendingSkill 记录一次 use_skill Execute 的待注入快照（body + 元数据）。
// 多次调用 → append 多条；runner outer-loop 一次性按调用序消费全部并清空。
// 字段顺序遵循"细节→宽载荷"惯例：Name/Version 是身份元数据，Body 是大字符串负载放末尾。
type PendingSkill struct {
	Name    string
	Version int
	Body    string
}

// UseSkillTurnState 是一次 runner.Run() 调用的 turn-scope 状态。
//
// Lifetime: runner.Run 在装载 binding 成功后构造，注入 ctx，Eino 内层多轮
// ReAct 共享同一实例。下次 runner.Run 调用（= 新 user input）重新构造新 state。
//
// 字段：
//   - InvocationCount: 本 turn 已 use_skill 调用次数，每次 +1，超 Cap 拒绝
//   - Cap: use_skill 调用上限（默认 UseSkillTurnCapDefault = 3）
//   - PendingSkills: use_skill Execute 顺序 append 的待注入快照列表；runner
//     在下次 LLM Generate 前一次性消费全部并清空，每条包成 <system-reminder>
//     user msg 注入。同 turn 多次调用必须全部保留——单字段会被后调用覆盖、
//     丢失先调用的 body（latent bug：tool_result 通道为主时不可见，outer-loop
//     注入路径 spec §3.3 路径 a 启用时丢 body）。
//   - SkillByID: runner 启动时 batchGet 缓存（spec §3.6 解 N+1）
//     use_skill 内部做 bound check（O(1) map lookup）
//   - SkillByName: 由 SkillByID 派生的反向索引（spec §S4-D26 调整 — 无 GetByName API）
//     use_skill 通过 name 找 Skill 用此 map，避免每次遍历 SkillByID
//
// 同名防御：runner 在 init 此 state 前已做 checkDuplicateSkillNames，
// 保证 SkillByName 1:1 不冲突（S1-D13）。
type UseSkillTurnState struct {
	InvocationCount int
	Cap             int
	PendingSkills   []PendingSkill
	SkillByID       map[uint]*model.Skill
	SkillByName     map[string]*model.Skill
}

// NewUseSkillTurnState 工厂——零值字段不会让 runner 启动崩 nil-deref。
// runner 调用方紧接着填 SkillByID / SkillByName，并通过 WithUseSkillTurn 注入 ctx。
func NewUseSkillTurnState(cap int) *UseSkillTurnState {
	if cap <= 0 {
		cap = UseSkillTurnCapDefault
	}
	return &UseSkillTurnState{
		InvocationCount: 0,
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

// WithSkillBindings 把 runner 启动时查到的 Skill 列表写入 ctx（spec §3.7 预留 + S4-D26 校正）。
// 主路径走 turn.SkillByID / SkillByName，本 key 供未来扩展使用（admin / observability）。
func WithSkillBindings(ctx context.Context, skills []model.Skill) context.Context {
	return context.WithValue(ctx, CtxKeySkillBindings, skills)
}

// SkillBindingsFromCtx 从 ctx 取 Skill 列表；不存在 / 空 / nil 均返回 ok=false。
func SkillBindingsFromCtx(ctx context.Context) ([]model.Skill, bool) {
	s, ok := ctx.Value(CtxKeySkillBindings).([]model.Skill)
	return s, ok && len(s) > 0
}

// ── useSkillTool (T03 完整 Invoke) ────────────────────────────────────────

// useSkillTool 实现 FullTool interface（嵌入 BaseTool 用默认行为）。
//
// T03 设计简化（vs spec §3.1 11 步）：runner 启动时 batchGet 把所有 binding
// 的 Skill 缓存到 turn.SkillByName + SkillByID 双 map。useSkillTool 完全
// 不依赖 skillService / bindingService — 所有 lookup 走 turn state（0 DB
// 调用）。bound check 隐式（不在 SkillByName 即未装载）。narration emit 由
// adapter_full_to_eino 在 PreToolCall/PostToolCall 自动按 tool name
// "use_skill" 查 tool-display.yaml 模板（T04 已加 entry），Execute 内不调。
type useSkillTool struct {
	BaseTool
	// 无依赖字段 — 所有运行时状态走 ctx.UseSkillTurnState
}

// NewUseSkillTool constructs the use_skill FullTool.
func NewUseSkillTool() FullTool {
	return &useSkillTool{}
}

// 编译期断言
var _ FullTool = (*useSkillTool)(nil)

func (t *useSkillTool) Name() string { return UseSkillToolName }

func (t *useSkillTool) Description() string {
	return "Call a Skill that is bound to this Agent. The Skill's detailed guide will be loaded into the conversation context. Input: {\"name\": string} — the Skill name (must be one listed in the '可用技能' section of system prompt)."
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

// Execute 实现 use_skill 的核心逻辑（spec §3.1，T03 完整实现）。
//
// 7 步：
//  1. JSON unmarshal & validate name
//  2. 取 ctx 注入的 turn state（runner 维护）
//  3. cap check（默认 ≤3 次/turn）
//  4. lookup Skill via turn.SkillByName（runner 缓存，0 DB 调用；不在 map = 未装载或不存在）
//  5. business validate（IsActive / BodyMd 非空）
//  6. append turn.PendingSkills（同 turn 内串调累积保留所有，不覆盖；
//     runner 在下次 LLM Generate 前一次性消费，每条包 system-reminder user msg 注入）
//  7. InvocationCount++ + 返回 acknowledgement JSON
//
// 永不返回非 nil error — 所有错误用 tool result 表达让 LLM 优雅恢复。
// Langfuse span 记录 skill metadata（spec §7）。
func (t *useSkillTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	// 1. JSON unmarshal
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return ToolResult(jsonErr("use_skill 参数解析失败：%s", err.Error())), nil
	}
	if p.Name == "" {
		return ToolResult(jsonErr("name 参数不能为空")), nil
	}

	// 2. 取 ctx 注入的 turn state
	turn, ok := UseSkillTurnFromCtx(ctx)
	if !ok {
		// runner 未注入 turn state — 本 Agent 没有 binding 走 legacy 路径
		// use_skill 不应注册到 toolMap，但防御性返回 error tool result
		return ToolResult(jsonErr("use_skill 未启用（本 Agent 无任何技能装载，请联系配置者）")), nil
	}

	// Langfuse span 起 — 即使中途错误也记录（spec §7）
	var traceID, spanID string
	var skillIDForSpan uint
	var bodyLenForSpan int
	resultStatus := toolStatusLoaded
	resultError := ""
	defer func() {
		if traceID == "" || spanID == "" {
			return
		}
		langfuse.EndSpan(traceID, spanID,
			langfuse.WithSpanOutput(map[string]any{
				"status":           resultStatus,
				"error":            resultError,
				"skill_id":         skillIDForSpan,
				"body_token_count": bodyLenForSpan, // 近似：bytes ≈ tokens / 1.5；S5 verify aligns
			}),
		)
	}()
	if tc := langfuse.FromContext(ctx); tc != nil {
		traceID = tc.TraceID
		spanID = langfuse.SpanID()
		langfuse.CreateSpan(traceID, spanID, "tool.use_skill",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{
				"skill_name":          p.Name,
				"turn_invocation_pre": turn.InvocationCount,
				"turn_cap":            turn.Cap,
			}),
		)
	}

	// 3. cap check
	if turn.InvocationCount >= turn.Cap {
		resultStatus = toolStatusError
		resultError = "turn_cap_exceeded"
		return ToolResult(jsonErr("已达本轮技能调用上限 (%d 次)，本轮无法再调用其他技能", turn.Cap)), nil
	}

	// 4. lookup via turn.SkillByName (0 DB 调用，由 runner 启动时 batchGet 缓存)
	sk, found := turn.SkillByName[p.Name]
	if !found || sk == nil {
		resultStatus = toolStatusError
		resultError = "skill_not_bound"
		return ToolResult(jsonErr("技能 '%s' 不存在或未装载到本 Agent", p.Name)), nil
	}
	skillIDForSpan = sk.ID
	bodyLenForSpan = len(sk.BodyMd)

	// 5. business validation
	if !sk.IsActive {
		resultStatus = toolStatusError
		resultError = "skill_inactive"
		return ToolResult(jsonErr("技能 '%s' 已被禁用", p.Name)), nil
	}
	if sk.BodyMd == "" {
		resultStatus = toolStatusError
		resultError = "skill_body_empty"
		return ToolResult(jsonErr("技能 '%s' 内容为空，请联系配置者更新", p.Name)), nil
	}

	// 6. append PendingSkill — runner outer-loop 在下次 attempt 的 Generate 前可消费 (备用通道)
	// 注意 (S4-D27): Eino 单 attempt 场景下 outer-loop 注入对本 turn 不生效；
	// 实际 body 通过 ack JSON 的 body 字段直接给 LLM (tool result 通道 LLM 必读)。
	// PendingSkills 保留为多 attempt / 未来 Eino hook 扩展兼容。
	// 必须 append 而非覆盖：同 turn 内 LLM 可串调 use_skill(A) → use_skill(B)，
	// 若覆盖则 B 顶掉 A 的 body，outer-loop 注入启用时只看到 B (latent bug)。
	turn.PendingSkills = append(turn.PendingSkills, PendingSkill{
		Name:    sk.Name,
		Version: int(sk.Version),
		Body:    sk.BodyMd,
	})

	// 7. count++ + 返回 acknowledgement (含完整 body 包 system-reminder，S4-D27 主通道)
	turn.InvocationCount++

	// system-reminder 包装的 body 直接放进 ack — LLM 通过 tool result 必读，
	// 不依赖 runner outer-loop attempt 注入（spec §3.3 路径 b: tool result）。
	bodyWrapped := fmt.Sprintf("<system-reminder>\n以下是你刚调用的技能 '%s' 的详细指引（v%d）。请按这些指引继续完成用户的任务：\n\n%s\n</system-reminder>",
		sk.Name, sk.Version, sk.BodyMd)

	ack := map[string]any{
		"status":          toolStatusLoaded, // ack 永远 "loaded"——LLM 视角 Skill 已就绪
		"skill_name":      sk.Name,
		"skill_version":   sk.Version,
		"body_length":     len(sk.BodyMd),
		"body":            bodyWrapped, // S4-D27: 完整 body in tool result (LLM 必读)
		"turn_invocation": turn.InvocationCount,
		"turn_cap":        turn.Cap,
		"message":         fmt.Sprintf("技能 '%s' 已载入对话上下文，请根据技能指引完成任务", sk.Name),
	}
	out, err := json.Marshal(ack)
	if err != nil || len(out) == 0 {
		// 防御：成功路径 Marshal 永不应失败，万一失败也要返回非空 fallback
		out = []byte(fmt.Sprintf(`{"status":"loaded","skill_name":%q,"message":"技能已载入"}`, sk.Name))
	}
	return ToolResult(out), nil
}

// ── 辅助函数（T03 也会用）──────────────────────────────────────────────────

// jsonErr 构造统一的 error tool result JSON。
func jsonErr(format string, args ...interface{}) string {
	msg := fmt.Sprintf(format, args...)
	b, _ := json.Marshal(map[string]string{"status": toolStatusError, "error": msg})
	return string(b)
}
