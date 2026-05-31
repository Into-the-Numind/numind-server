package agent

import (
	"strings"
	"testing"

	"numind-server/internal/pkg/model"

	"numind-server/internal/numind/biz/agent/skills"
)

// catalogRegistry is a minimal test double for skills.Registry that only
// implements List() / Get() (the methods buildUnifiedSkillCatalog calls).
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

func TestUnifiedCatalog_DiskHappyPath(t *testing.T) {
	reg := &catalogRegistry{manifests: []skills.SkillManifest{
		{Name: "pptx-author", Description: "生成 PowerPoint 演示文档"},
		{Name: "xlsx-author", Description: "生成 Excel 工作簿"},
	}}
	got := buildUnifiedSkillCatalog(nil, reg)
	if got == "" {
		t.Fatal("expected non-empty catalog")
	}
	for _, want := range []string{
		"pptx-author", "PowerPoint",
		"xlsx-author", "Excel",
		"load_skill", "run_python",
		"必须先 load_skill",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog missing %q in:\n%s", want, got)
		}
	}
	// The merged tool must teach load_skill, never the deleted tools.
	if strings.Contains(got, "read_skill") || strings.Contains(got, "use_skill") {
		t.Errorf("catalog must not reference the deleted read_skill/use_skill:\n%s", got)
	}
}

// AC-5: a unified catalog lists BOTH DB-bound business skills and disk skills.
func TestUnifiedCatalog_DBAndDisk(t *testing.T) {
	reg := &catalogRegistry{manifests: []skills.SkillManifest{
		{Name: "pptx-author", Description: "PowerPoint"},
	}}
	db := []model.Skill{{ID: 1, Name: "销售话术训练", Description: "客户对话技巧", WhenToUse: "卖货时", IsActive: true}}
	got := buildUnifiedSkillCatalog(db, reg)
	for _, want := range []string{"销售话术训练", "客户对话技巧", "何时使用", "pptx-author"} {
		if !strings.Contains(got, want) {
			t.Errorf("unified catalog missing %q in:\n%s", want, got)
		}
	}
}

// D3: a DB skill named identically to a disk skill shadows it (DB wins) — the disk
// entry is dropped so the catalog shows exactly one entry for that name.
func TestUnifiedCatalog_DBShadowsDisk(t *testing.T) {
	reg := &catalogRegistry{manifests: []skills.SkillManifest{
		{Name: "pptx-author", Description: "PLATFORM-DESC"},
	}}
	db := []model.Skill{{ID: 1, Name: "pptx-author", Description: "DB-DESC", IsActive: true}}
	got := buildUnifiedSkillCatalog(db, reg)
	if !strings.Contains(got, "DB-DESC") {
		t.Errorf("DB skill desc must appear (DB wins):\n%s", got)
	}
	if strings.Contains(got, "PLATFORM-DESC") {
		t.Errorf("shadowed disk skill must be dropped:\n%s", got)
	}
}

func TestUnifiedCatalog_EmptyAll(t *testing.T) {
	if got := buildUnifiedSkillCatalog(nil, &catalogRegistry{manifests: nil}); got != "" {
		t.Errorf("no skills should render '', got: %q", got)
	}
	if got := buildUnifiedSkillCatalog(nil, nil); got != "" {
		t.Errorf("nil registry + nil skills should render '', got: %q", got)
	}
}

func TestUnifiedCatalog_LongDescriptionTruncated(t *testing.T) {
	longDesc := strings.Repeat("超长描述文字", 50) // 6 chars * 50 = 300 chars
	reg := &catalogRegistry{manifests: []skills.SkillManifest{
		{Name: "bloated-skill", Description: longDesc},
	}}
	got := buildUnifiedSkillCatalog(nil, reg)
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis truncation marker, got:\n%s", got)
	}
	if strings.Contains(got, longDesc) {
		t.Errorf("expected description truncation; full text appeared in:\n%s", got)
	}
}

func TestUnifiedCatalog_DeterministicDiskOrder(t *testing.T) {
	reg := &catalogRegistry{manifests: []skills.SkillManifest{
		{Name: "xlsx-author", Description: "X"},
		{Name: "pptx-author", Description: "P"},
		{Name: "docx-author", Description: "D"},
	}}
	got := buildUnifiedSkillCatalog(nil, reg)
	posDocx := strings.Index(got, "docx-author")
	posPptx := strings.Index(got, "pptx-author")
	posXlsx := strings.Index(got, "xlsx-author")
	if posDocx < 0 || posPptx < 0 || posXlsx < 0 {
		t.Fatal("all three skills should appear in catalog")
	}
	if !(posDocx < posPptx && posPptx < posXlsx) {
		t.Errorf("expected alphabetical disk order docx < pptx < xlsx; positions %d, %d, %d in:\n%s",
			posDocx, posPptx, posXlsx, got)
	}
}

// Trailing disk skills are dropped when the catalog would exceed the char cap;
// the footer is always retained.
func TestUnifiedCatalog_TotalCapEnforced(t *testing.T) {
	mans := make([]skills.SkillManifest, 0, 20)
	for i := 0; i < 20; i++ {
		mans = append(mans, skills.SkillManifest{
			Name:        "skill-" + string(rune('a'+i)),
			Description: strings.Repeat("DESCRIPTION ", 10), // ~120 chars
		})
	}
	reg := &catalogRegistry{manifests: mans}
	got := buildUnifiedSkillCatalog(nil, reg)
	if len(got) > skillCatalogTotalCharCap+50 {
		t.Errorf("catalog len = %d, well above cap %d", len(got), skillCatalogTotalCharCap)
	}
	if !strings.Contains(got, "工作流") {
		t.Errorf("footer must always be present even when truncated; got:\n%s", got)
	}
}
