package agent

import (
	"strings"
	"testing"
)

// TestRegression_NoInvokeSkillInProgressiveLoaderSurfaces is the permanent
// regression guard for the 2026-05-29 skill-progressive-loader refactor.
//
// Original bug-from-customer (dev agent_run_id=83, 2026-05-29):
//
//	invoke_skill ran the pptx-author skill, the inner LLM looked at
//	SKILL.md's declarative pseudo-code (`import invoke_skill;
//	invoke_skill("pptx-author", {...})`) and faithfully copied it into
//	the Python it generated — sandbox raised
//	`ModuleNotFoundError: No module named 'invoke_skill'`.
//
// The refactor deleted the inner LLM by replacing the invoke_skill tool with
// read_skill + run_python (Codex-style progressive disclosure). This test
// pins the load-bearing surfaces so a future rollback / accidental edit
// can't quietly restore the broken architecture.
//
// If this test fails the right fix is to investigate, not to update the
// assertions.
func TestRegression_NoInvokeSkillInProgressiveLoaderSurfaces(t *testing.T) {
	// (1) tool_flag → tool-name mapping must point at read_skill, not the
	// deleted invoke_skill.
	got, ok := categoryToTools["enable_skills"]
	if !ok {
		t.Fatal("categoryToTools['enable_skills'] is missing — backward-compat flag must remain")
	}
	// 2026-05-29 hotfix: the progressive-disclosure flow needs BOTH tools
	// wired through the same flag — read_skill loads SKILL.md, run_python
	// executes the Python the LLM authors from it. Dev QA caught the missing
	// run_python when the agent successfully called read_skill but crashed
	// with `tool run_python not found in toolsNode indexes` because the
	// agent_definition's enable_skills=true only exposed read_skill.
	wantSet := map[string]bool{"read_skill": true, "run_python": true}
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	for want := range wantSet {
		if !gotSet[want] {
			t.Errorf("categoryToTools['enable_skills'] missing %q (got %v)", want, got)
		}
	}
	for _, name := range got {
		if name == "invoke_skill" {
			t.Errorf("invoke_skill must not appear in categoryToTools mapping")
		}
	}

	// (2) safeToolBaseline must include read_skill (so SSE narration / progress
	// surfaces it) and must not include the deleted invoke_skill.
	var seenRead, seenInvoke bool
	for _, n := range safeToolBaseline {
		switch n {
		case "read_skill":
			seenRead = true
		case "invoke_skill":
			seenInvoke = true
		}
	}
	if !seenRead {
		t.Error("safeToolBaseline must include read_skill")
	}
	if seenInvoke {
		t.Error("safeToolBaseline must NOT include the deleted invoke_skill")
	}

	// (3) OutputToolsPriorityAddendum must teach the read_skill → run_python
	// two-step flow and must not reference invoke_skill (which would
	// contradict the skill catalog injected in §2 of the system prompt).
	if !strings.Contains(OutputToolsPriorityAddendum, "read_skill") {
		t.Error("OutputToolsPriorityAddendum must teach read_skill")
	}
	if !strings.Contains(OutputToolsPriorityAddendum, "run_python") {
		t.Error("OutputToolsPriorityAddendum must teach run_python")
	}
	if strings.Contains(OutputToolsPriorityAddendum, "invoke_skill") {
		t.Error("OutputToolsPriorityAddendum must not mention the deleted invoke_skill")
	}

	// (4) Skill catalog header instructs the LLM to use read_skill — not to
	// write Python directly without consulting SKILL.md (the precise mistake
	// the inner LLM made in the original bug).
	if !strings.Contains(skillCatalogHeader, "read_skill") {
		t.Error("skill catalog header must instruct the LLM to call read_skill")
	}
	if !strings.Contains(skillCatalogHeader, "不要直接编写 Python") &&
		!strings.Contains(skillCatalogHeader, "do NOT") {
		// One of the bilingual guards must remain.
		t.Error("skill catalog header must warn against writing Python without read_skill")
	}
}
