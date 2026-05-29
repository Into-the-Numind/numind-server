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
)

// readSkillInput is the JSON input for the read_skill tool.
type readSkillInput struct {
	SkillName string `json:"skill_name"`
}

// readSkillOutput is the JSON output returned by the read_skill tool.
type readSkillOutput struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	BodyMarkdown      string   `json:"body_md"`
	MaxRuntimeSeconds int      `json:"max_runtime_seconds"`
	Categories        []string `json:"categories,omitempty"`
}

// readSkillTool implements FullTool for the read_skill built-in.
//
// Semantics (Codex-style progressive disclosure):
//
//	system_prompt 在 §2 Institution Section 注入 skill catalog (name+description+提示)；
//	LLM 决定使用某个 skill 时调用 read_skill({skill_name}) 获得 SKILL.md 全文；
//	再用 run_python 执行按 SKILL.md 示例编写的真实代码。
//
// 不使用 sandbox — 直接从 server 容器 disk 读 /app/skills/<name>/SKILL.md。
// nil registry 容忍：NewReadSkillTool(nil) 允许；每次 Execute 返回 soft error。
type readSkillTool struct {
	BaseTool
	registry skills.Registry
}

// NewReadSkillTool constructs a readSkillTool with the given skill registry.
// Passing nil is permitted (Execute will soft-error with "registry not configured").
func NewReadSkillTool(reg skills.Registry) FullTool {
	return &readSkillTool{registry: reg}
}

var _ FullTool = (*readSkillTool)(nil)

func (t *readSkillTool) Name() string { return "read_skill" }
func (t *readSkillTool) Description() string {
	return "Read the full guidance for a skill listed in the skill catalog. " +
		"Use this when you decide to generate a structured file (PowerPoint, Excel, Word, PDF) — " +
		"read the skill's SKILL.md to learn the exact Python code patterns, then call run_python to execute. " +
		"Input: { skill_name: string }. " +
		"Returns: { name, description, body_md, max_runtime_seconds, categories? }."
}
func (t *readSkillTool) UserFacingName() string      { return "读取技能指南" }
func (t *readSkillTool) NarrationVerb() string       { return "查阅技能" }
func (t *readSkillTool) IsReadOnly() bool            { return true }
func (t *readSkillTool) IsSearchOrReadCommand() bool { return true }
func (t *readSkillTool) AlwaysLoad() bool            { return false }

// IsEnabled gates registration via the enable_skills tool_flags JSON key
// (reused from the prior invoke_skill semantics — same flag now gates read_skill).
func (t *readSkillTool) IsEnabled(cfg ToolConfig) bool {
	return cfg.EnableSkills
}

// readSkillMaxBodyBytes is a defensive runtime cap on SKILL.md size.
// The build-time gate (S4 Task 5) enforces ≤4096 per file; this cap catches
// disk-level surprises (someone edited the file post-deploy).
const readSkillMaxBodyBytes = 4096

func (t *readSkillTool) returnSoftError(skillName, format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(readSkillOutput{
		Name:         skillName,
		Description:  "",
		BodyMarkdown: "ERROR: " + msg,
	})
	return ToolResult(out), nil
}

// Execute returns the SKILL.md body for the requested skill.
//
// All validation failures return (ToolResult, nil) with "ERROR: ..." content so
// the LLM sees the message and can self-correct, mirroring the soft-error
// pattern used by file_read post-2026-05-29-Hotfix-A. Codex equivalent:
// codex-rs/tools/src/function_call_error.rs `RespondToModel` vs `Fatal`.
func (t *readSkillTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in readSkillInput
	if err := json.Unmarshal(input, &in); err != nil {
		return t.returnSoftError("", "invalid input JSON: %s", err.Error())
	}
	if in.SkillName == "" {
		return t.returnSoftError("", "skill_name is required")
	}
	if t.registry == nil {
		return t.returnSoftError(in.SkillName, "skill registry not configured")
	}

	// Langfuse span for the read.
	var spanID string
	var traceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		traceID = tc.TraceID
		langfuse.CreateSpan(tc.TraceID, spanID, "tool.read_skill.execute",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(in),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	entry, err := t.registry.Get(in.SkillName)
	if err != nil {
		// "skill not found" — surface available names so LLM can pick a valid one.
		available := availableSkillNames(t.registry)
		return t.returnSoftError(in.SkillName,
			"skill %q not found. available: %s", in.SkillName, available)
	}

	// SKILL.md path is always <root>/<skill>/SKILL.md by registry construction.
	// `entry.RootDir` was filepath.Join(skillsRoot, name) — no user-controlled
	// component, so no path-traversal concern. Defensive: clean it anyway.
	skillMDPath := filepath.Clean(filepath.Join(entry.RootDir, "SKILL.md"))

	bodyBytes, err := os.ReadFile(skillMDPath)
	if err != nil {
		return t.returnSoftError(in.SkillName, "SKILL.md unreadable: %s", err.Error())
	}
	if len(bodyBytes) > readSkillMaxBodyBytes {
		return t.returnSoftError(in.SkillName,
			"SKILL.md exceeds %d-byte cap (got %d) — file must be trimmed",
			readSkillMaxBodyBytes, len(bodyBytes))
	}

	out, _ := json.Marshal(readSkillOutput{
		Name:              entry.Manifest.Name,
		Description:       entry.Manifest.Description,
		BodyMarkdown:      string(bodyBytes),
		MaxRuntimeSeconds: entry.Manifest.EffectiveMaxRuntime(),
		Categories:        entry.Manifest.Categories,
	})
	return ToolResult(out), nil
}

// availableSkillNames returns a sorted comma-separated list of skill names for
// error messages. Stable order helps both LLM consumption and snapshot tests.
func availableSkillNames(reg skills.Registry) string {
	if reg == nil {
		return "(none)"
	}
	mans := reg.List()
	names := make([]string, 0, len(mans))
	for _, m := range mans {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}
