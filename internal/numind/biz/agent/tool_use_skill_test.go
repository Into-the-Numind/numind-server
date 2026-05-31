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

// 空/nil 切片明确返回 ok=false (P2-2 fix)
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

func TestUseSkillTurn_Execute_HappyPath_LoadsBody(t *testing.T) {
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
	// S4-D27: body 字段必须含 system-reminder 包装的全文 (tool result 通道，LLM 必读)
	bodyStr, _ := parsed["body"].(string)
	if bodyStr == "" || !contains(bodyStr, "<system-reminder>") || !contains(bodyStr, sk.BodyMd) {
		t.Errorf("ack body should contain <system-reminder> wrapped full body, got %q", bodyStr)
	}

	// turn state mutation 检查
	if len(turn.PendingSkills) != 1 {
		t.Fatalf("PendingSkills should have 1 entry, got %d", len(turn.PendingSkills))
	}
	if turn.PendingSkills[0].Body != sk.BodyMd {
		t.Errorf("PendingSkills[0].Body should be set to skill body, got %q", turn.PendingSkills[0].Body)
	}
	if turn.PendingSkills[0].Name != sk.Name {
		t.Errorf("PendingSkills[0].Name = %q, want %q", turn.PendingSkills[0].Name, sk.Name)
	}
	if turn.PendingSkills[0].Version != int(sk.Version) {
		t.Errorf("PendingSkills[0].Version = %d, want %d", turn.PendingSkills[0].Version, sk.Version)
	}
	if turn.InvocationCount != 1 {
		t.Errorf("InvocationCount = %d, want 1", turn.InvocationCount)
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
	if len(turn.PendingSkills) != 0 {
		t.Errorf("PendingSkills should be empty when lookup fails, got %d entries", len(turn.PendingSkills))
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

// 同 turn 串调 use_skill(A) → use_skill(B) → use_skill(C)：PendingSkills 必须按调用序
// append 全部三条；任何一条丢失 = outer-loop 注入路径启用时 LLM 漏看 Skill 指引
// (fix/fix-use-skill-pending-body-overwrite — 修复前是单字段覆盖)。
func TestUseSkillTurn_Execute_MultipleCalls_PendingSkillsAccumulate(t *testing.T) {
	// Version 1/2/3 + body/name 都互不相同，确保 == 比较能完整区分三条
	// (顺序错乱 / 字段错配 都会被抓)。
	skA := &model.Skill{ID: 1, ParentUserID: 100, Name: "SkillA", BodyMd: "bodyA-detailed-guide", Version: 1, IsActive: true, AllowedTools: []byte(`["toolA"]`)}
	skB := &model.Skill{ID: 2, ParentUserID: 100, Name: "SkillB", BodyMd: "bodyB-detailed-guide", Version: 2, IsActive: true, AllowedTools: []byte(`["toolB"]`)}
	skC := &model.Skill{ID: 3, ParentUserID: 100, Name: "SkillC", BodyMd: "bodyC-detailed-guide", Version: 3, IsActive: true, AllowedTools: []byte(`[]`)}
	ctx, turn := buildTurnWithSkills(t, skA, skB, skC)

	tool := NewUseSkillTool()
	for _, name := range []string{"SkillA", "SkillB", "SkillC"} {
		_, err := tool.Execute(ctx, ToolInput(fmt.Sprintf(`{"name":%q}`, name)))
		if err != nil {
			t.Fatalf("Execute(%s) returned Go error: %v", name, err)
		}
	}

	// 核心断言：PendingSkills.len == invocation_count，证明无覆盖
	if len(turn.PendingSkills) != turn.InvocationCount {
		t.Fatalf("PendingSkills len = %d, want == InvocationCount %d (no entries should be overwritten)",
			len(turn.PendingSkills), turn.InvocationCount)
	}
	if len(turn.PendingSkills) != 3 {
		t.Fatalf("PendingSkills len = %d, want 3", len(turn.PendingSkills))
	}

	// 按调用顺序断言 — 顺序错乱也是 latent bug
	want := []PendingSkill{
		{Name: "SkillA", Version: 1, Body: "bodyA-detailed-guide"},
		{Name: "SkillB", Version: 2, Body: "bodyB-detailed-guide"},
		{Name: "SkillC", Version: 3, Body: "bodyC-detailed-guide"},
	}
	for i, w := range want {
		if turn.PendingSkills[i] != w {
			t.Errorf("PendingSkills[%d] = %+v, want %+v", i, turn.PendingSkills[i], w)
		}
	}
}

// 成功调用 → 失败调用：失败 (NameNotFound) 不应破坏已 append 的 PendingSkills slice
// (append 路径的典型回归场景：早返回前若 mutate slice 就会污染)。
func TestUseSkillTurn_Execute_SuccessThenNotFound_DoesNotMutateExisting(t *testing.T) {
	sk := &model.Skill{ID: 1, ParentUserID: 100, Name: "GoodSkill", BodyMd: "good-body", Version: 5, IsActive: true, AllowedTools: []byte(`[]`)}
	ctx, turn := buildTurnWithSkills(t, sk)

	tool := NewUseSkillTool()
	if _, err := tool.Execute(ctx, ToolInput(`{"name":"GoodSkill"}`)); err != nil {
		t.Fatalf("first Execute Go error: %v", err)
	}
	if _, err := tool.Execute(ctx, ToolInput(`{"name":"DoesNotExist"}`)); err != nil {
		t.Fatalf("second Execute Go error: %v", err)
	}

	if len(turn.PendingSkills) != 1 {
		t.Fatalf("PendingSkills len = %d, want 1 (failed lookup must not mutate)", len(turn.PendingSkills))
	}
	want := PendingSkill{Name: "GoodSkill", Version: 5, Body: "good-body"}
	if turn.PendingSkills[0] != want {
		t.Errorf("PendingSkills[0] = %+v, want %+v", turn.PendingSkills[0], want)
	}
	if turn.InvocationCount != 1 {
		t.Errorf("InvocationCount = %d, want 1 (failed lookup must not count)", turn.InvocationCount)
	}
}

// open-tools-skill-as-guidance: use_skill no longer reads allowed_tools (skills
// don't gate/unlock tools anymore — full-open). A malformed allowed_tools DB field
// is simply ignored; the skill still loads (status "loaded", body + PendingSkills set).
func TestUseSkillTurn_Execute_MalformedAllowedToolsIgnored_StillLoaded(t *testing.T) {
	sk := fixedSkill(42, "格式异常技能", "## 指引...", `not valid json [`)
	ctx, turn := buildTurnWithSkills(t, sk)

	tool := NewUseSkillTool()
	out, err := tool.Execute(ctx, ToolInput(`{"name":"格式异常技能"}`))
	if err != nil {
		t.Fatalf("malformed allowed_tools should not abort, got Go error %v", err)
	}
	var parsed map[string]any
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		t.Fatalf("ack not valid JSON: %v", jerr)
	}
	// LLM 视角技能仍载入成功
	if parsed["status"] != "loaded" {
		t.Errorf("ack status should be 'loaded' (warn 不影响 LLM 流程), got %v", parsed["status"])
	}
	// Skill body 已写入 PendingSkills，turn.InvocationCount++
	if len(turn.PendingSkills) != 1 || turn.PendingSkills[0].Body != sk.BodyMd {
		t.Error("PendingSkills[0].Body should be set even with malformed allowed_tools")
	}
	if turn.InvocationCount != 1 {
		t.Errorf("InvocationCount = %d, want 1 (cap should be consumed)", turn.InvocationCount)
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
