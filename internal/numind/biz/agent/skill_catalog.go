package agent

import (
	"fmt"
	"sort"
	"strings"

	"numind-server/internal/numind/biz/agent/skills"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// skillCatalogTotalCharCap is the soft cap for the rendered catalog. Includes
// header boilerplate (~300 chars) — skill entries get ~1700 chars. When the
// total would exceed this, trailing disk skills are dropped with a WARN log.
const skillCatalogTotalCharCap = 2000

// skillCatalogDescriptionCharCap clamps a single skill's description so one
// pathological manifest doesn't crowd out everyone else.
const skillCatalogDescriptionCharCap = 200

// skillCatalogHeader is the boilerplate that tells the agent LLM how to use
// skills (single-loop progressive disclosure: the same agent loads the guidance
// via load_skill, then writes + runs the real code via run_python).
const skillCatalogHeader = `## 可用技能（Skills）

需要某个技能时，用 load_skill({"name":"<技能名>"}) 把它的详细指引载入对话。
生成 PPT/Excel/Word/PDF 等结构化文件时：
**重要**: 不要直接编写 Python 代码 — 必须先 load_skill 读取详细指南，按指南示例写代码，然后通过 run_python 执行。

可用技能：
`

const skillCatalogFooter = `
工作流（结构化文件）：
1. 加载技能指南: load_skill({"name": "<选定技能>"}) → 看返回的 body_md
2. 按指南示例编写完整 Python 代码
3. 执行: run_python({"code": "<完整代码>", "input_files": [<用户上传 URL，可选>]})
4. run_python 返回 {files: [{url: "https://...agent-outputs/.../result.pptx"}]} — 把 url 嵌入最终回答
`

// buildUnifiedSkillCatalog renders the single "## 可用技能" §2 block listing BOTH
// this agent's DB-bound business skills and the disk platform skills, instructing
// load_skill. It replaces the former two-renderer split (buildSkillCatalogBlock for
// DB skills + RenderSkillCatalog for disk skills) — open-tools-skill-as-guidance.
//
// Returns "" when there are no skills at all (caller treats "" as "no catalog").
//
// Ordering: DB-bound skills first (binding order, as passed), then disk skills
// alphabetical (cache-friendly, snapshot-stable). On a name collision the DB skill
// wins (D3): a disk skill whose name also names a DB skill is dropped + WARN-logged,
// so the catalog has exactly one entry per name and the LLM never sees a duplicate.
//
// Soft caps: each disk description trimmed to skillCatalogDescriptionCharCap;
// total output soft-capped at skillCatalogTotalCharCap (trailing disk skills
// dropped + WARN). DB skills are not dropped (the agent owner bound them on purpose).
func buildUnifiedSkillCatalog(dbSkills []model.Skill, reg skills.Registry) string {
	// Active DB skills + their names for collision dedup.
	dbNames := make(map[string]struct{}, len(dbSkills))
	dbActive := make([]*model.Skill, 0, len(dbSkills))
	for i := range dbSkills {
		sk := &dbSkills[i]
		if !sk.IsActive {
			continue
		}
		dbNames[sk.Name] = struct{}{}
		dbActive = append(dbActive, sk)
	}

	// Disk skills, sorted, minus any shadowed by a DB skill of the same name.
	var diskMans []skills.SkillManifest
	if reg != nil {
		for _, m := range reg.List() {
			if _, shadowed := dbNames[m.Name]; shadowed {
				log.Warnw("skill catalog: disk platform skill shadowed by DB skill of same name (DB wins)",
					"skill_name", m.Name)
				continue
			}
			diskMans = append(diskMans, m)
		}
		sort.Slice(diskMans, func(i, j int) bool { return diskMans[i].Name < diskMans[j].Name })
	}

	if len(dbActive) == 0 && len(diskMans) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(skillCatalogHeader)

	// DB-bound business skills first.
	for _, sk := range dbActive {
		b.WriteString(fmt.Sprintf("- **%s**：%s\n", sk.Name, sk.Description))
		if sk.WhenToUse != "" {
			b.WriteString(fmt.Sprintf("  - 何时使用：%s\n", sk.WhenToUse))
		}
	}

	// Disk platform skills, with the total-length soft cap (DB skills never dropped).
	dropped := 0
	for _, m := range diskMans {
		desc := truncateUnicode(m.Description, skillCatalogDescriptionCharCap)
		entry := "- `" + m.Name + "`: " + desc + "\n"
		if b.Len()+len(entry)+len(skillCatalogFooter) > skillCatalogTotalCharCap {
			dropped++
			continue
		}
		b.WriteString(entry)
	}
	b.WriteString(skillCatalogFooter)

	if dropped > 0 {
		log.Warnw("unified skill catalog truncated by char cap",
			"dropped_disk_skills", dropped,
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
