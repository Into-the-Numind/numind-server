package validators

import (
	"context"
	"fmt"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/permission"
)

// UseSkillTurnScope — v2 #2 use_skill turn-scope 工具白名单 validator
// 位置：permission pipeline 第 8 个 validator（与现有 7 个并列）
//
// 行为：
//   - 工具名 == "use_skill" → Passthrough (use_skill 自己永远 allow)
//   - 工具名在 ctx CtxKeyAgentBaseToolNames (Agent 基础工具白名单 []string) → Passthrough
//   - 工具名在 turn state.AllowedTools map → Passthrough
//   - 否则（工具属于某 Skill 的 allowed_tools 但当前 turn 还没 use_skill 调用）→ Deny
//   - legacy 路径 (turn state == nil) → Passthrough (不动 v1 Agent 行为)
//
// Passthrough 而非 Allow：让后续 7 validator 继续判（ToolFlag / TenantAdminRule 等），
// 保持判定语义独立性。
type UseSkillTurnScope struct{}

// NewUseSkillTurnScope 构造 UseSkillTurnScope validator。
func NewUseSkillTurnScope() permission.Validator { return &UseSkillTurnScope{} }

// ID 返回 validator 标识符（出现在 PermissionResult.ValidatorID 中）。
func (v *UseSkillTurnScope) ID() string { return "UseSkillTurnScope" }

// Validate 按 spec §3.4 实现 4 分支决策（含 legacy 兜底 + nil tool 边界）。
func (v *UseSkillTurnScope) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
	if req.Tool == nil {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no tool")
	}
	toolName := req.Tool.Name()

	// use_skill 自身永远 allow
	if toolName == agent.UseSkillToolName {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "use_skill tool itself")
	}

	// base 白名单 check（从 ctx 读 []string）
	if baseNames, ok := ctx.Value(agent.CtxKeyAgentBaseToolNames).([]string); ok {
		for _, n := range baseNames {
			if n == toolName {
				return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "in base whitelist")
			}
		}
	}

	// turn-scope check
	turn, ok := ctx.Value(agent.CtxKeyUseSkillTurn).(*agent.UseSkillTurnState)
	if !ok || turn == nil {
		// legacy 路径（无 binding），所有 RunRequest.ToolNames 已在 base whitelist
		// — 走到这里说明工具不在 base 白名单，让后续 validator 判（不应当 deny）
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no turn state — legacy path")
	}
	if _, allowed := turn.AllowedTools[toolName]; allowed {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "in turn-scope allowed_tools")
	}

	// 工具属于某 Skill 的 allowed_tools，但当前 turn 还没 use_skill 调用 → deny
	return permission.Deny(v.ID()+":"+toolName, permission.DecisionReasonRule,
		fmt.Sprintf("工具 '%s' 当前未启用。该工具属于某个技能，请先用 use_skill 调用对应技能。", toolName))
}
