// Skill turn-state + ctx helpers shared by the load_skill tool.
//
// Originally introduced by agent-mode-v2-skill-invocation for use_skill; after
// open-tools-skill-as-guidance merged use_skill + read_skill into load_skill, the
// use_skill tool itself moved to tool_load_skill.go. This file retains the
// per-run turn state (cap counting + loaded-skill snapshots + the runner skill
// cache) and the ctx helpers load_skill reads. Struct/const names keep their
// "UseSkill" prefix to bound the rename churn (internal-only).
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"numind-server/internal/pkg/model"
)

const (
	// UseSkillTurnCapDefault 是单 turn 内 load_skill 最大调用次数 (S0-D6)。
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

// ── 辅助函数（T03 也会用）──────────────────────────────────────────────────

// jsonErr 构造统一的 error tool result JSON。
func jsonErr(format string, args ...interface{}) string {
	msg := fmt.Sprintf(format, args...)
	b, _ := json.Marshal(map[string]string{"status": toolStatusError, "error": msg})
	return string(b)
}
