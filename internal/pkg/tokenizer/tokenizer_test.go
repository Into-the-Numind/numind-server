package tokenizer

import (
	"fmt"
	"strings"
	"testing"
)

func TestCountTokens(t *testing.T) {
	tokenizer, err := NewTokenizer()
	if err != nil {
		t.Fatalf("Failed to create tokenizer: %v", err)
	}

	tests := []struct {
		name     string
		text     string
		expected int // Approximate expected count, or minimum
	}{
		{"English", "Hello world", 2},
		{"Chinese", "你好世界", 3}, // Approximate
		{"Empty", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := tokenizer.CountTokens(tt.text)
			fmt.Printf("Text: %s, Count: %d\n", tt.text, count)
			if count == 0 && tt.text != "" {
				t.Error("Expected non-zero token count for non-empty text")
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	tokenizer, err := NewTokenizer()
	if err != nil {
		t.Fatalf("Failed to create tokenizer: %v", err)
	}

	tests := []string{
		"Hello world",
		"你好世界",
		"中英文 mixed content with symbols: {\"ok\": true}",
	}

	if SafetyCoefficient < 1.0 {
		t.Fatalf("SafetyCoefficient must not be below 1.0; got %.2f", SafetyCoefficient)
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			rawCount := tokenizer.CountTokens(text)
			estimated := tokenizer.EstimateTokens(text)

			if estimated < rawCount {
				t.Errorf("EstimateTokens must not under-estimate exact tokens: estimated=%d raw=%d", estimated, rawCount)
			}
		})
	}
}

func TestEstimateMessageTokens(t *testing.T) {
	tokenizer, err := NewTokenizer()
	if err != nil {
		t.Fatalf("Failed to create tokenizer: %v", err)
	}

	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello"},
	}

	// Just ensure it returns a reasonable number and treats messages differently than raw concatenation
	count := tokenizer.EstimateMessageTokens(messages)
	if count <= 0 {
		t.Errorf("Expected non-zero message token count")
	}

	// Check that adding a message increases count significantly (overhead)
	messages2 := append(messages, Message{Role: "assistant", Content: "Hi"})
	count2 := tokenizer.EstimateMessageTokens(messages2)

	diff := count2 - count
	tokenForHi := tokenizer.EstimateTokens("Hi")

	if diff <= tokenForHi {
		t.Errorf("Message overhead seems missing, diff %d is <= token count %d", diff, tokenForHi)
	}
}

func TestPruneContext(t *testing.T) {
	tokenizer, err := NewTokenizer()
	if err != nil {
		t.Fatalf("Failed to create tokenizer: %v", err)
	}

	// Construct a long conversation
	// System prompt (always kept)
	sysMsg := Message{Role: "system", Content: "System Prompt"}

	// Create many messages to exceed limit
	msgs := []Message{sysMsg}
	for i := 0; i < 100; i++ {
		msgs = append(msgs, Message{
			Role:    "user",
			Content: fmt.Sprintf("Message %d %s", i, strings.Repeat("long text ", 10)),
		})
	}

	// Estimate total
	total := tokenizer.EstimateMessageTokens(msgs)
	target := total / 2

	pruned, newTotal := tokenizer.PruneContext(msgs, target)

	if newTotal > target {
		t.Errorf("Failed to prune to target. Got %d, target %d", newTotal, target)
	}

	if len(pruned) == 0 {
		t.Fatal("Pruned result is empty")
	}

	// Check system prompt is preserved
	if pruned[0].Role != "system" || pruned[0].Content != "System Prompt" {
		t.Error("System prompt was not preserved")
	}

	// Check that we actually removed some messages
	if len(pruned) >= len(msgs) {
		t.Error("No messages were pruned")
	}
}

func TestTruncateText(t *testing.T) {
	tokenizer, err := NewTokenizer()
	if err != nil {
		t.Fatalf("Failed to create tokenizer: %v", err)
	}

	text := strings.Repeat("hello ", 1000)
	maxTokens := 100

	truncated := tokenizer.TruncateText(text, maxTokens)
	// count := tokenizer.EstimateTokens(truncated)

	// It should be close to maxTokens.
	// TruncateText Logic:
	// 1. Encode -> get tokens
	// 2. Slice to maxTokens
	// 3. Decode
	// 4. Append notice

	// So raw count of truncated text should be <= maxTokens + tokens(notice)

	// Let's just check length is significantly reduced
	if len(truncated) >= len(text) {
		t.Errorf("Text was not truncated")
	}

	suffix := "\n\n[提示：由于内容过长，系统已自动截取核心部分进行处理。]"
	if !strings.Contains(truncated, suffix) {
		t.Errorf("Truncation notice missing or incorrect.\nExpected suffix: %s\nGot end of string: %s", suffix, truncated[len(truncated)-len(suffix):])
	}
}
