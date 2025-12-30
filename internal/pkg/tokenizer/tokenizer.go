package tokenizer

import (
	"fmt"

	"github.com/pkoukk/tiktoken-go"
)

const (
	// DeepSeek V3 Context Window
	MaxContextWindow = 128000
	// Safety Coefficient for estimation
	SafetyCoefficient = 1.1
	// Target token count after pruning (to leave room for new input and generation)
	TargetPrunedTokens = 80000
)

// Message represents a chat message for tokenization
type Message struct {
	Role    string
	Content string
}

// Tokenizer wrapper for tiktoken
type Tokenizer struct {
	tk *tiktoken.Tiktoken
}

// NewTokenizer creates a new tokenizer instance using cl100k_base encoding
func NewTokenizer() (*Tokenizer, error) {
	// DeepSeek uses a tokenizer similar to OpenAI's cl100k_base
	tk, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		return nil, fmt.Errorf("failed to get encoding: %w", err)
	}
	return &Tokenizer{tk: tk}, nil
}

// CountTokens returns the exact token count for a text string based on tiktoken
func (t *Tokenizer) CountTokens(text string) int {
	return len(t.tk.Encode(text, nil, nil))
}

// EstimateTokens returns the estimated token count with safety coefficient
func (t *Tokenizer) EstimateTokens(text string) int {
	count := t.CountTokens(text)
	return int(float64(count) * SafetyCoefficient)
}

// EstimateMessageTokens estimates tokens for a list of messages including overhead
func (t *Tokenizer) EstimateMessageTokens(messages []Message) int {
	total := 0
	// Per-message overhead (approximate for ChatML/OpenAI format)
	// <|im_start|>role\ncontent<|im_end|>\n
	const perMessageOverhead = 4

	for _, msg := range messages {
		total += t.EstimateTokens(msg.Content)
		total += perMessageOverhead
	}
	// Reply prefix overhead
	total += 3
	return total
}

// PruneContext automatically removes oldest messages to fit within limit
// Strategies:
// 1. Always keep System Prompt (if present at index 0)
// 2. Keep the user's latest input (assumed to be appended before calling this, or handled by caller)
// 3. Remove oldest messages from history until total tokens < target
func (t *Tokenizer) PruneContext(messages []Message, targetLimit int) ([]Message, int) {
	if len(messages) == 0 {
		return messages, 0
	}

	totalTokens := t.EstimateMessageTokens(messages)
	if totalTokens <= targetLimit {
		return messages, totalTokens
	}

	// Identify system prompt
	var systemMsg *Message
	startIdx := 0
	if messages[0].Role == "system" {
		systemMsg = &messages[0]
		startIdx = 1
	}

	// Create a new slice for remaining messages
	remainingMessages := make([]Message, 0, len(messages))
	if systemMsg != nil {
		remainingMessages = append(remainingMessages, *systemMsg)
	}

	// We need to keep the latest messages.
	// We'll iterate from the end backwards to find how many we can keep.
	// However, the standard requirements usually imply "sliding window" from the *start* of history.

	// Let's calculate tokens for immutable parts first (System Prompt)
	currentTokens := 0
	if systemMsg != nil {
		currentTokens = t.EstimateMessageTokens([]Message{*systemMsg})
	}

	// Calculate tokens for all non-system messages
	// We want to keep the LATEST N messages that fit.

	// Working backwards from the last message
	tempKept := []Message{}
	for i := len(messages) - 1; i >= startIdx; i-- {
		msgTokens := t.EstimateMessageTokens([]Message{messages[i]})
		if currentTokens+msgTokens > targetLimit {
			break
		}
		currentTokens += msgTokens
		// Prepend to tempKept (since we are iterating backwards)
		tempKept = append([]Message{messages[i]}, tempKept...)
	}

	remainingMessages = append(remainingMessages, tempKept...)

	return remainingMessages, currentTokens
}

// TruncateText truncates a string to fit within maxTokens
func (t *Tokenizer) TruncateText(text string, maxTokens int) string {
	tokens := t.tk.Encode(text, nil, nil)
	if len(tokens) <= maxTokens {
		return text
	}

	// Decode back the first maxTokens
	truncatedTokens := tokens[:maxTokens]
	truncatedText := t.tk.Decode(truncatedTokens)

	return truncatedText + "\n\n[提示：由于内容过长，系统已自动截取核心部分进行处理。]"
}
