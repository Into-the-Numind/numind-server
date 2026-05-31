// open-tools-skill-as-guidance (T2): load_skill — the single skill-loading tool.
//
// It merges the two former tools after the permission layer was removed:
//   - use_skill (DB business skills, bound to the agent): body is parent-authored
//     markdown guidance, injected wrapped in <system-reminder>.
//   - read_skill (disk platform skills, <skills_root>/<name>/SKILL.md): body is a
//     code-gen recipe the LLM follows, then runs via run_python.
//
// After permission removal use_skill's only unique function (unlocking allowed_tools)
// vanished, so the two are homogeneous: both just inject a guidance string into the
// same agent context (single loop). load_skill resolves DB-first, disk-second.
//
// allowed_tools (DB skills) is no longer a whitelist — it is rendered as a
// "recommended tools" hint appended to the body (zero migration; the field stays).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"numind-server/internal/numind/biz/agent/skills"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// LoadSkillToolName 是平台 tool 标识，注册到 AgentToolRegistry。
const LoadSkillToolName = "load_skill"

// loadSkillMaxBodyBytes 是磁盘 SKILL.md 的防御性大小上限（沿用 read_skill 的 4096）。
const loadSkillMaxBodyBytes = 4096

// loadSkillTool implements FullTool for the unified load_skill built-in.
//
// registry (disk platform skills) may be nil — then load_skill serves only DB
// business skills via the ctx turn state. DB skills are looked up from
// UseSkillTurnState (runner-cached, 0 DB calls); disk skills from registry.Get.
type loadSkillTool struct {
	BaseTool
	registry skills.Registry
}

// NewLoadSkillTool constructs the load_skill FullTool. Passing nil registry is
// permitted (only DB-bound skills will resolve; disk lookups soft-error).
func NewLoadSkillTool(reg skills.Registry) FullTool {
	return &loadSkillTool{registry: reg}
}

var _ FullTool = (*loadSkillTool)(nil)

func (t *loadSkillTool) Name() string { return LoadSkillToolName }

func (t *loadSkillTool) Description() string {
	return "Load a skill's guidance into the conversation. Skills are listed in the " +
		"'可用技能' section of the system prompt — both this agent's bound skills and the " +
		"platform skills (xlsx-author / docx-author / pptx-author / pdf-from-html). " +
		"For structured files, load the matching skill, write Python per its guidance, then " +
		"call run_python. Input: {\"name\": string}."
}

func (t *loadSkillTool) UserFacingName() string      { return "加载技能" }
func (t *loadSkillTool) NarrationVerb() string       { return "加载技能" }
func (t *loadSkillTool) IsReadOnly() bool            { return true }
func (t *loadSkillTool) IsSearchOrReadCommand() bool { return true }
func (t *loadSkillTool) AlwaysLoad() bool            { return false }

// IsEnabled gates registration via the enable_skills tool_flags key (key name
// retained for zero-migration backwards compat; was read_skill's gate).
func (t *loadSkillTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableSkills }

func (t *loadSkillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "要加载的技能名称（必须是系统提示「可用技能」中列出的之一）"
    }
  },
  "required": ["name"]
}`)
}

// Execute resolves the named skill DB-first then disk, and returns its guidance.
// Never returns a non-nil Go error — all failures are soft errors in the tool
// result so the LLM can self-correct.
func (t *loadSkillTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &p); err != nil {
		return ToolResult(jsonErr("load_skill 参数解析失败：%s", err.Error())), nil
	}
	if p.Name == "" {
		return ToolResult(jsonErr("name 参数不能为空")), nil
	}

	turn, hasTurn := UseSkillTurnFromCtx(ctx)

	// cap check (only when a turn state exists — DB-bound agents). Disk-only agents
	// have no turn state and no cap, matching read_skill's prior behavior.
	if hasTurn && turn.InvocationCount >= turn.Cap {
		return ToolResult(jsonErr("已达本轮技能调用上限 (%d 次)，本轮无法再加载其他技能", turn.Cap)), nil
	}

	// 1. DB-first: bound business skill (runner-cached, 0 DB calls).
	if hasTurn {
		if sk, ok := turn.SkillByName[p.Name]; ok && sk != nil {
			// collision observability: a DB skill named like a platform skill shadows it (D3).
			if t.registry != nil {
				if _, derr := t.registry.Get(p.Name); derr == nil {
					log.Warnw("load_skill: DB skill shadows platform skill of the same name (DB wins)",
						"skill_name", p.Name)
				}
			}
			return t.loadDBSkill(ctx, turn, sk)
		}
	}

	// 2. disk: platform skill (SKILL.md on the server container disk).
	return t.loadDiskSkill(ctx, turn, hasTurn, p.Name)
}

// loadDBSkill injects a parent-authored business skill body wrapped in
// <system-reminder>, with the allowed_tools recommendation appended.
func (t *loadSkillTool) loadDBSkill(ctx context.Context, turn *UseSkillTurnState, sk *model.Skill) (ToolResult, error) {
	if !sk.IsActive {
		return ToolResult(jsonErr("技能 '%s' 已被禁用", sk.Name)), nil
	}
	if sk.BodyMd == "" {
		return ToolResult(jsonErr("技能 '%s' 内容为空，请联系配置者更新", sk.Name)), nil
	}

	// Langfuse span.
	var traceID, spanID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		traceID, spanID = tc.TraceID, langfuse.SpanID()
		langfuse.CreateSpan(traceID, spanID, "tool.load_skill.db",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{"skill_name": sk.Name, "skill_id": sk.ID}),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	// allowed_tools → recommendation hint (Move D). Not enforced; just shown to the LLM.
	recommended := parseRecommendedTools([]byte(sk.AllowedTools))
	bodyWithHint := sk.BodyMd
	if len(recommended) > 0 {
		bodyWithHint += "\n\n💡 推荐配合使用的工具：" + strings.Join(recommended, ", ")
	}

	bodyWrapped := fmt.Sprintf("<system-reminder>\n以下是你刚加载的技能 '%s' 的详细指引（v%d）。请按这些指引继续完成用户的任务：\n\n%s\n</system-reminder>",
		sk.Name, sk.Version, bodyWithHint)

	turn.PendingSkills = append(turn.PendingSkills, PendingSkill{
		Name:    sk.Name,
		Version: int(sk.Version),
		Body:    bodyWrapped,
	})
	turn.InvocationCount++

	ack := map[string]any{
		"status":            toolStatusLoaded,
		"source":            "db",
		"skill_name":        sk.Name,
		"skill_version":     sk.Version,
		"body_length":       len(sk.BodyMd),
		"body":              bodyWrapped,
		"recommended_tools": recommended,
		"turn_invocation":   turn.InvocationCount,
		"turn_cap":          turn.Cap,
		"message":           fmt.Sprintf("技能 '%s' 已载入对话上下文，请根据技能指引完成任务", sk.Name),
	}
	out, err := json.Marshal(ack)
	if err != nil || len(out) == 0 {
		out = []byte(fmt.Sprintf(`{"status":"loaded","source":"db","skill_name":%q,"message":"技能已载入"}`, sk.Name))
	}
	return ToolResult(out), nil
}

// loadDiskSkill reads a platform SKILL.md and returns it in the read_skill output
// shape (preserves the LLM's downstream run_python flow — I3).
func (t *loadSkillTool) loadDiskSkill(ctx context.Context, turn *UseSkillTurnState, hasTurn bool, name string) (ToolResult, error) {
	if t.registry == nil {
		return t.diskSoftError(turn, hasTurn, name, "skill %q not found (no skill registry configured)", name)
	}

	var traceID, spanID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		traceID, spanID = tc.TraceID, langfuse.SpanID()
		langfuse.CreateSpan(traceID, spanID, "tool.load_skill.disk",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{"skill_name": name}),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	entry, err := t.registry.Get(name)
	if err != nil {
		// the soft-error ack carries the full available-names list in its "available" field.
		return t.diskSoftError(turn, hasTurn, name, "skill %q not found", name)
	}

	skillMDPath := filepath.Clean(filepath.Join(entry.RootDir, "SKILL.md"))
	bodyBytes, err := os.ReadFile(skillMDPath)
	if err != nil {
		return t.diskSoftError(turn, hasTurn, name, "SKILL.md unreadable: %s", err.Error())
	}
	if len(bodyBytes) > loadSkillMaxBodyBytes {
		return t.diskSoftError(turn, hasTurn, name,
			"SKILL.md exceeds %d-byte cap (got %d) — file must be trimmed", loadSkillMaxBodyBytes, len(bodyBytes))
	}

	// count the disk load against the cap too, but only when a turn state exists.
	if hasTurn {
		turn.InvocationCount++
	}

	ack := map[string]any{
		"status":              toolStatusLoaded,
		"source":              "disk",
		"name":                entry.Manifest.Name,
		"description":         entry.Manifest.Description,
		"body_md":             string(bodyBytes),
		"max_runtime_seconds": entry.Manifest.EffectiveMaxRuntime(),
		"categories":          entry.Manifest.Categories,
		"message":             fmt.Sprintf("技能 '%s' 指引已加载，请按指引写代码后用 run_python 执行", entry.Manifest.Name),
	}
	out, err := json.Marshal(ack)
	if err != nil || len(out) == 0 {
		out = []byte(fmt.Sprintf(`{"status":"loaded","source":"disk","name":%q,"message":"技能指引已加载"}`, entry.Manifest.Name))
	}
	return ToolResult(out), nil
}

// diskSoftError returns a soft-error ack; it does NOT bump the cap (a failed
// lookup must not consume a turn slot — same invariant as use_skill).
func (t *loadSkillTool) diskSoftError(turn *UseSkillTurnState, hasTurn bool, name, format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(map[string]any{
		"status":     toolStatusError,
		"skill_name": name,
		"error":      msg,
		"available":  t.allSkillNames(turn, hasTurn),
	})
	return ToolResult(out), nil
}

// allSkillNames returns a sorted, comma-separated union of this agent's DB-bound
// skill names (from the turn state) and disk platform skill names, so the LLM can
// pick a valid one on a miss.
func (t *loadSkillTool) allSkillNames(turn *UseSkillTurnState, hasTurn bool) string {
	seen := map[string]struct{}{}
	var names []string
	add := func(n string) {
		if _, dup := seen[n]; dup {
			return
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	if hasTurn && turn != nil {
		for n := range turn.SkillByName {
			add(n)
		}
	}
	if t.registry != nil {
		for _, m := range t.registry.List() {
			add(m.Name)
		}
	}
	if len(names) == 0 {
		return "(none)"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// parseRecommendedTools parses a skill's allowed_tools JSON ([]string) into a
// recommendation list. Malformed JSON → nil (ignored; no enforcement either way).
func parseRecommendedTools(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var tools []string
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	return tools
}
