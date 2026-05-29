package agent

import (
	"sort"
	"strings"

	"numind-server/internal/numind/biz/agent/skills"
	"numind-server/internal/pkg/log"
)

// skillCatalogTotalCharCap is the soft cap for the rendered catalog. Includes
// header boilerplate (~300 chars) — skill entries get ~1700 chars. When the
// total would exceed this, trailing skills are dropped with a WARN log.
const skillCatalogTotalCharCap = 2000

// skillCatalogDescriptionCharCap clamps a single skill's description so one
// pathological manifest doesn't crowd out everyone else.
const skillCatalogDescriptionCharCap = 200

// skillCatalogHeader is the boilerplate that tells the outer agent LLM how to
// use skills (Codex-style progressive disclosure: model opens SKILL.md itself
// via read_skill, then run_python executes the real code).
const skillCatalogHeader = `## 可用技能（Skills）

需要生成 PPT/Excel/Word/PDF 等结构化文件时，使用以下技能。
**重要**: 不要直接编写 Python 代码 — 必须先调用 read_skill 读取详细指南，按指南示例写代码，然后通过 run_python 执行。

可用技能：
`

const skillCatalogFooter = `
工作流：
1. 读取技能指南: read_skill({"skill_name": "<选定技能>"}) → 看返回的 body_md
2. 按指南示例编写完整 Python 代码
3. 执行: run_python({"code": "<完整代码>", "input_files": [<用户上传 URL，可选>]})
4. run_python 返回 {files: [{url: "https://...agent-outputs/.../result.pptx"}]} — 把 url 嵌入最终回答
`

// RenderSkillCatalog returns the §2 institution-section block listing the
// available skills + the read_skill / run_python workflow guidance.
//
// Returns "" when reg is nil OR when the registry has zero skills — caller
// (runner.go) treats "" as "no skill catalog needed" and BuildInstitutionSection
// joins remaining segments without an empty line.
//
// Ordering is deterministic alphabetical by skill name, which keeps the LLM
// prompt cache-friendly and snapshot tests stable.
//
// Soft caps applied:
//   - Each skill's description trimmed to skillCatalogDescriptionCharCap chars
//     (200) with a single-character ellipsis "…" suffix.
//   - Total output length soft-capped at skillCatalogTotalCharCap (2000), with
//     trailing skills dropped + a WARN log when the cap is exceeded.
func RenderSkillCatalog(reg skills.Registry) string {
	if reg == nil {
		return ""
	}
	mans := reg.List()
	if len(mans) == 0 {
		return ""
	}
	// Sort by Name for determinism.
	sort.Slice(mans, func(i, j int) bool { return mans[i].Name < mans[j].Name })

	var b strings.Builder
	b.WriteString(skillCatalogHeader)

	dropped := 0
	for _, m := range mans {
		desc := truncateUnicode(m.Description, skillCatalogDescriptionCharCap)
		entry := "- `" + m.Name + "`: " + desc + "\n"
		// Speculative add — if the total would blow the cap, abort and log.
		// We always include the footer in the budget so footer never gets
		// truncated.
		projected := b.Len() + len(entry) + len(skillCatalogFooter)
		if projected > skillCatalogTotalCharCap {
			dropped++
			continue
		}
		b.WriteString(entry)
	}
	b.WriteString(skillCatalogFooter)

	if dropped > 0 {
		log.Warnw("skill catalog truncated by char cap",
			"dropped_skills", dropped,
			"total_chars_cap", skillCatalogTotalCharCap,
			"total_chars_emitted", b.Len(),
		)
	}
	return b.String()
}

// truncateUnicode trims s to at most n runes, appending "…" when it actually
// truncates (counted in the same char budget — i.e. the result has at most
// n+1 runes, but call sites treat n+1 vs n as equivalent for budget math).
func truncateUnicode(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}
