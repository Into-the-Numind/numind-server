package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── helpers ─────────────────────────────────────────────────────────────────

// newChartTool returns a *createPNGChartTool for use in tests.
// No external dependencies (COS upload is mocked via context — COS disabled = local placeholder URL).
func newChartTool() *createPNGChartTool { return &createPNGChartTool{} }

// executeChart is a helper that marshals req to JSON and calls tool.Execute.
func executeChart(t *testing.T, req createPNGChartInput) (chartCreateOutput, error) {
	t.Helper()
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	result, execErr := newChartTool().Execute(context.Background(), raw)
	if execErr != nil {
		return chartCreateOutput{}, execErr
	}
	// Check if result is an error payload {"error":"..."}
	var errResult struct {
		Error string `json:"error"`
	}
	if err2 := json.Unmarshal(result, &errResult); err2 == nil && errResult.Error != "" {
		return chartCreateOutput{}, fmt.Errorf("%s", errResult.Error)
	}
	var out chartCreateOutput
	if err3 := json.Unmarshal(result, &out); err3 != nil {
		return chartCreateOutput{}, err3
	}
	return out, nil
}

// ── Bar chart tests ──────────────────────────────────────────────────────────

func TestCreatePNGChartTool_Execute_Bar_Basic(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "bar",
		Title:     "Sales by Quarter",
		Data: pngChartData{
			Labels: []string{"Q1", "Q2", "Q3"},
			Series: []pngChartSeries{
				{Name: "2023", Values: []float64{120, 95, 180}},
				{Name: "2024", Values: []float64{140, 110, 200}},
				{Name: "2025", Values: []float64{160, 130, 220}},
			},
		},
	}
	out, err := executeChart(t, req)
	require.NoError(t, err)
	assert.Equal(t, "bar", out.ChartType)
	assert.NotEmpty(t, out.URL, "URL must be set")
	assert.Equal(t, chartDefaultWidth, out.Width)
	assert.Equal(t, chartDefaultHeight, out.Height)
	assert.Greater(t, out.SizeBytes, int64(0))
}

// ── Line chart test ──────────────────────────────────────────────────────────

func TestCreatePNGChartTool_Execute_Line_Basic(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "line",
		Data: pngChartData{
			Labels: []string{"Jan", "Feb", "Mar", "Apr"},
			Series: []pngChartSeries{
				{Name: "Revenue", Values: []float64{45, 62, 78, 91}},
			},
		},
	}
	out, err := executeChart(t, req)
	require.NoError(t, err)
	assert.Equal(t, "line", out.ChartType)
	assert.NotEmpty(t, out.URL)
}

// ── Pie chart test ───────────────────────────────────────────────────────────

func TestCreatePNGChartTool_Execute_Pie_Basic(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "pie",
		Title:     "Market Share",
		Data: pngChartData{
			Labels: []string{"Alpha", "Beta", "Gamma", "Delta", "Epsilon"},
			Series: []pngChartSeries{
				{Values: []float64{30, 25, 20, 15, 10}},
			},
		},
	}
	out, err := executeChart(t, req)
	require.NoError(t, err)
	assert.Equal(t, "pie", out.ChartType)
	assert.NotEmpty(t, out.URL)
	assert.Greater(t, out.SizeBytes, int64(0))
}

// ── Scatter chart test ───────────────────────────────────────────────────────

func TestCreatePNGChartTool_Execute_Scatter_WithXValues(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "scatter",
		Title:     "Correlation",
		Data: pngChartData{
			Series: []pngChartSeries{
				{
					Name:    "Dataset A",
					XValues: []float64{1.0, 2.0, 3.0, 4.0, 5.0},
					Values:  []float64{2.1, 3.9, 6.2, 7.8, 10.1},
				},
			},
		},
	}
	out, err := executeChart(t, req)
	require.NoError(t, err)
	assert.Equal(t, "scatter", out.ChartType)
	assert.NotEmpty(t, out.URL)
}

// ── Error cases ──────────────────────────────────────────────────────────────

func TestCreatePNGChartTool_Execute_InvalidChartType(t *testing.T) {
	raw, _ := json.Marshal(createPNGChartInput{
		ChartType: "radar",
		Data:      pngChartData{Series: []pngChartSeries{{Values: []float64{1, 2}}}},
	})
	result, execErr := newChartTool().Execute(context.Background(), raw)
	require.NoError(t, execErr, "Execute should not return a Go error for invalid chart type")
	var errResult struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(result, &errResult))
	assert.Contains(t, errResult.Error, "unsupported chart_type")
	assert.Contains(t, errResult.Error, "radar")
}

func TestCreatePNGChartTool_Execute_EmptySeries(t *testing.T) {
	raw, _ := json.Marshal(createPNGChartInput{
		ChartType: "bar",
		Data:      pngChartData{Series: []pngChartSeries{}},
	})
	result, execErr := newChartTool().Execute(context.Background(), raw)
	require.NoError(t, execErr)
	var errResult struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(result, &errResult))
	assert.Contains(t, errResult.Error, "series must not be empty")
}

func TestCreatePNGChartTool_Execute_ScatterMismatchedXY(t *testing.T) {
	raw, _ := json.Marshal(createPNGChartInput{
		ChartType: "scatter",
		Data: pngChartData{
			Series: []pngChartSeries{
				{
					XValues: []float64{1.0, 2.0, 3.0}, // length 3
					Values:  []float64{1.5, 2.5},      // length 2 — mismatch
				},
			},
		},
	})
	result, execErr := newChartTool().Execute(context.Background(), raw)
	require.NoError(t, execErr)
	var errResult struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(result, &errResult))
	assert.Contains(t, errResult.Error, "x_values length")
}

// ── Dimension tests ──────────────────────────────────────────────────────────

func TestCreatePNGChartTool_Execute_DefaultDimensions(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "line",
		Data:      pngChartData{Series: []pngChartSeries{{Values: []float64{1, 2, 3}}}},
		// Options omitted → defaults
	}
	out, err := executeChart(t, req)
	require.NoError(t, err)
	assert.Equal(t, chartDefaultWidth, out.Width, "default width should be 800")
	assert.Equal(t, chartDefaultHeight, out.Height, "default height should be 600")
}

func TestCreatePNGChartTool_Execute_ClampDimensions(t *testing.T) {
	tooSmall := 50
	req := createPNGChartInput{
		ChartType: "bar",
		Data:      pngChartData{Series: []pngChartSeries{{Values: []float64{10, 20}}}},
		Options:   &pngChartOptions{Width: tooSmall, Height: tooSmall},
	}
	out, err := executeChart(t, req)
	require.NoError(t, err)
	assert.Equal(t, chartMinDim, out.Width, "width should be clamped to min")
	assert.Equal(t, chartMinDim, out.Height, "height should be clamped to min")
}

func TestCreatePNGChartTool_Execute_ClampDimensions_ToMax(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "scatter",
		Data:      pngChartData{Series: []pngChartSeries{{Values: []float64{1, 2}}}},
		Options:   &pngChartOptions{Width: 9999, Height: 9999},
	}
	out, err := executeChart(t, req)
	require.NoError(t, err)
	assert.Equal(t, chartMaxDim, out.Width, "width should be clamped to max")
	assert.Equal(t, chartMaxDim, out.Height, "height should be clamped to max")
}

// ── plotToPNG produces valid PNG ─────────────────────────────────────────────

func TestPlotToPNG_ProducesValidPNG(t *testing.T) {
	// Build a minimal bar chart plot directly and verify the PNG bytes.
	req := createPNGChartInput{
		ChartType: "bar",
		Data: pngChartData{
			Labels: []string{"A", "B"},
			Series: []pngChartSeries{{Name: "s", Values: []float64{1, 2}}},
		},
	}
	pngBytes, err := renderBarChart(req, 400, 300)
	require.NoError(t, err)
	require.Greater(t, len(pngBytes), 0)

	// Decode as PNG to verify the bytes are a well-formed image.
	img, imgErr := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, imgErr, "plotToPNG should produce valid PNG bytes")
	bounds := img.Bounds()
	assert.Greater(t, bounds.Dx(), 0, "PNG width must be > 0")
	assert.Greater(t, bounds.Dy(), 0, "PNG height must be > 0")
}

// ── InputSchema is valid JSON ────────────────────────────────────────────────

func TestCreatePNGChartTool_InputSchema_ValidJSON(t *testing.T) {
	schema := newChartTool().InputSchema()
	require.NotNil(t, schema)
	var obj map[string]interface{}
	err := json.Unmarshal(schema, &obj)
	require.NoError(t, err, "InputSchema should return valid JSON")
	assert.Equal(t, "object", obj["type"])
	props, ok := obj["properties"].(map[string]interface{})
	require.True(t, ok, "schema must have 'properties'")
	assert.Contains(t, props, "chart_type")
	assert.Contains(t, props, "data")
}

// ── IsEnabled always returns true ────────────────────────────────────────────

func TestCreatePNGChartTool_IsEnabled_Always(t *testing.T) {
	tool := newChartTool()
	assert.True(t, tool.IsEnabled(ToolConfig{}))
	assert.True(t, tool.IsEnabled(ToolConfig{EnableSandbox: false}))
	assert.True(t, tool.IsEnabled(ToolConfig{EnableSandbox: true}))
}

// ── Metadata methods ─────────────────────────────────────────────────────────

func TestCreatePNGChartTool_Metadata(t *testing.T) {
	tool := newChartTool()
	assert.Equal(t, "create_png_chart", tool.Name())
	assert.Equal(t, "生成图表（PNG）", tool.UserFacingName())
	assert.Equal(t, "生成", tool.NarrationVerb())
	assert.False(t, tool.IsReadOnly(), "uploading to COS makes it not read-only")
	assert.Equal(t, 0, tool.MaxResultSizeChars())
	assert.NotEmpty(t, tool.Description())
}

// ── Scatter fallback: no XValues uses sequential indices ─────────────────────

func TestCreatePNGChartTool_Execute_Scatter_NoXValues_Fallback(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "scatter",
		Data: pngChartData{
			Series: []pngChartSeries{
				{Name: "points", Values: []float64{5.0, 3.0, 8.0}},
				// XValues absent → should use 0, 1, 2 as X
			},
		},
	}
	out, err := executeChart(t, req)
	require.NoError(t, err, "scatter without x_values should use sequential X indices")
	assert.Equal(t, "scatter", out.ChartType)
	assert.NotEmpty(t, out.URL)
}

// ── imageDecodeValidPNG helper ───────────────────────────────────────────────

// imageDecodeValidPNG is a test-local helper to validate that a PNG bytes slice
// is a decodable image with non-zero bounds.
func imageDecodeValidPNG(t *testing.T, pngBytes []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, err, "PNG bytes should decode without error")
	require.NotNil(t, img)
	return img
}

// TestRenderPieChart_ProducesValidPNG validates the go-chart/v2 pie path.
func TestRenderPieChart_ProducesValidPNG(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "pie",
		Data: pngChartData{
			Labels: []string{"X", "Y", "Z"},
			Series: []pngChartSeries{{Values: []float64{40, 35, 25}}},
		},
	}
	pngBytes, err := renderPieChart(req, 400, 400)
	require.NoError(t, err)
	require.Greater(t, len(pngBytes), 0)
	img := imageDecodeValidPNG(t, pngBytes)
	assert.Greater(t, img.Bounds().Dx(), 0)
}

// TestRenderLineChart_ProducesValidPNG validates the gonum/plot line path.
func TestRenderLineChart_ProducesValidPNG(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "line",
		Data: pngChartData{
			Labels: []string{"W1", "W2", "W3", "W4"},
			Series: []pngChartSeries{{Name: "sales", Values: []float64{45, 62, 78, 91}}},
		},
	}
	pngBytes, err := renderLineChart(req, 600, 400)
	require.NoError(t, err)
	require.Greater(t, len(pngBytes), 0)
	imageDecodeValidPNG(t, pngBytes)
}

// TestRenderScatterChart_ProducesValidPNG validates the gonum/plot scatter path.
func TestRenderScatterChart_ProducesValidPNG(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "scatter",
		Data: pngChartData{
			Series: []pngChartSeries{
				{XValues: []float64{1, 2, 3}, Values: []float64{2, 4, 6}},
			},
		},
	}
	pngBytes, err := renderScatterChart(req, 400, 300)
	require.NoError(t, err)
	require.Greater(t, len(pngBytes), 0)
	imageDecodeValidPNG(t, pngBytes)
}

// ── 4.3 review fix tests ─────────────────────────────────────────────────────

// TestRenderPieChart_AllZeroValues verifies that renderPieChart returns an error
// when all values are zero (no positive slices to display).
func TestRenderPieChart_AllZeroValues(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "pie",
		Data: pngChartData{
			Labels: []string{"A", "B", "C"},
			Series: []pngChartSeries{{Values: []float64{0, 0, 0}}},
		},
	}
	_, err := renderPieChart(req, 400, 400)
	require.Error(t, err, "renderPieChart with all-zero values must return an error")
	assert.Contains(t, err.Error(), "no positive values")
}

// TestRenderBarChart_EmptySeriesValues verifies that renderBarChart returns an
// error when a series has empty Values (not nil, but len == 0).
func TestRenderBarChart_EmptySeriesValues(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "bar",
		Data: pngChartData{
			Labels: []string{"X", "Y"},
			Series: []pngChartSeries{
				{Name: "empty series", Values: []float64{}},
			},
		},
	}
	_, err := renderBarChart(req, 400, 300)
	require.Error(t, err, "renderBarChart with empty Values must return an error")
	assert.Contains(t, err.Error(), "empty Values")
}

// TestRenderBarChart_NaNValues verifies that NaN values in series data produce
// an error rather than panicking or producing invalid PNG.
// Note: JSON does not support NaN/Inf, so these tests bypass the JSON input
// path and call the render functions directly.
func TestRenderBarChart_NaNValues(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "bar",
		Data: pngChartData{
			Labels: []string{"A", "B"},
			Series: []pngChartSeries{
				{Name: "with NaN", Values: []float64{1.0, math.NaN()}},
			},
		},
	}
	_, err := renderBarChart(req, 400, 300)
	require.Error(t, err, "renderBarChart must return error for NaN values")
	assert.Contains(t, err.Error(), "NaN")
}

// TestRenderLineChart_InfValues verifies that Inf values produce an error.
// Note: JSON does not support NaN/Inf, so these tests bypass the JSON input
// path and call the render functions directly.
func TestRenderLineChart_InfValues(t *testing.T) {
	req := createPNGChartInput{
		ChartType: "line",
		Data: pngChartData{
			Labels: []string{"A", "B"},
			Series: []pngChartSeries{
				{Name: "with Inf", Values: []float64{math.Inf(1), 2.0}},
			},
		},
	}
	_, err := renderLineChart(req, 400, 300)
	require.Error(t, err, "renderLineChart must return error for Inf values")
	assert.Contains(t, err.Error(), "Inf")
}
