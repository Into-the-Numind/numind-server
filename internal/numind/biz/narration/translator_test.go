package narration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func mkTranslator(t *testing.T, yamlSrc string, fallback LLMFallback) *Translator {
	t.Helper()
	r, err := NewRendererFromBytes([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("NewRendererFromBytes: %v", err)
	}
	return NewTranslator(r, fallback)
}

func freezeTime(t *testing.T, ts time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return ts }
	t.Cleanup(func() { nowFunc = prev })
}

func TestTranslator_YamlHit_Use(t *testing.T) {
	tr := mkTranslator(t, minimalValidYAML, nil)
	ts := time.Unix(1700000000, 0)
	freezeTime(t, ts)

	ev := tr.Translate(context.Background(), EmitPayload{
		RunID:      1,
		ToolCallID: "1-1",
	}, "bash_exec", StateUse)

	if ev.RunID != 1 || ev.ToolCallID != "1-1" {
		t.Errorf("ID mismatch: %+v", ev)
	}
	if ev.Verb != "正在执行" {
		t.Errorf("Verb: got %q", ev.Verb)
	}
	if !strings.Contains(ev.Message, "正在执行") {
		t.Errorf("Message should contain verb: %q", ev.Message)
	}
	if ev.Icon != "⋯" {
		t.Errorf("Icon for StateUse: want ⋯, got %q", ev.Icon)
	}
	if !ev.Timestamp.Equal(ts) {
		t.Errorf("Timestamp: want %v, got %v", ts, ev.Timestamp)
	}
}

func TestTranslator_YamlHit_Result(t *testing.T) {
	tr := mkTranslator(t, minimalValidYAML, nil)
	ev := tr.Translate(context.Background(), EmitPayload{RunID: 2, ToolCallID: "2-1"}, "bash_exec", StateResult)
	if ev.Message != "命令执行完成" {
		t.Errorf("Result message: got %q", ev.Message)
	}
	if ev.Icon != "✓" {
		t.Errorf("Icon for StateResult: want ✓, got %q", ev.Icon)
	}
}

func TestTranslator_YamlHit_Error_UsesReasonFriendly(t *testing.T) {
	tr := mkTranslator(t, minimalValidYAML, nil)
	ev := tr.Translate(context.Background(), EmitPayload{
		RunID:      3,
		ToolCallID: "3-1",
		Err:        context.Canceled,
	}, "bash_exec", StateError)
	if !strings.Contains(ev.Message, "操作被中断") {
		t.Errorf("Error message should contain friendly reason: %q", ev.Message)
	}
	if ev.Icon != "⚠️" {
		t.Errorf("Icon for StateError: want ⚠️, got %q", ev.Icon)
	}
}

func TestTranslator_YamlHit_Error_NoRawErrText(t *testing.T) {
	tr := mkTranslator(t, minimalValidYAML, nil)
	ev := tr.Translate(context.Background(), EmitPayload{
		RunID: 4,
		Err:   errors.New("super secret stack trace abc123"),
	}, "bash_exec", StateError)
	for _, leak := range []string{"super", "secret", "stack", "abc123"} {
		if strings.Contains(ev.Message, leak) {
			t.Errorf("error message leaked %q from raw err: %q", leak, ev.Message)
		}
	}
}

func TestTranslator_YamlHit_Rejected(t *testing.T) {
	tr := mkTranslator(t, minimalValidYAML, nil)
	ev := tr.Translate(context.Background(), EmitPayload{RunID: 5}, "bash_exec", StateRejected)
	if ev.Message != "这个命令被规则拦截了" {
		t.Errorf("Rejected message: got %q", ev.Message)
	}
	if ev.Icon != "✕" {
		t.Errorf("Icon for StateRejected: want ✕, got %q", ev.Icon)
	}
}

func TestTranslator_YamlMiss_FallsBackToStub(t *testing.T) {
	// Use a yaml with NO entry for "unknown_tool" — falls back to defaults
	// in renderer. defaults templates render fine, so we get a defaults message,
	// not the stub. To force the stub path, we need a yaml where defaults.use
	// is empty too.
	src := `
tools:
  bash_exec:
    verb: v
    use_template: "{{ .verb }}"
    result_template: ok
    error_template: bad
    rejected_template: rej
defaults:
  verb: dv
  use_template: ""
  result_template: ok
  error_template: bad
  rejected_template: rej
`
	tr := mkTranslator(t, src, nil)
	ev := tr.Translate(context.Background(), EmitPayload{RunID: 6}, "unknown_tool", StateUse)
	// renderer.Render returns "" for use_template (empty); translator falls back to stub.
	if !strings.Contains(ev.Message, "正在执行") || !strings.Contains(ev.Message, "unknown_tool") {
		t.Errorf("expected stub fallback 'X unknown_tool', got %q", ev.Message)
	}
}

type captureFallback struct {
	called bool
	toolN  string
	st     State
}

func (c *captureFallback) Render(_ context.Context, toolName string, state State, _ EmitPayload) (string, string) {
	c.called = true
	c.toolN = toolName
	c.st = state
	return "FALLBACK_VERB", "FALLBACK_DETAIL"
}

func TestTranslator_YamlMiss_UsesProvidedFallback(t *testing.T) {
	src := `
tools:
  bash_exec:
    verb: v
    use_template: ""
    result_template: ok
    error_template: bad
    rejected_template: rej
defaults:
  verb: dv
  use_template: ""
  result_template: ok
  error_template: bad
  rejected_template: rej
`
	cf := &captureFallback{}
	tr := mkTranslator(t, src, cf)
	ev := tr.Translate(context.Background(), EmitPayload{RunID: 7}, "bash_exec", StateUse)
	if !cf.called {
		t.Fatal("expected fallback to be invoked")
	}
	if cf.toolN != "bash_exec" || cf.st != StateUse {
		t.Errorf("fallback received wrong args: toolN=%q state=%q", cf.toolN, cf.st)
	}
	if ev.Message != "FALLBACK_VERB FALLBACK_DETAIL" {
		t.Errorf("message should combine fallback verb+detail, got %q", ev.Message)
	}
}

func TestTranslator_OverrideMessage_Wins(t *testing.T) {
	tr := mkTranslator(t, minimalValidYAML, nil)
	ev := tr.Translate(context.Background(), EmitPayload{
		RunID:           8,
		OverrideMessage: "学员看到的优先消息",
	}, "bash_exec", StateUse)
	if ev.Message != "学员看到的优先消息" {
		t.Errorf("OverrideMessage should win, got %q", ev.Message)
	}
}

func TestTranslator_InputJSON_RoundtripsToMap(t *testing.T) {
	// Verify that JSON-encoded input fields render through templates.
	src := `
tools:
  document_generate:
    verb: "正在生成"
    detail_template: "{{ default \"文档\" .input.format }}"
    use_template: "{{ .verb }} {{ .detail }}"
    result_template: ok
    error_template: bad
    rejected_template: rej
defaults:
  verb: dv
  use_template: "{{ .verb }}"
  result_template: ok
  error_template: bad
  rejected_template: rej
`
	tr := mkTranslator(t, src, nil)
	input, _ := json.Marshal(map[string]string{"format": "PDF"})
	ev := tr.Translate(context.Background(), EmitPayload{
		RunID: 9,
		Input: input,
	}, "document_generate", StateUse)
	if !strings.Contains(ev.Message, "PDF") {
		t.Errorf("template should interpolate .input.format='PDF', got %q", ev.Message)
	}
}

func TestTranslator_NilInputJSON_DoesNotPanic(t *testing.T) {
	tr := mkTranslator(t, minimalValidYAML, nil)
	_ = tr.Translate(context.Background(), EmitPayload{
		RunID: 10,
		Input: nil, // explicit nil — buildTemplateData must produce empty map
	}, "bash_exec", StateUse)
}

func TestStubLLMFallback_AllStates(t *testing.T) {
	s := stubLLMFallback{}
	cases := []struct {
		state  State
		want   string
		detail string
	}{
		{StateUse, "正在执行", "tool"},
		{StateQueued, "正在执行", "tool"},
		{StateResult, "完成", "tool"},
		{StateError, "执行出错", "tool"},
		{StateRejected, "操作被拦截", "tool"},
		{StateProgress, "处理中", "tool"},
	}
	for _, c := range cases {
		verb, detail := s.Render(context.Background(), "tool", c.state, EmitPayload{})
		if verb != c.want || detail != c.detail {
			t.Errorf("state %q: want (%q, %q), got (%q, %q)", c.state, c.want, c.detail, verb, detail)
		}
	}
}

func TestNewTranslator_NilFallback_UsesStub(t *testing.T) {
	r := mustRenderer(t, minimalValidYAML)
	tr := NewTranslator(r, nil)
	if _, ok := tr.fallback.(stubLLMFallback); !ok {
		t.Errorf("nil fallback should default to stubLLMFallback, got %T", tr.fallback)
	}
}
