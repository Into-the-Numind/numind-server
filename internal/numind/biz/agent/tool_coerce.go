package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// LLMs routinely emit numeric/boolean tool arguments with the wrong JSON type — a
// quoted string ("30", "true") or a float (30.0) where the schema says integer/bool.
// A strict struct field turns that into a hard json.Unmarshal error, and a hard
// error inside a tool's Execute becomes an Eino NodeRunError that TERMINATES the
// whole agent run (web_search hit this on dev run 132). The coerce* helpers below,
// plus the per-tool UnmarshalJSON methods, absorb the type wobble so a working run
// is never killed by the model's formatting. coerceJSONInt lives in tool_web_search.go
// (the original site); the bool/float companions live here. All are package-internal.

// coerceJSONBool parses a raw JSON value that should be a bool but may arrive as a
// bool (true), a quoted string ("true"/"false"/"1"/"0"), or a number (1/0). Empty
// or JSON null yields false.
func coerceJSONBool(raw json.RawMessage) (bool, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return false, nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return false, err
		}
		s = strings.TrimSpace(strings.ToLower(str))
	}
	switch s {
	case "true", "1":
		return true, nil
	case "false", "0", "":
		return false, nil
	}
	// Tolerate other numbers: any non-zero is truthy.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f != 0, nil
	}
	return false, fmt.Errorf("not a bool: %q", s)
}

// coerceJSONFloat parses a raw JSON value that should be a float but may arrive as a
// number (5 / 5.5), a quoted string ("5" / "5.5"), or be empty/null (→ 0).
func coerceJSONFloat(raw json.RawMessage) (float64, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return 0, err
		}
		s = strings.TrimSpace(str)
		if s == "" {
			return 0, nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	return f, nil
}

// coerceJSONFloatSlice parses a JSON array whose elements should be floats but may
// be quoted strings (["1","2.5"]) or numbers ([1,2.5]). Empty/null yields nil.
func coerceJSONFloatSlice(raw json.RawMessage) ([]float64, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, err
	}
	out := make([]float64, 0, len(elems))
	for i, e := range elems {
		f, err := coerceJSONFloat(e)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-tool UnmarshalJSON — kept here (not in each tool file) so the whole loosely-
// typed-input contract reads in one place. Each uses a raw alias struct to avoid
// infinite recursion and coerces only the numeric/bool fields; string/slice fields
// pass through unchanged.
// ─────────────────────────────────────────────────────────────────────────────

func (in *runPythonInput) UnmarshalJSON(data []byte) error {
	var raw struct {
		Code                string          `json:"code"`
		InputFiles          []string        `json:"input_files,omitempty"`
		ExpectedOutputFiles []string        `json:"expected_output_files,omitempty"`
		TimeoutSeconds      json.RawMessage `json:"timeout_seconds,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	in.Code = raw.Code
	in.InputFiles = raw.InputFiles
	in.ExpectedOutputFiles = raw.ExpectedOutputFiles
	n, err := coerceJSONInt(raw.TimeoutSeconds)
	if err != nil {
		return fmt.Errorf("timeout_seconds: %w", err)
	}
	in.TimeoutSeconds = n
	return nil
}

func (r *annotateImageRegion) UnmarshalJSON(data []byte) error {
	var raw struct {
		X      json.RawMessage `json:"x"`
		Y      json.RawMessage `json:"y"`
		Width  json.RawMessage `json:"width"`
		Height json.RawMessage `json:"height"`
		Label  string          `json:"label"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	if r.X, err = coerceJSONInt(raw.X); err != nil {
		return fmt.Errorf("x: %w", err)
	}
	if r.Y, err = coerceJSONInt(raw.Y); err != nil {
		return fmt.Errorf("y: %w", err)
	}
	if r.Width, err = coerceJSONInt(raw.Width); err != nil {
		return fmt.Errorf("width: %w", err)
	}
	if r.Height, err = coerceJSONInt(raw.Height); err != nil {
		return fmt.Errorf("height: %w", err)
	}
	r.Label = raw.Label
	return nil
}

func (in *memoryReadToolInput) UnmarshalJSON(data []byte) error {
	var raw struct {
		Key   string          `json:"key,omitempty"`
		Kind  string          `json:"kind,omitempty"`
		Limit json.RawMessage `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	in.Key = raw.Key
	in.Kind = raw.Kind
	n, err := coerceJSONInt(raw.Limit)
	if err != nil {
		return fmt.Errorf("limit: %w", err)
	}
	in.Limit = n
	return nil
}

func (in *createJSONInput) UnmarshalJSON(data []byte) error {
	var raw struct {
		Data     json.RawMessage `json:"data"`
		Filename string          `json:"filename,omitempty"`
		Pretty   json.RawMessage `json:"pretty,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	in.Filename = raw.Filename
	if len(raw.Data) > 0 {
		if err := json.Unmarshal(raw.Data, &in.Data); err != nil {
			return fmt.Errorf("data: %w", err)
		}
	}
	p, err := coerceJSONBool(raw.Pretty)
	if err != nil {
		return fmt.Errorf("pretty: %w", err)
	}
	in.Pretty = p
	return nil
}

func (s *pngChartSeries) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name    string          `json:"name"`
		Values  json.RawMessage `json:"values"`
		XValues json.RawMessage `json:"x_values,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.Name = raw.Name
	var err error
	if s.Values, err = coerceJSONFloatSlice(raw.Values); err != nil {
		return fmt.Errorf("values: %w", err)
	}
	if s.XValues, err = coerceJSONFloatSlice(raw.XValues); err != nil {
		return fmt.Errorf("x_values: %w", err)
	}
	return nil
}

func (o *pngChartOptions) UnmarshalJSON(data []byte) error {
	var raw struct {
		Width      json.RawMessage `json:"width,omitempty"`
		Height     json.RawMessage `json:"height,omitempty"`
		XLabel     string          `json:"x_label,omitempty"`
		YLabel     string          `json:"y_label,omitempty"`
		ShowLegend json.RawMessage `json:"show_legend,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	if o.Width, err = coerceJSONInt(raw.Width); err != nil {
		return fmt.Errorf("width: %w", err)
	}
	if o.Height, err = coerceJSONInt(raw.Height); err != nil {
		return fmt.Errorf("height: %w", err)
	}
	o.XLabel = raw.XLabel
	o.YLabel = raw.YLabel
	// show_legend is an optional *bool (nil = unset). Coerce a string "true"/"false"
	// like the other bool fields; absent/null leaves it unset.
	if s := strings.TrimSpace(string(raw.ShowLegend)); s != "" && s != "null" {
		b, berr := coerceJSONBool(raw.ShowLegend)
		if berr != nil {
			return fmt.Errorf("show_legend: %w", berr)
		}
		o.ShowLegend = &b
	}
	return nil
}
