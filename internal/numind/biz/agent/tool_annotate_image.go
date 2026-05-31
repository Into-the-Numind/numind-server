package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
)

// ─────────────────────────────────────────────────────────────────────────────
// Input / Output structs
// ─────────────────────────────────────────────────────────────────────────────

// annotateImageRegion describes a rectangular region of interest in an image.
type annotateImageRegion struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Label  string `json:"label"`
}

// annotateImageInput is the JSON input for the annotate_image tool.
type annotateImageInput struct {
	AttachmentURL string                `json:"attachment_url"`
	Regions       []annotateImageRegion `json:"regions"`
}

// annotateImageAnnotation holds per-region analysis results.
type annotateImageAnnotation struct {
	Region      annotateImageRegion `json:"region"`
	Description string              `json:"description"`
	OCRText     string              `json:"ocr_text"`
}

// annotateImageOutput is the JSON output returned by the annotate_image tool.
type annotateImageOutput struct {
	Annotations []annotateImageAnnotation `json:"annotations"`
	ModelUsed   string                    `json:"model_used"`
}

// annotateImageMaxRegions is the max regions per single annotate_image invocation
// (each region triggers one VLM call). Distinct from annotateImageMaxPerRun
// (max invocations per agent run, defined in tool_vision_quota.go).
// Worst case: per-run VLM calls = annotateImageMaxPerRun * annotateImageMaxRegions = 50
const annotateImageMaxRegions = 10

// ─────────────────────────────────────────────────────────────────────────────
// Tool implementation
// ─────────────────────────────────────────────────────────────────────────────

// annotateImageTool implements FullTool for the annotate_image built-in.
type annotateImageTool struct {
	BaseTool
}

// NewAnnotateImageTool constructs an annotateImageTool.
func NewAnnotateImageTool() FullTool {
	return &annotateImageTool{}
}

var _ FullTool = (*annotateImageTool)(nil)

func (t *annotateImageTool) Name() string { return "annotate_image" }
func (t *annotateImageTool) Description() string {
	return "Analyze specific regions within an image. " +
		"Provide a list of bounding boxes (x, y, width, height) and labels; " +
		"returns a per-region description and OCR text using a vision specialist model. " +
		"Use when you need fine-grained spatial analysis " +
		"(e.g. comparing two areas of a dashboard, reading a specific table cell). " +
		"Input: { attachment_url: string, regions: [{x, y, width, height, label}] }. " +
		"Returns: { annotations: [{region, description, ocr_text}], model_used }. " +
		"Max 10 regions per call."
}
func (t *annotateImageTool) UserFacingName() string      { return "图像区域标注" }
func (t *annotateImageTool) NarrationVerb() string       { return "标注图像区域" }
func (t *annotateImageTool) IsReadOnly() bool            { return true }
func (t *annotateImageTool) IsSearchOrReadCommand() bool { return true }
func (t *annotateImageTool) AlwaysLoad() bool            { return true }

// Execute parses the input JSON and calls the vision model once per region
// (serial, to avoid blowing the provider QPS limit). Returns structured
// annotations. Never returns a non-nil error (graceful degradation — spec §5).
// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *annotateImageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"attachment_url": {"type": "string", "format": "uri", "description": "URL of the image to annotate."},
			"regions": {
				"type": "array",
				"description": "Rectangular regions of interest to analyze/label.",
				"items": {
					"type": "object",
					"properties": {
						"x":      {"type": "integer", "description": "Left pixel coordinate of the region."},
						"y":      {"type": "integer", "description": "Top pixel coordinate of the region."},
						"width":  {"type": "integer", "description": "Region width in pixels."},
						"height": {"type": "integer", "description": "Region height in pixels."},
						"label":  {"type": "string",  "description": "Optional caller-provided label for the region."}
					},
					"required": ["x", "y", "width", "height"]
				}
			}
		},
		"required": ["attachment_url", "regions"]
	}`)
}

func (t *annotateImageTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in annotateImageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return gracefulAnnotateError("invalid input JSON: " + err.Error()), nil
	}
	if in.AttachmentURL == "" {
		return gracefulAnnotateError("attachment_url is required"), nil
	}

	// ── Langfuse span ───────────────────────────────────────────────────────
	var spanID, traceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		traceID = tc.TraceID
		langfuse.CreateSpan(traceID, spanID, "tool.annotate_image",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{
				"url":          in.AttachmentURL,
				"region_count": len(in.Regions),
			}),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	// ── Quota check ─────────────────────────────────────────────────────────
	runID := RunIDFromContext(ctx)
	if err := checkAndIncVisionQuota(runID, "annotate_image"); err != nil {
		return gracefulAnnotateError(
			"Vision tool quota exceeded for this conversation " +
				"(max 10 analyze_image / 5 annotate_image per run).",
		), nil
	}

	// ── Early return for zero regions ────────────────────────────────────────
	if len(in.Regions) == 0 {
		result, _ := json.Marshal(annotateImageOutput{
			Annotations: []annotateImageAnnotation{},
			ModelUsed:   "(no regions)",
		})
		return ToolResult(result), nil
	}

	// ── Cap regions to maximum ───────────────────────────────────────────────
	regions := in.Regions
	if len(regions) > annotateImageMaxRegions {
		regions = regions[:annotateImageMaxRegions]
		log.Warnw("annotate_image: truncated regions to max", "max", annotateImageMaxRegions,
			"requested", len(in.Regions))
	}

	// ── Serial per-region vision calls ───────────────────────────────────────
	// Serial (not parallel) to avoid blowing QPS on the vision provider.
	// 30s timeout per region; each call gets its own context so a slow region
	// does not cancel the remaining ones.
	annotations := make([]annotateImageAnnotation, 0, len(regions))
	var lastModel string

	for _, region := range regions {
		ann, modelKey := t.analyzeRegion(ctx, in.AttachmentURL, region)
		annotations = append(annotations, ann)
		if modelKey != "" {
			lastModel = modelKey
		}
	}

	if lastModel == "" {
		lastModel = profile.AttachmentVisionDescribe
	}

	result, _ := json.Marshal(annotateImageOutput{
		Annotations: annotations,
		ModelUsed:   lastModel,
	})
	return ToolResult(result), nil
}

// analyzeRegion calls the vision model for a single bounding box region.
// On failure it returns a graceful annotation (error description in Description field)
// instead of propagating the error (spec §5).
func (t *annotateImageTool) analyzeRegion(
	ctx context.Context,
	imageURL string,
	region annotateImageRegion,
) (annotateImageAnnotation, string) {
	regionDesc := fmt.Sprintf(
		"region '%s' at coordinates (x=%d, y=%d, width=%d, height=%d)",
		region.Label, region.X, region.Y, region.Width, region.Height,
	)

	prompt := fmt.Sprintf(
		"Focus specifically on the %s in this image. "+
			"Describe what you see in that area and extract any visible text. "+
			"Be concise and precise.",
		regionDesc,
	)

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := chatFn(callCtx, profile.AttachmentVisionDescribe, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Parts: []aiservice.MessagePart{
						{
							Type: aiservice.MessagePartTypeText,
							Text: prompt,
						},
						{
							Type:     aiservice.MessagePartTypeImageURL,
							ImageURL: &aiservice.ImageURL{URL: imageURL},
						},
					},
				},
			},
		},
		Temperature: 0.2,
		MaxTokens:   1024,
	})
	if err != nil {
		log.Warnw("annotate_image: region vision call failed",
			"label", region.Label, "error", err)
		return annotateImageAnnotation{
			Region:      region,
			Description: fmt.Sprintf("Vision analysis failed for %s: %s", regionDesc, err.Error()),
			OCRText:     "",
		}, ""
	}

	content := strings.TrimSpace(resp.Content)
	return annotateImageAnnotation{
		Region:      region,
		Description: content,
		OCRText:     "", // vision model returns holistic description; OCR is a separate tool
	}, resp.Model
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// gracefulAnnotateError builds a graceful (non-error) ToolResult containing
// an error description. Per spec §5: vision tools must never return a Go error.
func gracefulAnnotateError(reason string) ToolResult {
	out, _ := json.Marshal(annotateImageOutput{
		Annotations: []annotateImageAnnotation{
			{
				Description: "Annotation failed: " + reason,
			},
		},
		ModelUsed: "(failed)",
	})
	return ToolResult(out)
}
