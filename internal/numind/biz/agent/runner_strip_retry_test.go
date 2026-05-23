package agent

// runner_strip_retry_test.go — Task 1.5 integration tests.
//
// Tests the 4 core scenarios from spec §"集成测试 4 scenario":
//  1. happy_retry: first call returns multimodal err, second call succeeds.
//  2. retry_exhausted: both calls fail → returns original error.
//  3. non_multimodal_err: non-image error → passes through without strip.
//  4. no_images_multimodal_err: multimodal err but no images → skip retry, return err.
//
// Also tests stripImagesFromMessages directly.
//
// Note: tests manipulate the package-level chatFn seam, which is the same hook
// used by adapter_test.go. Each test restores the original via defer to avoid
// cross-test pollution.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"numind-server/internal/pkg/aiservice"
)

// savedChatFn holds the original chatFn so we can restore it after each test.
var savedChatFn = chatFn

// withChatFn replaces chatFn for the duration of a test and restores it via
// the returned cleanup func. Always defer the returned func.
func withChatFn(fn func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) func() {
	chatFn = fn
	return func() { chatFn = savedChatFn }
}

// ---------------------------------------------------------------------------
// stripImagesFromMessages unit tests
// ---------------------------------------------------------------------------

func TestStripImagesFromMessages_NoImages(t *testing.T) {
	msgs := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Text: "hello"},
		},
	}
	got, n := stripImagesFromMessages(msgs)
	if n != 0 {
		t.Fatalf("expected 0 stripped, got %d", n)
	}
	if len(got) != 1 || got[0].Content.Text != "hello" {
		t.Fatalf("message should be preserved unchanged; got %+v", got)
	}
}

func TestStripImagesFromMessages_WithImages(t *testing.T) {
	msgs := []aiservice.ChatMessage{
		{
			Role: aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{
				Parts: []aiservice.MessagePart{
					{Type: aiservice.MessagePartTypeText, Text: "分析这张图"},
					{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://example.com/img1.png"}},
					{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://example.com/img2.png"}},
				},
			},
		},
	}
	got, n := stripImagesFromMessages(msgs)
	if n != 2 {
		t.Fatalf("expected 2 stripped, got %d", n)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 output message, got %d", len(got))
	}
	// Should have 1 original text part + 2 placeholder parts.
	parts := got[0].Content.Parts
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts (1 text + 2 placeholders), got %d", len(parts))
	}
	if parts[0].Text != "分析这张图" {
		t.Errorf("first part text mismatch: %q", parts[0].Text)
	}
	if !strings.Contains(parts[1].Text, "图片内容不可用") {
		t.Errorf("placeholder text missing: %q", parts[1].Text)
	}
}

func TestStripImagesFromMessages_PreservesRoles(t *testing.T) {
	msgs := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleSystem,
			Content: aiservice.MessageContent{Text: "system prompt"},
		},
		{
			Role:       aiservice.MessageRoleTool,
			ToolCallID: "tc-123",
			Content:    aiservice.MessageContent{Text: "tool result"},
		},
	}
	got, n := stripImagesFromMessages(msgs)
	if n != 0 {
		t.Fatalf("expected 0 stripped from non-user messages")
	}
	if got[0].Role != aiservice.MessageRoleSystem {
		t.Errorf("role not preserved: %v", got[0].Role)
	}
	if got[1].ToolCallID != "tc-123" {
		t.Errorf("tool_call_id not preserved: %v", got[1].ToolCallID)
	}
}

func TestStripImagesFromMessages_Empty(t *testing.T) {
	got, n := stripImagesFromMessages(nil)
	if n != 0 || len(got) != 0 {
		t.Fatalf("nil input should produce nil output, n=0; got n=%d len=%d", n, len(got))
	}
}

// ---------------------------------------------------------------------------
// callAIServiceWithStripRetry integration scenarios
// ---------------------------------------------------------------------------

// scenario1: first call returns multimodal error, second call succeeds.
func TestCallAIServiceWithStripRetry_HappyRetry(t *testing.T) {
	// withChatFn(nil) is used here purely for its cleanup side-effect: the returned
	// func restores savedChatFn at the end of the test. The nil value set by
	// withChatFn is immediately overwritten by the chatFn assignment below.
	// Prefer using withChatFn(actualFn) directly to avoid this two-step pattern.
	defer withChatFn(nil)()

	callCount := 0
	imageErrMsg := "Invalid value: 'image_url' is not supported for this model"
	chatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callCount++
		if callCount == 1 {
			return nil, errors.New(imageErrMsg)
		}
		// Second call (after strip): succeed.
		return &aiservice.ChatResponse{Content: "text answer without images"}, nil
	}

	msgs := []aiservice.ChatMessage{
		{
			Role: aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{
				Parts: []aiservice.MessagePart{
					{Type: aiservice.MessagePartTypeText, Text: "describe this"},
					{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://example.com/a.png"}},
					{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://example.com/b.png"}},
				},
			},
		},
	}
	req := aiservice.ChatRequest{Messages: msgs}

	resp, err := callAIServiceWithStripRetry(context.Background(), "agent.run", req, "glm-4-7")
	if err != nil {
		t.Fatalf("expected success after retry, got err: %v", err)
	}
	if resp == nil || resp.Content != "text answer without images" {
		t.Errorf("unexpected response: %v", resp)
	}
	if callCount != 2 {
		t.Errorf("expected 2 chatFn calls, got %d", callCount)
	}
}

// scenario2: both calls fail → original error returned.
func TestCallAIServiceWithStripRetry_RetryExhausted(t *testing.T) {
	defer withChatFn(nil)()

	callCount := 0
	originalErr := errors.New("Invalid value: 'image_url' not supported (first)")
	chatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callCount++
		return nil, originalErr
	}

	msgs := []aiservice.ChatMessage{
		{
			Role: aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{
				Parts: []aiservice.MessagePart{
					{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://example.com/x.png"}},
				},
			},
		},
	}
	req := aiservice.ChatRequest{Messages: msgs}

	_, err := callAIServiceWithStripRetry(context.Background(), "agent.run", req, "qwen-turbo")
	if err == nil {
		t.Fatal("expected error after retry exhausted, got nil")
	}
	// Should return the original error, not the retry error.
	if !errors.Is(err, originalErr) && err.Error() != originalErr.Error() {
		t.Errorf("expected original error, got: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected exactly 2 chatFn calls, got %d", callCount)
	}
}

// scenario3: non-multimodal error → pass through without strip.
func TestCallAIServiceWithStripRetry_NonMultimodalErr(t *testing.T) {
	defer withChatFn(nil)()

	callCount := 0
	nonImageErr := errors.New("internal server error: 500")
	chatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callCount++
		return nil, nonImageErr
	}

	msgs := []aiservice.ChatMessage{
		{
			Role: aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{
				Parts: []aiservice.MessagePart{
					{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://example.com/x.png"}},
				},
			},
		},
	}
	req := aiservice.ChatRequest{Messages: msgs}

	_, err := callAIServiceWithStripRetry(context.Background(), "agent.run", req, "glm-4-7")
	if err == nil {
		t.Fatal("expected error for non-multimodal failure")
	}
	if !errors.Is(err, nonImageErr) && err.Error() != nonImageErr.Error() {
		t.Errorf("expected the original non-multimodal error, got: %v", err)
	}
	// Must NOT retry — only one call.
	if callCount != 1 {
		t.Errorf("expected exactly 1 chatFn call (no retry), got %d", callCount)
	}
}

// scenario4: multimodal error but no image parts → skip retry.
func TestCallAIServiceWithStripRetry_NoImagesMultimodalErr(t *testing.T) {
	defer withChatFn(nil)()

	callCount := 0
	imageErrMsg := "model does not support image input"
	chatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callCount++
		return nil, errors.New(imageErrMsg)
	}

	// Message has NO image parts — only plain text.
	msgs := []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Text: "plain text question"},
		},
	}
	req := aiservice.ChatRequest{Messages: msgs}

	_, err := callAIServiceWithStripRetry(context.Background(), "agent.run", req, "glm-5-1")
	if err == nil {
		t.Fatal("expected error when no images to strip")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("unexpected error message: %v", err)
	}
	// Must NOT retry because there was nothing to strip.
	if callCount != 1 {
		t.Errorf("expected exactly 1 chatFn call (skipped retry), got %d", callCount)
	}
}

// scenario5: nil error → direct pass through.
func TestCallAIServiceWithStripRetry_NilError(t *testing.T) {
	defer withChatFn(nil)()

	callCount := 0
	chatFn = func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		callCount++
		return &aiservice.ChatResponse{Content: "ok"}, nil
	}

	req := aiservice.ChatRequest{Messages: []aiservice.ChatMessage{
		{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hello"}},
	}}

	resp, err := callAIServiceWithStripRetry(context.Background(), "agent.run", req, "deepseek-v3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("unexpected content: %v", resp.Content)
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 call, got %d", callCount)
	}
}

// ---------------------------------------------------------------------------
// attachmentReminderText constant
// ---------------------------------------------------------------------------

func TestAttachmentReminderText_NotEmpty(t *testing.T) {
	if attachmentReminderText == "" {
		t.Error("attachmentReminderText must not be empty")
	}
	if !strings.Contains(attachmentReminderText, "附件说明") {
		t.Errorf("attachmentReminderText should contain '附件说明', got: %q", attachmentReminderText)
	}
}
