package sop

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/tokenizer"
)

// ErrGatewayInputTooLong 裁剪后当前步骤自身仍超过 gateway token 上限。
// Controller 层可把此错误转成用户友好提示（"您本次输入的内容过长，请缩减后重试"）。
var ErrGatewayInputTooLong = errors.New("gateway input exceeds token cap even after trimming history")

// gatewayTokenCap 单次 Gateway 请求的 cl100k token 上限。
// 上游 aihubmix / deepseek-v3.2-thinking input 实测上限 98304 real tokens。
// 留 ~13k buffer 覆盖 cl100k vs DeepSeek tokenizer 偏差 + ChatML 格式开销 + 估算误差。
const gatewayTokenCap = 85000

// attachmentMarkerRegex 匹配 controller 层拼接的附件 block 起始行 `=== filename.ext ===`
// 见 internal/numind/controller/v1/sop/sop.go:717
// 只匹配常见文档扩展名白名单，避免误伤正文中恰好包含 `=== xxx ===` 格式的用户文字。
// Block 内容 = 从此 marker 起到下一个 marker 或消息末尾（依赖不变量：attachments 总在消息末尾
// 按 \n\n 连接，见 controller sop.go:731-735）。
// 注意：Go RE2 不支持 lookahead，因此 marker 单独匹配、block 边界通过索引计算。
var attachmentMarkerRegex = regexp.MustCompile(`(?m)^=== ([^\n]+?\.(?:docx|pdf|doc|txt|md|xlsx|csv|ppt|pptx)) ===$`)

// trimHistoryForGateway 在调用 Gateway ChatStream 前对历史消息渐进式裁剪，保证
// cl100k token 估算不超过 gatewayTokenCap。策略（5 级，严格递进）：
//
//  1. 若总量 ≤ cap，原样返回
//  2. 从最早历史步骤开始，逐步把该步骤的附件 block 替换为 `[附件已省略: xxx.ext]` 标签
//  3. 仍超 → 下一个历史步骤的附件同样处理，依序直到所有历史附件清空
//  4. 仍超 → 从最早整段丢历史消息（user + 紧跟的 assistant 视为一对），直到塞得下
//  5. 历史清空仍超（当前步骤自身过大）→ 返回 ErrGatewayInputTooLong，不触碰当前步骤
//
// 输入约定：messages[0] 若 role=system 视为 system prompt 永不裁剪；messages[len-1] 为当前步骤消息永不裁剪。
// 当前步骤的附件永远保留。
func (e *SopExecutor) trimHistoryForGateway(ctx context.Context, messages []LLMMessage) ([]LLMMessage, error) {
	if e.tokenizer == nil || len(messages) == 0 {
		return messages, nil
	}

	const perMessageOverhead = 4
	const replyPrefixOverhead = 3

	// 按消息缓存 token 数：初始各计一次，后续修改/丢弃时增量维护，避免 O(N²) 重扫大文本
	msgTokens := make([]int, len(messages))
	totalTokens := replyPrefixOverhead
	for i, m := range messages {
		msgTokens[i] = e.tokenizer.CountTokens(m.Content) + perMessageOverhead
		totalTokens += msgTokens[i]
	}

	if totalTokens <= gatewayTokenCap {
		return messages, nil
	}

	// 识别 system prompt 与当前步骤边界，historyStart..historyEnd 为可裁剪区间（左闭右开）
	historyStart := 0
	if messages[0].Role == "system" {
		historyStart = 1
	}
	historyEnd := len(messages) - 1 // 最后一条是当前步骤，不碰
	if historyStart >= historyEnd {
		log.C(ctx).Warnw("gateway trim: no history to trim, current step exceeds cap",
			"estimated_tokens", totalTokens, "cap", gatewayTokenCap)
		return messages, ErrGatewayInputTooLong
	}

	trimmed := make([]LLMMessage, len(messages))
	copy(trimmed, messages)

	// Step 2-3: 从最早到最晚历史步骤逐步剥附件
	// 历史消息构造约定（sop.go:569-592）：每个历史节点贡献 (user, assistant) 一对。附件只出现在 user 消息。
	for i := historyStart; i < historyEnd; i++ {
		if trimmed[i].Role != "user" {
			continue
		}
		stripped, files, saved := stripAttachmentBlocks(trimmed[i].Content)
		if len(files) == 0 {
			continue
		}
		trimmed[i] = LLMMessage{Role: trimmed[i].Role, Content: stripped}
		oldTokens := msgTokens[i]
		newTokens := e.tokenizer.CountTokens(stripped) + perMessageOverhead
		totalTokens += newTokens - oldTokens
		msgTokens[i] = newTokens

		log.C(ctx).Warnw("gateway trim: stripped history attachments",
			"msg_index", i, "files", files, "chars_saved", saved, "total_tokens_after", totalTokens)

		if totalTokens <= gatewayTokenCap {
			return trimmed, nil
		}
	}

	// Step 4: 整段丢最早的历史消息（成对：user + 紧跟的 assistant）
	for historyStart < historyEnd {
		droppedTokens := msgTokens[historyStart]
		droppedRole := trimmed[historyStart].Role
		droppedChars := len(trimmed[historyStart].Content)

		trimmed = append(trimmed[:historyStart], trimmed[historyStart+1:]...)
		msgTokens = append(msgTokens[:historyStart], msgTokens[historyStart+1:]...)
		totalTokens -= droppedTokens
		historyEnd--

		log.C(ctx).Warnw("gateway trim: dropped oldest history message",
			"role", droppedRole, "content_chars", droppedChars, "total_tokens_after", totalTokens)

		// 若紧跟的是对应的 assistant 回复，一并丢
		if historyStart < historyEnd && trimmed[historyStart].Role == "assistant" {
			droppedTokens = msgTokens[historyStart]
			droppedChars = len(trimmed[historyStart].Content)
			trimmed = append(trimmed[:historyStart], trimmed[historyStart+1:]...)
			msgTokens = append(msgTokens[:historyStart], msgTokens[historyStart+1:]...)
			totalTokens -= droppedTokens
			historyEnd--

			log.C(ctx).Warnw("gateway trim: dropped paired assistant message",
				"content_chars", droppedChars, "total_tokens_after", totalTokens)
		}

		if totalTokens <= gatewayTokenCap {
			return trimmed, nil
		}
	}

	// Step 5: 历史清空仍超 → 当前步骤自身超大，不触碰当前，返回友好错误
	log.C(ctx).Warnw("gateway trim: history exhausted but still over cap, current step too large",
		"final_estimated_tokens", totalTokens, "cap", gatewayTokenCap)
	return trimmed, ErrGatewayInputTooLong
}

// estimateExactTokens 仅供测试断言用：按 cl100k 精确 token 数估算消息总量。
// 生产路径用增量 totalTokens 维护，不走此函数。
func estimateExactTokens(tk *tokenizer.Tokenizer, messages []LLMMessage) int {
	total := 0
	const perMessageOverhead = 4
	for _, m := range messages {
		total += tk.CountTokens(m.Content)
		total += perMessageOverhead
	}
	total += 3
	return total
}

// stripAttachmentBlocks 把文本中所有 `=== filename.ext ===\n{content}` 替换为 `[附件已省略: filename.ext]`。
// 多附件之间插入 \n\n 分隔。依赖不变量：附件总在消息尾部按 \n\n 拼接（controller sop.go:717, 731-735）。
// 返回新文本、被剥离的文件名列表、节省的字符数。
func stripAttachmentBlocks(text string) (string, []string, int) {
	locs := attachmentMarkerRegex.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text, nil, 0
	}

	var b strings.Builder
	var files []string
	cursor := 0
	for i, loc := range locs {
		markerStart := loc[0]
		filename := strings.TrimSpace(text[loc[2]:loc[3]])
		files = append(files, filename)

		// 写入 cursor 到 marker 之前的文字（首个 marker 前是用户正文 + node prompt，后续 marker 前恒为空）
		b.WriteString(text[cursor:markerStart])
		b.WriteString(fmt.Sprintf("[附件已省略: %s]", filename))

		if i+1 < len(locs) {
			b.WriteString("\n\n")
			cursor = locs[i+1][0]
		} else {
			// 最后一个 block：假定附件延伸到消息末尾（依赖不变量）
			cursor = len(text)
		}
	}
	// 防御：写入最后一个 block 之后可能残存的尾部文字（invariant 不成立时的兜底）
	b.WriteString(text[cursor:])

	result := b.String()
	saved := len(text) - len(result)
	return result, files, saved
}

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

	finalMessages := messages
	var maxTokens int

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

	// 4. 计算剩余空间（仅作记录，不再限制输出）
	// 用户要求完全移除 max_tokens 限制，让模型自由生成直到上下文上限
	// 此时 maxTokens 返回 0，在 executor.go 中会因 omitempty 字段而不传给 API
	// 从而解除限制
	maxTokens = 0

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
