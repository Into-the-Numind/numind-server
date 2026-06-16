package agent

import (
	"encoding/json"
	"testing"
)

// These tests reproduce the class of run-killer where the model emits a numeric or
// boolean tool argument as a JSON string ("30") or a float (30.0). A strict int/bool
// struct field makes json.Unmarshal fail; that hard error inside a tool's Execute
// becomes a NodeRunError that terminates the whole agent run. Each struct must
// tolerate the model's type wobble (mirroring web_search's coerceJSONInt).

func TestRunPythonInput_CoercesStringAndFloatTimeout(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"string", `{"code":"print(1)","timeout_seconds":"30"}`},
		{"float", `{"code":"print(1)","timeout_seconds":30.0}`},
		{"int", `{"code":"print(1)","timeout_seconds":30}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var in runPythonInput
			if err := json.Unmarshal([]byte(c.body), &in); err != nil {
				t.Fatalf("unmarshal %s timeout must not fail: %v", c.name, err)
			}
			if in.TimeoutSeconds != 30 {
				t.Errorf("expected TimeoutSeconds=30, got %d", in.TimeoutSeconds)
			}
			if in.Code != "print(1)" {
				t.Errorf("Code lost during coercion: %q", in.Code)
			}
		})
	}
}

func TestAnnotateImageInput_CoercesStringCoordinates(t *testing.T) {
	body := `{"attachment_url":"https://x/y.png","regions":[{"x":"10","y":"20","width":"30","height":"40","label":"a"}]}`
	var in annotateImageInput
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("string coordinates must not fail unmarshal: %v", err)
	}
	if len(in.Regions) != 1 {
		t.Fatalf("expected 1 region, got %d", len(in.Regions))
	}
	r := in.Regions[0]
	if r.X != 10 || r.Y != 20 || r.Width != 30 || r.Height != 40 {
		t.Errorf("coordinates not coerced: %+v", r)
	}
	if r.Label != "a" {
		t.Errorf("label lost: %q", r.Label)
	}
}

func TestMemoryReadInput_CoercesStringLimit(t *testing.T) {
	var in memoryReadToolInput
	if err := json.Unmarshal([]byte(`{"kind":"fact","limit":"5"}`), &in); err != nil {
		t.Fatalf("string limit must not fail: %v", err)
	}
	if in.Limit != 5 {
		t.Errorf("expected Limit=5, got %d", in.Limit)
	}
	if in.Kind != "fact" {
		t.Errorf("kind lost: %q", in.Kind)
	}
}

func TestCreateJSONInput_CoercesStringPretty(t *testing.T) {
	var in createJSONInput
	if err := json.Unmarshal([]byte(`{"data":{"a":1},"pretty":"true"}`), &in); err != nil {
		t.Fatalf("string pretty must not fail: %v", err)
	}
	if !in.Pretty {
		t.Errorf("expected Pretty=true from \"true\"")
	}
	if in.Data == nil {
		t.Errorf("data lost during coercion")
	}
}

func TestCreatePNGChartInput_CoercesStringValuesAndDims(t *testing.T) {
	body := `{"chart_type":"bar","title":"t","data":{"labels":["a","b"],"series":[{"name":"s","values":["1","2.5"]}]},"options":{"width":"800","height":"600"}}`
	var in createPNGChartInput
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("string values/dims must not fail: %v", err)
	}
	if len(in.Data.Series) != 1 || len(in.Data.Series[0].Values) != 2 {
		t.Fatalf("series values not parsed: %+v", in.Data.Series)
	}
	if in.Data.Series[0].Values[0] != 1 || in.Data.Series[0].Values[1] != 2.5 {
		t.Errorf("values not coerced: %v", in.Data.Series[0].Values)
	}
	if in.Options == nil || in.Options.Width != 800 || in.Options.Height != 600 {
		t.Errorf("options dims not coerced: %+v", in.Options)
	}
}
