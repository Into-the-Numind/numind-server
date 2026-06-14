package narration

import (
	"strings"
	"testing"
	"text/template"
	"unicode/utf8"
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
	// To actually exercise the defer-recover path, we need a template that
	// PANICS at execute time (not just returns an error). text/template panics
	// when a registered FuncMap function panics during Execute. We can't
	// register a FuncMap on a yaml-loaded template, so we build a custom
	// template directly and exercise renderTemplate at the helper level.
	tmpl := template.Must(
		template.New("boom").
			Funcs(template.FuncMap{
				"boom": func() string { panic("intentional panic for test") },
			}).
			Option("missingkey=zero").
			Parse(`{{ boom }}`),
	)
	out := renderTemplate(tmpl, nil)
	if out != "" {
		t.Errorf("expected empty out on panic recovery, got %q", out)
	}
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

func Test_NewRendererFromPath_FileMissing_Errors(t *testing.T) {
	_, err := NewRendererFromPath("/nonexistent/path/does/not/exist.yaml")
	if err == nil {
		t.Fatal("expected file-read error, got nil")
	}
	if !strings.Contains(err.Error(), "read yaml") {
		t.Errorf("error should mention 'read yaml', got: %v", err)
	}
}

func Test_CompileTemplates_AllSlotsBlank_NoError(t *testing.T) {
	// All-empty slot strings are valid (yield nil *template.Template).
	// Renderer.Render with nil template returns "".
	src := `
tools:
  bash_exec:
    verb: v
    detail_template: ""
    use_template: ""
    result_template: ""
    error_template: ""
    rejected_template: ""
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
		t.Errorf("expected empty msg from all-blank templates, got %q", msg)
	}
}

func Test_TemplateFuncs_Truncate(t *testing.T) {
	// Exercise the truncate func directly via a template.
	src := `
tools:
  bash_exec:
    verb: v
    use_template: "{{ truncate 5 .input.long }}"
    result_template: "{{ truncate 100 .input.long }}"
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
		Input:    map[string]any{"long": "abcdefghij"}, // 10 chars
		Result:   map[string]any{},
	})
	if msg != "abcde..." {
		t.Errorf("truncate 5: want 'abcde...', got %q", msg)
	}

	_, _, msg2 := r.Render(renderRequest{
		ToolName: "bash_exec",
		State:    StateResult,
		Input:    map[string]any{"long": "short"},
		Result:   map[string]any{},
	})
	if msg2 != "short" {
		t.Errorf("truncate 100 on short input: want 'short', got %q", msg2)
	}
}

func Test_TemplateFuncs_Default_NonEmptyStringPassesThrough(t *testing.T) {
	src := `
tools:
  bash_exec:
    verb: v
    use_template: "{{ default \"fallback\" .input.present }}"
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
		Input:    map[string]any{"present": "actual_value"},
		Result:   map[string]any{},
	})
	if msg != "actual_value" {
		t.Errorf("default should pass through non-empty val: want 'actual_value', got %q", msg)
	}
}

func Test_TemplateFuncs_Default_NonStringValSprintfd(t *testing.T) {
	// Non-string val (e.g., int from JSON unmarshal) coerced via Sprintf.
	src := `
tools:
  bash_exec:
    verb: v
    use_template: "{{ default \"fallback\" .input.num }}"
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
		Input:    map[string]any{"num": 42},
		Result:   map[string]any{},
	})
	if msg != "42" {
		t.Errorf("default with int val: want '42', got %q", msg)
	}
}

func Test_TemplateFuncs_Truncate_RuneSafe_NoMojibake(t *testing.T) {
	// Byte-slicing a CJK string (the OLD truncate impl: s[:n]) cuts mid-rune and
	// produces mojibake. These templates now interpolate user-facing CJK (search
	// query, page title), so truncate MUST slice on rune boundaries.
	src := `
tools:
  bash_exec:
    verb: v
    use_template: "{{ truncate 4 .input.q }}"
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
		Input:    map[string]any{"q": "四川莫小派小红书陪跑"}, // 10 runes
		Result:   map[string]any{},
	})
	if msg != "四川莫小..." {
		t.Errorf("rune-safe truncate: want '四川莫小...', got %q", msg)
	}
	if !utf8.ValidString(msg) {
		t.Errorf("truncate produced invalid UTF-8 (mojibake): %q", msg)
	}
}

// agent-progress-clarity (2026-06-14): these tests pin the real production
// templates that surface the search query / fetched URL / page title in the
// narration message, plus their fallbacks. They run against the SHIPPED yaml so
// a future edit that drops the query interpolation fails CI.
func Test_RealYAML_WebSearch_SurfacesQueryAndCount(t *testing.T) {
	r, err := NewRendererFromPath("../../../../configs/tool-display.yaml")
	if err != nil {
		t.Fatalf("load yaml: %v", err)
	}

	// StateUse with a query → message carries the query, not the generic "网络".
	_, _, use := r.Render(renderRequest{
		ToolName: "web_search",
		State:    StateUse,
		Input:    map[string]any{"query": "四川莫小派 小红书陪跑"},
		Result:   map[string]any{},
	})
	if !strings.Contains(use, "四川莫小派") {
		t.Errorf("web_search use should contain the query, got %q", use)
	}
	if strings.Contains(use, "网络") {
		t.Errorf("web_search WITH query must not fall back to '网络', got %q", use)
	}

	// No query (missing key) → falls back to the static "网络" label, never empty.
	_, _, fallback := r.Render(renderRequest{
		ToolName: "web_search",
		State:    StateUse,
		Input:    map[string]any{},
		Result:   map[string]any{},
	})
	if !strings.Contains(fallback, "网络") {
		t.Errorf("web_search WITHOUT query should fall back to '网络', got %q", fallback)
	}

	// StateResult with results → count surfaced.
	_, _, res := r.Render(renderRequest{
		ToolName: "web_search",
		State:    StateResult,
		Input:    map[string]any{},
		Result:   map[string]any{"results": []any{"a", "b", "c"}},
	})
	if !strings.Contains(res, "3") {
		t.Errorf("web_search result should show count 3, got %q", res)
	}

	// StateResult with no results → safe fallback label. Cover BOTH the missing-key
	// shape and the actual soft-error shape returnSoftError marshals: {"results":[]}.
	for _, empty := range []map[string]any{{}, {"results": []any{}}} {
		_, _, res0 := r.Render(renderRequest{
			ToolName: "web_search",
			State:    StateResult,
			Input:    map[string]any{},
			Result:   empty,
		})
		if res0 != "已获取搜索结果" {
			t.Errorf("web_search result with empty results %v should be '已获取搜索结果', got %q", empty, res0)
		}
	}
}

func Test_RealYAML_WebFetch_SurfacesURLAndTitle(t *testing.T) {
	r, err := NewRendererFromPath("../../../../configs/tool-display.yaml")
	if err != nil {
		t.Fatalf("load yaml: %v", err)
	}
	_, _, use := r.Render(renderRequest{
		ToolName: "web_fetch",
		State:    StateUse,
		Input:    map[string]any{"url": "https://example.com/page"},
		Result:   map[string]any{},
	})
	if !strings.Contains(use, "example.com") {
		t.Errorf("web_fetch use should contain the url, got %q", use)
	}
	_, _, res := r.Render(renderRequest{
		ToolName: "web_fetch",
		State:    StateResult,
		Input:    map[string]any{},
		Result:   map[string]any{"title": "四川莫小派官网介绍"},
	})
	if !strings.Contains(res, "四川莫小派官网介绍") {
		t.Errorf("web_fetch result should contain the page title, got %q", res)
	}

	// Soft-error path: returnSoftError sets BOTH title (a Chinese error label) and
	// error. The template must NOT render "已读取：网址被安全策略拦截" — the error
	// guard makes it fall back to the neutral "已读取网页".
	_, _, softErr := r.Render(renderRequest{
		ToolName: "web_fetch",
		State:    StateResult,
		Input:    map[string]any{},
		Result:   map[string]any{"title": "网址被安全策略拦截", "error": "ERROR: web_fetch: blocked"},
	})
	if softErr != "已读取网页" {
		t.Errorf("web_fetch soft-error result should fall back to '已读取网页', got %q", softErr)
	}
}

// MANDATORY (S3 P0-1 fix): disk-load smoke test against the production yaml file.
// Validates that the file ships shippable, all 5 built-in tools have entries,
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
