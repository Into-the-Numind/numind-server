// Package chatbot — ChatStream 流式对话核心逻辑
package chatbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// chatStreamMaxHistory 历史消息上下文窗口（最近 N 条消息）
const chatStreamMaxHistory = 20

// chatStreamMaxChunks 向量检索最大返回切片数
const chatStreamMaxChunks = 6

// ChatStream 执行流式对话：向量检索 → 组装 prompt → LLM 流式输出 → 持久化消息
func (b *chatbotBiz) ChatStream(ctx context.Context, userID uint, sessionID uint, message string, modelKey string, thinking bool, handler StreamHandler) error {
	// 1. 获取会话并验证所有权
	session, err := b.ds.ChatbotSession().GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrSessionNotFound
		}
		return fmt.Errorf("ChatStream: get session: %w", err)
	}
	if session.UserID != userID {
		return errno.ErrForbidden
	}

	// 1a. 运行时权限守卫 —— 即便持有 session，当前对 chatbot 的白名单也必须仍然命中。
	// 实现 PRD AS-5「撤销即时生效」：父账号撤权后，子账号对已有 session 再发消息直接 403。
	// 放在 session 所有权校验之后、获取 chatbot config / LLM 调用之前（child-run-permission §3.5，
	// S3 Gate review P1-B 修正：原 plan 引用的 `Chat` 方法不存在，真正入口是 ChatStream）。
	//
	// 性能说明：HasChatbotPermission 内部每条 message 多 2 次 DB 查询
	// （user.First 判 parent / 子账号 + 白名单 Count），父账号走 bypass 省一次 Count。
	// 当前无 Redis 缓存；若 chatbot QPS 升高可考虑在 store 层加短 TTL（10-60s）缓存，
	// 与「撤销即时生效」的要求做 trade-off（缓存 TTL 即撤权最大延迟）。
	ok, err := b.ds.Customers().HasChatbotPermission(ctx, userID, session.ChatbotID)
	if err != nil {
		return fmt.Errorf("ChatStream permission: %w", err)
	}
	if !ok {
		return errno.ErrChatbotRunDenied
	}

	// 2. 获取智能体配置并验证可访问性
	config, err := b.ds.ChatbotConfig().Get(ctx, session.ChatbotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrChatbotNotFound
		}
		return fmt.Errorf("ChatStream: get chatbot config: %w", err)
	}
	if config.Status != model.ChatbotStatusPublished && config.UserID != userID {
		return errno.ErrChatbotNotPublished
	}

	// 3. Langfuse: 创建 trace
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "chatbot-chat",
		langfuse.WithUserID(userID),
		langfuse.WithSessionID(fmt.Sprintf("chatbot-session-%d", sessionID)),
		langfuse.WithTraceInput(map[string]interface{}{
			"chatbot_id":   session.ChatbotID,
			"chatbot_name": config.Name,
			"session_id":   sessionID,
			"user_id":      userID,
			"message":      message,
		}),
		langfuse.WithTraceTags("chatbot", "chat"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// 4. 创建 context-assembly span
	assemblySpanID := langfuse.SpanID()
	langfuse.CreateSpan(traceID, assemblySpanID, "context-assembly",
		langfuse.WithSpanInput(map[string]interface{}{"message": message}),
	)

	// 5. 向量检索（如果有挂载知识库）
	var retrievedChunks []string
	vectorSpanID := langfuse.SpanID()
	langfuse.CreateSpan(traceID, vectorSpanID, "vector-retrieval",
		langfuse.WithSpanParent(assemblySpanID),
	)

	kbs, err := b.ds.ChatbotConfig().ListMountedKBs(ctx, session.ChatbotID)
	if err != nil {
		log.C(ctx).Warnw("ChatStream: list mounted KBs failed", "error", err)
	}

	if len(kbs) > 0 {
		var kbIDs []uint
		for _, kb := range kbs {
			kbIDs = append(kbIDs, kb.ID)
		}

		docIDs, docErr := b.ds.KnowledgeBase().ListDocumentIDsByKBs(ctx, kbIDs)
		if docErr != nil {
			log.C(ctx).Warnw("ChatStream: list document IDs failed", "error", docErr)
		}

		if len(docIDs) > 0 && b.vectorStore != nil && b.embedder != nil {
			filter := port.SearchFilter{
				UserID:      config.UserID, // 使用智能体所有者的 userID 做隔离
				DocumentIDs: docIDs,
			}
			chunks, searchErr := b.vectorStore.Search(ctx, message, filter, chatStreamMaxChunks)
			if searchErr != nil {
				log.C(ctx).Warnw("ChatStream: vector search failed", "error", searchErr)
				langfuse.EndSpan(traceID, vectorSpanID, langfuse.WithSpanError(searchErr.Error()))
			} else {
				for _, chunk := range chunks {
					retrievedChunks = append(retrievedChunks, chunk.Content)
				}
				langfuse.EndSpan(traceID, vectorSpanID, langfuse.WithSpanOutput(map[string]interface{}{
					"chunk_count": len(retrievedChunks),
				}))
			}
		} else {
			langfuse.EndSpan(traceID, vectorSpanID, langfuse.WithSpanOutput(map[string]interface{}{
				"chunk_count": 0,
				"reason":      "no docs or vector store unavailable",
			}))
		}
	} else {
		langfuse.EndSpan(traceID, vectorSpanID, langfuse.WithSpanOutput(map[string]interface{}{
			"chunk_count": 0,
			"reason":      "no KBs mounted",
		}))
	}

	// 6. 构建 prompt
	promptSpanID := langfuse.SpanID()
	langfuse.CreateSpan(traceID, promptSpanID, "prompt-construction",
		langfuse.WithSpanParent(assemblySpanID),
	)

	messages := b.buildChatMessages(ctx, config, session, message, retrievedChunks)

	langfuse.EndSpan(traceID, promptSpanID, langfuse.WithSpanOutput(map[string]interface{}{
		"message_count":   len(messages),
		"has_context":     len(retrievedChunks) > 0,
		"history_fetched": true,
	}))

	// 结束 context-assembly span
	langfuse.EndSpan(traceID, assemblySpanID)

	// 7. 注入计费上下文 + skip-legacy-billing 标记，通过 AI Gateway 调用流式 LLM
	ctx = billing.WithBilling(ctx, userID, "chatbot_chat")
	// 将 userID 注入 aiservice middleware context，使 Tracing/Billing 中间件能正确读取（避免 user_id=0）
	ctx = aismw.WithUserID(ctx, userID)
	// ParentObservationID 设为空字符串，使 Gateway 创建的 generation 直接挂在 trace 根节点下
	ctx = langfuse.WithTraceAndParent(ctx, traceID, "")
	// 注入 skip-legacy-billing：Gateway 已统一记账，防止旧 billing 路径双记
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	// 将 messages ([]map[string]interface{}) 转换为 []aiservice.ChatMessage
	aiMessages := make([]aiservice.ChatMessage, 0, len(messages))
	for _, m := range messages {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		aiMessages = append(aiMessages, aiservice.ChatMessage{
			Role:    aiservice.MessageRole(role),
			Content: aiservice.MessageContent{Text: content},
		})
	}

	var fullContent strings.Builder
	var thinkingContent strings.Builder

	// 调用 Gateway ChatStream（profile.ChatbotStream 任务配置由 Registry 解析模型）
	gatewayReq := aiservice.ChatRequest{
		Messages:      aiMessages,
		Temperature:   0.7,
		ModelOverride: modelKey, // pass user's model choice; empty = use task profile default
		Thinking:      thinking,
	}

	ch, llmErr := aiservice.ChatStream(ctx, profile.ChatbotStream, gatewayReq)
	if llmErr != nil {
		return fmt.Errorf("ChatStream: LLM call failed: %w", llmErr)
	}

	var gatewayUsage *billing.TokenUsage
	var modelName string
	var streamErr error
	for chunk := range ch {
		if chunk.Model != "" {
			modelName = chunk.Model
		}
		if chunk.ReasoningDelta != "" {
			thinkingContent.WriteString(chunk.ReasoningDelta)
			if handlerErr := handler("thinking", map[string]string{"content": chunk.ReasoningDelta}); handlerErr != nil {
				log.C(ctx).Warnw("ChatStream: handler error on thinking chunk", "error", handlerErr)
			}
		}
		if chunk.Delta != "" {
			fullContent.WriteString(chunk.Delta)
			if handlerErr := handler("token", map[string]string{"content": chunk.Delta}); handlerErr != nil {
				log.C(ctx).Warnw("ChatStream: handler error on token chunk", "error", handlerErr)
			}
		}
		if chunk.IsFinal {
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
			if chunk.Usage != nil {
				gatewayUsage = &billing.TokenUsage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
					ReasoningTokens:  chunk.Usage.ReasoningTokens,
					ModelName:        modelName,
				}
			}
		}
	}
	if streamErr != nil {
		// Log and return so the SSE handler can emit an error event to the
		// client instead of silently treating a truncated reply as success.
		log.C(ctx).Warnw("ChatStream: stream ended with error",
			"session_id", sessionID, "error", streamErr)
		return fmt.Errorf("ChatStream: stream error: %w", streamErr)
	}
	if gatewayUsage == nil {
		log.C(ctx).Warnw("ChatStream: stream ended without final usage chunk", "session_id", sessionID)
	}
	usage := gatewayUsage

	// 9. 持久化消息（用户消息 + 助手消息）
	maxSeq, seqErr := b.ds.ChatbotSession().GetMaxSeq(ctx, sessionID)
	if seqErr != nil {
		log.C(ctx).Warnw("ChatStream: get max seq failed", "error", seqErr)
	}

	userMsg := &model.ChatbotMessage{
		SessionID: sessionID,
		UserID:    userID,
		Role:      "user",
		Content:   message,
		TraceID:   traceID,
		Seq:       maxSeq + 1,
		CreatedAt: time.Now(),
	}
	if err := b.ds.ChatbotSession().CreateMessage(ctx, userMsg); err != nil {
		log.C(ctx).Errorw("ChatStream: save user message failed", "error", err)
	}

	var promptTokens, completionTokens int
	var assistantModelName string
	if usage != nil {
		promptTokens = usage.PromptTokens
		completionTokens = usage.CompletionTokens
		assistantModelName = usage.ModelName
	}

	assistantMsg := &model.ChatbotMessage{
		SessionID:        sessionID,
		UserID:           userID,
		Role:             "assistant",
		Content:          fullContent.String(),
		Thinking:         thinkingContent.String(),
		TraceID:          traceID,
		Seq:              maxSeq + 2,
		ModelName:        assistantModelName,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CreatedAt:        time.Now(),
	}
	if err := b.ds.ChatbotSession().CreateMessage(ctx, assistantMsg); err != nil {
		log.C(ctx).Errorw("ChatStream: save assistant message failed", "error", err)
	}

	// 递增会话消息计数（+2: user + assistant）
	_ = b.ds.ChatbotSession().IncrementMessageCount(ctx, sessionID)
	_ = b.ds.ChatbotSession().IncrementMessageCount(ctx, sessionID)

	// 10. 更新 trace output
	langfuse.CreateTrace(traceID, "chatbot-chat",
		langfuse.WithTraceOutput(map[string]interface{}{
			"response_length":   fullContent.Len(),
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
		}),
	)

	// 11. 发送完成事件
	doneData := map[string]interface{}{
		"trace_id": traceID,
	}
	if usage != nil {
		doneData["prompt_tokens"] = usage.PromptTokens
		doneData["completion_tokens"] = usage.CompletionTokens
	}
	return handler("done", doneData)
}

// buildChatMessages 组装 LLM 消息数组：system + history + user
func (b *chatbotBiz) buildChatMessages(ctx context.Context, config *model.ChatbotConfig, session *model.ChatbotSession, userMessage string, retrievedChunks []string) []map[string]interface{} {
	var messages []map[string]interface{}

	// 1. system prompt
	systemPrompt := config.SystemPrompt
	if len(retrievedChunks) > 0 {
		systemPrompt += "\n\n参考资料：\n" + strings.Join(retrievedChunks, "\n\n")
	}
	messages = append(messages, map[string]interface{}{
		"role":    "system",
		"content": systemPrompt,
	})

	// 2. 历史消息（最近 N 条，按 seq 升序）
	// 使用 offset 技巧：先获取总数，然后取最后 N 条
	historyMsgs, total, err := b.ds.ChatbotSession().ListMessages(ctx, session.ID, 0, chatStreamMaxHistory)
	if err != nil {
		log.C(ctx).Warnw("ChatStream: fetch history failed", "error", err)
	} else if total > int64(chatStreamMaxHistory) {
		// 如果消息数超过窗口大小，取最后 N 条
		offset := int(total) - chatStreamMaxHistory
		historyMsgs, _, err = b.ds.ChatbotSession().ListMessages(ctx, session.ID, offset, chatStreamMaxHistory)
		if err != nil {
			log.C(ctx).Warnw("ChatStream: fetch recent history failed", "error", err)
			historyMsgs = nil
		}
	}

	for _, msg := range historyMsgs {
		messages = append(messages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// 3. 当前用户消息
	messages = append(messages, map[string]interface{}{
		"role":    "user",
		"content": userMessage,
	})

	return messages
}
