package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// parseAnnotateOutput unmarshals the ToolResult JSON into annotateImageOutput.
func parseAnnotateOutput(t *testing.T, result ToolResult) annotateImageOutput {
	t.Helper()
	var out annotateImageOutput
	require.NoError(t, json.Unmarshal(result, &out))
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// C7: 3 regions → serial calls → 3 annotations returned
// ─────────────────────────────────────────────────────────────────────────────

// C7: annotate_image with 3 regions → aiservice called 3 times serially.
func TestAnnotateImage_C7_ThreeRegions_ThreeAnnotations(t *testing.T) {
	const runID = uint64(3007)
	resetVisionQuota(runID, "annotate_image")

	callCount := 0
	responses := []string{"button A is green", "status LED is on", "error message: OK"}
	installMockChat(t, func(_ context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		assert.Equal(t, "attachment.vision_describe", taskID)
		resp := responses[callCount]
		callCount++
		return fixedChatResponse(resp, "qwen3-vl-flash"), nil
	})

	tool := NewAnnotateImageTool()
	regions := []annotateImageRegion{
		{X: 0, Y: 0, Width: 100, Height: 50, Label: "button A"},
		{X: 100, Y: 0, Width: 50, Height: 50, Label: "status LED"},
		{X: 0, Y: 100, Width: 200, Height: 30, Label: "error message"},
	}
	inp, _ := json.Marshal(annotateImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/panel.png",
		Regions:       regions,
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))

	require.NoError(t, err)
	assert.Equal(t, 3, callCount, "should call aiservice exactly 3 times")

	out := parseAnnotateOutput(t, result)
	require.Len(t, out.Annotations, 3)
	assert.Equal(t, "button A is green", out.Annotations[0].Description)
	assert.Equal(t, "status LED is on", out.Annotations[1].Description)
	assert.Equal(t, "error message: OK", out.Annotations[2].Description)

	// Verify region labels are echoed back.
	assert.Equal(t, "button A", out.Annotations[0].Region.Label)
	assert.Equal(t, "status LED", out.Annotations[1].Region.Label)
	assert.Equal(t, "error message", out.Annotations[2].Region.Label)
}

// ─────────────────────────────────────────────────────────────────────────────
// C8: zero regions → no aiservice call, empty annotations
// ─────────────────────────────────────────────────────────────────────────────

// C8: annotate_image with 0 regions → no call to aiservice, empty annotations.
func TestAnnotateImage_C8_ZeroRegions_NoAIServiceCall(t *testing.T) {
	const runID = uint64(3008)
	resetVisionQuota(runID, "annotate_image")

	called := false
	installMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		called = true
		return fixedChatResponse("should not be called", "model"), nil
	})

	tool := NewAnnotateImageTool()
	inp, _ := json.Marshal(annotateImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/panel.png",
		Regions:       []annotateImageRegion{},
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))

	require.NoError(t, err)
	assert.False(t, called, "aiservice should not be called for 0 regions")

	out := parseAnnotateOutput(t, result)
	assert.Empty(t, out.Annotations)
}

// ─────────────────────────────────────────────────────────────────────────────
// C9: annotate_image quota exceeded (> 5 per run)
// ─────────────────────────────────────────────────────────────────────────────

// C9: after 5 annotate_image calls, the 6th returns quota exceeded message.
func TestAnnotateImage_C9_QuotaExceeded(t *testing.T) {
	const runID = uint64(3009)
	resetVisionQuota(runID, "annotate_image")

	installMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return fixedChatResponse("ok", "model"), nil
	})

	tool := NewAnnotateImageTool()
	inp, _ := json.Marshal(annotateImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/quota.png",
		Regions:       []annotateImageRegion{{X: 0, Y: 0, Width: 10, Height: 10, Label: "test"}},
	})
	ctx := WithRunID(context.Background(), runID)

	// Exhaust the quota.
	for i := 0; i < annotateImageMaxPerRun; i++ {
		result, err := tool.Execute(ctx, ToolInput(inp))
		require.NoError(t, err)
		out := parseAnnotateOutput(t, result)
		assert.NotContains(t, out.Annotations[0].Description, "quota exceeded",
			"call %d should succeed", i+1)
	}

	// The (limit+1)-th call should hit the quota.
	result, err := tool.Execute(ctx, ToolInput(inp))
	require.NoError(t, err)
	out := parseAnnotateOutput(t, result)
	// Quota exceeded is returned as an error annotation.
	assert.Contains(t, out.Annotations[0].Description, "quota exceeded")
}

// ─────────────────────────────────────────────────────────────────────────────
// C10: metadata — analyze_image and annotate_image do not require main model vision
// ─────────────────────────────────────────────────────────────────────────────

// C10: factory_platform registers analyze_image and annotate_image; ToolMetadata
// for these tools does not include RequiresSandbox=true or similar blocking flags
// that would prevent single-modal models from using them.
func TestAnnotateImage_C10_MetadataDoesNotRequireVisionMainModel(t *testing.T) {
	f := NewPlatformToolFactory(nil, nil)
	_, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)

	var analyzeImageMeta, annotateImageMeta *ToolMetadata
	for i := range metadata {
		switch metadata[i].ToolName {
		case "analyze_image":
			m := metadata[i]
			analyzeImageMeta = &m
		case "annotate_image":
			m := metadata[i]
			annotateImageMeta = &m
		}
	}

	require.NotNil(t, analyzeImageMeta, "analyze_image must be registered")
	require.NotNil(t, annotateImageMeta, "annotate_image must be registered")

	// Neither tool requires sandbox (they don't run code).
	assert.False(t, analyzeImageMeta.RequiresSandbox,
		"analyze_image must not require sandbox")
	assert.False(t, annotateImageMeta.RequiresSandbox,
		"annotate_image must not require sandbox")

	// Source must be "platform".
	assert.Equal(t, "platform", analyzeImageMeta.Source)
	assert.Equal(t, "platform", annotateImageMeta.Source)

	// Category must be "视觉".
	assert.Equal(t, "视觉", analyzeImageMeta.Category)
	assert.Equal(t, "视觉", annotateImageMeta.Category)
}

// ─────────────────────────────────────────────────────────────────────────────
// Additional annotate_image tests
// ─────────────────────────────────────────────────────────────────────────────

// TestAnnotateImage_EmptyURL_ReturnsGraceful verifies missing URL gives graceful error.
func TestAnnotateImage_EmptyURL_ReturnsGraceful(t *testing.T) {
	tool := NewAnnotateImageTool()
	inp, _ := json.Marshal(annotateImageInput{
		AttachmentURL: "",
		Regions:       []annotateImageRegion{{X: 0, Y: 0, Width: 10, Height: 10, Label: "x"}},
	})
	result, err := tool.Execute(context.Background(), ToolInput(inp))
	require.NoError(t, err)
	out := parseAnnotateOutput(t, result)
	assert.Contains(t, out.Annotations[0].Description, "Annotation failed")
}

// TestAnnotateImage_InvalidJSON_ReturnsGraceful verifies bad JSON gives graceful error.
func TestAnnotateImage_InvalidJSON_ReturnsGraceful(t *testing.T) {
	tool := NewAnnotateImageTool()
	result, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	require.NoError(t, err)
	out := parseAnnotateOutput(t, result)
	assert.Contains(t, out.Annotations[0].Description, "Annotation failed")
}

// TestAnnotateImage_RegionPromptContainsCoordinates verifies the prompt sent to
// the vision model includes the region bounding box information.
func TestAnnotateImage_RegionPromptContainsCoordinates(t *testing.T) {
	const runID = uint64(3099)
	resetVisionQuota(runID, "annotate_image")

	var capturedPrompt string
	installMockChat(t, func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		if len(req.Messages) > 0 {
			for _, part := range req.Messages[0].Content.Parts {
				if part.Type == aiservice.MessagePartTypeText {
					capturedPrompt = part.Text
				}
			}
		}
		return fixedChatResponse("top-left content", "model"), nil
	})

	tool := NewAnnotateImageTool()
	region := annotateImageRegion{X: 10, Y: 20, Width: 300, Height: 150, Label: "top-left chart"}
	inp, _ := json.Marshal(annotateImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/slide.png",
		Regions:       []annotateImageRegion{region},
	})
	ctx := WithRunID(context.Background(), runID)
	_, _ = tool.Execute(ctx, ToolInput(inp))

	// The prompt must include coordinates and label so the vision model knows where to look.
	assert.Contains(t, capturedPrompt, "top-left chart")
	assert.Contains(t, capturedPrompt, "x=10")
	assert.Contains(t, capturedPrompt, "y=20")
}

// TestAnnotateImage_ToolMetadata verifies tool interface methods.
func TestAnnotateImage_ToolMetadata(t *testing.T) {
	tool := NewAnnotateImageTool()
	assert.Equal(t, "annotate_image", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Equal(t, "图像区域标注", tool.UserFacingName())
	assert.True(t, tool.IsReadOnly())
	assert.True(t, tool.AlwaysLoad())
}

// TestAnnotateImage_RegionVisionError_GracefulAnnotation verifies that a per-region
// vision call failure does not kill the whole tool call — the failed region gets a
// graceful error annotation and other regions still succeed.
func TestAnnotateImage_RegionVisionError_GracefulAnnotation(t *testing.T) {
	const runID = uint64(3098)
	resetVisionQuota(runID, "annotate_image")

	callCount := 0
	installMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callCount++
		if callCount == 2 {
			// Second region vision call fails.
			return nil, context.DeadlineExceeded
		}
		return fixedChatResponse("region ok", "model"), nil
	})

	tool := NewAnnotateImageTool()
	inp, _ := json.Marshal(annotateImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/multi.png",
		Regions: []annotateImageRegion{
			{X: 0, Y: 0, Width: 100, Height: 100, Label: "region1"},
			{X: 100, Y: 0, Width: 100, Height: 100, Label: "region2"},
			{X: 200, Y: 0, Width: 100, Height: 100, Label: "region3"},
		},
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))

	require.NoError(t, err) // always nil
	out := parseAnnotateOutput(t, result)
	require.Len(t, out.Annotations, 3)

	// region1 and region3 succeeded.
	assert.Equal(t, "region ok", out.Annotations[0].Description)
	// region2 failed gracefully.
	assert.Contains(t, out.Annotations[1].Description, "Vision analysis failed")
	assert.Equal(t, "region ok", out.Annotations[2].Description)
}
