// v2 #2 agent-mode-v2-skill-invocation T02 — types + ctx helpers + skeleton tests.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestUseSkillTurn_WithAgentBaseToolNames_RoundTrip(t *testing.T) {
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

func TestUseSkillTurn_AgentBaseToolNamesFromCtx_EmptyCtx_NotOK(t *testing.T) {
	_, ok := AgentBaseToolNamesFromCtx(context.Background())
	if ok {
		t.Error("empty ctx should return ok=false")
	}
}

func TestUseSkillTurn_WithSkillBindings_RoundTrip(t *testing.T) {
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

func TestUseSkillTurn_Tool_Skeleton_Metadata(t *testing.T) {
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

// ── T03 Execute full Invoke 8-step tests ────────────────────────────────

// buildTurnWithSkills 是 T03 测试 helper —
// 构造含 2 个 Skill 的 turn state 注入 ctx，返回 ctx + state（断言用）。
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

// fixedSkill 返回一个合法可载入的 Skill (IsActive + 非空 BodyMd)
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

func TestUseSkillTurn_Execute_HappyPath_LoadsBodyAndAllowedTools(t *testing.T) {
	sk := fixedSkill(42, "销售话术训练", "## 销售技巧\n详细指引...", `["web_search","chart_render"]`)
	ctx, turn := buildTurnWithSkills(t, sk)

	tool := NewUseSkillTool()
	out, err := tool.Execute(ctx, ToolInput(`{"name":"销售话术训练"}`))
	if err != nil {
		t.Fatalf("Execute should not return error, got %v", err)
	}
	var parsed map[string]any
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		t.Fatalf("output not valid JSON: %v", jerr)
	}

	// acknowledgement 检查
	if parsed["status"] != "loaded" {
		t.Errorf("status = %v, want 'loaded'", parsed["status"])
	}
	if parsed["skill_name"] != "销售话术训练" {
		t.Errorf("skill_name = %v, want '销售话术训练'", parsed["skill_name"])
	}

	// turn state mutation 检查
	if turn.PendingBody != sk.BodyMd {
		t.Errorf("PendingBody should be set to skill body, got %q", turn.PendingBody)
	}
	if turn.PendingSkillName != sk.Name {
		t.Errorf("PendingSkillName = %q, want %q", turn.PendingSkillName, sk.Name)
	}
	if turn.PendingSkillVersion != int(sk.Version) {
		t.Errorf("PendingSkillVersion = %d, want %d", turn.PendingSkillVersion, sk.Version)
	}
	if turn.InvocationCount != 1 {
		t.Errorf("InvocationCount = %d, want 1", turn.InvocationCount)
	}
	if _, ok := turn.AllowedTools["web_search"]; !ok {
		t.Error("AllowedTools should contain 'web_search'")
	}
	if _, ok := turn.AllowedTools["chart_render"]; !ok {
		t.Error("AllowedTools should contain 'chart_render'")
	}
}

func TestUseSkillTurn_Execute_NameNotFound_ReturnsError(t *testing.T) {
	sk := fixedSkill(42, "销售话术训练", "body", `[]`)
	ctx, turn := buildTurnWithSkills(t, sk)

	tool := NewUseSkillTool()
	out, _ := tool.Execute(ctx, ToolInput(`{"name":"不存在的技能"}`))
	var parsed map[string]string
	_ = json.Unmarshal(out, &parsed)
	if parsed["status"] != "error" {
		t.Errorf("status = %q, want 'error'", parsed["status"])
	}
	if parsed["error"] == "" || !contains(parsed["error"], "不存在") {
		t.Errorf("error msg should mention '不存在', got %q", parsed["error"])
	}
	// turn state 不应被 mutate
	if turn.PendingBody != "" {
		t.Error("PendingBody should not be set when lookup fails")
	}
	if turn.InvocationCount != 0 {
		t.Errorf("InvocationCount should remain 0, got %d", turn.InvocationCount)
	}
}

func TestUseSkillTurn_Execute_InactiveSkill_ReturnsError(t *testing.T) {
	sk := fixedSkill(42, "禁用技能", "body", `[]`)
	sk.IsActive = false // 已禁用
	ctx, turn := buildTurnWithSkills(t, sk)

	tool := NewUseSkillTool()
	out, _ := tool.Execute(ctx, ToolInput(`{"name":"禁用技能"}`))
	var parsed map[string]string
	_ = json.Unmarshal(out, &parsed)
	if parsed["status"] != "error" || !contains(parsed["error"], "禁用") {
		t.Errorf("expected 禁用 error, got %v", parsed)
	}
	if turn.InvocationCount != 0 {
		t.Error("inactive skill should not count toward cap")
	}
}

func TestUseSkillTurn_Execute_EmptyBody_ReturnsError(t *testing.T) {
	sk := fixedSkill(42, "空内容技能", "" /* empty body */, `[]`)
	ctx, _ := buildTurnWithSkills(t, sk)

	tool := NewUseSkillTool()
	out, _ := tool.Execute(ctx, ToolInput(`{"name":"空内容技能"}`))
	var parsed map[string]string
	_ = json.Unmarshal(out, &parsed)
	if parsed["status"] != "error" || !contains(parsed["error"], "内容为空") {
		t.Errorf("expected 内容为空 error, got %v", parsed)
	}
}

func TestUseSkillTurn_Execute_CapExceeded_ReturnsError(t *testing.T) {
	sk := fixedSkill(42, "正常技能", "body", `[]`)
	ctx, turn := buildTurnWithSkills(t, sk)
	turn.InvocationCount = turn.Cap // 已达 cap

	tool := NewUseSkillTool()
	out, _ := tool.Execute(ctx, ToolInput(`{"name":"正常技能"}`))
	var parsed map[string]string
	_ = json.Unmarshal(out, &parsed)
	if parsed["status"] != "error" || !contains(parsed["error"], "上限") {
		t.Errorf("expected 上限 error, got %v", parsed)
	}
	if turn.InvocationCount != turn.Cap {
		t.Errorf("InvocationCount should remain at cap=%d, got %d", turn.Cap, turn.InvocationCount)
	}
}

func TestUseSkillTurn_Execute_NoTurnState_ReturnsError(t *testing.T) {
	// 没有 inject turn state — 模拟 legacy Agent 误调用
	tool := NewUseSkillTool()
	out, _ := tool.Execute(context.Background(), ToolInput(`{"name":"X"}`))
	var parsed map[string]string
	_ = json.Unmarshal(out, &parsed)
	if parsed["status"] != "error" || !contains(parsed["error"], "未启用") {
		t.Errorf("expected 未启用 error, got %v", parsed)
	}
}

func TestUseSkillTurn_Execute_EmptyName_ReturnsError(t *testing.T) {
	ctx, _ := buildTurnWithSkills(t, fixedSkill(42, "x", "y", `[]`))
	tool := NewUseSkillTool()
	out, _ := tool.Execute(ctx, ToolInput(`{"name":""}`))
	var parsed map[string]string
	_ = json.Unmarshal(out, &parsed)
	if parsed["status"] != "error" || !contains(parsed["error"], "不能为空") {
		t.Errorf("expected 不能为空 error, got %v", parsed)
	}
}

func TestUseSkillTurn_Execute_MalformedJSON_ReturnsError(t *testing.T) {
	ctx, _ := buildTurnWithSkills(t, fixedSkill(42, "x", "y", `[]`))
	tool := NewUseSkillTool()
	out, _ := tool.Execute(ctx, ToolInput(`{not valid json`))
	var parsed map[string]string
	_ = json.Unmarshal(out, &parsed)
	if parsed["status"] != "error" || !contains(parsed["error"], "解析失败") {
		t.Errorf("expected 解析失败 error, got %v", parsed)
	}
}

func TestUseSkillTurn_Execute_MultipleCalls_CountIncrementsToCap(t *testing.T) {
	sk1 := fixedSkill(1, "S1", "b1", `[]`)
	sk2 := fixedSkill(2, "S2", "b2", `[]`)
	sk3 := fixedSkill(3, "S3", "b3", `[]`)
	sk4 := fixedSkill(4, "S4", "b4", `[]`)
	ctx, turn := buildTurnWithSkills(t, sk1, sk2, sk3, sk4)

	tool := NewUseSkillTool()
	for i, name := range []string{"S1", "S2", "S3"} {
		out, _ := tool.Execute(ctx, ToolInput(fmt.Sprintf(`{"name":%q}`, name)))
		var parsed map[string]any
		_ = json.Unmarshal(out, &parsed)
		if parsed["status"] != "loaded" {
			t.Fatalf("call #%d (%s) should succeed, got %v", i+1, name, parsed)
		}
	}
	if turn.InvocationCount != turn.Cap {
		t.Errorf("after 3 calls InvocationCount = %d, want cap=%d", turn.InvocationCount, turn.Cap)
	}

	// 第 4 次必须被 cap 拒绝
	out, _ := tool.Execute(ctx, ToolInput(`{"name":"S4"}`))
	var parsed map[string]string
	_ = json.Unmarshal(out, &parsed)
	if parsed["status"] != "error" || !contains(parsed["error"], "上限") {
		t.Errorf("4th call should hit cap, got %v", parsed)
	}
}

// (note: package-level `contains` helper is defined in tool_document_generate_test.go; we reuse it.)

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
