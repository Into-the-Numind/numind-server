package skills_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/skills"
)

// writeManifest writes a manifest.json into skillsRoot/<skillName>/.
func writeManifest(t *testing.T, root, skillName string, m map[string]interface{}) {
	t.Helper()
	dir := filepath.Join(root, skillName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644))
}

// writeSkillMD writes a SKILL.md into skillsRoot/<skillName>/.
func writeSkillMD(t *testing.T, root, skillName, content string) {
	t.Helper()
	dir := filepath.Join(root, skillName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

// validManifest returns a minimal valid manifest payload.
func validManifest(name string) map[string]interface{} {
	return map[string]interface{}{
		"name":                name,
		"version":             "1.0.0",
		"description":         "Test skill " + name,
		"categories":          []string{"test"},
		"required_libs":       []string{"openpyxl>=3.1"},
		"output_mime_types":   []string{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		"max_runtime_seconds": 30,
		"max_output_size_mb":  50,
	}
}

// TestNewRegistry_ValidSkillsDir_LoadsAll verifies that a registry built from a
// directory with two valid skills registers exactly those two skills.
func TestNewRegistry_ValidSkillsDir_LoadsAll(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "skill-a", validManifest("skill-a"))
	writeSkillMD(t, root, "skill-a", "# skill-a")
	writeManifest(t, root, "skill-b", validManifest("skill-b"))
	writeSkillMD(t, root, "skill-b", "# skill-b")

	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)

	manifests := reg.List()
	assert.Len(t, manifests, 2)

	names := make(map[string]bool, 2)
	for _, m := range manifests {
		names[m.Name] = true
	}
	assert.True(t, names["skill-a"])
	assert.True(t, names["skill-b"])
}

func TestProductionRegistry_ContainsExactlyGlobalPlatformSkills(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "..", "..", ".."))

	reg, err := skills.NewRegistry(filepath.Join(repoRoot, "skills"))
	require.NoError(t, err)

	got := make([]string, 0, len(reg.List()))
	for _, manifest := range reg.List() {
		got = append(got, manifest.Name)
	}
	sort.Strings(got)
	assert.Equal(t, []string{"docx-author", "pdf-from-html", "pptx-author", "xlsx-author"}, got)
}

// TestNewRegistry_MissingManifest_SkipsDir verifies that a directory without a
// manifest.json is silently skipped (the registry is created without error).
func TestNewRegistry_MissingManifest_SkipsDir(t *testing.T) {
	root := t.TempDir()
	// Create a subdirectory without manifest.json.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "no-manifest"), 0o755))
	// Create one valid skill alongside it.
	writeManifest(t, root, "valid-skill", validManifest("valid-skill"))

	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)

	manifests := reg.List()
	assert.Len(t, manifests, 1, "only the valid skill should be registered")
	assert.Equal(t, "valid-skill", manifests[0].Name)
}

// TestNewRegistry_InvalidManifestJSON_SkipsDir verifies that a directory whose
// manifest.json contains invalid JSON is skipped without panicking or returning
// an error from NewRegistry.
func TestNewRegistry_InvalidManifestJSON_SkipsDir(t *testing.T) {
	root := t.TempDir()
	// Write a bad manifest.json.
	dir := filepath.Join(root, "bad-skill")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{not valid json"), 0o644))
	// Add one good skill to confirm the registry itself is not nil.
	writeManifest(t, root, "good-skill", validManifest("good-skill"))

	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)

	manifests := reg.List()
	assert.Len(t, manifests, 1)
	assert.Equal(t, "good-skill", manifests[0].Name)
}

// TestRegistry_Get_NotFound_ErrSkillNotFound verifies that Get returns ErrSkillNotFound
// when the requested skill name is not in the registry.
func TestRegistry_Get_NotFound_ErrSkillNotFound(t *testing.T) {
	root := t.TempDir()
	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)

	entry, err := reg.Get("nonexistent")
	assert.Nil(t, entry)
	assert.ErrorIs(t, err, skills.ErrSkillNotFound)
}

// TestRegistry_Reload_PicksUpNewSkill verifies that Reload picks up a skill that was
// added to the skills_root directory after the initial NewRegistry call.
func TestRegistry_Reload_PicksUpNewSkill(t *testing.T) {
	root := t.TempDir()
	// Start with one skill.
	writeManifest(t, root, "first", validManifest("first"))

	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)
	assert.Len(t, reg.List(), 1)

	// Add a second skill after initial load.
	writeManifest(t, root, "second", validManifest("second"))

	require.NoError(t, reg.Reload())

	manifests := reg.List()
	assert.Len(t, manifests, 2, "Reload should pick up the new skill")
	entry, err := reg.Get("second")
	require.NoError(t, err)
	assert.Equal(t, "second", entry.Manifest.Name)
}

// TestNewRegistry_SkillsRootNotExist_ReturnsError verifies that NewRegistry returns
// a non-nil error when the skills_root directory does not exist.
func TestNewRegistry_SkillsRootNotExist_ReturnsError(t *testing.T) {
	reg, err := skills.NewRegistry("/tmp/this-path-must-not-exist-4567890abc")
	assert.Nil(t, reg)
	assert.Error(t, err)
}

// TestNewRegistry_SkillsRootIsFile_ReturnsError verifies that NewRegistry returns
// an error when the provided path is a file rather than a directory.
func TestNewRegistry_SkillsRootIsFile_ReturnsError(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "not-a-dir")
	require.NoError(t, os.WriteFile(filePath, []byte("hello"), 0o644))

	reg, err := skills.NewRegistry(filePath)
	assert.Nil(t, reg)
	assert.Error(t, err)
}

// TestRegistry_Get_Success verifies that Get returns a well-populated SkillEntry
// when the skill exists.
func TestRegistry_Get_Success(t *testing.T) {
	root := t.TempDir()
	m := validManifest("xlsx-author")
	writeManifest(t, root, "xlsx-author", m)
	writeSkillMD(t, root, "xlsx-author", "# SKILL\nGenerate Excel files.")

	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)

	entry, err := reg.Get("xlsx-author")
	require.NoError(t, err)
	assert.Equal(t, "xlsx-author", entry.Manifest.Name)
	assert.Equal(t, "1.0.0", entry.Manifest.Version)
	assert.Contains(t, entry.SkillMDPath, "xlsx-author")
	assert.Contains(t, entry.RootDir, "xlsx-author")
}

// TestNewRegistry_ManifestFailsValidation_SkipsDir verifies that a manifest with
// max_runtime_seconds > 180 is skipped.
func TestNewRegistry_ManifestFailsValidation_SkipsDir(t *testing.T) {
	root := t.TempDir()
	bad := validManifest("too-long")
	bad["max_runtime_seconds"] = 181 // violates ≤180 constraint
	writeManifest(t, root, "too-long", bad)
	writeManifest(t, root, "good", validManifest("good"))

	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)

	manifests := reg.List()
	assert.Len(t, manifests, 1, "only the valid skill should be registered")
	assert.Equal(t, "good", manifests[0].Name)
}

// TestSkillManifest_Validate_RequiredFields verifies that Validate catches
// missing required fields.
func TestSkillManifest_Validate_RequiredFields(t *testing.T) {
	cases := []struct {
		name     string
		manifest skills.SkillManifest
	}{
		{"missing_name", skills.SkillManifest{Version: "1.0", Description: "x"}},
		{"missing_version", skills.SkillManifest{Name: "x", Description: "x"}},
		{"missing_description", skills.SkillManifest{Name: "x", Version: "1.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.manifest.Validate()
			assert.Error(t, err)
		})
	}
}

// TestSkillManifest_EffectiveDefaults verifies that EffectiveMaxRuntime and
// EffectiveMaxOutputMB return sensible defaults when the manifest fields are zero.
func TestSkillManifest_EffectiveDefaults(t *testing.T) {
	m := &skills.SkillManifest{Name: "x", Version: "1", Description: "x"}
	assert.Equal(t, 30, m.EffectiveMaxRuntime())
	assert.Equal(t, 50, m.EffectiveMaxOutputMB())
	assert.Equal(t, "/workdir/input/", m.EffectiveInputDir())
	assert.Equal(t, "/workdir/output/", m.EffectiveOutputDir())

	m2 := &skills.SkillManifest{
		Name: "x", Version: "1", Description: "x",
		MaxRuntimeSeconds: 20, MaxOutputSizeMB: 10,
		InputDir: "/custom/in/", OutputDir: "/custom/out/",
	}
	assert.Equal(t, 20, m2.EffectiveMaxRuntime())
	assert.Equal(t, 10, m2.EffectiveMaxOutputMB())
	assert.Equal(t, "/custom/in/", m2.EffectiveInputDir())
	assert.Equal(t, "/custom/out/", m2.EffectiveOutputDir())
}

// TestSkillNames_Empty verifies that SkillNames returns "(none)" for an empty registry.
func TestSkillNames_Empty(t *testing.T) {
	root := t.TempDir()
	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)
	assert.Equal(t, "(none)", skills.SkillNames(reg))
}

// TestSkillNames_Sorted verifies that SkillNames returns a sorted comma-separated string.
func TestSkillNames_Sorted(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "zzz-skill", validManifest("zzz-skill"))
	writeManifest(t, root, "aaa-skill", validManifest("aaa-skill"))
	writeManifest(t, root, "mmm-skill", validManifest("mmm-skill"))

	reg, err := skills.NewRegistry(root)
	require.NoError(t, err)
	assert.Equal(t, "aaa-skill, mmm-skill, zzz-skill", skills.SkillNames(reg))
}
