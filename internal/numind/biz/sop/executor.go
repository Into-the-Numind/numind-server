package sop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/tokenizer"

	"github.com/spf13/viper"
)

// SopExecutor SOP执行器
type SopExecutor struct {
	ds        store.IStore
	tokenizer *tokenizer.Tokenizer
}

// NewSopExecutor 创建SOP执行器。
// 历史：曾接受 *llmrouter.Router 参数（老版 LLM 路由），ai-service-manager 上线后
// 所有 LLM 调用改走 internal/pkg/aiservice Gateway，参数已移除。
func NewSopExecutor(ds store.IStore) *SopExecutor {
	tk, err := tokenizer.NewTokenizer()
	if err != nil {
		// Log error but don't fail startup, just degrade gracefully (we can add checks later)
		// For now, assume it works or we'll panic/error on first use if nil.
		// Better to just log.
		log.Errorw("failed to initialize tokenizer", "error", err)
	}
	return &SopExecutor{
		ds:        ds,
		tokenizer: tk,
	}
}

// LLMRequest 大模型请求结构
type LLMRequest struct {
	Model         string         `json:"model"`
	Messages      []LLMMessage   `json:"messages"`
	Stream        bool           `json:"stream"`
	MaxTokens     int            `json:"max_tokens,omitempty"` // Add MaxTokens
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// StreamOptions 流式请求选项
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// LLMMessage 大模型消息结构
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMResponse 大模型响应结构
type LLMResponse struct {
	Choices []struct {
		Message LLMMessage `json:"message"`
	} `json:"choices"`
	Usage *TokenUsage `json:"usage"`
}

// StreamHandler 流式处理函数类型
// event: "thinking" | "message" | "done"
type StreamHandler func(event string, chunk string) error

// TokenUsage Token使用统计信息（类型别名，实际定义在 billing 包中）
type TokenUsage = billing.TokenUsage

// applyDefaultLLMConfig 当节点未配置 LLM 信息时，使用系统默认配置（volc.*）
func applyDefaultLLMConfig(node *model.SopNode) {
	if node.APIKey == "" {
		node.APIKey = viper.GetString("volc.api_key")
	}
	if node.BaseURL == "" {
		baseURL := viper.GetString("volc.base_url")
		if baseURL != "" {
			node.BaseURL = strings.TrimSuffix(baseURL, "/") + "/chat/completions"
		}
	}
	if node.ModelName == "" {
		// TODO(Task 15a): 此处仍从旧 volc.model 读取作为兜底；
		// Gateway 已接管模型选择（via Task Profile），此路径预期不再触发。
		node.ModelName = viper.GetString("volc.model")
		if node.ModelName == "" {
			node.ModelName = "deepseek-v3-250324"
		}
	}
}

// ExecuteNodeStream 流式执行单个节点（公开方法）
// 优先通过 AI Gateway 路由执行（modelKey != ""）；否则保留原有直连逻辑。
func (e *SopExecutor) ExecuteNodeStream(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, modelKey string, thinking bool, handler StreamHandler) (string, *TokenUsage, error) {
	// Gateway 路径：用户选择了特定模型
	if modelKey != "" {
		return e.executeViaGateway(ctx, node, input, history, modelKey, thinking, handler)
	}

	// 节点未配置 LLM 信息时，使用系统默认配置
	applyDefaultLLMConfig(node)

	// 检查API密钥是否配置
	if node.APIKey == "" {
		log.C(ctx).Errorw("Node API key is empty", "node_id", node.ID, "node_name", node.Name)
		return "", nil, fmt.Errorf("node %s (ID: %d) API key is not configured, please update the node with a valid API key", node.Name, node.ID)
	}

	// 脱敏日志：只显示前4位和后4位
	maskedKey := node.APIKey
	if len(node.APIKey) > 8 {
		maskedKey = node.APIKey[:4] + "****" + node.APIKey[len(node.APIKey)-4:]
	}
	log.C(ctx).Infow("Executing node with LLM API (stream)",
		"node_id", node.ID,
		"node_name", node.Name,
		"base_url", node.BaseURL,
		"model", node.ModelName,
		"api_key_masked", maskedKey)

	// 构建请求消息
	messages := make([]LLMMessage, len(history))
	copy(messages, history)

	// 添加当前输入
	// 如果节点有prompt模板，使用模板；否则直接使用输入
	var userMessage string
	if node.Prompt != "" {
		userMessage = fmt.Sprintf("%s\n\n%s", node.Prompt, input)
	} else {
		userMessage = input
	}

	messages = append(messages, LLMMessage{
		Role:    "user",
		Content: userMessage,
	})

	// 准备上下文（包含Token估算、裁剪等逻辑）
	messages, estimatedTokens, maxTokens, err := e.prepareContext(ctx, node, messages)
	if err != nil {
		return "", nil, fmt.Errorf("failed to prepare context: %w", err)
	}

	log.C(ctx).Infow("Stream Context prepared",
		"node_id", node.ID,
		"estimated_tokens", estimatedTokens,
		"max_tokens_setting", maxTokens,
		"messages_count", len(messages))

	// 构建请求（启用流式）
	reqBody := LLMRequest{
		Model:     node.ModelName,
		Messages:  messages,
		Stream:    true,
		MaxTokens: maxTokens,
		StreamOptions: &StreamOptions{
			IncludeUsage: true,
		},
	}

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 记录请求信息用于调试
	log.C(ctx).Infow("LLM API request",
		"node_id", node.ID,
		"node_name", node.Name,
		"url", node.BaseURL,
		"model", node.ModelName,
		"request_body", string(reqData),
		"messages_count", len(messages))

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", node.BaseURL, bytes.NewBuffer(reqData))
	if err != nil {
		return "", nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.APIKey)
	req.Header.Set("User-Agent", "numind-server/1.0")

	// 对于流式响应，不能使用Client.Timeout（会限制整个响应读取时间）
	// 而是使用Transport的ResponseHeaderTimeout来控制响应头超时
	// 流式响应的读取时间由context控制
	transport := &http.Transport{
		ResponseHeaderTimeout: time.Duration(node.TimeoutSeconds) * time.Second, // 响应头超时
		IdleConnTimeout:       90 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		// 不设置Timeout，让流式响应可以持续读取
		// Timeout会限制整个请求-响应周期，包括读取响应体的时间
	}

	// 发送请求
	log.C(ctx).Infow("Sending LLM API request",
		"node_id", node.ID,
		"url", node.BaseURL)

	resp, err := client.Do(req)
	if err != nil {
		log.C(ctx).Errorw("LLM API request failed",
			"node_id", node.ID,
			"url", node.BaseURL,
			"error", err.Error())
		return "", nil, fmt.Errorf("failed to call LLM API: %w", err)
	}
	defer resp.Body.Close()

	log.C(ctx).Infow("LLM API response received",
		"node_id", node.ID,
		"status_code", resp.StatusCode,
		"headers", resp.Header)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.C(ctx).Errorw("LLM API error response",
			"node_id", node.ID,
			"status_code", resp.StatusCode,
			"url", node.BaseURL,
			"model", node.ModelName,
			"response_body", string(body))
		return "", nil, fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(body))
	}

	// 流式读取响应
	var fullOutput strings.Builder
	var usage *TokenUsage
	reader := bufio.NewReader(resp.Body)
	sawDone := false

	for {
		// 检查context是否被取消
		select {
		case <-ctx.Done():
			log.C(ctx).Warnw("Context cancelled during stream", "node_id", node.ID)
			// 即使context取消，也返回已累积的输出
			if fullOutput.Len() > 0 {
				return fullOutput.String(), usage, nil
			}
			return "", nil, fmt.Errorf("stream cancelled: %w", ctx.Err())
		default:
		}

		// 直接阻塞读取，不再使用不安全的每行超时逻辑
		lineBytes, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if readErr == io.EOF {
				if len(lineBytes) == 0 {
					break
				}
			}
			log.C(ctx).Errorw("Read error during stream", "node_id", node.ID, "error", readErr)
			// 即使读取出错，也返回已累积的输出（如果有）
			if fullOutput.Len() > 0 {
				return fullOutput.String(), usage, nil
			}
			return "", nil, fmt.Errorf("read error: %w", readErr)
		}

		// 转换为字符串并去除换行符
		line := strings.TrimRight(string(lineBytes), "\r\n")

		// 过滤 SSE 注释行（以 : 开头）和空行，防止心跳等注释内容混入输出
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				sawDone = true
				log.C(ctx).Infow("LLM stream completed",
					"node_id", node.ID,
					"total_output_length", fullOutput.Len())
				break
			}

			// 记录每个chunk的原始数据用于调试
			log.C(ctx).Debugw("LLM stream chunk received",
				"node_id", node.ID,
				"raw_data", data)

			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				if rawUsage, ok := m["usage"]; ok && rawUsage != nil {
					usageBytes, _ := json.Marshal(rawUsage)
					var tempUsage TokenUsage
					if err := json.Unmarshal(usageBytes, &tempUsage); err == nil && (tempUsage.TotalTokens > 0 || tempUsage.PromptTokens > 0 || tempUsage.CompletionTokens > 0) {
						usage = &tempUsage
						log.C(ctx).Infow("Token usage received in plain stream",
							"node_id", node.ID,
							"prompt_tokens", usage.PromptTokens,
							"completion_tokens", usage.CompletionTokens,
							"total_tokens", usage.TotalTokens,
							"raw_usage", rawUsage)
					} else if err != nil {
						log.C(ctx).Warnw("Failed to unmarshal usage in plain stream", "error", err, "raw_usage", rawUsage)
					}
				}

				if choices, ok := m["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							// 记录delta的完整内容用于调试
							deltaJSON, _ := json.Marshal(delta)
							log.C(ctx).Debugw("LLM stream delta",
								"node_id", node.ID,
								"delta", string(deltaJSON))

							if content, ok := delta["content"].(string); ok && content != "" {
								// 累积完整输出
								fullOutput.WriteString(content)
								// 记录内容预览（仅前100字符，避免日志过大）
								contentPreview := content
								if len(contentPreview) > 100 {
									contentPreview = contentPreview[:100] + "..."
								}
								log.C(ctx).Debugw("LLM stream content chunk",
									"node_id", node.ID,
									"content_length", len(content),
									"content_preview", contentPreview)
								// 调用handler发送chunk
								if err := handler("message", content); err != nil {
									log.C(ctx).Warnw("Stream handler error, but continuing to read full response", "error", err)
									// 不立即返回错误，继续处理完整响应
									// 如果客户端断开连接，handler会返回错误，但我们继续读取完整响应以便保存
								}
							}
						}
					}
				}
			} else {
				// JSON解析失败，记录原始数据
				log.C(ctx).Warnw("Failed to parse LLM stream chunk",
					"node_id", node.ID,
					"raw_data", data,
					"error", err.Error())
			}
		}

		if readErr == io.EOF {
			break
		}
	}

	output := fullOutput.String()

	if !sawDone {
		log.C(ctx).Warnw("LLM stream ended without [DONE]",
			"node_id", node.ID,
			"node_name", node.Name,
			"model", node.ModelName,
			"output_length", len(output))
		return output, usage, fmt.Errorf("LLM stream ended unexpectedly: %w", io.ErrUnexpectedEOF)
	}

	// 记录最终响应结果
	log.C(ctx).Infow("LLM stream response completed",
		"node_id", node.ID,
		"node_name", node.Name,
		"model", node.ModelName,
		"output_length", len(output),
		"output", output)

	if output == "" {
		log.C(ctx).Errorw("LLM returned empty response",
			"node_id", node.ID)
		return "", nil, fmt.Errorf("LLM returned empty response")
	}

	if usage != nil {
		usage.Normalize()
		usage.EstimatedPromptTokens = estimatedTokens
	} else {
		// Create usage with estimate if missing
		usage = &TokenUsage{
			EstimatedPromptTokens: estimatedTokens,
		}
	}
	return output, usage, nil
}

// executeViaGateway 通过 AI Gateway 执行节点流式调用（取代 executeViaRouter）
//
// Task Profile 选择规则：
//   - SopVision：当任意消息含 image_url 类型的 MessagePart 时（视觉输入）
//   - SopText：其余所有情况（纯文本）
//
// 调用前注入 WithSkipLegacyBilling，防止 LLMRouter 旧路径与 Gateway 双记账。
// modelKey 参数当前未接通 Gateway ModelOverride（billing-baseline.md BLOCKER 3）；预留供未来扩展。
func (e *SopExecutor) executeViaGateway(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, modelKey string, thinking bool, handler StreamHandler) (string, *TokenUsage, error) {

	log.C(ctx).Infow("ExecuteNodeStream via AI Gateway",
		"node_id", node.ID,
		"node_name", node.Name)

	// 0. 将 billing context 中的 userID 注入 aiservice middleware，确保 Tracing/Billing 中间件能正确读取
	if bc := billing.FromContext(ctx); bc != nil {
		ctx = aismw.WithUserID(ctx, bc.UserID)
	}

	// 1. 构建 aiservice.ChatMessage 列表：system prompt → history → user input
	var aiMessages []aiservice.ChatMessage
	if node.Prompt != "" {
		aiMessages = append(aiMessages, aiservice.ChatMessage{
			Role:    aiservice.MessageRoleSystem,
			Content: aiservice.MessageContent{Text: node.Prompt},
		})
	}
	for _, msg := range history {
		role := aiservice.MessageRole(msg.Role)
		aiMessages = append(aiMessages, aiservice.ChatMessage{
			Role:    role,
			Content: aiservice.MessageContent{Text: msg.Content},
		})
	}
	if input != "" {
		aiMessages = append(aiMessages, aiservice.ChatMessage{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Text: input},
		})
	}

	// 2. 选择 Task Profile：任意消息含 image_url 部分 → SopVision，否则 SopText
	taskID := profile.SopText
	for _, m := range aiMessages {
		for _, part := range m.Content.Parts {
			if part.Type == aiservice.MessagePartTypeImageURL {
				taskID = profile.SopVision
				break
			}
		}
		if taskID == profile.SopVision {
			break
		}
	}

	// 3. 注入 skip-legacy-billing 标记，防止 Gateway 与旧 billing 路径双记账
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	// 4. 调用 Gateway ChatStream
	req := aiservice.ChatRequest{
		Messages:      aiMessages,
		Temperature:   0.7,
		ModelOverride: modelKey, // pass user's model choice; empty = use task profile default
		Thinking:      thinking,
	}
	ch, err := aiservice.ChatStream(ctx, taskID, req)
	if err != nil {
		return "", nil, fmt.Errorf("executeViaGateway: ChatStream: %w", err)
	}

	// 5. 消费 channel，将 ChatChunk 转换为 StreamHandler 回调
	var fullContent strings.Builder
	var finalUsage *TokenUsage
	var modelName string
	var providerName string
	var streamErr error
	for chunk := range ch {
		if chunk.Model != "" {
			modelName = chunk.Model
		}
		if chunk.Provider != "" {
			providerName = chunk.Provider
		}
		if chunk.ReasoningDelta != "" {
			if handlerErr := handler("thinking", chunk.ReasoningDelta); handlerErr != nil {
				log.C(ctx).Warnw("executeViaGateway: stream handler error on thinking chunk",
					"node_id", node.ID, "error", handlerErr)
			}
		}
		if chunk.Delta != "" {
			fullContent.WriteString(chunk.Delta)
			if handlerErr := handler("message", chunk.Delta); handlerErr != nil {
				log.C(ctx).Warnw("executeViaGateway: stream handler error on message chunk",
					"node_id", node.ID, "error", handlerErr)
			}
		}
		if chunk.IsFinal {
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
			if chunk.Usage != nil {
				finalUsage = &billing.TokenUsage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
					ReasoningTokens:  chunk.Usage.ReasoningTokens,
					ModelName:        modelName,
					Provider:         providerName,
				}
			}
		}
	}

	// Mid-stream failure: propagate so the caller can fail the node rather
	// than accepting a truncated output as "success".
	if streamErr != nil {
		return fullContent.String(), finalUsage, fmt.Errorf("executeViaGateway: stream error: %w", streamErr)
	}
	if fullContent.Len() == 0 && finalUsage == nil {
		return "", nil, fmt.Errorf("executeViaGateway: empty response from Gateway (no chunks received)")
	}

	return fullContent.String(), finalUsage, nil
}

// ExecuteNodeStreamWithThinking 流式执行单个节点，并分离思考内容和实际内容
// deepThinking: 是否开启深度思考模式（阿里百炼 enable_thinking）
// 返回: output, thinking, usage, error
func (e *SopExecutor) ExecuteNodeStreamWithThinking(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, handler StreamHandler, isLastNode bool, deepThinking bool, conversationID string) (string, string, *TokenUsage, error) {
	// 节点未配置 LLM 信息时，使用系统默认配置
	applyDefaultLLMConfig(node)

	// 检查API密钥是否配置
	if node.APIKey == "" {
		log.C(ctx).Errorw("Node API key is empty", "node_id", node.ID, "node_name", node.Name)
		return "", "", nil, fmt.Errorf("node %s (ID: %d) API key is not configured, please update the node with a valid API key", node.Name, node.ID)
	}

	// 脱敏日志：只显示前4位和后4位
	maskedKey := node.APIKey
	if len(node.APIKey) > 8 {
		maskedKey = node.APIKey[:4] + "****" + node.APIKey[len(node.APIKey)-4:]
	}
	log.C(ctx).Infow("Executing node with LLM API (stream with thinking)",
		"node_id", node.ID,
		"node_name", node.Name,
		"base_url", node.BaseURL,
		"model", node.ModelName,
		"api_key_masked", maskedKey,
		"is_last_node", isLastNode)

	// 构建请求消息
	messages := make([]LLMMessage, len(history))
	copy(messages, history)

	// 添加当前输入
	// 如果 input 为空字符串，说明用户问题已经在 history 中了（聊天场景），不需要再次添加
	if input != "" {
		// 所有节点统一使用相同的逻辑：如果有 prompt 就拼接，否则直接使用 input
		var userMessage string
		if node.Prompt != "" {
			// 如果有 prompt 就拼接：prompt + "\n\n" + input
			userMessage = fmt.Sprintf("%s\n\n%s", node.Prompt, input)
			log.C(ctx).Infow("Using prompt + input", "node_id", node.ID, "node_name", node.Name, "is_last_node", isLastNode, "prompt_length", len(node.Prompt), "input_length", len(input))
		} else {
			// 如果没有 prompt，直接使用 input
			userMessage = input
			log.C(ctx).Debugw("No prompt, using input directly", "node_id", node.ID, "node_name", node.Name, "is_last_node", isLastNode)
		}

		messages = append(messages, LLMMessage{
			Role:    "user",
			Content: userMessage,
		})
	} else {
		// input 为空，说明是聊天场景，用户问题已经在 history 中了
		log.C(ctx).Debugw("Input is empty, using history directly (chat scenario)", "node_id", node.ID, "node_name", node.Name, "is_last_node", isLastNode)
	}

	// 准备上下文（包含Token估算、裁剪等逻辑）
	messages, estimatedTokens, maxTokens, err := e.prepareContext(ctx, node, messages)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to prepare context: %w", err)
	}

	log.C(ctx).Infow("Deep Thinking Context prepared",
		"node_id", node.ID,
		"estimated_tokens", estimatedTokens,
		"max_tokens_setting", maxTokens,
		"messages_count", len(messages))

	// 调用流式接口，分别积累思考与答案
	var thinkingBuf, answerBuf strings.Builder
	var usage *TokenUsage

	isAli := strings.Contains(node.BaseURL, "aliyuncs.com") || strings.Contains(node.ModelName, "qwen")

	if isAli {
		log.C(ctx).Infow("Routing to Ali Deep Thinking API", "node_id", node.ID)
		usage, err = e.callAliDeepThinkingStream(ctx, node, messages, maxTokens, deepThinking, conversationID, func(event string, chunk string) error {
			switch event {
			case "thinking":
				thinkingBuf.WriteString(chunk)
				return handler("thinking", chunk)
			case "message":
				answerBuf.WriteString(chunk)
				return handler("message", chunk)
			case "done":
				return handler("done", "")
			default:
				return nil
			}
		})
	} else {
		log.C(ctx).Infow("Routing to Volcengine Deep Thinking API", "node_id", node.ID)
		usage, err = e.callVolcDeepThinkingStream(ctx, node, messages, maxTokens, deepThinking, conversationID, func(event string, chunk string) error {
			switch event {
			case "thinking":
				thinkingBuf.WriteString(chunk)
				return handler("thinking", chunk)
			case "message":
				answerBuf.WriteString(chunk)
				return handler("message", chunk)
			case "done":
				return handler("done", "")
			default:
				return nil
			}
		})
	}
	if err != nil {
		log.C(ctx).Errorw("LLM stream execution interrupted with error", "node_id", node.ID, "error", err, "partial_output_len", answerBuf.Len())
		// 即使出错，也返回已生成的内容和统计数据（Issue 3 & 4）
		return answerBuf.String(), thinkingBuf.String(), usage, err
	}

	output := answerBuf.String()
	thinking := thinkingBuf.String()

	if output == "" && thinking == "" {
		return "", "", nil, fmt.Errorf("LLM returned empty response")
	}

	// 兼容旧模型：若未返回reasoning，且开启了深度思考，尝试从输出中拆分
	if deepThinking && thinking == "" {
		thinking = extractThinkingContent(output)
		output = removeThinkingContent(output)
	}

	if usage != nil {
		usage.Normalize()
		usage.EstimatedPromptTokens = estimatedTokens
	} else {
		// 如果 usage 为空（例如出错中断），至少保留预估数据
		usage = &TokenUsage{
			EstimatedPromptTokens: estimatedTokens,
		}
	}

	// Langfuse generation 追踪
	if tc := langfuse.FromContext(ctx); tc != nil {
		genID := langfuse.SpanID()
		opts := []langfuse.GenOption{
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("sop-node-" + node.Name),
			langfuse.WithGenModel(node.ModelName),
			langfuse.WithGenOutput(output),
		}
		if usage != nil {
			opts = append(opts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
		}
		langfuse.CreateGeneration(tc.TraceID, genID, opts...)
		langfuse.EndGeneration(tc.TraceID, genID)
	}

	return output, thinking, usage, nil
}

// extractThinkingContent 从完整输出中提取思考内容（兼容旧模型不发 reasoning_content 的情况）
func extractThinkingContent(output string) string {
	// 查找思考部分的开始标记
	// 顺序重要："已思考" 必须在 "思考" 前面，因为 "思考" 是 "已思考" 的子串，
	// 放后面会导致匹配位置提前、多截取内容
	thinkingMarkers := []string{
		"已思考",
		"**已思考**",
		"思考",
		"**思考**",
	}

	var thinkingStart int = -1
	for _, marker := range thinkingMarkers {
		if idx := strings.Index(output, marker); idx != -1 {
			thinkingStart = idx
			break
		}
	}

	if thinkingStart == -1 {
		return ""
	}

	// 查找思考部分的结束标记
	endMarkers := []string{
		"请开始",
		"开始创作",
		"创作要求",
		"\n\n[完整",
	}

	var thinkingEnd int = len(output)
	for _, marker := range endMarkers {
		if idx := strings.Index(output[thinkingStart:], marker); idx != -1 {
			thinkingEnd = thinkingStart + idx
			break
		}
	}

	if thinkingEnd > thinkingStart {
		return output[thinkingStart:thinkingEnd]
	}

	return ""
}

// removeThinkingContent 从完整输出中移除思考内容，返回实际内容
func removeThinkingContent(output string) string {
	thinking := extractThinkingContent(output)
	if thinking == "" {
		return output
	}

	// 移除思考部分
	result := strings.Replace(output, thinking, "", 1)
	return strings.TrimSpace(result)
}

// callAliDeepThinkingStream 调用阿里百炼深度思考流式接口
// 使用兼容模式：enable_thinking=true，reasoning_content 为思考链条，content 为最终答案
// 返回 usage 信息用于统计 token 消耗
func (e *SopExecutor) callAliDeepThinkingStream(ctx context.Context, node *model.SopNode, messages []LLMMessage, maxTokens int, deepThinking bool, conversationID string, handler StreamHandler) (*TokenUsage, error) {
	var usage *TokenUsage // 用于存储 token 使用统计
	sawDone := false
	url := node.BaseURL
	if url == "" {
		url = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	}

	// 构建 payload（openai 兼容）
	payload := map[string]interface{}{
		"model":      node.ModelName,
		"messages":   messages,
		"stream":     true,
		"max_tokens": maxTokens,
		"extra_body": map[string]interface{}{
			"enable_thinking": deepThinking,
		},
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}
	// 为每个 run 透传独立的 conversation_id，避免百炼会话串联
	if conversationID != "" {
		payload["conversation_id"] = conversationID
	}
	// 温度按 deepThinking 微调
	if deepThinking {
		payload["temperature"] = 0.8
	} else {
		payload["temperature"] = 0.7
	}

	reqData, _ := json.Marshal(payload)

	// 添加详细的请求日志（用于调试思考模式）
	log.C(ctx).Infow("Sending request to Ali Deep Thinking API",
		"node_id", node.ID,
		"node_name", node.Name,
		"url", url,
		"model", node.ModelName,
		"request_body", string(reqData), // 关键：查看实际发送的完整请求体
		"enable_thinking_in_extra_body", payload["extra_body"])

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(reqData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.APIKey)
	req.Header.Set("User-Agent", "numind-server/1.0")
	if conversationID != "" {
		req.Header.Set("X-DashScope-Conversation-Id", conversationID)
	}

	client := &http.Client{
		// Timeout 设置为 0，因为我们已经通过 NewRequestWithContext 使用了 context 来控制全局超时
		// 这样连接会随 context 取消而立即物理切断（解决“影子账单”风险 2）
		Timeout: 0,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call ali deep thinking stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ali stream status %d: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)

	var (
		thinkingBuf        strings.Builder
		messageBuf         strings.Builder
		thinkingChunks     int
		messageChunks      int
		accumulatedContent strings.Builder         // 累积所有内容用于判断思考阶段
		hasThinkingMarker  bool            = false // 是否已检测到思考开始标记
		thinkingEnded      bool            = false // 思考阶段是否已结束
	)

	// 思考开始标记
	thinkingStartMarkers := []string{
		"已思考",
		"**已思考**",
		"思考",
		"**思考**",
	}

	// 思考结束标记
	thinkingEndMarkers := []string{
		"请开始",
		"开始创作",
		"创作要求",
		"\n\n[完整",
	}

	for {
		// 检查context是否被取消
		select {
		case <-ctx.Done():
			return usage, ctx.Err()
		default:
		}

		// 直接阻塞读取，不再使用不安全的每行超时逻辑
		lineBytes, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if readErr == io.EOF {
				if len(lineBytes) == 0 {
					break
				}
			}
			if ctx.Err() != nil {
				log.C(ctx).Warnw("LLM connection closed due to context cancellation", "node_id", node.ID, "error", readErr)
				return usage, ctx.Err()
			}
			log.C(ctx).Errorw("Read error in ali deep thinking stream", "node_id", node.ID, "error", readErr)
			return usage, fmt.Errorf("read error: %w", readErr)
		}

		// 转换为字符串并去除换行符
		line := strings.TrimRight(string(lineBytes), "\r\n")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		if raw == "[DONE]" {
			sawDone = true
			_ = handler("done", "")
			break
		}

		var m map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			log.C(ctx).Warnw("Failed to parse ali stream chunk", "error", err, "raw", raw)
			continue
		}

		// 记录原始 chunk 数据，帮助排查 token 统计问题
		log.C(ctx).Debugw("Ali stream chunk received", "node_id", node.ID, "raw", raw)

		// 检查是否有 usage 信息（通常在最后一个数据块中，且 choices 可能为空）
		if rawUsage, ok := m["usage"]; ok && rawUsage != nil {
			usageBytes, _ := json.Marshal(rawUsage)
			var tempUsage TokenUsage
			if err := json.Unmarshal(usageBytes, &tempUsage); err == nil && (tempUsage.TotalTokens > 0 || tempUsage.PromptTokens > 0 || tempUsage.CompletionTokens > 0) {
				usage = &tempUsage
				log.C(ctx).Infow("Token usage received from Ali",
					"node_id", node.ID,
					"prompt_tokens", usage.PromptTokens,
					"completion_tokens", usage.CompletionTokens,
					"total_tokens", usage.TotalTokens,
					"reasoning_tokens", usage.ReasoningTokens,
					"raw_usage", rawUsage)
			} else if err != nil {
				log.C(ctx).Warnw("Failed to unmarshal usage from Ali", "error", err, "raw_usage", rawUsage)
			}
		}

		choices, ok := m["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]interface{})
		delta, _ := choice["delta"].(map[string]interface{})

		// 添加调试日志：查看原始 delta 数据（用于诊断思考模式）
		if len(delta) > 0 {
			deltaJSON, _ := json.Marshal(delta)
			log.C(ctx).Debugw("Received delta from Ali API",
				"node_id", node.ID,
				"delta_keys", getMapKeys(delta),
				"delta_preview", string(deltaJSON),
				"has_reasoning_content", hasKey(delta, "reasoning_content"))
		}

		// 优先检查 reasoning_content（如果模型支持单独的 reasoning_content 字段）
		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			thinkingChunks++
			thinkingBuf.WriteString(rc)
			log.C(ctx).Infow("LLM thinking chunk (from reasoning_content)",
				"node_id", node.ID,
				"node_name", node.Name,
				"chunk_index", thinkingChunks,
				"chunk_length", len(rc),
				"chunk_preview", getStringPreview(rc, 50))
			if err := handler("thinking", rc); err != nil {
				return nil, err
			}
		}

		// 处理 content 字段（思考内容可能混在其中）
		if content, ok := delta["content"].(string); ok && content != "" {
			// 保存累积内容长度（用于计算相对位置）
			prevLen := accumulatedContent.Len()

			// 累积内容用于判断思考阶段
			accumulatedContent.WriteString(content)
			currentText := accumulatedContent.String()

			// 检查是否包含思考开始标记
			if deepThinking && !hasThinkingMarker {
				for _, marker := range thinkingStartMarkers {
					if strings.Contains(currentText, marker) {
						hasThinkingMarker = true
						log.C(ctx).Infow("Thinking phase detected",
							"node_id", node.ID,
							"node_name", node.Name,
							"marker", marker)
						break
					}
				}
			}

			// 如果已检测到思考标记，检查是否遇到结束标记
			if deepThinking && hasThinkingMarker && !thinkingEnded {
				// 查找结束标记的位置
				endMarkerIndex := -1
				var endMarker string
				for _, marker := range thinkingEndMarkers {
					if idx := strings.Index(currentText, marker); idx != -1 {
						endMarkerIndex = idx
						endMarker = marker
						break
					}
				}

				if endMarkerIndex != -1 {
					// 遇到结束标记，需要拆分当前 chunk
					// relativePos: 结束标记起始位置在当前 chunk 中的偏移
					relativePos := endMarkerIndex - prevLen
					// splitEnd: 结束标记末尾在当前 chunk 中的偏移（message 从此处开始）
					splitEnd := relativePos + len(endMarker)

					// 标记跨越 chunk 边界时需要 clamp，防止越界切片
					if relativePos < 0 {
						relativePos = 0
					}
					if splitEnd > len(content) {
						splitEnd = len(content)
					}

					if relativePos < len(content) {
						// 结束标记（至少部分）在当前 chunk 中，拆分
						thinkingPart := content[:splitEnd]
						messagePart := content[splitEnd:]

						// 发送思考部分
						if thinkingPart != "" {
							thinkingChunks++
							thinkingBuf.WriteString(thinkingPart)
							log.C(ctx).Infow("LLM thinking chunk (split at end marker)",
								"node_id", node.ID,
								"node_name", node.Name,
								"chunk_index", thinkingChunks,
								"chunk_length", len(thinkingPart),
								"end_marker", endMarker)
							if err := handler("thinking", thinkingPart); err != nil {
								return nil, err
							}
						}

						// 切换到 message 阶段
						thinkingEnded = true

						// 发送正式内容部分
						if messagePart != "" {
							messageChunks++
							messageBuf.WriteString(messagePart)
							log.C(ctx).Infow("LLM message chunk (after thinking ended)",
								"node_id", node.ID,
								"node_name", node.Name,
								"chunk_index", messageChunks,
								"chunk_length", len(messagePart))
							if err := handler("message", messagePart); err != nil {
								return nil, err
							}
						}
					} else {
						// 结束标记完全在之前的 chunk 中（当前 chunk 全部属于 message）
						thinkingEnded = true
						messageChunks++
						messageBuf.WriteString(content)
						log.C(ctx).Infow("End marker in previous chunk, current chunk is message",
							"node_id", node.ID,
							"node_name", node.Name,
							"chunk_index", messageChunks,
							"chunk_length", len(content))
						if err := handler("message", content); err != nil {
							return nil, err
						}
					}
				} else {
					// 仍在思考阶段，作为思考内容发送
					thinkingChunks++
					thinkingBuf.WriteString(content)
					log.C(ctx).Infow("LLM thinking chunk (from content)",
						"node_id", node.ID,
						"node_name", node.Name,
						"chunk_index", thinkingChunks,
						"chunk_length", len(content),
						"chunk_preview", getStringPreview(content, 50))
					if err := handler("thinking", content); err != nil {
						return nil, err
					}
				}
			} else if thinkingEnded || !hasThinkingMarker {
				// 正式内容阶段，或没有思考标记（向后兼容）
				messageChunks++
				messageBuf.WriteString(content)
				log.C(ctx).Infow("LLM message chunk",
					"node_id", node.ID,
					"node_name", node.Name,
					"chunk_index", messageChunks,
					"chunk_length", len(content),
					"has_thinking_marker", hasThinkingMarker,
					"thinking_ended", thinkingEnded)
				if err := handler("message", content); err != nil {
					return nil, err
				}
			}
		}

		if readErr == io.EOF {
			break
		}
	}

	if !sawDone {
		log.C(ctx).Warnw("Ali deep thinking stream ended without [DONE]",
			"node_id", node.ID,
			"node_name", node.Name,
			"model", node.ModelName,
			"conversation_id", conversationID,
			"thinking_chunks", thinkingChunks,
			"message_chunks", messageChunks)
		return usage, fmt.Errorf("ali deep thinking stream ended unexpectedly: %w", io.ErrUnexpectedEOF)
	}

	log.C(ctx).Infow("LLM deep thinking stream finished",
		"node_id", node.ID,
		"node_name", node.Name,
		"thinking_chunks", thinkingChunks,
		"message_chunks", messageChunks,
		"reasoning_output", thinkingBuf.String(),
		"message_output", messageBuf.String(),
		"usage", usage)

	usage.Normalize()
	return usage, nil
}

// callVolcDeepThinkingStream 调用火山方舟深度思考流式接口
// 使用 thinking.type: "enabled" 开启深度思考
// 返回 usage 信息用于统计 token 消耗
func (e *SopExecutor) callVolcDeepThinkingStream(ctx context.Context, node *model.SopNode, messages []LLMMessage, maxTokens int, deepThinking bool, conversationID string, handler StreamHandler) (*TokenUsage, error) {
	var usage *TokenUsage
	sawDone := false
	// 构建 URL（先 trim 掉空格，避免拼接错误）
	url := strings.TrimSpace(node.BaseURL)
	if url == "" {
		url = "https://ark.cn-beijing.volces.com/api/v3"
	}
	if !strings.HasSuffix(url, "/chat/completions") {
		// 确保 URL 末尾没有空格和斜杠
		url = strings.TrimRight(url, " /")
		if !strings.HasSuffix(url, "/") {
			url += "/"
		}
		url += "chat/completions"
	}

	// 转换 messages 格式
	volcMessages := make([]map[string]interface{}, len(messages))
	for i, msg := range messages {
		volcMessages[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
	}

	// 构建 payload
	thinkingType := "disabled"
	if deepThinking {
		thinkingType = "enabled"
	}

	payload := map[string]interface{}{
		"model":      node.ModelName,
		"messages":   volcMessages,
		"stream":     true,
		"max_tokens": maxTokens,
		"thinking": map[string]interface{}{
			"type": thinkingType,
		},
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	reqData, _ := json.Marshal(payload)

	// 添加详细的请求日志
	log.C(ctx).Infow("Sending request to Volcengine Ark Deep Thinking API",
		"node_id", node.ID,
		"node_name", node.Name,
		"url", url,
		"model", node.ModelName,
		"request_body", string(reqData),
		"thinking_enabled", deepThinking)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(reqData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+node.APIKey)
	req.Header.Set("User-Agent", "numind-server/1.0")

	// 设置超时时间（由 context 统一控制）
	client := &http.Client{
		Timeout: 0, // 由 context 统一控制（解决“影子账单”风险 2）
	}

	resp, err := client.Do(req)
	if err != nil {
		return usage, fmt.Errorf("failed to call volc deep thinking stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("volc stream status %d: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)

	var (
		thinkingBuf    strings.Builder
		messageBuf     strings.Builder
		thinkingChunks int
		messageChunks  int
	)

	for {
		// 检查context是否被取消
		select {
		case <-ctx.Done():
			return usage, ctx.Err()
		default:
		}

		// 直接阻塞读取，不再使用不安全的每行超时逻辑
		lineBytes, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			if readErr == io.EOF {
				if len(lineBytes) == 0 {
					break
				}
			}
			if ctx.Err() != nil {
				log.C(ctx).Warnw("LLM connection closed due to context cancellation", "node_id", node.ID, "error", readErr)
				return usage, ctx.Err()
			}
			log.C(ctx).Errorw("Read error in volc deep thinking stream", "node_id", node.ID, "error", readErr)
			return usage, fmt.Errorf("read error: %w", readErr)
		}

		// 转换为字符串并去除换行符
		line := strings.TrimRight(string(lineBytes), "\r\n")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		if raw == "[DONE]" {
			sawDone = true
			_ = handler("done", "")
			break
		}

		var m map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			log.C(ctx).Warnw("Failed to parse volc stream chunk", "error", err, "raw", raw)
			continue
		}

		// 检查是否有 usage 信息（通常在最后一个数据块中）
		if rawUsage, ok := m["usage"]; ok && rawUsage != nil {
			usageBytes, _ := json.Marshal(rawUsage)
			var tempUsage TokenUsage
			if err := json.Unmarshal(usageBytes, &tempUsage); err == nil && (tempUsage.TotalTokens > 0 || tempUsage.PromptTokens > 0 || tempUsage.CompletionTokens > 0) {
				usage = &tempUsage
				log.C(ctx).Infow("Token usage received from volc",
					"node_id", node.ID,
					"prompt_tokens", usage.PromptTokens,
					"completion_tokens", usage.CompletionTokens,
					"total_tokens", usage.TotalTokens,
					"reasoning_tokens", usage.ReasoningTokens,
					"raw_usage", rawUsage)
			} else if err != nil {
				log.C(ctx).Warnw("Failed to unmarshal usage from volc", "error", err, "raw_usage", rawUsage)
			}
		}

		choices, ok := m["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]interface{})
		delta, _ := choice["delta"].(map[string]interface{})

		// 添加调试日志
		if len(delta) > 0 {
			deltaJSON, _ := json.Marshal(delta)
			log.C(ctx).Debugw("Received delta from Volcengine Ark API",
				"node_id", node.ID,
				"delta_keys", getMapKeys(delta),
				"delta_preview", string(deltaJSON),
				"has_reasoning_content", hasKey(delta, "reasoning_content"))
		}

		// 优先处理 reasoning_content（思维链内容）
		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			thinkingChunks++
			thinkingBuf.WriteString(rc)
			log.C(ctx).Infow("LLM thinking chunk (from reasoning_content)",
				"node_id", node.ID,
				"node_name", node.Name,
				"chunk_index", thinkingChunks,
				"chunk_length", len(rc),
				"chunk_preview", getStringPreview(rc, 50))
			if err := handler("thinking", rc); err != nil {
				return nil, err
			}
		}

		// 处理 content 字段（最终回答内容）
		if content, ok := delta["content"].(string); ok && content != "" {
			messageChunks++
			messageBuf.WriteString(content)
			log.C(ctx).Infow("LLM message chunk",
				"node_id", node.ID,
				"node_name", node.Name,
				"chunk_index", messageChunks,
				"chunk_length", len(content),
				"chunk_preview", getStringPreview(content, 50))
			if err := handler("message", content); err != nil {
				return nil, err
			}
		}

		if readErr == io.EOF {
			break
		}
	}

	if !sawDone {
		log.C(ctx).Warnw("Volc deep thinking stream ended without [DONE]",
			"node_id", node.ID,
			"node_name", node.Name,
			"model", node.ModelName,
			"conversation_id", conversationID,
			"thinking_chunks", thinkingChunks,
			"message_chunks", messageChunks)
		return usage, fmt.Errorf("volc deep thinking stream ended unexpectedly: %w", io.ErrUnexpectedEOF)
	}

	log.C(ctx).Infow("LLM volc deep thinking stream finished",
		"node_id", node.ID,
		"node_name", node.Name,
		"thinking_chunks", thinkingChunks,
		"message_chunks", messageChunks,
		"reasoning_output", thinkingBuf.String(),
		"message_output", messageBuf.String(),
		"usage", usage)

	usage.Normalize()
	return usage, nil
}

// getStringPreview 获取字符串预览（用于日志，避免日志过长）
func getStringPreview(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// getMapKeys 获取 map 的所有键（用于调试日志）
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// hasKey 检查 map 是否包含指定键（用于调试日志）
func hasKey(m map[string]interface{}, key string) bool {
	_, ok := m[key]
	return ok
}

// CreateFinalNote 创建最终笔记（公开方法）
func (e *SopExecutor) CreateFinalNote(ctx context.Context, run *model.SopRun, content string) (*model.SopNote, error) {
	// 获取模板信息
	template, err := e.ds.Sop().GetTemplate(run.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	note := &model.SopNote{
		Content:    content,
		Title:      fmt.Sprintf("SOP执行结果 - %s", template.Name),
		UserID:     run.UserID,
		TemplateID: run.TemplateID,
		RunID:      run.ID,
	}

	if err := e.ds.Sop().CreateNote(note); err != nil {
		return nil, fmt.Errorf("failed to create note: %w", err)
	}

	return note, nil
}
