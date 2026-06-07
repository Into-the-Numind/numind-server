package agent

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper: collect, sort and stringify a tool-name slice for deterministic compare.
func sortedNames(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

func TestToolNamesFromFlags_EmptyFlags_ReturnsSafeBaseline(t *testing.T) {
	names := toolNamesFromFlags(nil)
	got := sortedNames(names)
	want := sortedNames(safeToolBaseline)
	assert.Equal(t, want, got, "empty ToolFlags must yield the full safe baseline (no dangerous tools)")
	// Explicit: dangerous-only tools must NOT appear.
	assert.NotContains(t, got, "bash_exec")
	assert.NotContains(t, got, "image_gen")
}

func TestToolNamesFromFlags_NilJSON_ReturnsSafeBaseline(t *testing.T) {
	names := toolNamesFromFlags([]byte{})
	got := sortedNames(names)
	want := sortedNames(safeToolBaseline)
	assert.Equal(t, want, got)
}

func TestToolNamesFromFlags_MalformedJSON_FallsBackToBaseline(t *testing.T) {
	names := toolNamesFromFlags([]byte("not-json"))
	got := sortedNames(names)
	want := sortedNames(safeToolBaseline)
	assert.Equal(t, want, got, "malformed JSON must not crash; fall back to safe baseline")
}

func TestToolNamesFromFlags_CategoryFlags_ExpandToToolSets(t *testing.T) {
	// AgentAdvancedEdit.vue stores tool_flags as {code_sandbox/media/dangerous: bool}
	raw, _ := json.Marshal(map[string]bool{
		"code_sandbox": true,
		"media":        true,
		"dangerous":    true,
	})
	names := toolNamesFromFlags(raw)
	got := sortedNames(names)

	// Baseline still on.
	assert.Contains(t, got, "kb_search")
	assert.Contains(t, got, "web_search")
	assert.Contains(t, got, "memory_read")
	assert.Contains(t, got, "ask_user_question")
	// Risk category tools enabled.
	assert.Contains(t, got, "bash_exec", "code_sandbox/dangerous category enables bash_exec")
	assert.Contains(t, got, "image_gen", "media category enables image_gen")
}

func TestToolNamesFromFlags_CategoryFlagsFalse_KeepBaselineOnly(t *testing.T) {
	raw, _ := json.Marshal(map[string]bool{
		"code_sandbox": false,
		"media":        false,
		"dangerous":    false,
	})
	names := toolNamesFromFlags(raw)
	got := sortedNames(names)
	want := sortedNames(safeToolBaseline)
	assert.Equal(t, want, got)
	assert.NotContains(t, got, "bash_exec")
	assert.NotContains(t, got, "image_gen")
}

func TestToolNamesFromFlags_PartialCategory(t *testing.T) {
	raw, _ := json.Marshal(map[string]bool{"media": true})
	names := toolNamesFromFlags(raw)
	got := sortedNames(names)
	assert.Contains(t, got, "image_gen", "media true enables image_gen")
	assert.NotContains(t, got, "bash_exec", "code_sandbox/dangerous not set → bash_exec stays off")
}

func TestToolNamesFromFlags_DirectToolName_OverridesBaseline(t *testing.T) {
	// Future-proof: when frontend supplies per-tool toggle, explicit false
	// disables a baseline tool.
	raw, _ := json.Marshal(map[string]bool{"web_search": false})
	names := toolNamesFromFlags(raw)
	got := sortedNames(names)
	assert.Contains(t, got, "kb_search", "other baseline tools unaffected")
	assert.NotContains(t, got, "web_search", "explicit false disables baseline tool")
}

func TestToolNamesFromFlags_MixedCategoryAndDirectName(t *testing.T) {
	raw, _ := json.Marshal(map[string]bool{
		"code_sandbox": true,  // category → bash_exec on
		"kb_search":    false, // direct → kb_search off
	})
	names := toolNamesFromFlags(raw)
	got := sortedNames(names)
	assert.Contains(t, got, "bash_exec")
	assert.NotContains(t, got, "kb_search", "explicit false disables baseline")
	assert.Contains(t, got, "web_search", "other baseline tools unaffected")
}

func TestToolNamesFromFlags_UnknownTool_PassesThrough(t *testing.T) {
	// If a future feature adds new tools, an unknown key with true should be
	// included (Registry.GetTool will then filter at lookup time).
	raw, _ := json.Marshal(map[string]bool{"future_tool_v2": true})
	names := toolNamesFromFlags(raw)
	assert.Contains(t, names, "future_tool_v2")
}

func TestToolNamesFromFlags_RealWorldBug_100001(t *testing.T) {
	// Regression: dev agent #100001 had this exact ToolFlags shape after the
	// user toggled 3 advanced categories. Before fix: short-circuit (0 tools).
	// After fix: full baseline + bash_exec + image_gen.
	raw := []byte(`{"media":true,"dangerous":true,"code_sandbox":true}`)
	names := toolNamesFromFlags(raw)
	require.NotEmpty(t, names, "fix regression: this used to return [media, dangerous, code_sandbox] which registry can't resolve, causing short-circuit")

	// Must contain the safe tools that learners expect to work.
	got := sortedNames(names)
	assert.Contains(t, got, "kb_search")
	assert.Contains(t, got, "web_search")
	assert.Contains(t, got, "memory_read")
	assert.Contains(t, got, "memory_write")
	assert.Contains(t, got, "get_current_date")
	assert.Contains(t, got, "ask_user_question")
	assert.Contains(t, got, "web_fetch")
	assert.Contains(t, got, "file_read")
	assert.Contains(t, got, "bash_exec")
	assert.Contains(t, got, "image_gen")
}
