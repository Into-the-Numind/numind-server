package compact

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
)

// chatFn is the package-level seam for aiservice.Chat, allowing unit tests to
// inject a mock without importing aiservice in test files.
var chatFn = func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return aiservice.Chat(ctx, taskID, req)
}

// AiserviceCompactProvider is the production CompactProvider backed by
// aiservice.Chat (Agent Mode #14/A4). Replaces MockCompactProvider for prod
// runtime; MockCompactProvider retained for unit tests of ptl_chain etc.
type AiserviceCompactProvider struct {
	cfg Config
}

// NewAIServiceCompactProvider constructs a real CompactProvider with the given config.
// Config controls max summary tokens via cfg.MaxCompactOutputTokens; the task
// profile routes to qwen-plus by default (profile.AgentCompact).
func NewAIServiceCompactProvider(cfg Config) CompactProvider {
	return &AiserviceCompactProvider{cfg: cfg}
}

// Compact runs the LLM compaction over req.Messages and returns a summary
// + token usage. Errors are propagated (caller decides retry per PTL chain).
func (p *AiserviceCompactProvider) Compact(ctx context.Context, req *CompactRequest) (*CompactResult, error) {
	// Use the system prompt already set on the request (callers set it via
	// FullCompactSystemPrompt()), falling back to the package default.
	systemPrompt := req.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = FullCompactSystemPrompt()
	}
	userContent := SerializeMessagesForCompact(req.Messages)

	maxTokens := p.cfg.MaxCompactOutputTokens
	if req.MaxOutputTokens > 0 {
		maxTokens = req.MaxOutputTokens
	}

	chatReq := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: systemPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: userContent}},
		},
		MaxTokens:   maxTokens,
		Temperature: 0.0, // deterministic compact summary
	}

	resp, err := chatFn(ctx, profile.AgentCompact, chatReq)
	if err != nil {
		return nil, fmt.Errorf("AiserviceCompactProvider.Compact: %w", err)
	}
	return &CompactResult{
		Summary:      resp.Content,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}, nil
}

// SerializeMessagesForCompact concatenates messages into a single string
// for the user-role content of the compact LLM call.
func SerializeMessagesForCompact(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}
