package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/skills"
)

// buildDiskRegistry writes a minimal valid skill (manifest.json + SKILL.md) under
// a temp skills_root and returns a Registry over it.
func buildDiskRegistry(t *testing.T, name, desc, skillMD string) skills.Registry {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	manifest := map[string]any{
		"name": name, "version": "1.0.0", "description": desc, "max_runtime_seconds": 30,
	}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o644))
	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)
	return reg
}

func execLoadSkill(t *testing.T, tool FullTool, ctx context.Context, name string) map[string]any {
	t.Helper()
	out, err := tool.Execute(ctx, ToolInput(`{"name":`+jsonQuote(name)+`}`))
	require.NoError(t, err, "load_skill never returns a Go error (soft errors only)")
	var ack map[string]any
	require.NoError(t, json.Unmarshal(out, &ack))
	return ack
}

func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// AC-3 / AC-4: a DB-bound business skill loads, body is wrapped in <system-reminder>,
// the allowed_tools recommendation line is appended, Chinese name round-trips.
func TestLoadSkill_DBSkill_WrapsBody_AndRecommendation(t *testing.T) {
	sk := fixedSkill(42, "销售话术训练", "## 销售技巧\n详细指引...", `["web_search","crm_search"]`)
	ctx, turn := buildTurnWithSkills(t, sk)

	tool := NewLoadSkillTool(nil) // nil disk registry — DB path only
	ack := execLoadSkill(t, tool, ctx, "销售话术训练")

	assert.Equal(t, "loaded", ack["status"])
	assert.Equal(t, "db", ack["source"])
	assert.Equal(t, "销售话术训练", ack["skill_name"], "Chinese skill name must round-trip")

	body, _ := ack["body"].(string)
	assert.True(t, strings.HasPrefix(body, "<system-reminder>"), "DB body must be system-reminder wrapped")
	assert.True(t, strings.HasSuffix(body, "</system-reminder>"))
	assert.Contains(t, body, sk.BodyMd, "wrapped body must contain the verbatim skill body")
	assert.Contains(t, body, "💡 推荐配合使用的工具", "recommendation line must be present")
	assert.Contains(t, body, "web_search")
	assert.Contains(t, body, "crm_search")

	assert.Equal(t, 1, turn.InvocationCount, "DB load bumps the cap counter")
	require.Len(t, turn.PendingSkills, 1)
	assert.Equal(t, "销售话术训练", turn.PendingSkills[0].Name)
}

func TestLoadSkill_DBSkill_NoAllowedTools_NoRecommendation(t *testing.T) {
	sk := fixedSkill(42, "纯指引技能", "## 纯指引", `[]`)
	ctx, _ := buildTurnWithSkills(t, sk)

	ack := execLoadSkill(t, NewLoadSkillTool(nil), ctx, "纯指引技能")
	body, _ := ack["body"].(string)
	assert.NotContains(t, body, "💡 推荐配合使用的工具", "no recommendation line when allowed_tools empty")
}

// AC-5: a disk platform skill loads via the same tool, returning the SKILL.md body.
func TestLoadSkill_DiskSkill_ReturnsBody(t *testing.T) {
	reg := buildDiskRegistry(t, "pptx-author", "PowerPoint generator", "# pptx-author\nuse python-pptx ...")
	tool := NewLoadSkillTool(reg)

	// no turn state — disk-only agent
	ack := execLoadSkill(t, tool, context.Background(), "pptx-author")
	assert.Equal(t, "loaded", ack["status"])
	assert.Equal(t, "disk", ack["source"])
	assert.Equal(t, "pptx-author", ack["name"])
	body, _ := ack["body_md"].(string)
	assert.Contains(t, body, "python-pptx", "disk body must be the SKILL.md content")
}

// D3: a DB skill named identically to a disk skill wins (DB-first resolution).
func TestLoadSkill_DBFirst_Collision(t *testing.T) {
	reg := buildDiskRegistry(t, "pptx-author", "platform pptx", "# platform SKILL.md")
	dbSk := fixedSkill(7, "pptx-author", "## my custom pptx guidance", `[]`)
	ctx, _ := buildTurnWithSkills(t, dbSk)

	ack := execLoadSkill(t, NewLoadSkillTool(reg), ctx, "pptx-author")
	assert.Equal(t, "db", ack["source"], "DB skill must win on a name collision")
	body, _ := ack["body"].(string)
	assert.Contains(t, body, "my custom pptx guidance")
}

// AC-6: per-turn cap exhaustion returns a graceful error ack, count not bumped.
func TestLoadSkill_CapExhausted_GracefulAck(t *testing.T) {
	sk := fixedSkill(1, "X", "body", `[]`)
	ctx, turn := buildTurnWithSkills(t, sk)
	turn.Cap = 2
	turn.InvocationCount = 2 // already at cap

	ack := execLoadSkill(t, NewLoadSkillTool(nil), ctx, "X")
	assert.Equal(t, "error", ack["status"])
	assert.Contains(t, ack["error"], "上限")
	assert.Equal(t, 2, turn.InvocationCount, "cap-rejected call must not bump the counter")
}

// On a miss, the soft error lists BOTH DB-bound and disk skill names.
func TestLoadSkill_Miss_ListsAvailableNames(t *testing.T) {
	reg := buildDiskRegistry(t, "xlsx-author", "excel", "# xlsx")
	sk := fixedSkill(5, "销售话术", "body", `[]`)
	ctx, _ := buildTurnWithSkills(t, sk)

	ack := execLoadSkill(t, NewLoadSkillTool(reg), ctx, "不存在的技能")
	assert.Equal(t, "error", ack["status"])
	avail, _ := ack["available"].(string)
	assert.Contains(t, avail, "xlsx-author", "available must list disk skills")
	assert.Contains(t, avail, "销售话术", "available must list DB-bound skills")
}

// Disk-only agents (no turn state) have no cap — repeated loads all succeed.
func TestLoadSkill_DiskOnly_NoTurnState_NoCap(t *testing.T) {
	reg := buildDiskRegistry(t, "docx-author", "word", "# docx")
	tool := NewLoadSkillTool(reg)
	for i := 0; i < 5; i++ {
		ack := execLoadSkill(t, tool, context.Background(), "docx-author")
		assert.Equal(t, "loaded", ack["status"], "disk-only load must not be capped")
	}
}

// Regression for the same-turn accumulation bug (originally
// fix-use-skill-pending-body-overwrite, commit 94910336): loading A→B→C in one turn
// must keep ALL three in PendingSkills. A single-field overwrite would lose A+B and
// the LLM (on the outer-loop injection path) would only ever see the last skill.
func TestLoadSkill_MultipleCalls_PendingSkillsAccumulate(t *testing.T) {
	skA := fixedSkill(1, "技能A", "bodyA", `[]`)
	skB := fixedSkill(2, "技能B", "bodyB", `[]`)
	skC := fixedSkill(3, "技能C", "bodyC", `[]`)
	ctx, turn := buildTurnWithSkills(t, skA, skB, skC) // cap defaults to 3

	tool := NewLoadSkillTool(nil)
	for _, name := range []string{"技能A", "技能B", "技能C"} {
		ack := execLoadSkill(t, tool, ctx, name)
		assert.Equal(t, "loaded", ack["status"], "load %q", name)
	}
	require.Len(t, turn.PendingSkills, 3, "all three same-turn loads must accumulate (no overwrite)")
	assert.Equal(t, "技能A", turn.PendingSkills[0].Name)
	assert.Equal(t, "技能B", turn.PendingSkills[1].Name)
	assert.Equal(t, "技能C", turn.PendingSkills[2].Name)
	assert.Contains(t, turn.PendingSkills[0].Body, "bodyA")
	assert.Contains(t, turn.PendingSkills[2].Body, "bodyC")
	assert.Equal(t, 3, turn.InvocationCount)
}

func TestLoadSkill_InactiveSkill_SoftError(t *testing.T) {
	sk := fixedSkill(9, "禁用技能", "body", `[]`)
	sk.IsActive = false
	ctx, _ := buildTurnWithSkills(t, sk)
	ack := execLoadSkill(t, NewLoadSkillTool(nil), ctx, "禁用技能")
	assert.Equal(t, "error", ack["status"])
	assert.Contains(t, ack["error"], "禁用")
}

func TestLoadSkill_EmptyName_SoftError(t *testing.T) {
	ack := execLoadSkill(t, NewLoadSkillTool(nil), context.Background(), "")
	assert.Equal(t, "error", ack["status"])
}

func TestLoadSkill_Metadata(t *testing.T) {
	tool := NewLoadSkillTool(nil)
	assert.Equal(t, LoadSkillToolName, tool.Name())
	assert.Equal(t, "load_skill", tool.Name())
	assert.True(t, tool.IsReadOnly())
	assert.True(t, tool.IsEnabled(FullyEnabledToolConfig()), "load_skill must pass the full-open filter")
	assert.NotEmpty(t, tool.Description())
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(tool.InputSchema(), &parsed))
	req, _ := parsed["required"].([]any)
	require.Len(t, req, 1)
	assert.Equal(t, "name", req[0])
}
