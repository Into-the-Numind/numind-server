package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFullOpen_RegistryFilter pins the open-tools-skill-as-guidance "full-open"
// registration contract that BOTH runner.go::Run and runner_runstream.go::RunStream
// apply identically: the runner registers every registry tool whose
// IsEnabled(FullyEnabledToolConfig()) is true, EXCEPT
//   - document_generate (hard stub — IsEnabled returns false unconditionally), and
//   - use_skill (registered separately, binding-gated, so it does not appear in the
//     base full-open set for a skill-less agent).
//
// Tools that were previously reachable only when a tool_flags category was enabled
// (bash_exec via code_sandbox, image_gen via media) are now open to EVERY agent.
// This is the mechanical core of AC-1; the runner registration loops in both paths
// share this exact filter so a single unit test guards both.
func TestFullOpen_RegistryFilter(t *testing.T) {
	reg := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	require.NoError(t, reg.RegisterFactory(NewPlatformToolFactory(nil, nil)))
	require.NoError(t, reg.LoadAll(context.Background()))

	fullCfg := FullyEnabledToolConfig()
	open := map[string]bool{}
	for _, ft := range reg.ListAllTools() {
		// EXACT replication of the runner base-loop filter (runner.go + runner_runstream.go).
		if !ft.IsEnabled(fullCfg) || ft.Name() == UseSkillToolName {
			continue
		}
		open[ft.Name()] = true
	}

	// Previously category-gated / always-on tools must all be open to every agent now.
	for _, name := range []string{
		"bash_exec",        // was code_sandbox-gated
		"image_gen",        // was media-gated
		"run_python",       // sandbox executor
		"kb_search",        // default-true base tool
		"web_search",       // default-true base tool
		"get_current_date", // default-true base tool (S0 cold-start example)
	} {
		if !open[name] {
			t.Errorf("full-open set must include %q (AC-1)", name)
		}
	}

	// Hard stub must be excluded (it errors on every call — exposing it is a UX regression).
	if open["document_generate"] {
		t.Error("document_generate (hard stub, IsEnabled==false) must be excluded from full-open")
	}

	// use_skill is binding-gated and registered by the separate conditional block,
	// so the base full-open loop must skip it.
	if open[UseSkillToolName] {
		t.Errorf("%q must be skipped in the base full-open loop (binding-gated)", UseSkillToolName)
	}
}
