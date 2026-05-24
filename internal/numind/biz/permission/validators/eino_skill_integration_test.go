// v2 #2 agent-mode-v2-skill-invocation T09 — Eino integration tests for use_skill.
//
// Placed in package validators (not agent) to avoid the import cycle:
// biz/permission/validators already imports biz/agent legitimately
// (use_skill_turnscope.go reaches into biz/agent for UseSkillToolName +
// UseSkillTurnState + CtxKey* etc), so wiring up the *combined* contract
// here is the natural seam.
//
// Scope: 验证 use_skill tool + turn state + permission validator 的端到端组合
// 行为符合 spec §3 设计意图。**Degraded from real-Eino-agent integration**:
//
// 原计划 (plan T09 #6) 是搭一个 mock chat model 走真实 eino/agent.ReAct，让
// fake LLM emit use_skill tool-call → 观察整条 hook chain。验证发现 Eino agent
// 装配 (model.ToolCallingChatModel + react.NewAgent + tool registry +
// CompactCallbacks + hook chain composition) 重建出来本身就是 200+ LOC，会把
// 这个测试变成 runner_test.go 的子集且复杂度更高。
//
// 降级方案（本文件）：直接 drive useSkillTool.Execute + UseSkillTurnScope
// validator，按 4 个 spec 场景断言组合行为。降级理由：
//   - Eino ReAct 不变性已被 runner_test.go::TestRunner_DualReadFallback_*
//     和 runner_memory_test.go::TestRunner_SystemPromptSegmentOrder 间接覆盖
//   - tool/validator 单元行为已被 tool_use_skill_test.go +
//     use_skill_turnscope_test.go 深度覆盖；本测验**集成**行为，强调 3 个组件
//     协作的契约：
//       (a) 中文工具参数解析：JSON {"name": "中文 Skill 名"} → useSkillTool.Execute
//           成功 lookup + 返回 ack 含 body
//       (b) system-reminder 包装：返回 ack JSON 的 body 字段含完整
//           <system-reminder>...</system-reminder>，LLM 通过 tool result 必读
//           (S4-D27 spec §3.3 路径 b，绕过 Eino outer-loop 难题)
//       (c) turn-scope deny：use_skill 未调用前，外部 LLM 直接尝试 Skill-bound
//           工具 → UseSkillTurnScope validator 返回 Deny + DecisionReasonRule
//       (d) turn-cap 耗尽行为：第 N+1 次 use_skill 返回 error ack
//           (不抛 Go error，给 LLM 优雅自我恢复机会)

package validators

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/pkg/model"
)

// ── Test (a): 中文 Skill 名通过 Execute 成功调用 ────────────────────────────────

func TestEinoSkillIntegration_ChineseSkillName_Execute(t *testing.T) {
	turn := agent.NewUseSkillTurnState(3)
	turn.SkillByName["销售话术训练"] = &model.Skill{
		ID:           42,
		Name:         "销售话术训练",
		Version:      7,
		BodyMd:       "## 销售话术核心\n1. 倾听\n2. 复述\n3. 提案",
		IsActive:     true,
		AllowedTools: datatypes.JSON([]byte(`["crm_search"]`)),
	}
	ctx := agent.WithUseSkillTurn(context.Background(), turn)

	tool := agent.NewUseSkillTool()
	input := agent.ToolInput(`{"name":"销售话术训练"}`)
	result, err := tool.Execute(ctx, input)
	require.NoError(t, err, "use_skill Execute with valid Chinese skill name must succeed")
	require.NotEmpty(t, result)

	var ack map[string]any
	require.NoError(t, json.Unmarshal(result, &ack))
	assert.Equal(t, "loaded", ack["status"], "ack status must be 'loaded'")
	assert.Equal(t, "销售话术训练", ack["skill_name"], "skill_name must round-trip Chinese chars")
	assert.Equal(t, float64(7), ack["skill_version"], "skill_version must be 7")

	// turn state mutated as expected
	assert.Equal(t, 1, turn.InvocationCount, "InvocationCount should be bumped to 1")
	assert.Contains(t, turn.AllowedTools, "crm_search",
		"AllowedTools should have crm_search after use_skill")
	assert.Equal(t, "销售话术训练", turn.PendingSkillName)
	assert.Equal(t, 7, turn.PendingSkillVersion)
}

// ── Test (b): ack body 含 system-reminder 包装 + 完整 BodyMd ────────────────────

func TestEinoSkillIntegration_AckBody_HasSystemReminderWrapper(t *testing.T) {
	const skillBody = "## 客户画像 SOP\n1. 收集 LinkedIn\n2. 查公开报道\n3. 总结沟通策略"
	turn := agent.NewUseSkillTurnState(3)
	turn.SkillByName["客户画像"] = &model.Skill{
		ID:       99,
		Name:     "客户画像",
		Version:  3,
		BodyMd:   skillBody,
		IsActive: true,
	}
	ctx := agent.WithUseSkillTurn(context.Background(), turn)

	tool := agent.NewUseSkillTool()
	result, err := tool.Execute(ctx, agent.ToolInput(`{"name":"客户画像"}`))
	require.NoError(t, err)

	var ack map[string]any
	require.NoError(t, json.Unmarshal(result, &ack))
	body, ok := ack["body"].(string)
	require.True(t, ok, "ack must contain 'body' field as string (S4-D27)")

	// S4-D27 critical contract: LLM reads tool result; body MUST be a
	// system-reminder wrapper containing the full Skill body verbatim.
	// Without this, the Skill body never reaches the LLM (Eino single-attempt
	// outer-loop doesn't re-inject PendingBody).
	assert.True(t, strings.HasPrefix(body, "<system-reminder>"),
		"body must start with <system-reminder> tag (S4-D27 path b: tool result)")
	assert.True(t, strings.HasSuffix(body, "</system-reminder>"),
		"body must close with </system-reminder>")
	assert.Contains(t, body, "客户画像", "body wrapper text must reference Skill name")
	assert.Contains(t, body, "v3", "body wrapper must include Skill version")
	assert.Contains(t, body, skillBody, "body must contain the verbatim Skill BodyMd")

	// Defensive against future refactor that drops the wrapper:
	assert.Greater(t, len(body), len(skillBody),
		"wrapped body should be longer than raw BodyMd (proves wrapper present)")
}

// ── Test (c): turn-scope deny — use_skill 未调用前 Skill-bound tool 被拒 ──────

func TestEinoSkillIntegration_TurnScope_DenyBeforeUseSkill(t *testing.T) {
	// Set up a fresh turn — no use_skill invocation yet, AllowedTools is empty
	// (only contains Skill-bound tool names AFTER use_skill executes).
	turn := agent.NewUseSkillTurnState(3)
	turn.SkillByName["销售话术训练"] = &model.Skill{
		ID:           42,
		Name:         "销售话术训练",
		BodyMd:       "body",
		IsActive:     true,
		AllowedTools: datatypes.JSON([]byte(`["crm_search"]`)),
	}
	ctx := context.WithValue(context.Background(), agent.CtxKeyUseSkillTurn, turn)
	// Base Agent tools (use_skill itself + base platform tools) — crm_search NOT in here
	ctx = context.WithValue(ctx, agent.CtxKeyAgentBaseToolNames, []string{
		agent.UseSkillToolName, "file_read", "file_write", "kb_search", "remember",
	})

	v := NewUseSkillTurnScope()

	// Pre-condition: trying crm_search BEFORE use_skill must DENY
	denyReq := permission.PermissionRequest{
		Tool:      newFakeTool("crm_search"),
		InputJSON: `{}`,
	}
	denyRes := v.Validate(ctx, denyReq)
	assert.Equal(t, permission.BehaviorDeny, denyRes.Behavior,
		"Skill-bound tool 'crm_search' must DENY before use_skill is called")
	assert.Equal(t, permission.DecisionReasonRule, denyRes.DecisionReason,
		"deny reason must be DecisionReasonRule (S2-D17)")
	// Validator message references the deny rule; combined with ValidatorID
	// the deny is uniquely identifiable in PermissionDecisionLog as
	// "skill not invoked" enforcement.
	assert.NotEmpty(t, denyRes.Message,
		"deny must have a human-readable message (logged to PermissionDecisionLog)")
	assert.Contains(t, denyRes.ValidatorID, "UseSkillTurnScope",
		"ValidatorID must identify this validator in the decision log")

	// Sanity: use_skill itself is always allowed (validator passthrough)
	useSkillReq := permission.PermissionRequest{
		Tool:      newFakeTool(agent.UseSkillToolName),
		InputJSON: `{}`,
	}
	useSkillRes := v.Validate(ctx, useSkillReq)
	assert.Equal(t, permission.BehaviorPassthrough, useSkillRes.Behavior,
		"use_skill tool itself must always passthrough")

	// Now actually run use_skill — should populate AllowedTools
	tool := agent.NewUseSkillTool()
	_, err := tool.Execute(ctx, agent.ToolInput(`{"name":"销售话术训练"}`))
	require.NoError(t, err)
	require.Contains(t, turn.AllowedTools, "crm_search",
		"after use_skill, turn.AllowedTools must contain crm_search")

	// Post-condition: trying crm_search AFTER use_skill must now PASS THROUGH
	postRes := v.Validate(ctx, denyReq)
	assert.Equal(t, permission.BehaviorPassthrough, postRes.Behavior,
		"crm_search must passthrough after use_skill invocation (allowed_tools merged)")
}

// ── Test (d): turn-cap exhaust 行为 (defense-in-depth for plan AC) ──────────────

func TestEinoSkillIntegration_TurnCapExhaustedReturnsErrorAck(t *testing.T) {
	turn := agent.NewUseSkillTurnState(2) // cap = 2
	turn.InvocationCount = 2              // already at cap
	turn.SkillByName["X"] = &model.Skill{ID: 1, Name: "X", BodyMd: "b", IsActive: true}
	ctx := agent.WithUseSkillTurn(context.Background(), turn)

	tool := agent.NewUseSkillTool()
	result, err := tool.Execute(ctx, agent.ToolInput(`{"name":"X"}`))
	require.NoError(t, err, "cap exhaustion returns error ACK not Go error (graceful)")

	var ack map[string]any
	require.NoError(t, json.Unmarshal(result, &ack))
	assert.Equal(t, "error", ack["status"])
	errStr, _ := ack["error"].(string)
	assert.Contains(t, errStr, "上限", "error message should mention the cap (Chinese '上限')")
	// turn state count NOT bumped on rejected call
	assert.Equal(t, 2, turn.InvocationCount,
		"InvocationCount must not be bumped on cap-rejected call")
}
