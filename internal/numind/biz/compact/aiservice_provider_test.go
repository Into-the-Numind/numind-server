package compact

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/aiservice"
)

// resetChatFn restores the package-level chatFn seam after each test.
func resetChatFn(t *testing.T, orig func(context.Context, string, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) {
	t.Helper()
	t.Cleanup(func() { chatFn = orig })
}

func TestAIServiceCompactProvider_HappyPath(t *testing.T) {
	orig := chatFn
	resetChatFn(t, orig)

	const wantSummary = "这是一段对话摘要。"
	chatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content: wantSummary,
			Usage: aiservice.TokenUsage{
				PromptTokens:     500,
				CompletionTokens: 100,
			},
		}, nil
	}

	p := NewAIServiceCompactProvider(DefaultConfig())
	req := &CompactRequest{
		Messages: []Message{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好，有什么可以帮你？"},
		},
		SystemPrompt:    FullCompactSystemPrompt(),
		MaxOutputTokens: 1000,
	}

	result, err := p.Compact(context.Background(), req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.Summary != wantSummary {
		t.Errorf("Summary = %q, want %q", result.Summary, wantSummary)
	}
}

func TestAIServiceCompactProvider_Error_Propagated(t *testing.T) {
	orig := chatFn
	resetChatFn(t, orig)

	wantErr := errors.New("provider unavailable")
	chatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, wantErr
	}

	p := NewAIServiceCompactProvider(DefaultConfig())
	req := &CompactRequest{
		Messages:        []Message{{Role: "user", Content: "test"}},
		SystemPrompt:    FullCompactSystemPrompt(),
		MaxOutputTokens: 1000,
	}

	_, err := p.Compact(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error chain does not contain wantErr; got: %v", err)
	}
}

func TestAIServiceCompactProvider_TokenUsage_RecordedFromResponse(t *testing.T) {
	orig := chatFn
	resetChatFn(t, orig)

	chatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content: "摘要内容",
			Usage: aiservice.TokenUsage{
				PromptTokens:     1000,
				CompletionTokens: 200,
			},
		}, nil
	}

	p := NewAIServiceCompactProvider(DefaultConfig())
	req := &CompactRequest{
		Messages:        []Message{{Role: "user", Content: "hi"}},
		SystemPrompt:    FullCompactSystemPrompt(),
		MaxOutputTokens: 500,
	}

	result, err := p.Compact(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.InputTokens != 1000 {
		t.Errorf("InputTokens = %d, want 1000", result.InputTokens)
	}
	if result.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", result.OutputTokens)
	}
}
