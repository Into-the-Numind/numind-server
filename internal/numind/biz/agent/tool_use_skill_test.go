// Skill turn-state + ctx-helper tests (originally agent-mode-v2-skill-invocation).
// open-tools-skill-as-guidance moved the tool itself to tool_load_skill.go, so the
// per-tool Execute tests now live in tool_load_skill_test.go. This file keeps the
// turn-state constructor + ctx helpers + jsonErr tests, plus the shared fixtures
// (buildTurnWithSkills / fixedSkill) reused by tool_load_skill_test.go.
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"numind-server/internal/pkg/model"
)

// ── UseSkillTurnState 构造 ─────────────────────────────────────────────────────

func TestUseSkillTurn_NewState_DefaultCap(t *testing.T) {
	s := NewUseSkillTurnState(0) // 0 → default
	if s.Cap != UseSkillTurnCapDefault {
		t.Errorf("default cap = %d, want %d", s.Cap, UseSkillTurnCapDefault)
	}
	if s.InvocationCount != 0 {
		t.Errorf("InvocationCount should start at 0, got %d", s.InvocationCount)
	}
	if s.SkillByID == nil || s.SkillByName == nil {
		t.Error("SkillByID and SkillByName maps should be non-nil")
	}
	if len(s.PendingSkills) != 0 {
		t.Errorf("PendingSkills should be empty, got %d entries", len(s.PendingSkills))
	}
}

func TestUseSkillTurn_NewState_ExplicitCap(t *testing.T) {
	s := NewUseSkillTurnState(5)
	if s.Cap != 5 {
		t.Errorf("explicit cap = %d, want 5", s.Cap)
	}
}

func TestUseSkillTurn_NewState_NegativeCap_FallsToDefault(t *testing.T) {
	s := NewUseSkillTurnState(-1)
	if s.Cap != UseSkillTurnCapDefault {
		t.Errorf("negative cap should fall to default %d, got %d", UseSkillTurnCapDefault, s.Cap)
	}
}

// ── ctx helpers (round trip) ───────────────────────────────────────────────────

func TestUseSkillTurn_WithCtx_RoundTrip(t *testing.T) {
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

func TestUseSkillTurn_WithSkillBindings_RoundTrip(t *testing.T) {
	want := []model.Skill{
		{ID: 200, Name: "话术", ParentUserID: 100},
		{ID: 201, Name: "画像", ParentUserID: 100},
	}
	ctx := WithSkillBindings(context.Background(), want)
	got, ok := SkillBindingsFromCtx(ctx)
	if !ok {
		t.Fatal("SkillBindingsFromCtx should find injected skills")
	}
	if len(got) != 2 || got[0].ID != 200 || got[1].Name != "画像" {
		t.Errorf("skills round trip mismatch: %+v", got)
	}
}

func TestUseSkillTurn_WithSkillBindings_EmptySlice_NotOK(t *testing.T) {
	ctx := WithSkillBindings(context.Background(), nil)
	if _, ok := SkillBindingsFromCtx(ctx); ok {
		t.Error("nil skills should return ok=false")
	}
	ctx2 := WithSkillBindings(context.Background(), []model.Skill{})
	if _, ok := SkillBindingsFromCtx(ctx2); ok {
		t.Error("empty skills should return ok=false")
	}
}

// ── shared fixtures (also used by tool_load_skill_test.go) ──────────────────────

// buildTurnWithSkills constructs a turn state pre-seeded with the given skills and
// injects it into a ctx, returning both (the state for assertions).
func buildTurnWithSkills(t *testing.T, skills ...*model.Skill) (context.Context, *UseSkillTurnState) {
	t.Helper()
	turn := NewUseSkillTurnState(UseSkillTurnCapDefault)
	for _, sk := range skills {
		turn.SkillByID[sk.ID] = sk
		turn.SkillByName[sk.Name] = sk
	}
	ctx := WithUseSkillTurn(context.Background(), turn)
	return ctx, turn
}

// fixedSkill returns a loadable DB Skill (IsActive + non-empty BodyMd).
func fixedSkill(id uint, name, body string, allowedToolsJSON string) *model.Skill {
	return &model.Skill{
		ID:           id,
		ParentUserID: 100,
		Name:         name,
		Description:  "测试技能",
		WhenToUse:    "测试场景",
		BodyMd:       body,
		Version:      1,
		IsActive:     true,
		AllowedTools: []byte(allowedToolsJSON),
	}
}

// ── jsonErr 辅助 ────────────────────────────────────────────────────────────

func TestUseSkillTurn_JsonErr_FormatString(t *testing.T) {
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
