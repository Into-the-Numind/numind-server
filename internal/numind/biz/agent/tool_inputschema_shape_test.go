package agent

import (
	"encoding/json"
	"sort"
	"testing"
)

// rawSchemaShape is the minimal JSON-Schema projection we assert against. Tools'
// InputSchema() methods ignore receiver state, so we can call them on zero-value
// structs without wiring real dependencies.
type rawSchemaShape struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func parseShape(t *testing.T, name string, raw json.RawMessage) rawSchemaShape {
	t.Helper()
	if len(raw) == 0 {
		t.Fatalf("%s: InputSchema() returned empty — expected a non-empty JSON Schema", name)
	}
	var s rawSchemaShape
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%s: InputSchema() is not valid JSON: %v\nraw: %s", name, err, string(raw))
	}
	if s.Type != "object" {
		t.Fatalf("%s: schema top-level type=%q, want \"object\"", name, s.Type)
	}
	return s
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func eqStrSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string{}, a...)
	bc := append([]string{}, b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// TestToolInputSchemas_MatchExecuteContract asserts each tool's InputSchema()
// declares exactly the properties + required fields that its Execute() json.Unmarshal
// struct expects (the data contract). Property names are derived from the json tags
// captured during S2 spec authoring.
func TestToolInputSchemas_MatchExecuteContract(t *testing.T) {
	cases := []struct {
		name     string
		raw      json.RawMessage
		props    []string
		required []string
	}{
		// ── Task 2: sandbox / side-effect ──
		{"bash_exec", (&bashExecTool{}).InputSchema(),
			[]string{"command"}, []string{"command"}},
		{"memory_write", (&memoryWriteTool{}).InputSchema(),
			[]string{"kind", "key", "value"}, []string{"kind", "key", "value"}},
		{"memory_read", (&memoryReadTool{}).InputSchema(),
			[]string{"key", "kind", "limit"}, nil},
		{"ask_user_question", (&askUserQuestionTool{}).InputSchema(),
			[]string{"question", "options", "header", "multi_select"}, []string{"question", "options"}},

		// ── Task 3: read ──
		{"web_search", (&webSearchTool{}).InputSchema(),
			[]string{"query", "max_results", "allowed_domains"}, []string{"query", "max_results"}},
		{"web_fetch", (&webFetchTool{}).InputSchema(),
			[]string{"url", "prompt"}, []string{"url"}},
		{"kb_search", (&kbSearchTool{}).InputSchema(),
			[]string{"query", "doc_ids"}, []string{"query"}},
		{"file_read", (&fileReadTool{}).InputSchema(),
			[]string{"file_url", "prompt"}, []string{"file_url"}},

		// ── Task 4: image / data / gen ──
		{"analyze_image", (&analyzeImageTool{}).InputSchema(),
			[]string{"attachment_url", "question"}, []string{"attachment_url"}},
		{"annotate_image", (&annotateImageTool{}).InputSchema(),
			[]string{"attachment_url", "regions"}, []string{"attachment_url", "regions"}},
		{"image_gen", (&imageGenTool{}).InputSchema(),
			[]string{"prompt"}, []string{"prompt"}},
		{"learner_data_query", (&learnerDataQueryTool{}).InputSchema(),
			[]string{"user_id", "field"}, []string{"user_id"}},
		{"document_generate", (&documentGenerateTool{}).InputSchema(),
			[]string{"prompt", "format"}, []string{"prompt"}},
		{"read_skill", (&readSkillTool{}).InputSchema(),
			[]string{"skill_name"}, []string{"skill_name"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := parseShape(t, c.name, c.raw)
			gotProps := keysOf(s.Properties)
			if !eqStrSet(gotProps, c.props) {
				t.Errorf("%s: properties = %v, want %v", c.name, gotProps, c.props)
			}
			if !eqStrSet(s.Required, c.required) {
				t.Errorf("%s: required = %v, want %v", c.name, s.Required, c.required)
			}
		})
	}
}

// TestPreexistingSchemas_AreValidObjectSchemas guards the 7 tools that already
// shipped InputSchema() before this feature: they must remain valid object
// schemas so the adapter feeds them to the LLM (regression for the keystone).
// use_skill's InputSchema() uses no struct state, so a zero-value receiver is safe.
func TestPreexistingSchemas_AreValidObjectSchemas(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"create_csv", (&createCSVTool{}).InputSchema()},
		{"create_json", (&createJSONTool{}).InputSchema()},
		{"create_text", (&createTextTool{}).InputSchema()},
		{"create_html", (&createHTMLTool{}).InputSchema()},
		{"create_png_chart", (&createPNGChartTool{}).InputSchema()},
		{"run_python", (&runPythonTool{}).InputSchema()},
		{"use_skill", (&useSkillTool{}).InputSchema()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := parseShape(t, c.name, c.raw)
			if len(s.Properties) == 0 {
				t.Errorf("%s: expected non-empty properties", c.name)
			}
		})
	}
}

// TestGetCurrentDate_HasNoSchema documents that the paramless tool intentionally
// keeps the BaseTool default (nil) → adapter falls back to empty params.
func TestGetCurrentDate_HasNoSchema(t *testing.T) {
	if raw := (&getCurrentDateTool{}).InputSchema(); len(raw) != 0 {
		t.Errorf("get_current_date should have no InputSchema (paramless), got: %s", string(raw))
	}
}
