package skills_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// repoRoot walks up from this test file's location until it finds go.mod,
// returning the absolute repo root path. Used to locate the real skills/
// directory (which lives at repo root, not under internal/).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller must resolve test file path")
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "reached filesystem root without finding go.mod")
		dir = parent
	}
}

// TestDocxAuthorSKILLMD_StrongImageEmbedInstruction guards the docx-author
// SKILL.md image-embed instruction (feature agent-output-redesign T1, #2).
//
// The instruction was upgraded from a soft blockquote hint to a strong
// main-text directive with a complete copy-pasteable code template. This test
// asserts the load-bearing tokens stay present so a future edit cannot silently
// weaken the guidance back into a soft hint (which let the model generate an
// image via image_gen yet omit it from the produced .docx).
func TestDocxAuthorSKILLMD_StrongImageEmbedInstruction(t *testing.T) {
	path := filepath.Join(repoRoot(t), "skills", "docx-author", "SKILL.md")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "docx-author SKILL.md must exist at %s", path)
	content := string(raw)

	// Each token is load-bearing for the strengthened instruction:
	//   必须        — strong imperative (not a soft "务必" blockquote suggestion)
	//   input_files — tells the model how to ship the COS URL into the sandbox
	//   add_picture — the actual python-docx embed call
	//   Inches      — width arg in the copy-pasteable code template
	for _, tok := range []string{"必须", "input_files", "add_picture", "Inches"} {
		require.True(t, strings.Contains(content, tok),
			"docx-author SKILL.md must contain %q to keep the strong image-embed instruction intact", tok)
	}
}
