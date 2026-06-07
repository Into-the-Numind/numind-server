package agent

import (
	"strings"
	"testing"

	"numind-server/internal/numind/biz/skill"
	"numind-server/internal/pkg/model"
)

// 本文件是 T6 集成测试 — 验证拼装层（BuildSystemPromptV2 / BuildSystemPromptLegacy /
// BuildInstitutionSection / BuildUserContextSection）的端到端组合行为。
//
// 设计取舍：完整的 runner.Run 集成测试需要 stub aiservice / memoryProvider /
// skillBindingService / langfuse 等多个外部依赖，stub 成本远高于回报。改为直接
// 用接近真实 runner.go 的入参组合调用拼装层，覆盖 spec §9 的关键场景：
//
//   - Legacy 路径产出（PlatformBasePrompt 头 + 段顺序 + 安全脚注尾）
//   - V2 路径含 systemPrompt + skill catalog + tools hint + memory + 5 段（含 tenant_hard_rules）
//   - D11 边界：V2 + 无 skills 时不混入 legacy body
//
// V2 fallback（SystemPrompt 纯空白时走 Legacy）由 runner.go:677 的 strings.TrimSpace
// 主分叉判定，不在拼装层；TestBuildInstitutionSection_EmptyParts 已经间接覆盖了
// 空白 trim 行为，这里不重复测试。

// TestPrompt_LegacyAgent_NoSystemPrompt 模拟老 agent（ad.SystemPrompt=""）走 Legacy
// 路径的入参组合，验证产出 prompt 的关键 invariants：
//   - 以 PlatformBasePrompt 开头
//   - 以 PlatformSafetyFooter 结尾
//   - body / agentMd / toolsSection 都按序出现
//
// 不做完整 golden fixture（成本高且 churny），只验关键 invariants 与段顺序。
func TestPrompt_LegacyAgent_NoSystemPrompt(t *testing.T) {
	const (
		body         = "[skill catalog or v1 body]"
		agentMd      = "\n\n## Agent Rules\nrule1"
		toolsSection = "\n\n## Output Tool Priority\n"
	)
	got := BuildSystemPromptLegacy(
		skill.PlatformBasePrompt,
		"",              // tenantHardRules
		body,            // body
		"## Memories\n", // memoriesHeader（非空 — 模拟有 memory 的场景）
		agentMd,         // agentMd
		"",              // selector
		"",              // dialectic
		"",              // temporal
		"",              // memoryDisclaimer
		"",              // memorySystem
		toolsSection,    // toolsSection
		skill.PlatformSafetyFooter,
	)

	// 1. 必须以 PlatformBasePrompt 开头
	if !strings.HasPrefix(got, skill.PlatformBasePrompt) {
		prefix := got
		if len(prefix) > 80 {
			prefix = prefix[:80]
		}
		t.Errorf("Legacy path must start with PlatformBasePrompt; got prefix %q", prefix)
	}

	// 2. 必须以 PlatformSafetyFooter 结尾
	if !strings.HasSuffix(got, skill.PlatformSafetyFooter) {
		suffix := got
		if len(suffix) > 80 {
			suffix = suffix[len(suffix)-80:]
		}
		t.Errorf("Legacy path must end with PlatformSafetyFooter; got suffix %q", suffix)
	}

	// 3. body / agentMd / toolsSection 内容都出现
	for _, sub := range []string{body, "Agent Rules", "rule1", "Output Tool Priority"} {
		if !strings.Contains(got, sub) {
			t.Errorf("Legacy prompt missing %q", sub)
		}
	}

	// 4. 段顺序：base → body → memories → agentMd → tools → footer
	idxBase := strings.Index(got, skill.PlatformBasePrompt)
	idxBody := strings.Index(got, body)
	idxMem := strings.Index(got, "## Memories")
	idxAgentMd := strings.Index(got, "Agent Rules")
	idxTools := strings.Index(got, "Output Tool Priority")
	idxFooter := strings.Index(got, skill.PlatformSafetyFooter)

	if !(idxBase < idxBody && idxBody < idxMem && idxMem < idxAgentMd &&
		idxAgentMd < idxTools && idxTools < idxFooter) {
		t.Errorf("Legacy segment order broken: base=%d body=%d mem=%d agentMd=%d tools=%d footer=%d",
			idxBase, idxBody, idxMem, idxAgentMd, idxTools, idxFooter)
	}
}

// TestPrompt_V2Path_WithSkillsAndSystemPrompt 模拟新 agent（ad.SystemPrompt 非空 +
// 已绑定 skills）走 V2 路径的入参组合，验证 5 段都到位 + 内容齐全。
//
// 模拟 runner.go 的调用方式：
//   - institutionSection = BuildInstitutionSection(systemPrompt, skillCatalog, toolsHint)
//   - userContext        = BuildUserContextSection(agentMd, selector, dialectic, temporal, disclaimer, memorySystem)
//   - 最终 prompt        = BuildSystemPromptV2(tenantHardRules, institutionSection, userContext)
//
// T5 (#1a): 现在断言 tenantHardRules 也出现在 V2 prompt 里（且在 head 之后、institution 之前）。
func TestPrompt_V2Path_WithSkillsAndSystemPrompt(t *testing.T) {
	const (
		tenantHardRules = "## 平台硬规则\n禁止编造客户隐私"
		systemPrompt    = "你是销售助手"
		skillCatalog    = "## 可用技能\n- 销售话术库\n- 客户画像查询"
		toolsHint       = "## 输出工具优先级\nGo 工具 > invoke_skill"
		agentMd         = "\n\n## Agent Rules\n规则 1：保持专业语气"
		memorySys       = "用户偏好简洁回复"
	)
	institutionSection := BuildInstitutionSection(systemPrompt, skillCatalog, toolsHint)
	userContext := BuildUserContextSection(agentMd, "", "", "", "", memorySys)

	got := BuildSystemPromptV2(tenantHardRules, institutionSection, userContext)

	// 1. 5 段都到位（platform head + tenant hard rules + institution 内子段 + userContext + footer）
	subs := []string{
		"平台硬规则",       // tenant_hard_rules（#1a：不再被 DROP）
		"你是销售助手",      // ad.SystemPrompt
		"销售话术库",       // skill catalog 内容
		"输出工具优先级",     // tools hint
		"Agent Rules", // agent.md
		"规则 1：保持专业语气", // agent.md 具体内容
		"用户偏好简洁回复",    // memorySystem
		"## Memories", // userContext header
		skill.PlatformBasePrompt,
		skill.PlatformSafetyFooter,
	}
	for _, sub := range subs {
		if !strings.Contains(got, sub) {
			t.Errorf("V2 prompt missing %q", sub)
		}
	}

	// 2. 段顺序：head → tenant_hard_rules → institution(sysPrompt → catalog → tools) → userContext → footer
	idxHead := strings.Index(got, skill.PlatformBasePrompt)
	idxHardRules := strings.Index(got, "平台硬规则")
	idxSys := strings.Index(got, systemPrompt)
	idxCatalog := strings.Index(got, "销售话术库")
	idxTools := strings.Index(got, "输出工具优先级")
	idxMem := strings.Index(got, "## Memories")
	idxFooter := strings.Index(got, skill.PlatformSafetyFooter)

	if !(idxHead < idxHardRules && idxHardRules < idxSys && idxSys < idxCatalog &&
		idxCatalog < idxTools && idxTools < idxMem && idxMem < idxFooter) {
		t.Errorf("V2 segment order broken: head=%d hardRules=%d sys=%d catalog=%d tools=%d mem=%d footer=%d",
			idxHead, idxHardRules, idxSys, idxCatalog, idxTools, idxMem, idxFooter)
	}
}

// TestPrompt_V2Path_SystemPromptOnly_NoSkills 验证 spec §9 case 3（D11 边界）：
// 当机构方写了 ad.SystemPrompt 但没绑定 skill 时，runner.go:688-691 会把
// skillCatalog 传成空字符串（不再叠加 v1 legacy body）。
//
// 这里直接模拟 runner 在 len(skills)==0 时的入参 — 即使 ad 上还有
// GeneratedSkillBody（spec 里叫 "legacy junk"），它不应进入 V2 prompt。
//
// 拼装层无法接收 "legacy junk"（runner 已经在 D11 决策里把它过滤掉），
// 本测试断言 V2 prompt 中只有 systemPrompt + toolsHint，没有任何 catalog 位的内容。
func TestPrompt_V2Path_SystemPromptOnly_NoSkills(t *testing.T) {
	const (
		systemPrompt = "你是销售助手"
		toolsHint    = "## 输出工具优先级"
	)
	institutionSection := BuildInstitutionSection(
		systemPrompt,
		"", // skillCatalog 空（D11：runner 在 len(skills)==0 时这样传，不叠加 v1 body）
		toolsHint,
	)
	userContext := BuildUserContextSection("", "", "", "", "", "")
	// T5 (#1a): empty tenantHardRules keeps this test focused on the D11 no-skills
	// boundary; the hard-rules-present case is covered elsewhere.
	got := BuildSystemPromptV2("", institutionSection, userContext)

	// D11 守护：哨兵字符串（代表运行时 v1 GeneratedSkillBody 残留）不应混入
	if strings.Contains(got, "LEGACY_BODY_SENTINEL") {
		t.Error("V2 prompt should NOT contain v1 GeneratedSkillBody")
	}
	// 关键字 "legacy" 也不应出现（防御未来回归引入 v1 body 的 fallback）
	if strings.Contains(strings.ToLower(got), "legacy") {
		t.Errorf("V2 prompt should NOT contain 'legacy' keyword; got=%q", got)
	}
	// systemPrompt 必须保留
	if !strings.Contains(got, systemPrompt) {
		t.Errorf("V2 prompt should contain user-written system_prompt; got=%q", got)
	}
	// toolsHint 必须保留
	if !strings.Contains(got, toolsHint) {
		t.Errorf("V2 prompt should contain toolsHint; got=%q", got)
	}
	// 平台外壳（platform_head 头 + safety_footer 尾）必须仍然在
	if !strings.HasPrefix(got, skill.PlatformBasePrompt) {
		t.Error("V2 prompt should start with PlatformBasePrompt")
	}
	if !strings.HasSuffix(got, skill.PlatformSafetyFooter) {
		t.Error("V2 prompt should end with PlatformSafetyFooter")
	}

	// userContext 全空 → "## Memories" header 不应出现
	if strings.Contains(got, "## Memories") {
		t.Error("V2 prompt with empty userContext should NOT contain '## Memories' header")
	}
}

// TestPrompt_ShouldUseV2Prompt_ForkDecision 覆盖 runner.go:677 V2/Legacy 分叉判定。
// 4 种 (SystemPrompt × skills 状态被 ad 字段单独决定的部分)：
//   - nil ad → Legacy
//   - SystemPrompt = "" → Legacy
//   - SystemPrompt = 纯空白 "  \n\t  " → Legacy（trim 后空）
//   - SystemPrompt = "你是 XX" → V2
//
// 注意：skills 与否不影响 ShouldUseV2Prompt 自身——它是 D11 的前提判定，
// skills 维度由 runner.go 内部 len(skills) 决策叠加（在已选 V2 路径后决定 skillCatalog 是否传 body）。
func TestPrompt_ShouldUseV2Prompt_ForkDecision(t *testing.T) {
	cases := []struct {
		name string
		ad   *model.AgentDefinition
		want bool
	}{
		{"nil_ad", nil, false},
		{"empty_string", &model.AgentDefinition{SystemPrompt: ""}, false},
		{"only_whitespace", &model.AgentDefinition{SystemPrompt: "  \n\t  \r\n  "}, false},
		{"non_empty", &model.AgentDefinition{SystemPrompt: "你是销售助手"}, true},
		{"non_empty_with_leading_whitespace", &model.AgentDefinition{SystemPrompt: "  你是 XX  "}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldUseV2Prompt(tc.ad)
			if got != tc.want {
				t.Errorf("ShouldUseV2Prompt(%+v) = %v, want %v", tc.ad, got, tc.want)
			}
		})
	}
}

// TestPrompt_V2Path_EmptyAfterTrim_FallsBackToLegacy 是 spec §9 case 4 的覆盖。
// 验证 SystemPrompt = "  \n\t  " 时通过 ShouldUseV2Prompt 应走 Legacy 路径。
// runner.go:677 用 ShouldUseV2Prompt(ad) 做决策——本测试断言决策结果正确。
func TestPrompt_V2Path_EmptyAfterTrim_FallsBackToLegacy(t *testing.T) {
	ad := &model.AgentDefinition{SystemPrompt: "  \n\t  "}
	if ShouldUseV2Prompt(ad) {
		t.Errorf("纯空白 SystemPrompt 应走 Legacy 路径（ShouldUseV2Prompt 应返回 false），但返回 true")
	}
	// 同时验证 Legacy 出口产出仍然以 PlatformBasePrompt 起头（即使 SystemPrompt 是空白字符串，
	// Legacy 路径完全不参考此字段）。
	got := BuildSystemPromptLegacy(
		skill.PlatformBasePrompt,
		"", "[body]", "", "", "", "", "", "", "", "", skill.PlatformSafetyFooter,
	)
	if !strings.HasPrefix(got, skill.PlatformBasePrompt) {
		t.Error("Legacy 路径出口应以 PlatformBasePrompt 起头")
	}
}
