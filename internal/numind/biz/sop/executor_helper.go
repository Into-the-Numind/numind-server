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

	// 3. 判断是否需要处理 (触发阈值 110k)
	if totalEstimated > limit110k {
		log.C(ctx).Infow("Token limit threshold reached (110k)", "node_id", node.ID, "estimated", totalEstimated)

		// 情况 A: 极其严重的溢出 (> 125k)，如果是首轮则需要对输入进行物理截断
		if totalEstimated > limit125k {
			// 检查是否是第一轮对话（只有 System + User）
			isFirstTurn := true
			for _, m := range messages {
				if m.Role == "assistant" {
					isFirstTurn = false
					break
				}
			}

			if isFirstTurn && e.tokenizer != nil {
				// 处理首轮超限：直接截断当前输入
				log.C(ctx).Warnw("First turn critical overflow (>125k), truncating input", "node_id", node.ID)

				// 估算系统提示词开销
				sysOverhead := 0
				if len(messages) > 0 && messages[0].Role == "system" {
					sysOverhead = e.tokenizer.EstimateTokens(messages[0].Content)
				}

				// 截断用户消息至约 100k
				targetInput := 100000 - sysOverhead
				if targetInput < 10000 {
					targetInput = 10000
				}

				lastMsgIdx := len(messages) - 1
				originalContent := messages[lastMsgIdx].Content
				truncatedInput := e.tokenizer.TruncateText(originalContent, targetInput)

				// 重组消息列表
				finalMessages = make([]LLMMessage, 0)
				if len(messages) > 0 && messages[0].Role == "system" {
					finalMessages = append(finalMessages, messages[0])
				}
				finalMessages = append(finalMessages, LLMMessage{
					Role:    "user",
					Content: truncatedInput,
				})

				// 重新评估 token
				totalEstimated = e.tokenizer.EstimateTokens(finalMessages[len(finalMessages)-1].Content) + sysOverhead
			} else if e.tokenizer != nil {
				// 历史溢出：裁剪至安全区 (80k)
				log.C(ctx).Warnw("History critical overflow (>125k), pruning to 80k", "node_id", node.ID)
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
		} else if e.tokenizer != nil {
			// 情况 B: 超过阈值 (110k - 125k)
			// 如果不是首轮，则进行历史裁剪至 80k
			isFirstTurn := true
			for _, m := range messages {
				if m.Role == "assistant" {
					isFirstTurn = false
					break
				}
			}

			if !isFirstTurn {
				log.C(ctx).Infow("Token exceeded 110k, pruning history to 80k", "node_id", node.ID)
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

	// Safety check: ensure finalMessages is not empty if original messages was not empty
	if len(finalMessages) == 0 && len(messages) > 0 {
		log.C(ctx).Warnw("finalMessages is empty after processing, forcing last message to be kept", "node_id", node.ID)
		finalMessages = []LLMMessage{messages[len(messages)-1]}
		totalEstimated = e.tokenizer.EstimateMessageTokens([]tokenizer.Message{
			{Role: finalMessages[0].Role, Content: finalMessages[0].Content},
		})
	}

	return finalMessages, totalEstimated, maxTokens, nil
}
