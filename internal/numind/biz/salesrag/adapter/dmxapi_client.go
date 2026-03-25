package adapter

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
)

const (
	// DMXAPIBaseURL DMXAPI 平台基础 URL
	DMXAPIBaseURL = "https://www.dmxapi.cn/v1"
	// DMXAPIKey DMXAPI 平台 API Key
	DMXAPIKey = "sk-XgINDoE22MHQfcSZnToYICS4rNnoknIrXhZHZYs3VQM9DP25"
)

// DMXAPIClient DMXAPI 平台客户端
type DMXAPIClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewDMXAPIClient 创建新的 DMXAPI 客户端
func NewDMXAPIClient() *DMXAPIClient {
	return &DMXAPIClient{
		baseURL: DMXAPIBaseURL,
		apiKey:  DMXAPIKey,
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

	// 自动计费
	if bc := billing.FromContext(ctx); bc != nil && result.Usage != nil {
		billing.RecordLLM(bc.UserID, "dmxapi", model, bc.Operation, result.Usage, bc.Meta)
	}

	// Langfuse generation 追踪
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

	// 调用增强后的 StreamChatCompletion
	content, usage, err := c.StreamChatCompletion(ctx, model, messages, temperature, maxTokens, true, func(eventType, chunk string) error {
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
// onEvent: 回调函数，参数为 content 片段
// StreamChatCompletion 调用聊天接口（流式）
// onEvent: 回调函数，参数为 (eventType string, content string)
// eventType: "thinking" (思维链内容) 或 "content" (正文内容)
func (c *DMXAPIClient) StreamChatCompletion(ctx context.Context, model string, messages []ChatMessage, temperature float64, maxTokens int, enableThinking bool, onEvent func(eventType, content string) error) (string, *billing.TokenUsage, error) {
	// 构建请求体（手动构建以添加 stream_options）
	bodyMap := map[string]interface{}{
		"model":           model,
		"messages":        messages,
		"temperature":     temperature,
		"stream":          true,
		"enable_thinking": enableThinking,
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
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
								_ = onEvent("content", realContent)
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
						_ = onEvent("content", content)
					}
				}
			}
		}
	}

	// 自动计费
	if bc := billing.FromContext(ctx); bc != nil && usage != nil {
		billing.RecordLLM(bc.UserID, "dmxapi", model, bc.Operation, usage, bc.Meta)
	}

	// Langfuse generation 追踪
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
