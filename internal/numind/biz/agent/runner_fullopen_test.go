package agent

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/model"
)

// TestFullOpen_RegistryFilter pins the open-tools-skill-as-guidance "full-open"
// registration contract that BOTH runner.go::Run and runner_runstream.go::RunStream
// apply identically: the runner registers every registry tool whose
// IsEnabled(FullyEnabledToolConfig()) is true, EXCEPT the hard stub
// document_generate (IsEnabled returns false unconditionally).
//
// Tools that were previously reachable only when a tool_flags category was enabled
// (bash_exec via code_sandbox, image_gen via media) are now open to EVERY agent.
// load_skill (the merged skill tool) also flows through this loop. The deleted
// use_skill / read_skill must NOT appear. This is the mechanical core of AC-1; the
// runner registration loops in both paths share this exact filter so one test guards both.
func TestFullOpen_RegistryFilter(t *testing.T) {
	reg := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	require.NoError(t, reg.RegisterFactory(NewPlatformToolFactory(nil, nil)))
	require.NoError(t, reg.LoadAll(context.Background()))

	fullCfg := FullyEnabledToolConfig()
	open := map[string]bool{}
	for _, ft := range reg.ListAllTools() {
		// EXACT replication of the runner base-loop filter (runner.go + runner_runstream.go).
		if !ft.IsEnabled(fullCfg) {
			continue
		}
		open[ft.Name()] = true
	}

	// Previously category-gated / always-on tools + the merged load_skill must all be open.
	for _, name := range []string{
		"bash_exec",        // was code_sandbox-gated
		"image_gen",        // was media-gated
		"run_python",       // sandbox executor
		"kb_search",        // default-true base tool
		"web_search",       // default-true base tool
		"get_current_date", // default-true base tool
		LoadSkillToolName,  // merged skill tool, IsEnabled=EnableSkills → flows through
	} {
		if !open[name] {
			t.Errorf("full-open set must include %q (AC-1)", name)
		}
	}

	// Hard stub must be excluded (it errors on every call — exposing it is a UX regression).
	if open["document_generate"] {
		t.Error("document_generate (hard stub, IsEnabled==false) must be excluded from full-open")
	}

	// The deleted tools must not survive anywhere.
	if open["use_skill"] || open["read_skill"] {
		t.Error("use_skill/read_skill were merged into load_skill and must not be registered")
	}
}

func TestApplyDefinitionToolPolicy_DirectFlagsCannotRestrictGlobalTools(t *testing.T) {
	req := RunRequest{ToolNames: []string{"bash_exec", "web_search"}, EnforceToolAllowlist: true}
	ad := &model.AgentDefinition{ToolFlags: datatypes.JSON(`{"file_read":true,"web_search":false,"bash_exec":false}`)}

	applyDefinitionToolPolicy(&req, ad)

	require.False(t, req.EnforceToolAllowlist, "AgentDefinition tool_flags are compatibility metadata, not an authorization boundary")
	require.Equal(t, []string{"bash_exec", "web_search"}, req.ToolNames)
}

func TestApplyDefinitionToolPolicy_CategoryOnlyKeepsLegacyCallerPolicy(t *testing.T) {
	req := RunRequest{ToolNames: []string{"bash_exec"}}
	ad := &model.AgentDefinition{ToolFlags: datatypes.JSON(`{"code_sandbox":false}`)}

	applyDefinitionToolPolicy(&req, ad)

	require.False(t, req.EnforceToolAllowlist)
	require.Equal(t, []string{"bash_exec"}, req.ToolNames)
}

func TestSelectToolsForRun_LegacyStrictAllowlistCannotExcludeGlobalTools(t *testing.T) {
	registry := newStaticRegistry(
		&stubFullTool{name: "xhs_note_list"},
		&stubFullTool{name: "file_read"},
		&stubFullTool{name: "web_search"},
		&stubFullTool{name: "bash_exec"},
	)

	strict := selectToolsForRun(registry, []string{"file_read"}, true)
	strictNames := make([]string, 0, len(strict))
	for _, tool := range strict {
		strictNames = append(strictNames, tool.Name())
	}
	sort.Strings(strictNames)
	require.Equal(t, []string{"bash_exec", "file_read", "web_search", "xhs_note_list"}, strictNames)

	compat := selectToolsForRun(registry, nil, false)
	compatNames := make([]string, 0, len(compat))
	for _, tool := range compat {
		compatNames = append(compatNames, tool.Name())
	}
	sort.Strings(compatNames)
	require.Equal(t, []string{"bash_exec", "file_read", "web_search", "xhs_note_list"}, compatNames)
}
