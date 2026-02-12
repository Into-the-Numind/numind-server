package adapter

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
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
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
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ChatCompletion 调用聊天接口（非流式）
func (c *DMXAPIClient) ChatCompletion(ctx context.Context, model string, messages []ChatMessage, temperature float64, maxTokens int) (string, error) {
	reqBody := ChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		Stream:      false,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode response failed: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("API error: %s - %s", result.Error.Code, result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty choices from API")
	}

	return result.Choices[0].Message.Content, nil
}

// ChatCompletionWithThinking 调用聊天接口（非流式 + 深度思考模式）
// 启用 enable_thinking=true，模型会在响应中包含 <think>...</think> 思维链
// 返回值只包含最终回答内容（思维链部分会被记录到日志但不返回）
func (c *DMXAPIClient) ChatCompletionWithThinking(ctx context.Context, model string, messages []ChatMessage, temperature float64, maxTokens int) (string, error) {
	enableThinking := true
	reqBody := ChatCompletionRequest{
		Model:          model,
		Messages:       messages,
		Temperature:    temperature,
		MaxTokens:      maxTokens,
		Stream:         false,
		EnableThinking: &enableThinking,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result ChatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode response failed: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("API error: %s - %s", result.Error.Code, result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty choices from API")
	}

	content := result.Choices[0].Message.Content

	// 提取并记录思维链内容（如果存在），然后只返回最终回答
	if thinkStart := strings.Index(content, "<think>"); thinkStart != -1 {
		if thinkEnd := strings.Index(content, "</think>"); thinkEnd != -1 {
			thinkContent := content[thinkStart+7 : thinkEnd]
			log.Infow("LLM thinking chain", "thinking", thinkContent[:min(len(thinkContent), 500)])
			// 只返回 </think> 之后的最终内容
			content = strings.TrimSpace(content[thinkEnd+8:])
		}
	}

	return content, nil
}

// StreamChatCompletion 调用聊天接口（流式）
// onEvent: 回调函数，参数为 content 片段
func (c *DMXAPIClient) StreamChatCompletion(ctx context.Context, model string, messages []ChatMessage, temperature float64, maxTokens int, onEvent func(content string) error) (string, error) {
	reqBody := ChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		Stream:      true,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var fullContent strings.Builder
	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fullContent.String(), fmt.Errorf("read stream failed: %w", err)
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

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}

		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.Warnw("Failed to parse SSE chunk", "data", data, "error", err)
			continue
		}

		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			content := chunk.Choices[0].Delta.Content
			fullContent.WriteString(content)
			if onEvent != nil {
				if err := onEvent(content); err != nil {
					return fullContent.String(), err
				}
			}
		}
	}

	return fullContent.String(), nil
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
func (c *DMXAPIClient) Rerank(ctx context.Context, query string, documents []string, topN int) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, nil
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
		return nil, fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/rerank", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result RerankResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response failed: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("rerank error: %s", result.Error.Message)
	}

	// 提取排序后的索引和分数
	results := make([]RerankResult, 0, len(result.Results))
	for _, r := range result.Results {
		results = append(results, RerankResult{
			Index: r.Index,
			Score: r.RelevanceScore,
		})
	}

	log.Infow("Rerank completed", "query_len", len(query), "doc_count", len(documents), "top_n", topN, "result_count", len(results))

	return results, nil
}
