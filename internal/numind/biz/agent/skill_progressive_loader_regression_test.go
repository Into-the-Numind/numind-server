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

	// (2) safeToolBaseline must include BOTH read_skill (loads SKILL.md) and
	// run_python (executes the Python the LLM authors from it). Without
	// run_python in the baseline, the agent crashes with
	// `tool run_python not found in toolsNode indexes` after a successful
	// read_skill call (dev QA 2026-05-29 follow-on hotfix). Must not include
	// the deleted invoke_skill.
	var seenRead, seenRun, seenInvoke bool
	for _, n := range safeToolBaseline {
		switch n {
		case "read_skill":
			seenRead = true
		case "run_python":
			seenRun = true
		case "invoke_skill":
			seenInvoke = true
		}
	}
	if !seenRead {
		t.Error("safeToolBaseline must include read_skill")
	}
	if !seenRun {
		t.Error("safeToolBaseline must include run_python (the read_skill executor)")
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

// TestRegression_RunPythonStatelessGuidance is the permanent guard for the
// 2026-05-31 bug-from-customer found in dev gstack /qa (agent_run_id=92).
//
// The LLM built a multi-slide deck across TWO run_python calls: call #1 did
// `prs = Presentation()` → saved → COS-uploaded a valid 34KB .pptx; call #2 did
// `prs = Presentation("/workdir/output/ai_acquisition_report.pptx")` to reopen
// and extend it. Call #2 failed because run_python is STATELESS — each call gets
// a fresh tmpfs container that is destroyed on return (sandbox/pool.go Return →
// Destroy; /workdir is a per-container tmpfs per security.go), so call #1's file
// does not exist in call #2's container. The LLM misread the failure as
// "file corrupted or locked."
//
// The fix is guidance: run_python's tool Description and the system-prompt
// OutputToolsPriorityAddendum must both tell the LLM run_python is stateless and
// the whole file must be built in one call. This test pins that guidance so a
// future edit can't silently drop it (same invariant-guard pattern as the
// skill-progressive-loader test above). If this fails, investigate — don't
// weaken the assertions.
func TestRegression_RunPythonStatelessGuidance(t *testing.T) {
	desc := (&runPythonTool{}).Description()
	for _, want := range []string{"STATELESS", "ONE single run_python call", "NEVER reopen"} {
		if !strings.Contains(desc, want) {
			t.Errorf("run_python Description must mention %q (stateless contract). Got:\n%s", want, desc)
		}
	}
	// The system-prompt addendum must also carry the stateless warning so every
	// agent — not only those that read a SKILL.md — gets it.
	if !strings.Contains(OutputToolsPriorityAddendum, "STATELESS") {
		t.Error("OutputToolsPriorityAddendum must warn that run_python is STATELESS (English section)")
	}
	if !strings.Contains(OutputToolsPriorityAddendum, "无状态") {
		t.Error("OutputToolsPriorityAddendum must warn that run_python is 无状态 (Chinese section)")
	}
	// No double-prefix path typo may creep back into the schema/description.
	if strings.Contains(desc, "/workdir/workdir/") {
		t.Error("run_python Description has a /workdir/workdir/ double-prefix typo")
	}
	if strings.Contains(string((&runPythonTool{}).InputSchema()), "/workdir/workdir/") {
		t.Error("run_python InputSchema has a /workdir/workdir/ double-prefix typo")
	}
}

// TestReopensOutputPath covers the stateless-reopen detector that drives the
// post-failure hint. It must catch the document-library open-by-path patterns
// on /workdir/output/ and must NOT fire on the normal create-fresh-then-save
// pattern (which would otherwise spam a misleading hint on every success).
func TestReopensOutputPath(t *testing.T) {
	cases := []struct {
		name string
		code string
		want bool
	}{
		{"pptx reopen double-quote", `prs = Presentation("/workdir/output/x.pptx")`, true},
		{"pptx reopen single-quote", `prs = Presentation('/workdir/output/x.pptx')`, true},
		{"xlsx reopen", `wb = load_workbook("/workdir/output/x.xlsx")`, true},
		{"docx reopen", `doc = Document('/workdir/output/x.docx')`, true},
		{"create fresh + save (normal)", "prs = Presentation()\nprs.save('/workdir/output/x.pptx')", false},
		{"reads input not output", `Presentation("/workdir/input/template.pptx")`, false},
		{"no workdir path at all", `prs = Presentation()`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reopensOutputPath(tc.code); got != tc.want {
				t.Errorf("reopensOutputPath(%q) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}
