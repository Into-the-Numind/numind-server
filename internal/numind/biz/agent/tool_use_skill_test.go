// v2 #2 agent-mode-v2-skill-invocation T02 — types + ctx helpers + skeleton tests.
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"numind-server/internal/pkg/model"
)

// ── UseSkillTurnState 构造 ─────────────────────────────────────────────────────

func TestNewUseSkillTurnState_DefaultCap(t *testing.T) {
	s := NewUseSkillTurnState(0) // 0 → default
	if s.Cap != UseSkillTurnCapDefault {
		t.Errorf("default cap = %d, want %d", s.Cap, UseSkillTurnCapDefault)
	}
	if s.InvocationCount != 0 {
		t.Errorf("InvocationCount should start at 0, got %d", s.InvocationCount)
	}
	if s.AllowedTools == nil {
		t.Error("AllowedTools map should be non-nil")
	}
	if s.SkillByID == nil || s.SkillByName == nil {
		t.Error("SkillByID and SkillByName maps should be non-nil")
	}
	if s.PendingBody != "" || s.PendingSkillName != "" || s.PendingSkillVersion != 0 {
		t.Error("Pending fields should be zero-valued")
	}
}

func TestNewUseSkillTurnState_ExplicitCap(t *testing.T) {
	s := NewUseSkillTurnState(5)
	if s.Cap != 5 {
		t.Errorf("explicit cap = %d, want 5", s.Cap)
	}
}

func TestNewUseSkillTurnState_NegativeCap_FallsToDefault(t *testing.T) {
	s := NewUseSkillTurnState(-1)
	if s.Cap != UseSkillTurnCapDefault {
		t.Errorf("negative cap should fall to default %d, got %d", UseSkillTurnCapDefault, s.Cap)
	}
}

// ── ctx helpers (round trip) ───────────────────────────────────────────────────

func TestWithUseSkillTurn_RoundTrip(t *testing.T) {
	want := NewUseSkillTurnState(3)
	want.SkillByName["销售话术训练"] = &model.Skill{ID: 42, Name: "销售话术训练"}

	ctx := WithUseSkillTurn(context.Background(), want)
	got, ok := UseSkillTurnFromCtx(ctx)
	if !ok {
		t.Fatal("UseSkillTurnFromCtx should find injected turn state")
	}
	if got != want {
		t.Error("UseSkillTurnFromCtx returned different pointer than injected")
	}
	if got.SkillByName["销售话术训练"].ID != 42 {
		t.Error("turn state data lost through ctx round trip")
	}
}

func TestUseSkillTurnFromCtx_EmptyCtx_NotOK(t *testing.T) {
	_, ok := UseSkillTurnFromCtx(context.Background())
	if ok {
		t.Error("empty ctx should return ok=false")
	}
}

func TestUseSkillTurnFromCtx_NilValue_NotOK(t *testing.T) {
	// Defensive: even if someone stuffs explicit nil, helper returns ok=false
	var nilState *UseSkillTurnState
	ctx := context.WithValue(context.Background(), CtxKeyUseSkillTurn, nilState)
	_, ok := UseSkillTurnFromCtx(ctx)
	if ok {
		t.Error("explicit nil should return ok=false (defensive guard)")
	}
}

func TestWithAgentBaseToolNames_RoundTrip(t *testing.T) {
	want := []string{"bash_exec", "kb_search", "web_search"}
	ctx := WithAgentBaseToolNames(context.Background(), want)
	got, ok := AgentBaseToolNamesFromCtx(ctx)
	if !ok {
		t.Fatal("AgentBaseToolNamesFromCtx should find injected list")
	}
	if len(got) != len(want) {
		t.Errorf("got len %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAgentBaseToolNamesFromCtx_EmptyCtx_NotOK(t *testing.T) {
	_, ok := AgentBaseToolNamesFromCtx(context.Background())
	if ok {
		t.Error("empty ctx should return ok=false")
	}
}

func TestWithSkillBindings_RoundTrip(t *testing.T) {
	want := []model.AgentSkillBinding{
		{ID: 1, AgentID: 100, SkillID: 200, SortOrder: 0},
		{ID: 2, AgentID: 100, SkillID: 201, SortOrder: 1},
	}
	ctx := WithSkillBindings(context.Background(), want)
	got, ok := SkillBindingsFromCtx(ctx)
	if !ok {
		t.Fatal("SkillBindingsFromCtx should find injected bindings")
	}
	if len(got) != 2 || got[0].SkillID != 200 || got[1].SortOrder != 1 {
		t.Errorf("binding round trip mismatch: %+v", got)
	}
}

// ── useSkillTool skeleton ───────────────────────────────────────────────────────

func TestUseSkillTool_Skeleton_Metadata(t *testing.T) {
	tool := NewUseSkillTool()

	if tool.Name() != UseSkillToolName {
		t.Errorf("Name() = %q, want %q", tool.Name(), UseSkillToolName)
	}
	if tool.Description() == "" {
		t.Error("Description should not be empty")
	}
	if tool.UserFacingName() != "调用技能" {
		t.Errorf("UserFacingName = %q, want '调用技能'", tool.UserFacingName())
	}
	if tool.NarrationVerb() != "调用技能" {
		t.Errorf("NarrationVerb = %q, want '调用技能'", tool.NarrationVerb())
	}
	if !tool.IsReadOnly() {
		t.Error("use_skill should be IsReadOnly = true (no DB write, only turn state mutation)")
	}
	if tool.IsDestructive() {
		t.Error("use_skill should NOT be destructive")
	}
	if tool.AlwaysLoad() {
		t.Error("use_skill should NOT AlwaysLoad — runner conditionally adds based on bindings")
	}

	// InputSchema 必须是合法 JSON 且含 name required
	schema := tool.InputSchema()
	if len(schema) == 0 {
		t.Fatal("InputSchema should not be empty")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	required, _ := parsed["required"].([]interface{})
	if len(required) != 1 || required[0] != "name" {
		t.Errorf("InputSchema required should be ['name'], got %v", required)
	}
}

func TestUseSkillTool_Skeleton_Execute_StubReturnsError(t *testing.T) {
	// T02 stub: Execute always returns error tool result (let LLM see status=error)
	tool := NewUseSkillTool()
	out, err := tool.Execute(context.Background(), ToolInput(`{"name":"x"}`))
	if err != nil {
		t.Errorf("stub Execute should not return Go-level error (always returns error tool result instead), got %v", err)
	}
	var parsed map[string]string
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		t.Fatalf("stub output not valid JSON: %v", jerr)
	}
	if parsed["status"] != "error" {
		t.Errorf("stub status should be 'error', got %q", parsed["status"])
	}
	if parsed["error"] == "" {
		t.Error("stub error message should not be empty")
	}
}

// ── jsonErr 辅助 ────────────────────────────────────────────────────────────

func TestJsonErr_FormatString(t *testing.T) {
	got := jsonErr("technical error: %s (code %d)", "DB down", 500)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("jsonErr output not valid JSON: %v", err)
	}
	if parsed["status"] != "error" {
		t.Errorf("status should be 'error', got %q", parsed["status"])
	}
	if parsed["error"] != "technical error: DB down (code 500)" {
		t.Errorf("error msg not formatted: %q", parsed["error"])
	}
}
