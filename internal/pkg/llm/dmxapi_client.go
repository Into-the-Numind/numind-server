package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// DMXAPIClient DMXAPI 平台客户端
type DMXAPIClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewDMXAPIClient 创建新的 DMXAPI 客户端
// 从 viper 配置读取 dmxapi.api_key 和 dmxapi.base_url，支持 fallback 默认值
func NewDMXAPIClient() *DMXAPIClient {
	apiKey := viper.GetString("dmxapi.api_key")
	if apiKey == "" {
		log.Warnw("dmxapi.api_key is not configured; DMXAPI calls will fail")
	}
	baseURL := viper.GetString("dmxapi.base_url")
	if baseURL == "" {
		baseURL = "https://www.dmxapi.cn/v1"
	}

	return &DMXAPIClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		// 流式响应不能使用 Client.Timeout（它覆盖整个请求生命周期包括 body 读取）
		// 改用 Transport 级别超时：只限制建连和握手，不限制 body 读取时间
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}
}

// NewDMXAPIClientWithConfig 创建使用动态配置的 DMXAPI 客户端
// 与 NewDMXAPIClient 不同，baseURL 和 apiKey 由调用方直接提供，不从 viper 读取
// 主要用于 LLMRouter，允许路由层统一管理提供商凭据
func NewDMXAPIClientWithConfig(baseURL, apiKey string) *DMXAPIClient {
	return &DMXAPIClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}
}

// llmRouterCtxKey 是 LLMRouter 调用标记的 context key
type llmRouterCtxKey struct{}

// WithLLMRouterMark 在 context 中注入 LLMRouter 调用标记
// LLMRouter 调用 DMXAPIClient 时须注入此标记，以跳过内部 Langfuse/billing（由 Router 统一处理，避免重复计费）
func WithLLMRouterMark(ctx context.Context) context.Context {
	return context.WithValue(ctx, llmRouterCtxKey{}, true)
}

// isFromLLMRouter 判断当前调用是否来自 LLMRouter
func isFromLLMRouter(ctx context.Context) bool {
	v, _ := ctx.Value(llmRouterCtxKey{}).(bool)
	return v
}

// ChatMessage 聊天消息结构
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest 聊天请求
type ChatCompletionRequest struct {
	Model          string        `json:"model"`
	Messages       []ChatMessage `json:"messages"`
	Temperature    float64       `json:"temperature,omitempty"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	Stream         bool          `json:"stream,omitempty"`
	EnableThinking *bool         `json:"enable_thinking,omitempty"` // Qwen 深度思考模式
}

// ChatCompletionResponse 聊天响应
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *billing.TokenUsage `json:"usage,omitempty"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ChatCompletion 调用聊天接口（非流式）
func (c *DMXAPIClient) ChatCompletion(ctx context.Context, model string, messages []ChatMessage, temperature float64, maxTokens int) (string, *billing.TokenUsage, error) {
	// 非流式请求添加整体超时保护（覆盖建连+等待响应+读取 body 全流程）
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	reqBody := ChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		Stream:      false,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil, fmt.Errorf("decode response failed: %w", err)
	}

	if result.Error != nil {
		return "", nil, fmt.Errorf("API error: %s - %s", result.Error.Code, result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", nil, fmt.Errorf("empty choices from API")
	}

	if result.Usage != nil {
		result.Usage.Normalize()
	}

	// 自动计费（LLMRouter 调用时跳过，由 Router 统一处理）
	if !isFromLLMRouter(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && result.Usage != nil {
			billing.RecordLLM(bc.UserID, "dmxapi", model, bc.Operation, result.Usage, bc.Meta)
		}
	}

	// Langfuse generation 追踪（LLMRouter 调用时跳过，由 Router 统一处理）
	if !isFromLLMRouter(ctx) {
		if tc := langfuse.FromContext(ctx); tc != nil {
			genID := langfuse.SpanID()
			genOpts := []langfuse.GenOption{
				langfuse.WithGenParent(tc.ParentObservationID),
				langfuse.WithGenName("dmxapi-chat"),
				langfuse.WithGenModel(model),
				langfuse.WithGenInput(messages),
				langfuse.WithGenOutput(result.Choices[0].Message.Content),
			}
			if tc.PromptName != "" {
				genOpts = append(genOpts, langfuse.WithGenPromptName(tc.PromptName, tc.PromptVersion))
			}
			langfuse.CreateGeneration(tc.TraceID, genID, genOpts...)
			var endOpts []langfuse.GenOption
			if result.Usage != nil {
				endOpts = append(endOpts, langfuse.WithGenUsage(result.Usage.PromptTokens, result.Usage.CompletionTokens))
			}
			langfuse.EndGeneration(genID, endOpts...)
		}
	}

	return result.Choices[0].Message.Content, result.Usage, nil
}

// ChatCompletionWithThinking 调用聊天接口（非流式 + 深度思考模式）
// 启用 enable_thinking=true，模型会在响应中包含 <think>...</think> 思维链
// 返回值只包含最终回答内容（思维链部分会被记录到日志但不返回）
// ChatCompletionWithThinking 调用聊天接口（流式 + 深度思考模式）
// 启用 enable_thinking=true，模型会在响应中包含 <think>...</think> 思维链
// 注意：虽然函数签名看起来是阻塞调用，但内部必须使用流式请求（Stream=true），因为当前 API 关于 thinking 参数仅支持流式调用
func (c *DMXAPIClient) ChatCompletionWithThinking(ctx context.Context, model string, messages []ChatMessage, temperature float64, maxTokens int) (string, *billing.TokenUsage, error) {
	var fullThinking strings.Builder

	// 调用增强后的 StreamChatCompletion（SalesRAG 内部调用，默认 enable_thinking 格式）
	content, usage, err := c.StreamChatCompletion(ctx, model, messages, temperature, maxTokens, "enable_thinking", func(eventType, chunk string) error {
		if eventType == "thinking" {
			fullThinking.WriteString(chunk)
		}
		return nil
	})

	if err != nil {
		return "", nil, err
	}

	// 记录思维链日志
	if fullThinking.Len() > 0 {
		logContent := fullThinking.String()
		if len(logContent) > 500 {
			logContent = logContent[:500] + "..."
		}
		log.Infow("LLM thinking chain", "thinking", logContent)
	}

	return content, usage, nil
}

// StreamChatCompletion 调用聊天接口（流式）
// onEvent: 回调函数，参数为 (eventType string, content string)
// eventType: "thinking" (思维链内容) 或 "message" (正文内容)
// thinkingFormat: "" (不启用), "enable_thinking" (Gemini/Qwen), "anthropic" (Claude)
func (c *DMXAPIClient) StreamChatCompletion(ctx context.Context, model string, messages []ChatMessage, temperature float64, maxTokens int, thinkingFormat string, onEvent func(eventType, content string) error) (string, *billing.TokenUsage, error) {
	// 构建请求体（手动构建以添加 stream_options）
	bodyMap := map[string]interface{}{
		"model":       model,
		"messages":    messages,
		"temperature": temperature,
		"stream":      true,
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	switch thinkingFormat {
	case "enable_thinking":
		bodyMap["enable_thinking"] = true
	case "doubao":
		// Doubao / 豆包：thinking:{type:"enabled"}，走 OpenAI 兼容端点
		bodyMap["thinking"] = map[string]interface{}{
			"type": "enabled",
		}
	// 注意：ThinkingAnthropic ("anthropic") 不会走到这里——
	// Router 会把 Claude 路由到 StreamAnthropicMessages()
	}
	if maxTokens > 0 {
		bodyMap["max_tokens"] = maxTokens
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", nil, fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)

		// 安全网：如果 thinking 参数导致 400 错误，去掉 thinking 参数重试
		if resp.StatusCode == http.StatusBadRequest && thinkingFormat != "" &&
			(strings.Contains(string(respBody), "enable_thinking") ||
				strings.Contains(string(respBody), "thinking") ||
				strings.Contains(string(respBody), "unknown_parameter")) {
			log.Warnw("Thinking parameter rejected by provider, retrying without thinking",
				"model", model, "thinking_format", thinkingFormat, "status", resp.StatusCode)
			return c.StreamChatCompletion(ctx, model, messages, temperature, maxTokens, "", onEvent)
		}

		return "", nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var fullContent strings.Builder
	var fullThinking strings.Builder
	var usage *billing.TokenUsage
	reader := bufio.NewReader(resp.Body)

	// 标记是否正在处理思维链（用于解析 <think> 标签）
	inThinkingTag := false

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fullContent.String(), usage, fmt.Errorf("read stream failed: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// SSE 格式: data: {...}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		// 提取 usage
		if u := billing.ExtractUsageFromSSEData(data); u != nil {
			usage = u
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content  string `json:"content"`
					Thinking string `json:"reasoning_content"` // 有些厂商可能使用 reasoning_content
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Warnw("Failed to parse SSE chunk", "data", data, "error", err)
			continue
		}

		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta

			// 1. 处理显式的 reasoning_content 字段
			if delta.Thinking != "" {
				fullThinking.WriteString(delta.Thinking)
				if onEvent != nil {
					if err := onEvent("thinking", delta.Thinking); err != nil {
						return fullContent.String(), usage, err
					}
				}
			}

			// 2. 处理 content 字段（可能包含 <think> 标签）
			content := delta.Content
			if content != "" {
				// 检查 <think> 开始
				if strings.Contains(content, "<think>") {
					inThinkingTag = true
					content = strings.Replace(content, "<think>", "", 1)
				}

				// 检查 </think> 结束
				if strings.Contains(content, "</think>") {
					inThinkingTag = false
					parts := strings.Split(content, "</think>")

					// </think> 之前的部分属于 thinking
					if len(parts) > 0 && parts[0] != "" {
						fullThinking.WriteString(parts[0])
						if onEvent != nil {
							_ = onEvent("thinking", parts[0])
						}
					}

					// </think> 之后的部分属于 content
					if len(parts) > 1 && parts[1] != "" {
						// 过滤掉可能的换行
						realContent := strings.TrimLeft(parts[1], "\n")
						if realContent != "" {
							fullContent.WriteString(realContent)
							if onEvent != nil {
								// 统一使用 "message" 事件名（与 executor.go / volc / ali 保持一致）；
								// SOP controller 只识别 "message"，若发 "content" 会被静默丢弃。
								_ = onEvent("message", realContent)
							}
						}
					}
					continue // 本次 chunk 处理完毕
				}

				if inThinkingTag {
					// 在 <think>...</think> 内部，全部视为思考内容
					fullThinking.WriteString(content)
					if onEvent != nil {
						_ = onEvent("thinking", content)
					}
				} else {
					// 正常内容
					fullContent.WriteString(content)
					if onEvent != nil {
						// 统一使用 "message" 事件名（与 executor.go / volc / ali 保持一致）
						_ = onEvent("message", content)
					}
				}
			}
		}
	}

	// 自动计费（LLMRouter 调用时跳过，由 Router 统一处理）
	if !isFromLLMRouter(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && usage != nil {
			billing.RecordLLM(bc.UserID, "dmxapi", model, bc.Operation, usage, bc.Meta)
		}
	}

	// Langfuse generation 追踪（LLMRouter 调用时跳过，由 Router 统一处理）
	if !isFromLLMRouter(ctx) {
		if tc := langfuse.FromContext(ctx); tc != nil {
			genID := langfuse.SpanID()
			opts := []langfuse.GenOption{
				langfuse.WithGenParent(tc.ParentObservationID),
				langfuse.WithGenName("dmxapi-stream"),
				langfuse.WithGenModel(model),
				langfuse.WithGenInput(messages),
				langfuse.WithGenOutput(fullContent.String()),
			}
			if usage != nil {
				opts = append(opts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
			}
			if tc.PromptName != "" {
				opts = append(opts, langfuse.WithGenPromptName(tc.PromptName, tc.PromptVersion))
			}
			langfuse.CreateGeneration(tc.TraceID, genID, opts...)
			langfuse.EndGeneration(genID)
		}
	}

	return fullContent.String(), usage, nil
}

// RerankRequest Rerank 请求结构
type RerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
}

// RerankResponse Rerank 响应结构
type RerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// RerankResult Rerank 结果，包含索引和相关性分数
type RerankResult struct {
	Index int     // 原始文档的索引位置
	Score float64 // Rerank 相关性分数 (0-1)
}

// Rerank 调用 Rerank 模型
// 返回按相关性排序的文档索引和对应的 relevance_score
func (c *DMXAPIClient) Rerank(ctx context.Context, query string, documents []string, topN int) ([]RerankResult, int, error) {
	// 非流式请求添加整体超时保护
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	if len(documents) == 0 {
		return nil, 0, nil
	}

	// 限制 topN 不超过文档数量
	if topN <= 0 || topN > len(documents) {
		topN = len(documents)
	}

	reqBody := RerankRequest{
		Model:     "qwen3-rerank",
		Query:     query,
		Documents: documents,
		TopN:      topN,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/rerank", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result RerankResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, 0, fmt.Errorf("decode response failed: %w", err)
	}

	if result.Error != nil {
		return nil, 0, fmt.Errorf("rerank error: %s", result.Error.Message)
	}

	// 提取排序后的索引和分数
	results := make([]RerankResult, 0, len(result.Results))
	for _, r := range result.Results {
		results = append(results, RerankResult{
			Index: r.Index,
			Score: r.RelevanceScore,
		})
	}

	// 自动计费
	if bc := billing.FromContext(ctx); bc != nil {
		billing.RecordRerank(bc.UserID, "dmxapi", bc.Operation, len(documents), bc.Meta)
	}

	// Langfuse generation 追踪
	if tc := langfuse.FromContext(ctx); tc != nil {
		genID := langfuse.SpanID()
		langfuse.CreateGeneration(tc.TraceID, genID,
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("dmxapi-rerank"),
			langfuse.WithGenModel("qwen3-rerank"),
			langfuse.WithGenInput(map[string]interface{}{"query": query, "doc_count": len(documents), "top_n": topN}),
			langfuse.WithGenOutput(results),
		)
		langfuse.EndGeneration(genID)
	}

	log.Infow("Rerank completed", "query_len", len(query), "doc_count", len(documents), "top_n", topN, "result_count", len(results))

	return results, len(documents), nil
}

// StreamAnthropicMessages 调用 Anthropic Messages API（/v1/messages）流式接口
// 用于 Claude 模型，支持 adaptive thinking
// onEvent: 回调函数，eventType 为 "thinking" 或 "message"
func (c *DMXAPIClient) StreamAnthropicMessages(ctx context.Context, model string, messages []ChatMessage, temperature float64, maxTokens int, enableThinking bool, onEvent func(eventType, content string) error) (string, *billing.TokenUsage, error) {
	// 构建 Anthropic Messages 格式的消息
	anthMessages := make([]map[string]interface{}, 0, len(messages))
	var systemPrompt string
	for _, msg := range messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
			continue
		}
		anthMessages = append(anthMessages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// Anthropic Messages API 要求 max_tokens 必填且 > 0
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	bodyMap := map[string]interface{}{
		"model":      model,
		"messages":   anthMessages,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if systemPrompt != "" {
		bodyMap["system"] = systemPrompt
	}
	if enableThinking {
		// Anthropic adaptive thinking：不发 temperature（API 要求）
		bodyMap["thinking"] = map[string]interface{}{
			"type": "adaptive",
		}
		bodyMap["output_config"] = map[string]interface{}{
			"effort": "high",
		}
	} else {
		bodyMap["temperature"] = temperature
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", nil, fmt.Errorf("marshal request failed: %w", err)
	}

	// Anthropic Messages 端点：baseURL 去掉 /v1 后拼 /v1/messages
	baseURL := strings.TrimSuffix(c.baseURL, "/v1")
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/messages", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var fullContent strings.Builder
	var fullThinking strings.Builder
	var usage *billing.TokenUsage
	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fullContent.String(), usage, fmt.Errorf("read stream failed: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Anthropic SSE 格式: event: xxx 和 data: {...}
		if strings.HasPrefix(line, "event:") {
			continue // 事件类型由 data 中的 type 字段决定
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Error   *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
			// content_block_start
			ContentBlock *struct {
				Type string `json:"type"` // "thinking" 或 "text"
			} `json:"content_block"`
			// content_block_delta
			Delta *struct {
				Type     string `json:"type"`     // "thinking_delta" 或 "text_delta"
				Thinking string `json:"thinking"` // thinking_delta 的内容
				Text     string `json:"text"`     // text_delta 的内容
			} `json:"delta"`
			// message_start → message.usage
			Message *struct {
				Usage *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			// message_delta → usage
			Usage *struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				usage = &billing.TokenUsage{
					PromptTokens: event.Message.Usage.InputTokens,
				}
			}

		case "error":
			errMsg := "unknown Anthropic API error"
			if event.Error != nil {
				errMsg = fmt.Sprintf("%s: %s", event.Error.Type, event.Error.Message)
			}
			return fullContent.String(), usage, fmt.Errorf("Anthropic stream error: %s", errMsg)

		case "content_block_start":
			// content_block 类型由 delta.Type 判断，无需单独追踪

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "thinking_delta":
				if event.Delta.Thinking != "" {
					fullThinking.WriteString(event.Delta.Thinking)
					if onEvent != nil {
						if err := onEvent("thinking", event.Delta.Thinking); err != nil {
							return fullContent.String(), usage, err
						}
					}
				}
			case "text_delta":
				if event.Delta.Text != "" {
					fullContent.WriteString(event.Delta.Text)
					if onEvent != nil {
						if err := onEvent("message", event.Delta.Text); err != nil {
							return fullContent.String(), usage, err
						}
					}
				}
			}

		case "content_block_stop":
			// no-op

		case "message_delta":
			// 更新 usage（补充 output_tokens）
			if event.Usage != nil && usage != nil {
				usage.CompletionTokens = event.Usage.OutputTokens
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}

		case "message_stop":
			// 流结束
		}
	}

	// 如果有 thinking 内容，记录 reasoning_tokens（估算）
	if usage != nil && fullThinking.Len() > 0 {
		// Anthropic 不单独返回 reasoning_tokens，用 thinking 字符数估算
		usage.ReasoningTokens = fullThinking.Len() / 3
	}

	return fullContent.String(), usage, nil
}
