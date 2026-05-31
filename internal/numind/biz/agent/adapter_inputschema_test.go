package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// schemaStubTool is a minimal FullTool whose InputSchema() is configurable, used
// to exercise the adapter's Info() schema-conversion + fallback paths in
// isolation. It embeds BaseTool so only the methods we care about are overridden.
type schemaStubTool struct {
	BaseTool
	name   string
	schema json.RawMessage
}

func (t *schemaStubTool) Name() string                 { return t.name }
func (t *schemaStubTool) Description() string          { return "stub for schema tests" }
func (t *schemaStubTool) UserFacingName() string       { return t.name }
func (t *schemaStubTool) NarrationVerb() string        { return "stub" }
func (t *schemaStubTool) InputSchema() json.RawMessage { return t.schema }
func (t *schemaStubTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	return ToolResult("{}"), nil
}

var _ FullTool = (*schemaStubTool)(nil)

// propertyNames extracts the top-level property keys from a ParamsOneOf by
// rendering it back to a JSON Schema (the same path Eino uses when building the
// model request). Returns nil if the schema has no properties.
func paramsTopLevelProps(t *testing.T, tool FullTool) []string {
	t.Helper()
	adapter := adaptFullToEinoTool(tool, nil)
	info, err := adapter.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() returned error: %v", err)
	}
	if info.ParamsOneOf == nil {
		t.Fatal("Info().ParamsOneOf is nil — adapter must always return a non-nil ParamsOneOf")
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ParamsOneOf.ToJSONSchema() error: %v", err)
	}
	if js == nil || js.Properties == nil {
		return nil
	}
	var keys []string
	for pair := js.Properties.Oldest(); pair != nil; pair = pair.Next() {
		keys = append(keys, pair.Key)
	}
	return keys
}

func paramsRequired(t *testing.T, tool FullTool) []string {
	t.Helper()
	adapter := adaptFullToEinoTool(tool, nil)
	info, err := adapter.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema() error: %v", err)
	}
	if js == nil {
		return nil
	}
	return js.Required
}

// TestAdapterInfo_PopulatesSchema verifies a tool with a real JSON Schema gets a
// non-empty ParamsOneOf carrying the declared properties + required fields.
func TestAdapterInfo_PopulatesSchema(t *testing.T) {
	tool := &schemaStubTool{
		name: "stub_populated",
		schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query":       {"type": "string", "description": "the query"},
				"max_results": {"type": "integer", "minimum": 1, "maximum": 10}
			},
			"required": ["query"]
		}`),
	}

	props := paramsTopLevelProps(t, tool)
	if len(props) != 2 {
		t.Fatalf("expected 2 properties, got %d: %v", len(props), props)
	}
	got := map[string]bool{}
	for _, p := range props {
		got[p] = true
	}
	if !got["query"] || !got["max_results"] {
		t.Fatalf("expected properties {query, max_results}, got %v", props)
	}

	req := paramsRequired(t, tool)
	if len(req) != 1 || req[0] != "query" {
		t.Fatalf("expected required=[query], got %v", req)
	}
}

// TestAdapterInfo_FallbackNilSchema verifies a tool with InputSchema()==nil (the
// BaseTool default) falls back to empty params: non-nil ParamsOneOf, no props, no
// panic, no error. This is the zero-regression guarantee for paramless tools.
func TestAdapterInfo_FallbackNilSchema(t *testing.T) {
	tool := &schemaStubTool{name: "stub_nil", schema: nil}
	props := paramsTopLevelProps(t, tool)
	if len(props) != 0 {
		t.Fatalf("nil schema must yield empty params, got props %v", props)
	}
}

// TestAdapterInfo_FallbackEmptySchema verifies whitespace-only schema → empty params.
func TestAdapterInfo_FallbackEmptySchema(t *testing.T) {
	tool := &schemaStubTool{name: "stub_empty", schema: json.RawMessage("   \n  ")}
	props := paramsTopLevelProps(t, tool)
	if len(props) != 0 {
		t.Fatalf("empty schema must yield empty params, got props %v", props)
	}
}

// TestAdapterInfo_FallbackMalformedSchema verifies invalid JSON does not panic and
// falls back to empty params (the adapter must never break a run over a bad schema).
func TestAdapterInfo_FallbackMalformedSchema(t *testing.T) {
	tool := &schemaStubTool{name: "stub_bad", schema: json.RawMessage(`{"type":`)}
	// Must not panic; must return empty params.
	props := paramsTopLevelProps(t, tool)
	if len(props) != 0 {
		t.Fatalf("malformed schema must yield empty params, got props %v", props)
	}
}

// TestParamsOneOfFromInputSchema_NeverNil guards the helper's invariant directly.
// Each case is named so a failure points at the exact input.
func TestParamsOneOfFromInputSchema_NeverNil(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"nil", nil},
		{"empty", json.RawMessage("")},
		{"whitespace", json.RawMessage("   ")},
		{"malformed", json.RawMessage(`{"type":`)},
		// JSON null parses into a zero-value schema (non-empty ParamsOneOf, empty
		// schema) — distinct from the empty-params fallback but still never nil.
		// No tool returns null today; this guards the invariant regardless.
		{"json_null", json.RawMessage("null")},
		{"valid", json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := paramsOneOfFromInputSchema("case_"+c.name, c.raw); got == nil {
				t.Fatalf("case %q: paramsOneOfFromInputSchema returned nil", c.name)
			}
		})
	}
}
