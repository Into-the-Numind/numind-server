package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
)

// ─────────────────────────────────────────────────────────────────────────────
// Input / Output structs
// ─────────────────────────────────────────────────────────────────────────────

// analyzeImageInput is the JSON input for the analyze_image tool.
type analyzeImageInput struct {
	AttachmentURL string `json:"attachment_url"`
	Question      string `json:"question"`
}

// analyzeImageOutput is the JSON output returned by the analyze_image tool.
type analyzeImageOutput struct {
	Description string  `json:"description"`
	OCRText     string  `json:"ocr_text"`
	Confidence  float32 `json:"confidence"`
	ModelUsed   string  `json:"model_used"`
	FromCache   bool    `json:"from_cache"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Cache bypass keyword list (spec §4)
// ─────────────────────────────────────────────────────────────────────────────

// bypassCacheKeywords lists words in the question that signal a fresh vision
// call is required instead of using the pre-generated attachment fallback.
// If the question contains any of these substrings (case-insensitive) the
// cached vision_description / ocr_text is bypassed.
//
// Extend this list based on dev usage data after the V1.5 rollout (R4).
var bypassCacheKeywords = []string{
	"count", "数一下", "几个", "多少个",
	"compare", "对比", "比较",
	"extract", "提取", "抽取",
	"specific", "具体",
	"region", "区域", "位置",
	"color", "颜色",
	"size", "大小", "尺寸",
}

// shouldBypassCache returns true when the question indicates that cached
// attachment data is insufficient and a fresh vision call is required.
func shouldBypassCache(question string) bool {
	q := strings.ToLower(question)
	for _, kw := range bypassCacheKeywords {
		if strings.Contains(q, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Tool implementation
// ─────────────────────────────────────────────────────────────────────────────

// analyzeImageTool implements FullTool for the analyze_image built-in.
type analyzeImageTool struct {
	BaseTool
	attStore store.IAgentAttachmentStore // nil-safe; used for cache reads
}

// NewAnalyzeImageTool constructs an analyzeImageTool with the given attachment store.
// attStore may be nil; in that case cache lookup is skipped and every call
// hits the vision model directly (acceptable in test or nil-ds contexts).
func NewAnalyzeImageTool(attStore store.IAgentAttachmentStore) FullTool {
	return &analyzeImageTool{attStore: attStore}
}

var _ FullTool = (*analyzeImageTool)(nil)

func (t *analyzeImageTool) Name() string { return "analyze_image" }
func (t *analyzeImageTool) Description() string {
	return "Analyze an image attachment in detail using a vision specialist model. " +
		"Use this tool when you need to extract text, identify visual elements, count items, " +
		"or describe layouts in an image — even if you can see the image directly, this tool " +
		"gives more precise structured results. " +
		"Input: { attachment_url: string, question: string }. " +
		"Returns: { description, ocr_text, confidence, model_used, from_cache }. " +
		"Costs ~1 vision call per invocation; do not call more than necessary."
}
func (t *analyzeImageTool) UserFacingName() string      { return "图像分析" }
func (t *analyzeImageTool) NarrationVerb() string       { return "分析图像" }
func (t *analyzeImageTool) IsReadOnly() bool            { return true }
func (t *analyzeImageTool) IsSearchOrReadCommand() bool { return true }
func (t *analyzeImageTool) AlwaysLoad() bool            { return true }

// Execute parses the input JSON, optionally returns cached data, otherwise
// calls the vision model and returns structured output. It never returns a
// non-nil error (graceful degradation — see spec §5).
func (t *analyzeImageTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in analyzeImageInput
	if err := json.Unmarshal(input, &in); err != nil {
		return gracefulAnalyzeError("invalid input JSON: " + err.Error()), nil
	}
	if in.AttachmentURL == "" {
		return gracefulAnalyzeError("attachment_url is required"), nil
	}

	// ── Langfuse span ───────────────────────────────────────────────────────
	var spanID, traceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		traceID = tc.TraceID
		langfuse.CreateSpan(traceID, spanID, "tool.analyze_image",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{
				"url":      in.AttachmentURL,
				"question": in.Question,
			}),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	// ── Quota check ─────────────────────────────────────────────────────────
	runID := RunIDFromContext(ctx)
	if err := checkAndIncVisionQuota(runID, "analyze_image"); err != nil {
		return gracefulAnalyzeError(
			"Vision tool quota exceeded for this conversation " +
				"(max 10 analyze_image / 5 annotate_image per run). " +
				"Use cached descriptions from attachment metadata instead.",
		), nil
	}

	// ── Cache lookup ─────────────────────────────────────────────────────────
	// If the attachment has a pre-generated vision_description + ocr_text
	// (task 1.2 async fallback) and the question doesn't require a fresh call,
	// return the cached data immediately without hitting the vision model.
	if !shouldBypassCache(in.Question) && t.attStore != nil {
		if cached, ok := t.tryLoadCache(ctx, in.AttachmentURL); ok {
			log.Infow("analyze_image: cache hit", "url", in.AttachmentURL)
			result, _ := json.Marshal(analyzeImageOutput{
				Description: cached.description,
				OCRText:     cached.ocrText,
				Confidence:  0.85, // default when provider does not return confidence
				ModelUsed:   "cached",
				FromCache:   true,
			})
			return ToolResult(result), nil
		}
	}

	// ── Vision model call ───────────────────────────────────────────────────
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	question := in.Question
	if question == "" {
		question = "Please describe this image in detail, including any text you can see."
	}

	resp, err := chatFn(callCtx, profile.AttachmentVisionDescribe, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Parts: []aiservice.MessagePart{
						{
							Type: aiservice.MessagePartTypeText,
							Text: question,
						},
						{
							Type:     aiservice.MessagePartTypeImageURL,
							ImageURL: &aiservice.ImageURL{URL: in.AttachmentURL},
						},
					},
				},
			},
		},
		Temperature: 0.2,
		MaxTokens:   1024,
	})
	if err != nil {
		log.Warnw("analyze_image: vision call failed", "url", in.AttachmentURL, "error", err)
		return gracefulAnalyzeError(fmt.Sprintf("vision model call failed: %s", err.Error())), nil
	}

	modelUsed := profile.AttachmentVisionDescribe
	if resp.Model != "" {
		modelUsed = resp.Model
	}

	result, _ := json.Marshal(analyzeImageOutput{
		Description: strings.TrimSpace(resp.Content),
		OCRText:     "", // vision model provides descriptive answer; OCR is a separate concern
		Confidence:  0.85,
		ModelUsed:   modelUsed,
		FromCache:   false,
	})
	return ToolResult(result), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

// cachedAttachmentData holds pre-generated vision/OCR data from the attachment table.
type cachedAttachmentData struct {
	description string
	ocrText     string
}

// tryLoadCache attempts to return pre-computed VLM data from agent_attachment.vision_description.
//
// TODO(task-1.2-followup): Currently this is a stub returning (nil, false) because
// IAgentAttachmentStore has no GetByURL method. Until that's added, every analyze_image
// call hits the vision LLM regardless of cache state, and the bypassCacheKeywords
// logic is effectively dead code (kept for forward compatibility).
func (t *analyzeImageTool) tryLoadCache(ctx context.Context, url string) (cachedAttachmentData, bool) {
	if t.attStore == nil {
		return cachedAttachmentData{}, false
	}
	return cachedAttachmentData{}, false
}

// gracefulAnalyzeError builds a graceful (non-error) ToolResult containing
// an error description. Per spec §5: vision tools must never return a Go error.
func gracefulAnalyzeError(reason string) ToolResult {
	out, _ := json.Marshal(analyzeImageOutput{
		Description: "Vision analysis failed: " + reason,
		OCRText:     "",
		Confidence:  0,
		ModelUsed:   "(failed)",
		FromCache:   false,
	})
	return ToolResult(out)
}
