package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// installMockChat replaces the package-level chatFn seam for the duration of
// the test and restores it in a Cleanup handler.
func installMockChat(t *testing.T, fn func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)) {
	t.Helper()
	orig := chatFn
	chatFn = fn
	t.Cleanup(func() { chatFn = orig })
}

// resetVisionQuota clears per-run quota counters in visionQuotaStore so that
// each sub-test starts with a clean slate.
func resetVisionQuota(runID uint64, toolName string) {
	key := visionQuotaKey{runID: runID, toolName: toolName}
	visionQuotaStore.Delete(key)
}

// fixedChatResponse builds a simple successful ChatResponse.
func fixedChatResponse(content, model string) *aiservice.ChatResponse {
	return &aiservice.ChatResponse{
		Content:      content,
		Model:        model,
		FinishReason: "stop",
	}
}

// parseAnalyzeOutput unmarshals the ToolResult JSON into analyzeImageOutput.
func parseAnalyzeOutput(t *testing.T, result ToolResult) analyzeImageOutput {
	t.Helper()
	var out analyzeImageOutput
	require.NoError(t, json.Unmarshal(result, &out))
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// C1: cache hit — question does not trigger bypass keywords
// ─────────────────────────────────────────────────────────────────────────────

// C1: question="describe this image" → generic, no bypass keyword → cache checked.
// attStore is nil (V1.5 limitation: no GetByURL), so cache returns miss; but we
// can test the bypass-keyword logic directly via shouldBypassCache.
func TestAnalyzeImage_C1_ShouldBypassCache_GenericQuestion(t *testing.T) {
	// "describe this image" should NOT trigger bypass → cache is attempted.
	assert.False(t, shouldBypassCache("describe this image"))
	assert.False(t, shouldBypassCache("what is in this picture?"))
	assert.False(t, shouldBypassCache("please describe the scene"))
}

// ─────────────────────────────────────────────────────────────────────────────
// C2: bypass keyword → aiservice called
// ─────────────────────────────────────────────────────────────────────────────

// C2: question contains "count" → bypass cache, call aiservice once.
func TestAnalyzeImage_C2_BypassKeyword_CallsAIService(t *testing.T) {
	const runID = uint64(2001)
	resetVisionQuota(runID, "analyze_image")

	var called int
	installMockChat(t, func(_ context.Context, taskID string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		called++
		assert.Equal(t, "attachment.vision_describe", taskID)
		return fixedChatResponse("I can see 5 buttons on the panel.", "qwen3-vl-flash"), nil
	})

	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/test.png",
		Question:      "count the buttons in this image",
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))

	require.NoError(t, err) // graceful: never returns error
	assert.Equal(t, 1, called, "aiservice should be called exactly once")

	out := parseAnalyzeOutput(t, result)
	assert.Equal(t, "I can see 5 buttons on the panel.", out.Description)
	assert.False(t, out.FromCache)
	assert.Equal(t, "qwen3-vl-flash", out.ModelUsed)
}

// ─────────────────────────────────────────────────────────────────────────────
// C3: attachment URL not found (aiservice returns error) → graceful
// ─────────────────────────────────────────────────────────────────────────────

// C3: aiservice.Chat returns error → tool returns graceful failure description.
func TestAnalyzeImage_C3_AIServiceError_ReturnsGraceful(t *testing.T) {
	const runID = uint64(2003)
	resetVisionQuota(runID, "analyze_image")

	installMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, errors.New("404 not found: image URL unreachable")
	})

	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/notfound.png",
		Question:      "describe this image",
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))

	require.NoError(t, err) // graceful: always nil
	out := parseAnalyzeOutput(t, result)
	assert.Contains(t, out.Description, "Vision analysis failed")
	assert.Equal(t, "(failed)", out.ModelUsed)
	assert.Equal(t, float32(0), out.Confidence)
}

// ─────────────────────────────────────────────────────────────────────────────
// C4: aiservice timeout → graceful
// ─────────────────────────────────────────────────────────────────────────────

// C4: aiservice.Chat simulates a timeout → graceful failure.
func TestAnalyzeImage_C4_Timeout_ReturnsGraceful(t *testing.T) {
	const runID = uint64(2004)
	resetVisionQuota(runID, "analyze_image")

	installMockChat(t, func(ctx context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		// Simulate a context deadline exceeded.
		return nil, context.DeadlineExceeded
	})

	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/slow.png",
		Question:      "count the items",
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))

	require.NoError(t, err)
	out := parseAnalyzeOutput(t, result)
	assert.Contains(t, out.Description, "Vision analysis failed")
	assert.Equal(t, float32(0), out.Confidence)
}

// ─────────────────────────────────────────────────────────────────────────────
// C5: quota exceeded → graceful quota message
// ─────────────────────────────────────────────────────────────────────────────

// C5: after analyzeImageMaxPerRun calls, the next one returns quota error.
func TestAnalyzeImage_C5_QuotaExceeded(t *testing.T) {
	const runID = uint64(2005)
	resetVisionQuota(runID, "analyze_image")

	installMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return fixedChatResponse("description", "model"), nil
	})

	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/quota.png",
		Question:      "describe this",
	})
	ctx := WithRunID(context.Background(), runID)

	// Exhaust the quota.
	for i := 0; i < analyzeImageMaxPerRun; i++ {
		result, err := tool.Execute(ctx, ToolInput(inp))
		require.NoError(t, err)
		out := parseAnalyzeOutput(t, result)
		// Should succeed for all within-quota calls.
		assert.NotContains(t, out.Description, "quota exceeded", "call %d should succeed", i+1)
	}

	// The (analyzeImageMaxPerRun+1)-th call should hit the quota.
	result, err := tool.Execute(ctx, ToolInput(inp))
	require.NoError(t, err)
	out := parseAnalyzeOutput(t, result)
	assert.Contains(t, out.Description, "quota exceeded")
}

// ─────────────────────────────────────────────────────────────────────────────
// C6: Confidence defaults to 0.85 when provider doesn't return it
// ─────────────────────────────────────────────────────────────────────────────

// C6: successful call → Confidence=0.85 (default).
func TestAnalyzeImage_C6_DefaultConfidence(t *testing.T) {
	const runID = uint64(2006)
	resetVisionQuota(runID, "analyze_image")

	installMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		// Provider does not return a confidence field in ChatResponse.
		return fixedChatResponse("A diagram showing data flow.", "qwen3-vl-plus"), nil
	})

	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/diagram.png",
		Question:      "describe this diagram",
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))

	require.NoError(t, err)
	out := parseAnalyzeOutput(t, result)
	assert.Equal(t, float32(0.85), out.Confidence, "default confidence when provider omits it")
}

// ─────────────────────────────────────────────────────────────────────────────
// Additional tests: input validation, bypass keyword list
// ─────────────────────────────────────────────────────────────────────────────

// TestAnalyzeImage_EmptyURL_ReturnsGraceful verifies missing URL gives graceful error.
func TestAnalyzeImage_EmptyURL_ReturnsGraceful(t *testing.T) {
	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "",
		Question:      "describe",
	})
	result, err := tool.Execute(context.Background(), ToolInput(inp))
	require.NoError(t, err)
	out := parseAnalyzeOutput(t, result)
	assert.Contains(t, out.Description, "Vision analysis failed")
}

// TestAnalyzeImage_InvalidJSON_ReturnsGraceful verifies bad JSON gives graceful error.
func TestAnalyzeImage_InvalidJSON_ReturnsGraceful(t *testing.T) {
	tool := NewAnalyzeImageTool(nil)
	result, err := tool.Execute(context.Background(), ToolInput([]byte("not-json")))
	require.NoError(t, err)
	out := parseAnalyzeOutput(t, result)
	assert.Contains(t, out.Description, "Vision analysis failed")
}

// TestShouldBypassCache_BypassKeywords verifies that all bypass keywords trigger.
func TestShouldBypassCache_BypassKeywords(t *testing.T) {
	bypassQuestions := []string{
		"count the items",
		"数一下有几个按钮",
		"compare these two sections",
		"对比两张图的差异",
		"extract the table data",
		"提取表格里的数字",
		"specific details about the color",
		"focus on the region around the logo",
		"区域内有什么文字",
		"what color is the button",
		"颜色是什么",
		"what is the size of the chart",
		"大小如何",
	}
	for _, q := range bypassQuestions {
		assert.True(t, shouldBypassCache(q), "expected bypass for: %q", q)
	}
}

// TestAnalyzeImage_ZeroRunID_NoQuotaEnforcement verifies that runID=0 skips quota.
func TestAnalyzeImage_ZeroRunID_NoQuotaEnforcement(t *testing.T) {
	// With runID=0, quota is not enforced; calls succeed indefinitely.
	installMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return fixedChatResponse("ok", "model"), nil
	})

	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/x.png",
		Question:      "count the items",
	})
	// Do more calls than the quota limit, all with runID=0.
	ctx := context.Background() // no run ID injected
	for i := 0; i < analyzeImageMaxPerRun+5; i++ {
		result, err := tool.Execute(ctx, ToolInput(inp))
		require.NoError(t, err)
		out := parseAnalyzeOutput(t, result)
		assert.NotContains(t, out.Description, "quota exceeded", "runID=0 should not enforce quota")
	}
}

// TestAnalyzeImage_ToolMetadata verifies tool interface methods.
func TestAnalyzeImage_ToolMetadata(t *testing.T) {
	tool := NewAnalyzeImageTool(nil)
	assert.Equal(t, "analyze_image", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Equal(t, "图像分析", tool.UserFacingName())
	assert.True(t, tool.IsReadOnly())
	assert.True(t, tool.AlwaysLoad())
}

// TestAnalyzeImage_EmptyQuestion_UsesDefaultPrompt verifies fallback default prompt.
func TestAnalyzeImage_EmptyQuestion_UsesDefaultPrompt(t *testing.T) {
	const runID = uint64(2099)
	resetVisionQuota(runID, "analyze_image")

	var capturedParts []aiservice.MessagePart
	installMockChat(t, func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		// Capture the message parts to verify that a default prompt was injected.
		if len(req.Messages) > 0 {
			capturedParts = req.Messages[0].Content.Parts
		}
		return fixedChatResponse("description", "model"), nil
	})

	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/empty.png",
		Question:      "", // empty
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))
	require.NoError(t, err)

	// A default question should have been used.
	require.NotEmpty(t, capturedParts, "should have sent a message with parts")
	textPart := capturedParts[0]
	assert.Equal(t, aiservice.MessagePartTypeText, textPart.Type)
	assert.NotEmpty(t, textPart.Text, "default question should be non-empty")

	out := parseAnalyzeOutput(t, result)
	assert.NotEmpty(t, out.Description)
}

// TestAnalyzeImage_ResponseUsesModelfromChatResponse checks model_used is set from resp.Model.
func TestAnalyzeImage_ResponseModelFromChatResponse(t *testing.T) {
	const runID = uint64(2098)
	resetVisionQuota(runID, "analyze_image")
	installMockChat(t, func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content:      "detailed description",
			Model:        "qwen3-vl-flash-2026-01-22",
			FinishReason: "stop",
		}, nil
	})

	tool := NewAnalyzeImageTool(nil)
	inp, _ := json.Marshal(analyzeImageInput{
		AttachmentURL: "https://example.cos.myqcloud.com/img/check.png",
		Question:      "describe",
	})
	ctx := WithRunID(context.Background(), runID)
	result, err := tool.Execute(ctx, ToolInput(inp))

	require.NoError(t, err)
	out := parseAnalyzeOutput(t, result)
	assert.Equal(t, "qwen3-vl-flash-2026-01-22", out.ModelUsed,
		"ModelUsed should reflect the actual model from ChatResponse")
}
