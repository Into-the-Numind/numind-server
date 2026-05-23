package validators

import (
	"context"
	"strings"
	"testing"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/permission"
)

// TestUseSkillTurnScope_UseSkillItself_Passthrough — use_skill 工具自身永远放行。
func TestUseSkillTurnScope_UseSkillItself_Passthrough(t *testing.T) {
	v := NewUseSkillTurnScope()
	req := permission.PermissionRequest{
		Tool:      newFakeTool(agent.UseSkillToolName),
		InputJSON: `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for use_skill self, got %q (msg=%q)", got.Behavior, got.Message)
	}
}

// TestUseSkillTurnScope_InBaseWhitelist_Passthrough — 工具在 Agent 基础白名单中 → Passthrough。
func TestUseSkillTurnScope_InBaseWhitelist_Passthrough(t *testing.T) {
	v := NewUseSkillTurnScope()
	ctx := context.WithValue(context.Background(), agent.CtxKeyAgentBaseToolNames,
		[]string{"file_read", "bash_exec", "web_search"})
	req := permission.PermissionRequest{
		Tool:      newFakeTool("bash_exec"),
		InputJSON: `{}`,
	}
	got := v.Validate(ctx, req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for base-whitelist tool, got %q (msg=%q)", got.Behavior, got.Message)
	}
}

// TestUseSkillTurnScope_InTurnAllowedTools_Passthrough — 工具在 turn-scope AllowedTools 中 → Passthrough。
func TestUseSkillTurnScope_InTurnAllowedTools_Passthrough(t *testing.T) {
	v := NewUseSkillTurnScope()
	turn := &agent.UseSkillTurnState{
		AllowedTools: map[string]struct{}{
			"pptx_generate": {},
			"chart_render":  {},
		},
	}
	ctx := context.WithValue(context.Background(), agent.CtxKeyUseSkillTurn, turn)
	req := permission.PermissionRequest{
		Tool:      newFakeTool("pptx_generate"),
		InputJSON: `{}`,
	}
	got := v.Validate(ctx, req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for turn-allowed tool, got %q (msg=%q)", got.Behavior, got.Message)
	}
}

// TestUseSkillTurnScope_NotInAnySet_Deny — 工具不在 base 也不在 turn-scope 但有 turn state → Deny。
func TestUseSkillTurnScope_NotInAnySet_Deny(t *testing.T) {
	v := NewUseSkillTurnScope()
	turn := &agent.UseSkillTurnState{
		AllowedTools: map[string]struct{}{
			"pptx_generate": {},
		},
	}
	ctx := context.WithValue(context.Background(), agent.CtxKeyAgentBaseToolNames, []string{"file_read"})
	ctx = context.WithValue(ctx, agent.CtxKeyUseSkillTurn, turn)
	req := permission.PermissionRequest{
		Tool:      newFakeTool("chart_render"), // 不在 base 也不在 turn
		InputJSON: `{}`,
	}
	got := v.Validate(ctx, req)
	if got.Behavior != permission.BehaviorDeny {
		t.Fatalf("want deny for skill-bound tool without use_skill call, got %q (msg=%q)", got.Behavior, got.Message)
	}
	if got.DecisionReason != permission.DecisionReasonRule {
		t.Errorf("want reason=rule, got %q", got.DecisionReason)
	}
	if !strings.Contains(got.Message, "技能") {
		t.Errorf("deny message should explain it belongs to a skill, got %q", got.Message)
	}
	if !strings.Contains(got.ValidatorID, "chart_render") {
		t.Errorf("ValidatorID should include tool name for diagnostics, got %q", got.ValidatorID)
	}
}

// TestUseSkillTurnScope_LegacyPath_NoTurnState_Passthrough — 无 turn state（v1 legacy 路径）→ Passthrough（让后续 validator 判）。
func TestUseSkillTurnScope_LegacyPath_NoTurnState_Passthrough(t *testing.T) {
	v := NewUseSkillTurnScope()
	// 工具不在 base 白名单，也无 turn state — v1 Agent 老路径不应被此 validator 拦截。
	req := permission.PermissionRequest{
		Tool:      newFakeTool("some_random_tool"),
		InputJSON: `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough on legacy path (no turn state), got %q (msg=%q)", got.Behavior, got.Message)
	}
}

// TestUseSkillTurnScope_NilTool_Passthrough — req.Tool == nil 边界 → Passthrough。
func TestUseSkillTurnScope_NilTool_Passthrough(t *testing.T) {
	v := NewUseSkillTurnScope()
	req := permission.PermissionRequest{
		Tool:      nil,
		InputJSON: `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for nil tool, got %q (msg=%q)", got.Behavior, got.Message)
	}
}

// TestUseSkillTurnScope_NilTurnState_FromCtx_Passthrough — ctx 里显式存了 *UseSkillTurnState 为 nil → Passthrough（防御性 nil check）。
func TestUseSkillTurnScope_NilTurnState_FromCtx_Passthrough(t *testing.T) {
	v := NewUseSkillTurnScope()
	var nilTurn *agent.UseSkillTurnState
	ctx := context.WithValue(context.Background(), agent.CtxKeyUseSkillTurn, nilTurn)
	req := permission.PermissionRequest{
		Tool:      newFakeTool("anything"),
		InputJSON: `{}`,
	}
	got := v.Validate(ctx, req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough when ctx stores nil *UseSkillTurnState, got %q (msg=%q)", got.Behavior, got.Message)
	}
}

// TestUseSkillTurnScope_ID_Stable — ID 字符串与 validator id slot 保持稳定（用于日志和 PermissionResult.ValidatorID）。
func TestUseSkillTurnScope_ID_Stable(t *testing.T) {
	v := NewUseSkillTurnScope()
	if got := v.ID(); got != "UseSkillTurnScope" {
		t.Errorf("ID() = %q, want %q", got, "UseSkillTurnScope")
	}
}
