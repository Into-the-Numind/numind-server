package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"math"

	chart "github.com/wcharczuk/go-chart/v2"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
)

// createPNGChartTool implements FullTool for the "create_png_chart" platform tool.
//
// Generates a static PNG chart (bar / line / pie / scatter) from structured data
// and uploads it to COS via uploadGeneratedFile (task 4.2 helper, same package).
// Returns a 24-hour presigned URL on success.
//
// Library split (per decision T11 / T16):
//   - bar / line / scatter: gonum.org/v1/plot (native PNG, pure Go, no CGO)
//   - pie:                  github.com/wcharczuk/go-chart/v2 (mature pie support)
//
// Layer 1 constraint: no sandbox, no headless browser, no external processes.
// Labels must be in English/ASCII; Chinese characters may not render correctly
// with the default gonum/plot embedded Nimbus font (V2 will embed WenQuanYi).
type createPNGChartTool struct {
	BaseTool
}

var _ FullTool = (*createPNGChartTool)(nil)

// ── Input / output structs ──────────────────────────────────────────────────

// createPNGChartInput is the JSON input for create_png_chart.
type createPNGChartInput struct {
	// ChartType selects the renderer: "bar" | "line" | "pie" | "scatter".
	ChartType string `json:"chart_type"`

	// Title is an optional chart title shown at the top.
	Title string `json:"title,omitempty"`

	// Data holds the structured data to visualise.
	Data pngChartData `json:"data"`

	// Options holds optional rendering configuration.
	Options *pngChartOptions `json:"options,omitempty"`

	// Filename is an optional output file name (will be sanitized + ".png" appended).
	Filename string `json:"filename,omitempty"`
}

// pngChartData carries labels and series for the chart.
type pngChartData struct {
	// Labels are category labels for bar / line / pie.
	// For scatter, Labels is ignored (X coordinates come from XValues).
	Labels []string `json:"labels,omitempty"`

	// Series holds one or more data series.
	Series []pngChartSeries `json:"series"`
}

// pngChartSeries describes one series (line, bar group, pie slice set, scatter cloud).
type pngChartSeries struct {
	// Name is the series label shown in the legend.
	Name string `json:"name"`

	// Values is the numeric data:
	//   bar/line/pie: value per label position.
	//   scatter:      Y coordinates — parallel to XValues.
	Values []float64 `json:"values"`

	// XValues are X coordinates for scatter charts.
	// If absent in scatter mode, indices 0, 1, 2, … are used as X.
	XValues []float64 `json:"x_values,omitempty"`
}

// pngChartOptions holds optional rendering parameters.
type pngChartOptions struct {
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	XLabel     string `json:"x_label,omitempty"`
	YLabel     string `json:"y_label,omitempty"`
	ShowLegend *bool  `json:"show_legend,omitempty"`
}

// chartCreateOutput is the JSON ToolResult returned to the LLM.
type chartCreateOutput struct {
	URL       string `json:"url"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	ChartType string `json:"chart_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// ── FullTool required methods ───────────────────────────────────────────────

func (t *createPNGChartTool) Name() string { return "create_png_chart" }

func (t *createPNGChartTool) Description() string {
	return "Generate a static PNG chart (bar, line, pie, or scatter) from structured data and upload it. " +
		"Returns a download URL. Use for data visualization in reports, analysis summaries, or presentation slides. " +
		"Labels must be in English or ASCII for correct rendering (Chinese labels may not render in V1; use V2 when available). " +
		"For interactive HTML charts, use the create_interactive_chart skill instead. " +
		"For multi-axis or complex static charts, prefer the read_skill → run_python path."
}

func (t *createPNGChartTool) UserFacingName() string { return "生成图表（PNG）" }
func (t *createPNGChartTool) NarrationVerb() string  { return "生成" }

// IsReadOnly returns false: the tool uploads a file to COS.
func (t *createPNGChartTool) IsReadOnly() bool { return false }

// IsEnabled returns true regardless of config; no sandbox required.
func (t *createPNGChartTool) IsEnabled(_ ToolConfig) bool { return true }

// MaxResultSizeChars returns 0 (no limit): the result is a short JSON URL payload.
func (t *createPNGChartTool) MaxResultSizeChars() int { return 0 }

// InputSchema returns the JSON Schema for LLM parameter validation.
func (t *createPNGChartTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"chart_type": {
				"type": "string",
				"enum": ["bar", "line", "pie", "scatter"],
				"description": "Type of chart to generate"
			},
			"title": {
				"type": "string",
				"description": "Optional chart title"
			},
			"data": {
				"type": "object",
				"properties": {
					"labels": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Category labels (bar/line/pie). Ignored for scatter."
					},
					"series": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"name":     {"type": "string", "description": "Series name (legend)"},
								"values":   {"type": "array", "items": {"type": "number"}, "description": "Numeric values"},
								"x_values": {"type": "array", "items": {"type": "number"}, "description": "X coordinates for scatter charts"}
							},
							"required": ["values"]
						}
					}
				},
				"required": ["series"]
			},
			"options": {
				"type": "object",
				"properties": {
					"width":       {"type": "integer", "description": "Image width in pixels (default 800, max 4096)"},
					"height":      {"type": "integer", "description": "Image height in pixels (default 600, max 4096)"},
					"x_label":     {"type": "string", "description": "X-axis label"},
					"y_label":     {"type": "string", "description": "Y-axis label"},
					"show_legend": {"type": "boolean", "description": "Whether to show the legend (default true)"}
				}
			},
			"filename": {
				"type": "string",
				"description": "Optional output filename (will be sanitized and '.png' appended)"
			}
		},
		"required": ["chart_type", "data"]
	}`)
}

// ── Execute ─────────────────────────────────────────────────────────────────

// Execute parses input, validates, renders the chart, uploads, and returns the URL.
func (t *createPNGChartTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var req createPNGChartInput
	if err := json.Unmarshal(input, &req); err != nil {
		return chartFriendlyError("create_png_chart: invalid JSON input: " + err.Error()), nil
	}

	// Validate and normalise options.
	w, h := resolveChartDimensions(req.Options)

	// Validate series.
	if len(req.Data.Series) == 0 {
		return chartFriendlyError("create_png_chart: data.series must not be empty"), nil
	}

	// Build PNG bytes based on chart_type.
	// Render errors are returned as friendly LLM-readable payloads (nil Go error)
	// so the LLM can react to bad input. Only genuine system failures (COS upload)
	// propagate as Go errors to the hook layer.
	var pngBytes []byte
	var renderErr error
	switch req.ChartType {
	case "bar":
		pngBytes, renderErr = renderBarChart(req, w, h)
	case "line":
		pngBytes, renderErr = renderLineChart(req, w, h)
	case "scatter":
		pngBytes, renderErr = renderScatterChart(req, w, h)
	case "pie":
		pngBytes, renderErr = renderPieChart(req, w, h)
	default:
		return chartFriendlyError(fmt.Sprintf(
			"create_png_chart: unsupported chart_type: %q, allowed: bar/line/pie/scatter",
			req.ChartType,
		)), nil
	}
	if renderErr != nil {
		// Return as LLM-readable payload; nil Go error means the hook layer does
		// not mark the run as failed (the LLM will see the error and can retry).
		return chartFriendlyError("create_png_chart: render failed: " + renderErr.Error()), nil
	}

	// Guard against unexpectedly large PNG outputs (e.g. huge canvas with many data points).
	const maxPNGSizeBytes = 10 * 1024 * 1024 // 10 MB
	if int64(len(pngBytes)) > maxPNGSizeBytes {
		return chartFriendlyError("create_png_chart: 生成的 PNG 文件超过 10MB 限制，请减少数据量或降低图表分辨率"), nil
	}

	// Determine output filename.
	outFilename := req.Filename
	if outFilename == "" {
		outFilename = req.ChartType + "_chart.png"
	} else {
		// Ensure .png extension.
		if len(outFilename) < 4 || outFilename[len(outFilename)-4:] != ".png" {
			outFilename = sanitizeOutputFilename(outFilename) + ".png"
		}
	}

	// Upload via shared COS helper (task 4.2, same package).
	result, err := uploadGeneratedFile(ctx, pngBytes, "image/png", outFilename, "png")
	if err != nil {
		return chartFriendlyError("create_png_chart: upload failed: " + err.Error()), err
	}

	// Augment the result with chart-specific fields.
	var base fileCreateOutput
	if err := json.Unmarshal(result, &base); err != nil {
		return result, nil // return as-is if augment fails
	}
	out := chartCreateOutput{
		URL:       base.URL,
		Filename:  base.Filename,
		SizeBytes: base.SizeBytes,
		ChartType: req.ChartType,
		Width:     w,
		Height:    h,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return result, nil
	}
	return b, nil
}

// ── Dimension helpers ────────────────────────────────────────────────────────

const (
	chartDefaultWidth  = 800
	chartDefaultHeight = 600
	chartMinDim        = 100
	// chartMaxDim caps image dimensions to prevent memory exhaustion.
	// Override spec §5 max 4000 → 4096 (power of 2, equivalent practical limit).
	chartMaxDim = 4096
)

// resolveChartDimensions extracts width/height from options with clamping.
func resolveChartDimensions(opts *pngChartOptions) (w, h int) {
	w, h = chartDefaultWidth, chartDefaultHeight
	if opts != nil {
		if opts.Width > 0 {
			w = opts.Width
		}
		if opts.Height > 0 {
			h = opts.Height
		}
	}
	w = clampDim(w)
	h = clampDim(h)
	return w, h
}

func clampDim(v int) int {
	if v < chartMinDim {
		return chartMinDim
	}
	if v > chartMaxDim {
		return chartMaxDim
	}
	return v
}

// ── Colour palette ───────────────────────────────────────────────────────────

// chartPalette is a fixed 8-colour palette used for multi-series charts.
var chartPalette = []color.RGBA{
	{R: 70, G: 130, B: 180, A: 255},  // Steel Blue
	{R: 255, G: 140, B: 0, A: 255},   // Dark Orange
	{R: 50, G: 168, B: 82, A: 255},   // Medium Green
	{R: 220, G: 50, B: 47, A: 255},   // Crimson Red
	{R: 148, G: 103, B: 189, A: 255}, // Medium Purple
	{R: 140, G: 86, B: 75, A: 255},   // Brown
	{R: 227, G: 119, B: 194, A: 255}, // Orchid Pink
	{R: 127, G: 127, B: 127, A: 255}, // Gray
}

func paletteColor(i int) color.RGBA {
	return chartPalette[i%len(chartPalette)]
}

// ── gonum/plot helpers ───────────────────────────────────────────────────────

// vgPixelInch is the vg.Length of 1 pixel at 96 DPI (gonum/plot's DefaultDPI).
// Conversion: 1 pixel = (1/96) inch.
const vgDefaultDPI = 96

// plotToPNG renders a gonum/plot.Plot to a PNG byte slice at the given pixel dimensions.
// Uses p.WriterTo with format "png" — pure Go, no CGO, no external processes.
// Pixel dimensions are converted to vg.Length via 96 DPI (vgimg.DefaultDPI).
func plotToPNG(p *plot.Plot, widthPx, heightPx int) ([]byte, error) {
	// Convert pixels → vg.Length at 96 DPI.
	w := vg.Length(float64(widthPx)/vgDefaultDPI) * vg.Inch
	h := vg.Length(float64(heightPx)/vgDefaultDPI) * vg.Inch

	wt, err := p.WriterTo(w, h, "png")
	if err != nil {
		return nil, fmt.Errorf("plotToPNG: create canvas: %w", err)
	}

	var buf bytes.Buffer
	if _, err := wt.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("plotToPNG: write png: %w", err)
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("plotToPNG: produced 0-byte PNG")
	}
	return buf.Bytes(), nil
}

// newPlot creates a plot.Plot with title + axis labels applied.
func newPlot(title, xLabel, yLabel string) *plot.Plot {
	p := plot.New()
	if title != "" {
		p.Title.Text = title
	}
	if xLabel != "" {
		p.X.Label.Text = xLabel
	}
	if yLabel != "" {
		p.Y.Label.Text = yLabel
	}
	return p
}

// showLegend returns true unless the caller explicitly set show_legend=false.
func showLegend(opts *pngChartOptions) bool {
	if opts == nil || opts.ShowLegend == nil {
		return true
	}
	return *opts.ShowLegend
}

// ── Bar chart ────────────────────────────────────────────────────────────────

func renderBarChart(req createPNGChartInput, w, h int) ([]byte, error) {
	p := newPlot(req.Title, xLabelOf(req.Options), yLabelOf(req.Options))

	series := req.Data.Series
	labels := req.Data.Labels

	// Calculate per-bar width so multi-series bars fit side by side.
	// Total nominal slot width is 1.0 in NominalX units.
	n := len(series)
	barWidthFrac := 0.8 / float64(n)         // leave 20% padding between groups
	barWidth := vg.Points(barWidthFrac * 40) // scale to vg points

	for i, s := range series {
		if len(s.Values) == 0 {
			return nil, fmt.Errorf("renderBarChart: series[%d] %q has empty Values", i, s.Name)
		}
		if err := validateSeriesValues(s.Values, i, s.Name); err != nil {
			return nil, fmt.Errorf("renderBarChart: %w", err)
		}
		vals := alignValues(s.Values, labels)
		bars, err := plotter.NewBarChart(plotter.Values(vals), barWidth)
		if err != nil {
			return nil, fmt.Errorf("renderBarChart series[%d] %q: %w", i, s.Name, err)
		}
		col := paletteColor(i)
		bars.Color = col
		// Offset each series so bars in the same group sit next to each other.
		bars.Offset = vg.Length(float64(i) * float64(barWidth))
		p.Add(bars)
		if showLegend(req.Options) && s.Name != "" {
			p.Legend.Add(s.Name, bars)
		}
	}

	if len(labels) > 0 {
		p.NominalX(labels...)
	}
	if showLegend(req.Options) {
		p.Legend.Top = true
	}

	return plotToPNG(p, w, h)
}

// ── Line chart ───────────────────────────────────────────────────────────────

func renderLineChart(req createPNGChartInput, w, h int) ([]byte, error) {
	p := newPlot(req.Title, xLabelOf(req.Options), yLabelOf(req.Options))

	series := req.Data.Series
	labels := req.Data.Labels

	for i, s := range series {
		if len(s.Values) == 0 {
			return nil, fmt.Errorf("renderLineChart: series[%d] %q has empty Values", i, s.Name)
		}
		if err := validateSeriesValues(s.Values, i, s.Name); err != nil {
			return nil, fmt.Errorf("renderLineChart: %w", err)
		}
		vals := alignValues(s.Values, labels)
		pts := make(plotter.XYs, len(vals))
		for j, v := range vals {
			pts[j] = plotter.XY{X: float64(j), Y: v}
		}
		line, err := plotter.NewLine(pts)
		if err != nil {
			return nil, fmt.Errorf("renderLineChart series[%d] %q: %w", i, s.Name, err)
		}
		col := paletteColor(i)
		line.Color = col
		line.Width = vg.Points(2)
		p.Add(line)
		if showLegend(req.Options) && s.Name != "" {
			p.Legend.Add(s.Name, line)
		}
	}

	// Use label strings as X-axis tick marks when provided.
	if len(labels) > 0 {
		ticks := make([]plot.Tick, len(labels))
		for j, lbl := range labels {
			ticks[j] = plot.Tick{Value: float64(j), Label: lbl}
		}
		p.X.Tick.Marker = plot.ConstantTicks(ticks)
	}
	if showLegend(req.Options) {
		p.Legend.Top = true
	}

	return plotToPNG(p, w, h)
}

// ── Scatter chart ────────────────────────────────────────────────────────────

func renderScatterChart(req createPNGChartInput, w, h int) ([]byte, error) {
	p := newPlot(req.Title, xLabelOf(req.Options), yLabelOf(req.Options))

	series := req.Data.Series

	for i, s := range series {
		if len(s.Values) == 0 {
			return nil, fmt.Errorf("renderScatterChart: series[%d] %q has empty Values", i, s.Name)
		}
		if err := validateSeriesValues(s.Values, i, s.Name); err != nil {
			return nil, fmt.Errorf("renderScatterChart: %w", err)
		}
		if err := validateSeriesValues(s.XValues, i, s.Name); err != nil {
			return nil, fmt.Errorf("renderScatterChart XValues: %w", err)
		}
		if len(s.XValues) > 0 && len(s.XValues) != len(s.Values) {
			return nil, fmt.Errorf(
				"renderScatterChart series[%d] %q: x_values length (%d) != values length (%d)",
				i, s.Name, len(s.XValues), len(s.Values),
			)
		}
		pts := make(plotter.XYs, len(s.Values))
		for j, y := range s.Values {
			x := float64(j) // fallback: sequential index
			if len(s.XValues) > 0 {
				x = s.XValues[j]
			}
			pts[j] = plotter.XY{X: x, Y: y}
		}
		sc, err := plotter.NewScatter(pts)
		if err != nil {
			return nil, fmt.Errorf("renderScatterChart series[%d] %q: %w", i, s.Name, err)
		}
		col := paletteColor(i)
		sc.Color = col
		sc.GlyphStyle.Color = col
		sc.GlyphStyle.Radius = vg.Points(3)
		p.Add(sc)
		if showLegend(req.Options) && s.Name != "" {
			p.Legend.Add(s.Name, sc)
		}
	}
	if showLegend(req.Options) {
		p.Legend.Top = true
	}

	return plotToPNG(p, w, h)
}

// ── Pie chart (go-chart/v2) ──────────────────────────────────────────────────

func renderPieChart(req createPNGChartInput, w, h int) ([]byte, error) {
	series := req.Data.Series
	labels := req.Data.Labels

	// Pie: collect all values from all series; use labels for slice names.
	// If multiple series, concatenate (uncommon, but gracefully handled).
	var values []chart.Value

	if len(series) == 1 {
		// Standard case: one series, labels are slice names.
		s := series[0]
		if err := validateSeriesValues(s.Values, 0, s.Name); err != nil {
			return nil, fmt.Errorf("renderPieChart: %w", err)
		}
		for j, v := range s.Values {
			lbl := fmt.Sprintf("Slice %d", j+1)
			if j < len(labels) {
				lbl = labels[j]
			}
			if v > 0 { // go-chart/v2 drops zero values anyway, be explicit
				values = append(values, chart.Value{Label: lbl, Value: v})
			}
		}
	} else {
		// Multi-series: use series names as labels, sum or first value per series.
		for i, s := range series {
			if err := validateSeriesValues(s.Values, i, s.Name); err != nil {
				return nil, fmt.Errorf("renderPieChart: %w", err)
			}
			var sum float64
			for _, v := range s.Values {
				sum += v
			}
			lbl := s.Name
			if lbl == "" {
				lbl = fmt.Sprintf("Series %d", i+1)
			}
			if sum > 0 {
				values = append(values, chart.Value{Label: lbl, Value: sum})
			}
		}
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("renderPieChart: no positive values to display")
	}

	pc := chart.PieChart{
		Title:  req.Title,
		Width:  w,
		Height: h,
		Values: values,
	}

	var buf bytes.Buffer
	if err := pc.Render(chart.PNG, &buf); err != nil {
		return nil, fmt.Errorf("renderPieChart: %w", err)
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("renderPieChart: produced 0-byte PNG")
	}
	return buf.Bytes(), nil
}

// ── Utility ──────────────────────────────────────────────────────────────────

// validateSeriesValues checks each value in a series for NaN or Inf, returning
// a descriptive error if any are found. This must be called before passing values
// to gonum/plot or go-chart/v2 which can panic or produce invalid PNGs.
func validateSeriesValues(values []float64, seriesIdx int, seriesName string) error {
	for i, v := range values {
		if math.IsNaN(v) {
			return fmt.Errorf("series[%d] %q value[%d] is NaN — all values must be finite numbers", seriesIdx, seriesName, i)
		}
		if math.IsInf(v, 0) {
			return fmt.Errorf("series[%d] %q value[%d] is Inf — all values must be finite numbers", seriesIdx, seriesName, i)
		}
	}
	return nil
}

// alignValues truncates or pads s.Values to match the length of labels.
// Per spec: "warn + truncate to shorter" for bar/line/pie (no error).
// For an empty labels list the original values are returned unchanged.
func alignValues(values []float64, labels []string) []float64 {
	if len(labels) == 0 {
		return values
	}
	n := len(labels)
	if len(values) > n {
		return values[:n]
	}
	// Pad with zeros if values shorter than labels (rare but graceful).
	padded := make([]float64, n)
	copy(padded, values)
	return padded
}

func xLabelOf(opts *pngChartOptions) string {
	if opts == nil {
		return ""
	}
	return opts.XLabel
}

func yLabelOf(opts *pngChartOptions) string {
	if opts == nil {
		return ""
	}
	return opts.YLabel
}

// chartFriendlyError returns a ToolResult JSON {"error": "<msg>"} readable by the LLM.
func chartFriendlyError(msg string) ToolResult {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}
