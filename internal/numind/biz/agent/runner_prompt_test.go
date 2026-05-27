package agent

import (
	"strings"
	"testing"

	"numind-server/internal/numind/biz/skill"
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

// TestBuildSystemPromptV2_FourSegments 验证 happy path：四段都注入，含 platform 常量、
// institution/userContext 输入；段间分隔符是 "\n\n"（PromptSegments.Render 的契约）。
//
// 注意：PlatformBasePrompt 自身末尾带 \n、PlatformSafetyFooter 自身开头带 \n，
// 与 "\n\n" 分隔符拼接后会出现 3+ 连续 newline——这是 platform 常量的固有性质，
// 与 legacy 路径（runner.go:676-687 字符串直拼）行为一致，不是 bug。
func TestBuildSystemPromptV2_FourSegments(t *testing.T) {
	institution := "你是 XX 公司销售助手"
	userCtx := "## Memories\n用户喜欢简洁回复"
	got := BuildSystemPromptV2(institution, userCtx)

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
	got := BuildSystemPromptV2("", "")

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
	want = "## Memories\n" + "" + "" + "" + "" + "免责声明：" + "用户偏好 A"
	if got != want {
		t.Errorf("memorySystem + disclaimer:\n  want: %q\n  got:  %q", want, got)
	}

	// 所有 5 个 block 都非空 — 顺序：agentMd → selector → dialectic → temporal → disclaimer → memorySystem
	got = BuildUserContextSection("A", "S", "D", "T", "Disc", "M")
	want = "## Memories\n" + "A" + "S" + "D" + "T" + "Disc" + "M"
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
