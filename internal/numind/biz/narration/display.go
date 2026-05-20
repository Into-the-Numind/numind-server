package narration

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"text/template"

	"gopkg.in/yaml.v3"
)

// YAMLConfig is the structure of configs/tool-display.yaml.
type YAMLConfig struct {
	Tools    map[string]ToolTemplates `yaml:"tools"`
	Defaults ToolTemplates            `yaml:"defaults"`
}

// ToolTemplates holds the v1-emitted template strings + verb/detail.
// All fields are template source strings (text/template); Renderer compiles
// them at NewRenderer time so runtime hits no parse cost.
type ToolTemplates struct {
	Verb             string `yaml:"verb"`
	DetailTemplate   string `yaml:"detail_template"`
	UseTemplate      string `yaml:"use_template"`
	ResultTemplate   string `yaml:"result_template"`
	ErrorTemplate    string `yaml:"error_template"`
	RejectedTemplate string `yaml:"rejected_template"`
}

// Renderer holds parsed templates ready for execution.
// Once created, Renderer is read-only and safe for concurrent use.
type Renderer struct {
	tools    map[string]*compiledTemplates
	defaults *compiledTemplates
}

type compiledTemplates struct {
	verb     string
	detail   *template.Template
	use      *template.Template
	result   *template.Template
	err      *template.Template
	rejected *template.Template
}

// renderRequest is the internal struct passed to Render.
// Translator (translator.go) builds this from EmitPayload via buildTemplateData.
type renderRequest struct {
	ToolName       string
	State          State
	Input          map[string]any
	Result         map[string]any
	ReasonFriendly string
}

// NewRendererFromPath loads YAML from disk and compiles all templates.
// Per S1-D9 + S1-D16, all parse-time failures are fail-fast.
func NewRendererFromPath(path string) (*Renderer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read yaml %q: %w", path, err)
	}
	return NewRendererFromBytes(data)
}

// NewRendererFromBytes is the test-friendly variant; same fail-fast semantics.
func NewRendererFromBytes(data []byte) (*Renderer, error) {
	var cfg YAMLConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal yaml: %w", err)
	}
	if cfg.Defaults == (ToolTemplates{}) {
		return nil, fmt.Errorf("defaults block required (block must contain at least one non-empty template)")
	}

	defaults, err := compileTemplates("defaults", cfg.Defaults)
	if err != nil {
		return nil, err
	}

	tools := make(map[string]*compiledTemplates, len(cfg.Tools))
	for name, tmpls := range cfg.Tools {
		compiled, err := compileTemplates(name, tmpls)
		if err != nil {
			return nil, err
		}
		tools[name] = compiled
	}

	return &Renderer{tools: tools, defaults: defaults}, nil
}

// compileTemplates parses every non-empty template string under a single key.
// Per S2-D1, every template.New uses .Option("missingkey=zero") so absent map
// keys evaluate to empty string instead of the literal "<no value>".
func compileTemplates(key string, src ToolTemplates) (*compiledTemplates, error) {
	parse := func(slot, source string) (*template.Template, error) {
		if source == "" {
			return nil, nil
		}
		t, err := template.New(key + "." + slot).
			Funcs(templateFuncs()).
			Option("missingkey=zero").
			Parse(source)
		if err != nil {
			return nil, fmt.Errorf("%s.%s parse: %w", key, slot, err)
		}
		return t, nil
	}

	out := &compiledTemplates{verb: src.Verb}
	var err error
	if out.detail, err = parse("detail_template", src.DetailTemplate); err != nil {
		return nil, err
	}
	if out.use, err = parse("use_template", src.UseTemplate); err != nil {
		return nil, err
	}
	if out.result, err = parse("result_template", src.ResultTemplate); err != nil {
		return nil, err
	}
	if out.err, err = parse("error_template", src.ErrorTemplate); err != nil {
		return nil, err
	}
	if out.rejected, err = parse("rejected_template", src.RejectedTemplate); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidateToolNames returns the names that exist in the registered tool set
// but have no entry in yaml. Per S1-D10, missing names use defaults fallback;
// this is observability for boot, not a fatal check.
// Returned slice is sorted for deterministic boot-log output and test assertions.
func (r *Renderer) ValidateToolNames(names []string) (missing []string) {
	for _, n := range names {
		if _, ok := r.tools[n]; !ok {
			missing = append(missing, n)
		}
	}
	sort.Strings(missing)
	return missing
}

// Render dispatches by State + tool name. Falls back to defaults block when
// the tool is not in yaml. NEVER returns error; on template execution panic,
// produces ("","","") and the caller (Translator) falls through to LLMFallback.
//
// Returns (verb, detail, message). Empty message means "this slot has no
// configured template OR template execution failed" — caller decides next step.
func (r *Renderer) Render(req renderRequest) (verb, detail, message string) {
	ct := r.tools[req.ToolName]
	if ct == nil {
		ct = r.defaults
	}
	verb = ct.verb

	// Build the template data map with verb/detail prefilled.
	// detail is computed from detail_template (which may itself reference input).
	data := map[string]any{
		"input":           req.Input,
		"result":          req.Result,
		"reason_friendly": req.ReasonFriendly,
		"verb":            verb,
		"detail":          "",
	}
	detail = renderTemplate(ct.detail, data)
	data["detail"] = detail

	// Pick template by state.
	var tmpl *template.Template
	switch req.State {
	case StateUse, StateQueued, StateProgress:
		tmpl = ct.use
	case StateResult:
		tmpl = ct.result
	case StateError:
		tmpl = ct.err
	case StateRejected:
		tmpl = ct.rejected
	}
	message = renderTemplate(tmpl, data)
	return verb, detail, message
}

// renderTemplate executes a single template with defer-recover safety.
// Returns "" if tmpl is nil OR execution fails OR a panic was recovered.
//
// NOTE (S4 amendment to spec §3): spec proposed a `(out string, ok bool)`
// signature to let the caller distinguish "no template configured" from
// "execution panicked." However spec §5 translator only checks
// `if message == "" { fallback... }`, never the bool. The two-value return
// is dead weight; collapsed to single value. If a future caller (e.g. a
// metrics layer in #11) needs the distinction, restore the bool then.
func renderTemplate(tmpl *template.Template, data any) (out string) {
	if tmpl == nil {
		return ""
	}
	defer func() {
		if r := recover(); r != nil {
			out = ""
		}
	}()
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return ""
	}
	return buf.String()
}

// templateFuncs is the FuncMap registered on all templates.
//
// NOTE (S2 P0-2 fix): `len` is a text/template BUILTIN — re-registering panics
// "function len already defined". Keep this map free of any builtin name.
// Builtins available without registration: and, call, html, index, slice, js,
// len, not, or, print, printf, println, urlquery, eq, ne, lt, le, gt, ge.
//
// NOTE (S2 P1 amendment): `default`'s val parameter is `any`, NOT `string` as
// loosely sketched in spec §3. With missingkey=zero on a map[string]any, the
// lookup returns nil (zero value of `any`), not "". A `string`-typed parameter
// would fail type assertion at template execution. The `any`-typed parameter
// safely coerces nil/empty-string/non-string values to the fallback, matching
// the intuitive `default "fallback" .input.maybe_missing` template idiom.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"truncate": func(n int, s string) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"default": func(fallback string, val any) string {
			if val == nil {
				return fallback
			}
			if s, ok := val.(string); ok {
				if s == "" {
					return fallback
				}
				return s
			}
			return fmt.Sprintf("%v", val)
		},
	}
}
