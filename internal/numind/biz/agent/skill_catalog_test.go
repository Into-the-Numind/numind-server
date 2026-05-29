package agent

import (
	"strings"
	"testing"

	"numind-server/internal/numind/biz/agent/skills"
)

// catalogRegistry is a minimal test double for skills.Registry that only
// implements List() (the only method RenderSkillCatalog calls). Get/Reload
// are stubbed.
type catalogRegistry struct {
	manifests []skills.SkillManifest
}

func (c *catalogRegistry) Get(name string) (*skills.SkillEntry, error) {
	for _, m := range c.manifests {
		if m.Name == name {
			return &skills.SkillEntry{Manifest: m}, nil
		}
	}
	return nil, skills.ErrSkillNotFound
}

func (c *catalogRegistry) List() []skills.SkillManifest { return c.manifests }
func (c *catalogRegistry) Reload() error                { return nil }

func TestRenderSkillCatalog_HappyPath(t *testing.T) {
	reg := &catalogRegistry{manifests: []skills.SkillManifest{
		{Name: "pptx-author", Description: "生成 PowerPoint 演示文档"},
		{Name: "xlsx-author", Description: "生成 Excel 工作簿"},
	}}
	got := RenderSkillCatalog(reg)
	if got == "" {
		t.Fatal("expected non-empty catalog")
	}
	for _, want := range []string{
		"pptx-author", "PowerPoint",
		"xlsx-author", "Excel",
		"read_skill", "run_python",
		"必须先调用 read_skill",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderSkillCatalog_EmptyRegistry(t *testing.T) {
	reg := &catalogRegistry{manifests: nil}
	if got := RenderSkillCatalog(reg); got != "" {
		t.Errorf("empty registry should render '', got: %q", got)
	}
}

func TestRenderSkillCatalog_NilRegistry(t *testing.T) {
	if got := RenderSkillCatalog(nil); got != "" {
		t.Errorf("nil registry should render '', got: %q", got)
	}
}

func TestRenderSkillCatalog_LongDescriptionTruncated(t *testing.T) {
	longDesc := strings.Repeat("超长描述文字", 50) // 6 chars * 50 = 300 chars
	reg := &catalogRegistry{manifests: []skills.SkillManifest{
		{Name: "bloated-skill", Description: longDesc},
	}}
	got := RenderSkillCatalog(reg)
	// The truncated description should end with the ellipsis marker.
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis truncation marker, got:\n%s", got)
	}
	// And should NOT contain the full original string.
	if strings.Contains(got, longDesc) {
		t.Errorf("expected description truncation; full text appeared in:\n%s", got)
	}
}

func TestRenderSkillCatalog_DeterministicOrder(t *testing.T) {
	// Insert in reverse-alphabetical order; expect sorted output.
	reg := &catalogRegistry{manifests: []skills.SkillManifest{
		{Name: "xlsx-author", Description: "X"},
		{Name: "pptx-author", Description: "P"},
		{Name: "docx-author", Description: "D"},
	}}
	got := RenderSkillCatalog(reg)
	posDocx := strings.Index(got, "docx-author")
	posPptx := strings.Index(got, "pptx-author")
	posXlsx := strings.Index(got, "xlsx-author")
	if posDocx < 0 || posPptx < 0 || posXlsx < 0 {
		t.Fatal("all three skills should appear in catalog")
	}
	if !(posDocx < posPptx && posPptx < posXlsx) {
		t.Errorf("expected alphabetical order docx < pptx < xlsx; positions %d, %d, %d in:\n%s",
			posDocx, posPptx, posXlsx, got)
	}
}

// TestRenderSkillCatalog_TotalCapEnforced verifies trailing skills are dropped
// when the catalog would exceed skillCatalogTotalCharCap.
func TestRenderSkillCatalog_TotalCapEnforced(t *testing.T) {
	// 20 skills × ~150 chars/entry would blow the 2000 cap.
	mans := make([]skills.SkillManifest, 0, 20)
	for i := 0; i < 20; i++ {
		mans = append(mans, skills.SkillManifest{
			Name:        "skill-" + string(rune('a'+i)),
			Description: strings.Repeat("DESCRIPTION ", 10), // ~120 chars
		})
	}
	reg := &catalogRegistry{manifests: mans}
	got := RenderSkillCatalog(reg)
	if len(got) > skillCatalogTotalCharCap+50 {
		// +50 grace for the final entry that pushed projection over.
		t.Errorf("catalog len = %d, well above cap %d:\n%s",
			len(got), skillCatalogTotalCharCap, got)
	}
	// Footer must always be included even when truncated.
	if !strings.Contains(got, "工作流：") {
		t.Errorf("footer must always be present even when truncated; got:\n%s", got)
	}
}
