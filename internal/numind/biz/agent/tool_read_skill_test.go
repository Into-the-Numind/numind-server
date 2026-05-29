package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"numind-server/internal/numind/biz/agent/skills"
)

// ─── mock Registry ──────────────────────────────────────────────────────────

type mockReadSkillRegistry struct {
	entries map[string]*skills.SkillEntry
}

func (m *mockReadSkillRegistry) Get(name string) (*skills.SkillEntry, error) {
	if e, ok := m.entries[name]; ok {
		return e, nil
	}
	return nil, skills.ErrSkillNotFound
}

func (m *mockReadSkillRegistry) List() []skills.SkillManifest {
	out := make([]skills.SkillManifest, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.Manifest)
	}
	return out
}

func (m *mockReadSkillRegistry) Reload() error { return nil }

// ─── helpers ────────────────────────────────────────────────────────────────

// writeSkillFixture creates a temp skill dir with the given SKILL.md content
// and returns a registry entry pointing at it.
func writeSkillFixture(t *testing.T, name, description, mdBody string, maxRuntimeSec int) *skills.SkillEntry {
	t.Helper()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(mdBody), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return &skills.SkillEntry{
		Manifest: skills.SkillManifest{
			Name:              name,
			Version:           "1.0.0",
			Description:       description,
			Categories:        []string{"test"},
			MaxRuntimeSeconds: maxRuntimeSec,
		},
		SkillMDPath: filepath.Join(skillDir, "SKILL.md"),
		RootDir:     skillDir,
	}
}

func newRegistryWith(entries ...*skills.SkillEntry) *mockReadSkillRegistry {
	r := &mockReadSkillRegistry{entries: make(map[string]*skills.SkillEntry)}
	for _, e := range entries {
		r.entries[e.Manifest.Name] = e
	}
	return r
}

// ─── Execute tests ──────────────────────────────────────────────────────────

func TestReadSkill_HappyPath_ReturnsBody(t *testing.T) {
	body := "# pptx-author\n\nUse `from pptx import Presentation`...\n"
	entry := writeSkillFixture(t, "pptx-author", "Generate PowerPoint", body, 45)
	reg := newRegistryWith(entry)
	tool := &readSkillTool{registry: reg}

	in, _ := json.Marshal(readSkillInput{SkillName: "pptx-author"})
	res, err := tool.Execute(context.Background(), ToolInput(in))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	var out readSkillOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Name != "pptx-author" {
		t.Errorf("expected name pptx-author, got %q", out.Name)
	}
	if out.Description != "Generate PowerPoint" {
		t.Errorf("description mismatch: %q", out.Description)
	}
	if out.BodyMarkdown != body {
		t.Errorf("body mismatch: %q", out.BodyMarkdown)
	}
	if out.MaxRuntimeSeconds != 45 {
		t.Errorf("max_runtime_seconds = %d, want 45", out.MaxRuntimeSeconds)
	}
	if strings.HasPrefix(out.BodyMarkdown, "ERROR:") {
		t.Error("happy path should not produce ERROR-prefixed body")
	}
}

func TestReadSkill_SkillNotFound_ReturnsSoftError(t *testing.T) {
	reg := newRegistryWith(
		writeSkillFixture(t, "pptx-author", "PPT", "body", 30),
		writeSkillFixture(t, "xlsx-author", "XLS", "body", 30),
	)
	tool := &readSkillTool{registry: reg}

	in, _ := json.Marshal(readSkillInput{SkillName: "nonexistent-skill"})
	res, err := tool.Execute(context.Background(), ToolInput(in))
	if err != nil {
		t.Fatalf("expected nil Go error (soft), got: %v", err)
	}
	var out readSkillOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.BodyMarkdown, "ERROR:") {
		t.Errorf("expected ERROR: in body, got: %s", out.BodyMarkdown)
	}
	if !strings.Contains(out.BodyMarkdown, "nonexistent-skill") {
		t.Errorf("error should echo requested name: %s", out.BodyMarkdown)
	}
	// Available list should mention both registered skills.
	if !strings.Contains(out.BodyMarkdown, "pptx-author") || !strings.Contains(out.BodyMarkdown, "xlsx-author") {
		t.Errorf("error should list available skills: %s", out.BodyMarkdown)
	}
}

func TestReadSkill_BadInputJSON_ReturnsSoftError(t *testing.T) {
	tool := &readSkillTool{registry: newRegistryWith()}
	res, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	if err != nil {
		t.Fatalf("expected nil Go error (soft), got: %v", err)
	}
	var out readSkillOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.BodyMarkdown, "ERROR: invalid input JSON") {
		t.Errorf("expected invalid-input-json soft error, got: %s", out.BodyMarkdown)
	}
}

func TestReadSkill_EmptySkillName_ReturnsSoftError(t *testing.T) {
	tool := &readSkillTool{registry: newRegistryWith()}
	in, _ := json.Marshal(readSkillInput{SkillName: ""})
	res, err := tool.Execute(context.Background(), ToolInput(in))
	if err != nil {
		t.Fatalf("expected nil Go error (soft), got: %v", err)
	}
	var out readSkillOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.BodyMarkdown, "ERROR: skill_name is required") {
		t.Errorf("expected skill_name-required soft error, got: %s", out.BodyMarkdown)
	}
}

func TestReadSkill_NilRegistry_ReturnsSoftError(t *testing.T) {
	tool := &readSkillTool{registry: nil}
	in, _ := json.Marshal(readSkillInput{SkillName: "any"})
	res, err := tool.Execute(context.Background(), ToolInput(in))
	if err != nil {
		t.Fatalf("expected nil Go error (soft), got: %v", err)
	}
	var out readSkillOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.BodyMarkdown, "ERROR: skill registry not configured") {
		t.Errorf("expected registry-nil soft error, got: %s", out.BodyMarkdown)
	}
}

// TestReadSkill_PathTraversalAttempt_ReturnsSoftError verifies the whitelist
// is intact: even if the LLM passes "../etc/passwd", registry.Get rejects it
// before any os.ReadFile happens. The skill registry is a closed set keyed by
// strict name match (no path resolution).
func TestReadSkill_PathTraversalAttempt_ReturnsSoftError(t *testing.T) {
	reg := newRegistryWith(writeSkillFixture(t, "pptx-author", "PPT", "body", 30))
	tool := &readSkillTool{registry: reg}

	for _, evil := range []string{
		"../etc/passwd",
		"../../skill",
		"pptx-author/../../etc",
		"./pptx-author",
		"skill/../pptx-author",
	} {
		t.Run(evil, func(t *testing.T) {
			in, _ := json.Marshal(readSkillInput{SkillName: evil})
			res, err := tool.Execute(context.Background(), ToolInput(in))
			if err != nil {
				t.Fatalf("expected nil Go error (soft), got: %v", err)
			}
			var out readSkillOutput
			if err := json.Unmarshal(res, &out); err != nil {
				t.Fatalf("invalid json: %v", err)
			}
			if !strings.Contains(out.BodyMarkdown, "not found") {
				t.Errorf("path traversal %q should produce not-found soft error, got: %s",
					evil, out.BodyMarkdown)
			}
		})
	}
}

// TestReadSkill_SKILLMDOver4KB_ReturnsSoftError is the defensive runtime cap:
// even though Task 5 enforces ≤4KB at build/commit time, an operator could
// edit the file post-deploy. Verify the runtime check fires.
func TestReadSkill_SKILLMDOver4KB_ReturnsSoftError(t *testing.T) {
	big := strings.Repeat("a", readSkillMaxBodyBytes+1)
	entry := writeSkillFixture(t, "bloated-skill", "too big", big, 30)
	reg := newRegistryWith(entry)
	tool := &readSkillTool{registry: reg}

	in, _ := json.Marshal(readSkillInput{SkillName: "bloated-skill"})
	res, err := tool.Execute(context.Background(), ToolInput(in))
	if err != nil {
		t.Fatalf("expected nil Go error (soft), got: %v", err)
	}
	var out readSkillOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.BodyMarkdown, "exceeds") {
		t.Errorf("expected exceeds-cap soft error, got: %s", out.BodyMarkdown)
	}
}

// TestReadSkill_SKILLMDUnreadable_ReturnsSoftError covers the case where the
// SKILL.md file exists in the registry entry but the filesystem call fails
// (deleted between scan and read, permission denied, etc.).
func TestReadSkill_SKILLMDUnreadable_ReturnsSoftError(t *testing.T) {
	dir := t.TempDir()
	entry := &skills.SkillEntry{
		Manifest:    skills.SkillManifest{Name: "ghost", Description: "x"},
		SkillMDPath: filepath.Join(dir, "ghost", "SKILL.md"),
		RootDir:     filepath.Join(dir, "ghost"),
	}
	// SKILL.md never created — ReadFile will fail with ENOENT.
	reg := newRegistryWith(entry)
	tool := &readSkillTool{registry: reg}

	in, _ := json.Marshal(readSkillInput{SkillName: "ghost"})
	res, err := tool.Execute(context.Background(), ToolInput(in))
	if err != nil {
		t.Fatalf("expected nil Go error (soft), got: %v", err)
	}
	var out readSkillOutput
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out.BodyMarkdown, "SKILL.md unreadable") {
		t.Errorf("expected unreadable soft error, got: %s", out.BodyMarkdown)
	}
}

// ─── IsEnabled tests ───────────────────────────────────────────────────────

func TestReadSkill_IsEnabled(t *testing.T) {
	tool := &readSkillTool{}
	cases := []struct {
		name string
		cfg  ToolConfig
		want bool
	}{
		{"both off", ToolConfig{EnableSandbox: false, EnableSkills: false}, false},
		{"sandbox-on skills-off", ToolConfig{EnableSandbox: true, EnableSkills: false}, false},
		{"sandbox-off skills-on (read_skill does not need sandbox)", ToolConfig{EnableSandbox: false, EnableSkills: true}, true},
		{"both on", ToolConfig{EnableSandbox: true, EnableSkills: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tool.IsEnabled(tc.cfg); got != tc.want {
				t.Errorf("IsEnabled(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

// ─── Metadata tests ────────────────────────────────────────────────────────

func TestReadSkillTool_Metadata(t *testing.T) {
	tool := &readSkillTool{}
	if tool.Name() != "read_skill" {
		t.Errorf("name = %q, want read_skill", tool.Name())
	}
	if !tool.IsReadOnly() {
		t.Error("read_skill should be read-only")
	}
	if !tool.IsSearchOrReadCommand() {
		t.Error("read_skill should be IsSearchOrReadCommand")
	}
	if tool.AlwaysLoad() {
		t.Error("read_skill should not AlwaysLoad (gated by enable_skills)")
	}
	if tool.UserFacingName() == "" || tool.NarrationVerb() == "" {
		t.Error("UI strings must not be empty")
	}
}

func TestAvailableSkillNames_NilRegistry(t *testing.T) {
	if got := availableSkillNames(nil); got != "(none)" {
		t.Errorf("nil registry → %q, want '(none)'", got)
	}
}

func TestAvailableSkillNames_EmptyRegistry(t *testing.T) {
	if got := availableSkillNames(newRegistryWith()); got != "(none)" {
		t.Errorf("empty registry → %q, want '(none)'", got)
	}
}

// TestPlatformFactory_ReadSkillRegisteredWhenRegistryProvided verifies the
// Task 4 wire: NewPlatformToolFactoryWithSkills must register read_skill (and
// the corresponding metadata entry) whenever a non-nil registry is provided.
// SkillPool may be nil — read_skill does not depend on sandbox.
func TestPlatformFactory_ReadSkillRegisteredWhenRegistryProvided(t *testing.T) {
	entry := writeSkillFixture(t, "pptx-author", "Generate PowerPoint", "# pptx-author\n", 30)
	reg := newRegistryWith(entry)

	f := NewPlatformToolFactoryWithSkills(nil, nil, reg, nil) // pool nil to assert read_skill no longer requires it
	tools, metadata, err := f.LoadTools(context.Background())
	if err != nil {
		t.Fatalf("LoadTools: %v", err)
	}

	var foundTool bool
	for _, tl := range tools {
		if tl.Name() == "read_skill" {
			foundTool = true
			break
		}
	}
	if !foundTool {
		t.Errorf("read_skill must be in LoadTools result when registry provided")
	}

	var foundMeta bool
	for _, m := range metadata {
		if m.ToolName == "read_skill" {
			foundMeta = true
			if m.Category != "技能" {
				t.Errorf("read_skill metadata category = %q, want '技能'", m.Category)
			}
			break
		}
	}
	if !foundMeta {
		t.Errorf("read_skill metadata must be present")
	}
}

func TestAvailableSkillNames_DeterministicOrder(t *testing.T) {
	// Even if inserted in non-alphabetical order, output must be sorted.
	reg := newRegistryWith(
		writeSkillFixture(t, "xlsx-author", "X", "body", 30),
		writeSkillFixture(t, "pptx-author", "P", "body", 30),
		writeSkillFixture(t, "docx-author", "D", "body", 30),
	)
	got := availableSkillNames(reg)
	want := "docx-author, pptx-author, xlsx-author"
	if got != want {
		t.Errorf("availableSkillNames = %q, want %q", got, want)
	}
}
