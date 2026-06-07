package agent

import (
	"strings"
	"testing"

	"numind-server/internal/numind/biz/skill"
	"numind-server/internal/pkg/model"
)

// TestPromptSegments_Append_FiltersEmpty 验证 Append 过滤纯空白段。
func TestPromptSegments_Append_FiltersEmpty(t *testing.T) {
	ps := &PromptSegments{}
	ps.Append("a", "")
	ps.Append("b", "   \n\t ")
	ps.Append("c", "real content")
	if len(ps.Segments) != 1 {
		t.Fatalf("expected 1 segment after filter, got %d", len(ps.Segments))
	}
	if ps.Segments[0].Name != "c" {
		t.Errorf("expected name c, got %s", ps.Segments[0].Name)
	}
	if ps.Segments[0].Text != "real content" {
		t.Errorf("expected text 'real content', got %q", ps.Segments[0].Text)
	}
}

// TestPromptSegments_Render_NoExtraNewlines 验证 Render 用 "\n\n" 拼段，不产生 3+
// 连续 newline。空段已被 Append 过滤，所以拼接结果干净。
func TestPromptSegments_Render_NoExtraNewlines(t *testing.T) {
	ps := &PromptSegments{}
	ps.Append("a", "alpha")
	ps.Append("b", "")        // 过滤
	ps.Append("c", "  \n\t ") // 过滤
	ps.Append("d", "delta")

	got := ps.Render()
	want := "alpha\n\ndelta"
	if got != want {
		t.Errorf("Render mismatch:\n  want: %q\n  got:  %q", want, got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("found 3+ consecutive newlines in Render output: %q", got)
	}
}

// TestBuildSystemPromptV2_PlatformInstitutionUserSegments 验证 happy path：platform /
// institution / userContext 段都注入，含 platform 常量、institution/userContext 输入；
// 段间分隔符是 "\n\n"（PromptSegments.Render 的契约）。新增的 tenant_hard_rules 段
// 由 TestBuildSystemPromptV2_HardRules 单独覆盖（本测试传 "" 故不含该段）。
//
// 注意：PlatformBasePrompt 自身末尾带 \n、PlatformSafetyFooter 自身开头带 \n，
// 与 "\n\n" 分隔符拼接后会出现 3+ 连续 newline——这是 platform 常量的固有性质，
// 与 legacy 路径（BuildSystemPromptLegacy 字符串直拼）行为一致，不是 bug。
func TestBuildSystemPromptV2_PlatformInstitutionUserSegments(t *testing.T) {
	institution := "你是 XX 公司销售助手"
	userCtx := "## Memories\n用户喜欢简洁回复"
	// T5 (#1a): BuildSystemPromptV2 now takes tenantHardRules as first arg.
	// Empty here keeps this test focused on the institution/userContext segments;
	// the tenant_hard_rules segment is covered by TestBuildSystemPromptV2_HardRules below.
	got := BuildSystemPromptV2("", institution, userCtx)

	if !strings.Contains(got, skill.PlatformBasePrompt) {
		t.Errorf("missing PlatformBasePrompt in output")
	}
	if !strings.Contains(got, institution) {
		t.Errorf("missing institution section in output")
	}
	if !strings.Contains(got, userCtx) {
		t.Errorf("missing userContext section in output")
	}
	if !strings.Contains(got, skill.PlatformSafetyFooter) {
		t.Errorf("missing PlatformSafetyFooter in output")
	}

	// 段间分隔符是 "\n\n" — 验证 institution 与 userCtx 之间正好两个 newline
	idxInst := strings.Index(got, institution)
	idxUser := strings.Index(got, userCtx)
	if idxInst < 0 || idxUser < 0 || idxUser <= idxInst {
		t.Fatalf("institution / userContext order broken: instIdx=%d, userIdx=%d", idxInst, idxUser)
	}
	between := got[idxInst+len(institution) : idxUser]
	if between != "\n\n" {
		t.Errorf("expected exactly '\\n\\n' between institution and userContext, got %q", between)
	}

	// 整体顺序：head → institution → userCtx → footer
	idxHead := strings.Index(got, skill.PlatformBasePrompt)
	idxFoot := strings.Index(got, skill.PlatformSafetyFooter)
	if !(idxHead < idxInst && idxInst < idxUser && idxUser < idxFoot) {
		t.Errorf("segment order broken: head=%d inst=%d user=%d foot=%d", idxHead, idxInst, idxUser, idxFoot)
	}
}

// TestBuildSystemPromptV2_AllEmpty 验证当 institution 和 userContext 都为空时，
// 仍包含 PlatformBasePrompt + PlatformSafetyFooter（两个常量非空），且段间用 "\n\n" 连接。
func TestBuildSystemPromptV2_AllEmpty(t *testing.T) {
	// T5 (#1a): all three inputs empty (tenantHardRules, institution, userContext).
	got := BuildSystemPromptV2("", "", "")

	if !strings.Contains(got, skill.PlatformBasePrompt) {
		t.Errorf("missing PlatformBasePrompt when both inputs empty")
	}
	if !strings.Contains(got, skill.PlatformSafetyFooter) {
		t.Errorf("missing PlatformSafetyFooter when both inputs empty")
	}
	// 期望恰好两段：head + footer，用 "\n\n" 连接
	want := skill.PlatformBasePrompt + "\n\n" + skill.PlatformSafetyFooter
	if got != want {
		t.Errorf("expected only platform_head + platform_safety_footer joined by '\\n\\n':\n  want: %q\n  got:  %q", want, got)
	}
}

// TestBuildInstitutionSection_EmptyParts 验证空子段被过滤；剩余子段用 "\n\n" 分隔。
func TestBuildInstitutionSection_EmptyParts(t *testing.T) {
	// 全空 → 空字符串
	if got := BuildInstitutionSection("", "", ""); got != "" {
		t.Errorf("all-empty inputs should produce empty string, got %q", got)
	}

	// 只有 systemPrompt
	if got := BuildInstitutionSection("sys", "", ""); got != "sys" {
		t.Errorf("only systemPrompt: want %q, got %q", "sys", got)
	}

	// 只有 toolsHint（其它空）
	if got := BuildInstitutionSection("", "", "tools"); got != "tools" {
		t.Errorf("only toolsHint: want %q, got %q", "tools", got)
	}

	// systemPrompt + toolsHint（中间 catalog 空 → 跳过）
	got := BuildInstitutionSection("sys", "", "tools")
	want := "sys\n\ntools"
	if got != want {
		t.Errorf("sys+tools (no catalog):\n  want: %q\n  got:  %q", want, got)
	}

	// 三段都非空
	got = BuildInstitutionSection("sys", "catalog", "tools")
	want = "sys\n\ncatalog\n\ntools"
	if got != want {
		t.Errorf("all three:\n  want: %q\n  got:  %q", want, got)
	}

	// 纯空白也算空段
	if got := BuildInstitutionSection("  \n\t ", "", "  "); got != "" {
		t.Errorf("whitespace-only inputs should be filtered, got %q", got)
	}
}

// TestBuildInstitutionSection_SpecialChars 验证 CJK / 换行 / 双引号 不会被错误转义或截断。
func TestBuildInstitutionSection_SpecialChars(t *testing.T) {
	systemPrompt := "你是【XX 公司】的销售助手。\n职责：帮销售应对客户异议。\n规则：永远不说\"对不起\"。"
	skillCatalog := "可用 skill：\n- 推单话术\n- 投诉转人工"
	toolsHint := "工具优先级：Go 工具 > invoke_skill"

	got := BuildInstitutionSection(systemPrompt, skillCatalog, toolsHint)

	// 整段保真：原文 substring 仍能找到
	if !strings.Contains(got, systemPrompt) {
		t.Errorf("systemPrompt content lost / escaped: not found in output")
	}
	if !strings.Contains(got, skillCatalog) {
		t.Errorf("skillCatalog content lost / escaped: not found in output")
	}
	if !strings.Contains(got, toolsHint) {
		t.Errorf("toolsHint content lost / escaped: not found in output")
	}

	// 双引号 / CJK / 内部 \n 保留
	if !strings.Contains(got, "你是【XX 公司】") {
		t.Errorf("CJK + 全角括号丢失")
	}
	if !strings.Contains(got, `不说"对不起"`) {
		t.Errorf("双引号被错误转义或丢失")
	}
	if !strings.Contains(got, "职责：帮销售应对客户异议。") {
		t.Errorf("内部换行被截断")
	}
}

// TestBuildUserContextSection_NoneSet 验证 5 个判定 block 全空 → 整段为空。
// disclaimer 单独设值不应触发 header（D9：disclaimer 与 memorySystem 同进同退）。
func TestBuildUserContextSection_NoneSet(t *testing.T) {
	if got := BuildUserContextSection("", "", "", "", "", ""); got != "" {
		t.Errorf("all-empty: want empty, got %q", got)
	}
}

// TestBuildUserContextSection_SomeSet 验证任一 memory block 非空 → 挂 "## Memories" header
// + 5 个 block 顺序拼接。
func TestBuildUserContextSection_SomeSet(t *testing.T) {
	// 只有 agentMd
	got := BuildUserContextSection("\n\n[agent.md rules]", "", "", "", "", "")
	want := "## Memories\n\n\n[agent.md rules]"
	if got != want {
		t.Errorf("only agentMd:\n  want: %q\n  got:  %q", want, got)
	}

	// 只有 memorySystem（+ disclaimer 同时挂上，因 disclaimer 与 memorySystem 同进同退）
	got = BuildUserContextSection("", "", "", "", "免责声明：", "用户偏好 A")
	want = "## Memories\n免责声明：用户偏好 A"
	if got != want {
		t.Errorf("memorySystem + disclaimer:\n  want: %q\n  got:  %q", want, got)
	}

	// 所有 5 个 block 都非空 — 顺序：agentMd → selector → dialectic → temporal → disclaimer → memorySystem
	got = BuildUserContextSection("A", "S", "D", "T", "Disc", "M")
	want = "## Memories\nASDTDiscM"
	if got != want {
		t.Errorf("all blocks set:\n  want: %q\n  got:  %q", want, got)
	}

	// header 字面值：以 "## Memories\n" 开头（不是 "\n\n## Memories\n"，前导 \n\n 由 Render 加）
	if !strings.HasPrefix(got, "## Memories\n") {
		prefixLen := 20
		if len(got) < prefixLen {
			prefixLen = len(got)
		}
		t.Errorf("expected output to start with '## Memories\\n' (no leading newlines), got prefix %q", got[:prefixLen])
	}
}

// TestBuildUserContextSection_DisclaimerOnlyImpossible 是 sanity / 不变量测试：
// 即使 caller 只填了 memoryDisclaimer（其它 5 个 block 空），disclaimer 不应单独出现。
// 这强制保证 D9 决策：disclaimer 与 memorySystem 同进同退，不会出现"disclaimer 孤儿"。
func TestBuildUserContextSection_DisclaimerOnlyImpossible(t *testing.T) {
	got := BuildUserContextSection("", "", "", "", "免责声明：仅参考", "")
	if got != "" {
		t.Errorf("disclaimer-only (5 判定 block 全空) 应输出空，但得到 %q", got)
	}
	if strings.Contains(got, "免责声明") {
		t.Errorf("disclaimer 不应在判定 block 全空时出现在输出，但 found in %q", got)
	}
}

// TestBuildSystemPromptV2_DropsLegacyBodyWhenNoSkillCatalog 验证 D11：
// V2 路径调用 BuildInstitutionSection 时，调用方应在 skills 空时传 skillCatalog=""。
// 此处直接测试 BuildInstitutionSection 在 skillCatalog="" 时只渲染 systemPrompt + toolsHint，
// 不混入任何 "legacy junk"。
func TestBuildSystemPromptV2_DropsLegacyBodyWhenNoSkillCatalog(t *testing.T) {
	got := BuildInstitutionSection(
		"你是销售助手", // systemPrompt
		"",       // skillCatalog 空（模拟 D11 决策：runner 在 len(skills)==0 时传空）
		"## 工具优先级\n...",
	)
	if strings.Contains(got, "legacy") {
		t.Errorf("BuildInstitutionSection 不应包含 legacy 内容; got=%q", got)
	}
	if !strings.Contains(got, "你是销售助手") {
		t.Errorf("BuildInstitutionSection 应包含 systemPrompt; got=%q", got)
	}
	if !strings.Contains(got, "## 工具优先级") {
		t.Errorf("BuildInstitutionSection 应包含 toolsHint; got=%q", got)
	}
}

// TestBuildSystemPromptV2_HardRules 覆盖 #1a：V2 路径此前 DROP 了 tenantHardRules
// （complianceGate.SystemPromptBlock 产出的 L0/L1 平台+租户硬规则），机构设了
// system_prompt 时硬规则被静默丢弃。修复后 BuildSystemPromptV2 把 tenantHardRules
// 作为 §2 段注入，位置在 platform_head 之后、institution 之前（镜像 legacy 顺序）。
func TestBuildSystemPromptV2_HardRules(t *testing.T) {
	const (
		hardRules   = "## 平台与租户硬规则\nL0：禁止泄露其它租户数据。\nL1：禁止编造客户隐私。"
		institution = "你是 XX 公司销售助手"
		userCtx     = "## Memories\n用户喜欢简洁回复"
	)
	got := BuildSystemPromptV2(hardRules, institution, userCtx)

	// 硬规则必须出现（#1a 核心断言）
	if !strings.Contains(got, hardRules) {
		t.Fatalf("V2 prompt 缺少 tenant hard rules（#1a 回归）; got=%q", got)
	}

	// 位置：platform_head 之后、institution 之前
	idxHead := strings.Index(got, skill.PlatformBasePrompt)
	idxHard := strings.Index(got, hardRules)
	idxInst := strings.Index(got, institution)
	idxUser := strings.Index(got, userCtx)
	idxFoot := strings.Index(got, skill.PlatformSafetyFooter)
	if !(idxHead < idxHard && idxHard < idxInst && idxInst < idxUser && idxUser < idxFoot) {
		t.Errorf("V2 段顺序错误（期望 head < hardRules < institution < userCtx < footer）: head=%d hard=%d inst=%d user=%d foot=%d",
			idxHead, idxHard, idxInst, idxUser, idxFoot)
	}

	// 段间分隔符 "\n\n"：hardRules 与 institution 之间正好两个 newline
	between := got[idxHard+len(hardRules) : idxInst]
	if between != "\n\n" {
		t.Errorf("hardRules 与 institution 间应为 '\\n\\n'，got %q", between)
	}

	// 空 hardRules 时该段被 Append 过滤（不引入空段 / 多余空行）
	gotEmpty := BuildSystemPromptV2("", institution, userCtx)
	if strings.Contains(gotEmpty, "\n\n\n\n") {
		t.Errorf("空 hardRules 不应留下空段造成 4+ 连续 newline; got=%q", gotEmpty)
	}
}

// TestAssembleSystemPrompt_V2InjectsSystemPromptAndHardRules 覆盖 #3 + #1a 的端到端：
// 共享 assembler 在 ad.SystemPrompt 非空时走 V2 分支，产出的 prompt 必须同时包含
// ad.SystemPrompt（行为指引——这正是 RunStream 之前 DROP 的，#3）与 tenantHardRules
// （#1a）。RunStream 现在调同一个 assembler，所以这条断言等价于证明 RunStream 两者都注入。
func TestAssembleSystemPrompt_V2InjectsSystemPromptAndHardRules(t *testing.T) {
	r := &agentRunner{} // platformSkillRegistry == nil → buildUnifiedSkillCatalog(nil,nil) == ""
	const (
		systemPrompt = "你是【XX 公司】销售助手，永远保持专业语气"
		hardRules    = "## 平台硬规则\n禁止跨租户数据访问"
	)
	ad := &model.AgentDefinition{SystemPrompt: systemPrompt}

	got := r.assembleSystemPrompt(
		ad,
		hardRules, // tenantHardRulesPlaceholder
		"",        // body（skills 为空 → 走 D11 丢弃分支）
		nil,       // skills
		"",        // agentMd
		"",        // selector
		"",        // dialectic
		"",        // temporal
		"",        // memoryDisclaimer
		"",        // memorySystem
		"",        // memoriesSectionHeader（legacy 分支才用，V2 不用）
		"",        // toolsSection
	)

	// #3：行为指引（ad.SystemPrompt）必须注入
	if !strings.Contains(got, systemPrompt) {
		t.Errorf("#3 回归：assembler V2 分支缺少 ad.SystemPrompt（行为指引）; got=%q", got)
	}
	// #1a：tenant hard rules 必须注入
	if !strings.Contains(got, hardRules) {
		t.Errorf("#1a 回归：assembler V2 分支缺少 tenant hard rules; got=%q", got)
	}
	// 平台外壳仍在
	if !strings.HasPrefix(got, skill.PlatformBasePrompt) {
		t.Error("assembler V2 输出应以 PlatformBasePrompt 起头")
	}
	if !strings.HasSuffix(got, skill.PlatformSafetyFooter) {
		t.Error("assembler V2 输出应以 PlatformSafetyFooter 结尾")
	}
	// 顺序：head < hardRules < systemPrompt
	idxHead := strings.Index(got, skill.PlatformBasePrompt)
	idxHard := strings.Index(got, hardRules)
	idxSys := strings.Index(got, systemPrompt)
	if !(idxHead < idxHard && idxHard < idxSys) {
		t.Errorf("assembler V2 段顺序错误: head=%d hard=%d sys=%d", idxHead, idxHard, idxSys)
	}
}

// TestAssembleSystemPrompt_LegacyByteIdenticalToBuildLegacy 是无回归守护：
// ad.SystemPrompt 为空时 assembler 走 legacy 分支，输出必须与直接调用
// BuildSystemPromptLegacy 字节一致——证明 legacy / 空 system_prompt 这条（含 RunStream
// 之前的 flat 拼装语义）完全没变。RunStream 旧 flat 拼装与 BuildSystemPromptLegacy
// 已逐字段核对为字节等价（platformBase + tenantHardRules + body + memoriesHeader +
// agentMd + selector + dialectic + temporal + memoryDisclaimer + memorySystem +
// toolsSection + platformFooter，纯 + 直拼无额外分隔符），故此测试也守护 RunStream legacy 路径。
func TestAssembleSystemPrompt_LegacyByteIdenticalToBuildLegacy(t *testing.T) {
	r := &agentRunner{}
	const (
		hardRules    = "## 硬规则\nL0/L1"
		body         = "[v1 generated body or skill catalog]"
		agentMd      = "\n\n## Agent Rules\nrule1"
		selector     = "\n\n<personal_context>fact</personal_context>"
		dialectic    = "\n\n<insight>insight</insight>"
		temporal     = "\n\n<digest>digest</digest>"
		disclaimer   = "\n\n[disclaimer]\n"
		memorySys    = "memory facts"
		toolsSection = "\n\n## Output Tool Priority\n"
		memHeader    = "\n\n## Memories\n"
	)
	// ad.SystemPrompt == "" → legacy 分支
	ad := &model.AgentDefinition{SystemPrompt: ""}

	got := r.assembleSystemPrompt(
		ad,
		hardRules,
		body,
		nil, // skills（legacy 分支不读 len(skills)）
		agentMd,
		selector,
		dialectic,
		temporal,
		disclaimer,
		memorySys,
		memHeader,
		toolsSection,
	)

	want := BuildSystemPromptLegacy(
		skill.PlatformBasePrompt,
		hardRules,
		body,
		memHeader,
		agentMd,
		selector,
		dialectic,
		temporal,
		disclaimer,
		memorySys,
		toolsSection,
		skill.PlatformSafetyFooter,
	)

	if got != want {
		t.Errorf("legacy 分支输出与 BuildSystemPromptLegacy 不一致（无回归被破坏）:\n  want: %q\n  got:  %q", want, got)
	}
}
