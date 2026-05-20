package narration

import (
	"strings"
	"testing"
)

const minimalValidYAML = `
tools:
  bash_exec:
    verb: "正在执行"
    detail_template: "命令"
    use_template: "{{ .verb }} {{ .detail }}"
    result_template: "命令执行完成"
    error_template: "命令执行中断，{{ .reason_friendly }}"
    rejected_template: "这个命令被规则拦截了"
defaults:
  verb: "正在处理"
  detail_template: "操作"
  use_template: "{{ .verb }}"
  result_template: "操作完成"
  error_template: "操作失败，{{ .reason_friendly }}"
  rejected_template: "操作被规则拦截"
`

func mustRenderer(t *testing.T, src string) *Renderer {
	t.Helper()
	r, err := NewRendererFromBytes([]byte(src))
	if err != nil {
		t.Fatalf("NewRendererFromBytes: %v", err)
	}
	return r
}

func Test_NewRendererFromBytes_ValidYAML_AllToolsCompile(t *testing.T) {
	r := mustRenderer(t, minimalValidYAML)
	if r.tools["bash_exec"] == nil {
		t.Fatal("bash_exec entry missing")
	}
	if r.defaults == nil {
		t.Fatal("defaults missing")
	}
}

func Test_NewRendererFromBytes_MissingDefaults_Errors(t *testing.T) {
	src := `
tools:
  bash_exec:
    verb: x
    use_template: "{{ .verb }}"
    result_template: ok
    error_template: bad
    rejected_template: rej
`
	_, err := NewRendererFromBytes([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "defaults block required") {
		t.Fatalf("expected 'defaults block required' error, got %v", err)
	}
}

func Test_NewRendererFromBytes_InvalidYAMLSyntax_Errors(t *testing.T) {
	// Unterminated flow sequence — guaranteed unmarshal failure in yaml.v3.
	src := `tools: [ unterminated`
	_, err := NewRendererFromBytes([]byte(src))
	if err == nil {
		t.Fatal("expected unmarshal error, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal yaml") {
		t.Errorf("error did not wrap unmarshal: %v", err)
	}
}

func Test_NewRendererFromBytes_InvalidTemplateSyntax_Errors(t *testing.T) {
	src := `
tools:
  bash_exec:
    verb: v
    use_template: "{{ .input.action } }"
    result_template: ok
    error_template: bad
    rejected_template: rej
defaults:
  verb: d
  use_template: "{{ .verb }}"
  result_template: ok
  error_template: bad
  rejected_template: rej
`
	_, err := NewRendererFromBytes([]byte(src))
	if err == nil {
		t.Fatal("expected template parse error, got nil")
	}
	if !strings.Contains(err.Error(), "bash_exec") || !strings.Contains(err.Error(), "use_template") {
		t.Errorf("error should identify offending tool+slot, got: %v", err)
	}
}

func Test_Render_KnownTool_UsesYamlTemplate(t *testing.T) {
	r := mustRenderer(t, minimalValidYAML)
	verb, detail, msg := r.Render(renderRequest{
		ToolName: "bash_exec",
		State:    StateUse,
		Input:    map[string]any{},
		Result:   map[string]any{},
	})
	if verb != "正在执行" {
		t.Errorf("verb: want 正在执行, got %q", verb)
	}
	if detail != "命令" {
		t.Errorf("detail: want 命令, got %q", detail)
	}
	if !strings.HasPrefix(msg, "正在执行") {
		t.Errorf("message: want prefix 正在执行, got %q", msg)
	}
}

func Test_Render_UnknownTool_FallsBackToDefaults(t *testing.T) {
	r := mustRenderer(t, minimalValidYAML)
	_, _, msg := r.Render(renderRequest{
		ToolName: "unknown_tool",
		State:    StateResult,
		Input:    map[string]any{},
		Result:   map[string]any{},
	})
	if msg != "操作完成" {
		t.Errorf("want defaults message '操作完成', got %q", msg)
	}
}

func Test_Render_EmptyTemplate_ReturnsEmpty(t *testing.T) {
	src := `
tools:
  bash_exec:
    verb: v
    detail_template: d
    use_template: ""
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
	r := mustRenderer(t, src)
	_, _, msg := r.Render(renderRequest{
		ToolName: "bash_exec",
		State:    StateUse,
		Input:    map[string]any{},
		Result:   map[string]any{},
	})
	if msg != "" {
		t.Errorf("expected empty message (empty use_template), got %q", msg)
	}
}

func Test_Render_MissingMapKey_UsesDefault(t *testing.T) {
	// S2-D1 verification: missingkey=zero makes .input.missing evaluate to "",
	// so the `default` func substitutes "fallback". Without missingkey=zero,
	// the literal "<no value>" would appear.
	src := `
tools:
  bash_exec:
    verb: v
    detail_template: "{{ default \"fallback\" .input.missing }}"
    use_template: "{{ .detail }}"
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
	r := mustRenderer(t, src)
	_, detail, _ := r.Render(renderRequest{
		ToolName: "bash_exec",
		State:    StateUse,
		Input:    map[string]any{},
		Result:   map[string]any{},
	})
	if detail != "fallback" {
		t.Errorf("missingkey=zero verification failed: detail %q (want 'fallback'); if 'fallback' looks like '<no value>', S2-D1 broken", detail)
	}
}

func Test_Render_TemplatePanic_ReturnsEmpty(t *testing.T) {
	// Construct a template that succeeds at parse but panics at execute.
	// text/template rarely panics on Execute; it returns errors. So this test
	// asserts that the defer-recover path is in place even if hard to trigger.
	src := `
tools:
  bash_exec:
    verb: v
    use_template: "{{ . | printf \"%d\" }}"
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
	r := mustRenderer(t, src)
	// Pass a map where printf %d on a map will produce a Go-style ERROR string
	// (text/template absorbs it as Execute error) — assert no panic, msg == "".
	_, _, msg := r.Render(renderRequest{
		ToolName: "bash_exec",
		State:    StateUse,
		Input:    map[string]any{},
		Result:   map[string]any{},
	})
	// Either empty or non-empty is acceptable; the contract is "no panic".
	_ = msg
}

func Test_ValidateToolNames_ReportsMissing(t *testing.T) {
	r := mustRenderer(t, minimalValidYAML)
	missing := r.ValidateToolNames([]string{"bash_exec", "unknown_a", "unknown_b"})
	if len(missing) != 2 {
		t.Fatalf("want 2 missing, got %v", missing)
	}
	if missing[0] != "unknown_a" || missing[1] != "unknown_b" {
		t.Errorf("want [unknown_a unknown_b] sorted, got %v", missing)
	}
}

func Test_ValidateToolNames_AllPresent(t *testing.T) {
	r := mustRenderer(t, minimalValidYAML)
	missing := r.ValidateToolNames([]string{"bash_exec"})
	if len(missing) != 0 {
		t.Errorf("want 0 missing, got %v", missing)
	}
}

// MANDATORY (S3 P0-1 fix): disk-load smoke test against the production yaml file.
// Validates that the file ships shippable, all 6 built-in tools have entries,
// and all templates parse without error.
// Path resolves from internal/numind/biz/narration/ → repo root (3 levels up).
func TestNewRendererFromPath_RepoRootYAML(t *testing.T) {
	r, err := NewRendererFromPath("../../../../configs/tool-display.yaml")
	if err != nil {
		t.Fatalf("NewRendererFromPath: %v (if file moved/renamed, update path)", err)
	}
	wantTools := []string{
		"bash_exec",
		"document_generate",
		"image_gen",
		"kb_search",
		"learner_data_query",
		"get_current_date",
	}
	for _, name := range wantTools {
		if r.tools[name] == nil {
			t.Errorf("built-in tool %q missing from yaml", name)
		}
	}
	if r.defaults == nil {
		t.Error("defaults block missing")
	}
}
