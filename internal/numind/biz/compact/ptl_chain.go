package compact

import (
	"context"
	"fmt"
)

// CollapseDrain implements PTL chain outer Step 1 (blueprint §4.1.6):
// strip tool_result messages older than the last keepTurns user-turns.
// Protected from dropping: messages flagged IsCompactMark or HasFileRef, and
// every message at or after the keep boundary (user/assistant text included).
func CollapseDrain(messages []Message, keepTurns int) []Message {
	if keepTurns <= 0 {
		keepTurns = 4
	}
	if keepTurns >= len(messages) {
		return messages
	}

	// Find user-turn boundaries.
	userIndices := make([]int, 0, len(messages))
	for i, m := range messages {
		if m.Role == "user" {
			userIndices = append(userIndices, i)
		}
	}

	var keepFrom int
	if len(userIndices) <= keepTurns {
		keepFrom = 0
	} else {
		keepFrom = userIndices[len(userIndices)-keepTurns]
	}

	out := make([]Message, 0, len(messages))
	for i, m := range messages {
		if i >= keepFrom {
			out = append(out, m)
			continue
		}
		if m.IsCompactMark || m.HasFileRef {
			out = append(out, m)
			continue
		}
		if m.Role == "tool" {
			// Strip the tool_result block.
			continue
		}
		// user / assistant text outside keep window: retained per §4.1.6 Step 1.
		out = append(out, m)
	}
	return out
}

// ReactiveCompact implements PTL chain outer Step 2 (blueprint §4.8.5): call
// the LLM to compress the entire history into a CompactSummary. On provider
// error, recurse with headDropRetry up to 3 times (inner retry, distinct from
// the outer PTLRetries tracked in state.go).
//
// Returns (result, finalMessages, err) where finalMessages is the message
// slice that actually fed the successful Compact call — caller must use this
// (not the original `messages`) when computing the post-compact view, so the
// summary's coverage matches the collapsed tail.
func ReactiveCompact(ctx context.Context, provider CompactProvider, messages []Message, cfg Config) (*CompactResult, []Message, error) {
	if provider == nil {
		return nil, nil, fmt.Errorf("ReactiveCompact: nil provider")
	}
	req := &CompactRequest{
		Messages:        messages,
		SystemPrompt:    FullCompactSystemPrompt(),
		MaxOutputTokens: cfg.MaxCompactOutputTokens,
	}
	result, err := provider.Compact(ctx, req)
	if err == nil {
		return result, messages, nil
	}

	innerDropAttempts := 0
	truncated := messages
	const maxInner = 3
	for innerDropAttempts < maxInner {
		truncated = headDropRetry(truncated, 0.25)
		req.Messages = truncated
		result, err = provider.Compact(ctx, req)
		if err == nil {
			return result, truncated, nil
		}
		innerDropAttempts++
	}
	return nil, nil, fmt.Errorf("ReactiveCompact: exhausted %d innerDropAttempts: %w", maxInner, err)
}

// headDropRetry drops the head dropPercent of user-turn groups (blueprint
// §4.8.5). Protection rules:
//   - never drop a turn that contains IsCompactMark or HasFileRef messages
//   - never drop the most recent 10 turns
//   - stop advancing the drop boundary as soon as a protected turn is hit —
//     never interleave drops across protected turns, since gaps in the message
//     timeline confuse the LLM more than a less aggressive trim.
func headDropRetry(messages []Message, dropPercent float64) []Message {
	if len(messages) == 0 || dropPercent <= 0 {
		return messages
	}

	turnStarts := make([]int, 0, len(messages))
	for i, m := range messages {
		if m.Role == "user" {
			turnStarts = append(turnStarts, i)
		}
	}
	if len(turnStarts) <= 10 {
		return messages
	}

	numTurns := len(turnStarts)
	const keepRecentTurns = 10
	maxDropTurns := numTurns - keepRecentTurns
	dropCount := int(float64(numTurns) * dropPercent)
	if dropCount > maxDropTurns {
		dropCount = maxDropTurns
	}
	if dropCount <= 0 {
		return messages
	}

	actualDrop := 0
	dropEndIdx := 0 // exclusive
	for t := 0; t < dropCount && t < len(turnStarts); t++ {
		startIdx := turnStarts[t]
		endIdx := len(messages)
		if t+1 < len(turnStarts) {
			endIdx = turnStarts[t+1]
		}
		protected := false
		for j := startIdx; j < endIdx; j++ {
			if messages[j].IsCompactMark || messages[j].HasFileRef {
				protected = true
				break
			}
		}
		if protected {
			break // stop advancing — avoid interleaved drops
		}
		dropEndIdx = endIdx
		actualDrop++
	}
	if actualDrop == 0 {
		return messages
	}
	return messages[dropEndIdx:]
}
