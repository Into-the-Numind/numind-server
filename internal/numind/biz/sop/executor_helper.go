package sop

import (
	"context"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/tokenizer"
)

// prepareContext 处理上下文 Token 管理（估算、裁剪、截断、设置 MaxTokens）
func (e *SopExecutor) prepareContext(ctx context.Context, node *model.SopNode, messages []LLMMessage) ([]LLMMessage, int, int, error) {
	// 2. 转换为 Tokenizer 格式进行估算
	tkMessages := make([]tokenizer.Message, len(messages))
	for i, msg := range messages {
		tkMessages[i] = tokenizer.Message{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	totalEstimated := 0
	if e.tokenizer != nil {
		totalEstimated = e.tokenizer.EstimateMessageTokens(tkMessages)
	} else {
		// Fallback simple estimation if tokenizer init failed (approx 4 chars/token * 1.1)
		// This is just a rough fallback to avoid panic
		totalLen := 0
		for _, m := range messages {
			totalLen += len(m.Content)
		}
		totalEstimated = int(float64(totalLen) / 3.0)
	}

	limit110k := 110000
	limit125k := 125000
	maxContext := 128000

	finalMessages := messages
	maxTokens := 4096 // Default buffer

	// 3. 判断是否需要处理
	if totalEstimated > limit110k {
		log.C(ctx).Infow("Token limit warning triggered", "node_id", node.ID, "estimated", totalEstimated)

		// Situation: Critical Overflow (> 125k)
		if totalEstimated > limit125k {
			// Check if it's first turn (only System + User)
			isFirstTurn := true
			for _, m := range messages {
				if m.Role == "assistant" {
					isFirstTurn = false
					break
				}
			}

			if isFirstTurn && e.tokenizer != nil {
				// Handle First Turn Overflow: Truncate Input
				log.C(ctx).Warnw("First turn overflow detected, truncating input", "node_id", node.ID)

				// Estimate overhead of system prompt
				sysOverhead := 0
				if len(messages) > 0 && messages[0].Role == "system" {
					sysOverhead = e.tokenizer.EstimateTokens(messages[0].Content)
				}

				// Truncate user message to fit roughly 100k
				targetInput := 100000 - sysOverhead
				if targetInput < 10000 {
					targetInput = 10000
				} // Safety floor

				// Extract user message content (assuming it's the last one)
				lastMsgIdx := len(messages) - 1
				originalContent := messages[lastMsgIdx].Content
				truncatedInput := e.tokenizer.TruncateText(originalContent, targetInput)

				// Rebuild messages
				finalMessages = make([]LLMMessage, 0)
				if len(messages) > 0 && messages[0].Role == "system" {
					finalMessages = append(finalMessages, messages[0])
				}
				finalMessages = append(finalMessages, LLMMessage{
					Role:    "user",
					Content: truncatedInput,
				})

				// Re-estimate
				totalEstimated = e.tokenizer.EstimateTokens(finalMessages[len(finalMessages)-1].Content) + sysOverhead

			} else if e.tokenizer != nil {
				// Handle History Overflow: Prune Context
				log.C(ctx).Warnw("History overflow detected, pruning context", "node_id", node.ID)
				prunedTkMessages, newEst := e.tokenizer.PruneContext(tkMessages, 80000)

				// Convert back to LLMMessage
				finalMessages = make([]LLMMessage, len(prunedTkMessages))
				for i, m := range prunedTkMessages {
					finalMessages[i] = LLMMessage{
						Role:    m.Role,
						Content: m.Content,
					}
				}
				totalEstimated = newEst
			}
		} else if e.tokenizer != nil {
			// Situation: Warning Zone (110k - 125k)
			// Prune history if possible to get back to safe zone (80k)
			// But if it is first turn, we do nothing here (unless it hits critical)
			isFirstTurn := true
			for _, m := range messages {
				if m.Role == "assistant" {
					isFirstTurn = false
					break
				}
			}

			if !isFirstTurn {
				log.C(ctx).Infow("Pruning history to safe zone", "node_id", node.ID)
				prunedTkMessages, newEst := e.tokenizer.PruneContext(tkMessages, 80000)
				finalMessages = make([]LLMMessage, len(prunedTkMessages))
				for i, m := range prunedTkMessages {
					finalMessages[i] = LLMMessage{
						Role:    m.Role,
						Content: m.Content,
					}
				}
				totalEstimated = newEst
			}
		}
	}

	// 4. 计算剩余空间并设置 MaxTokens
	remaining := maxContext - totalEstimated
	if remaining < 0 {
		remaining = 0
	} // Should be handled by truncation/pruning, but just in case

	// Dynamic MaxTokens strategy:
	// If remaining is ample (> 4k), use default 4096 (or model limit).
	// If remaining is tight (< 4k), squeeze max_tokens to fit window.
	// Minimum safety floor: 200 tokens (if less, we might get incomplete answer but better than error)

	if remaining < 1000 {
		// Very tight, give it all remaining space
		maxTokens = remaining
	} else if remaining < 5000 {
		// Moderately tight, use safe buffer
		maxTokens = remaining - 100 // Leave tiny buffer
	} else {
		// Plenty of space
		maxTokens = 4096
	}

	// Safety floor
	if maxTokens < 1 {
		maxTokens = 1
	}

	return finalMessages, totalEstimated, maxTokens, nil
}
