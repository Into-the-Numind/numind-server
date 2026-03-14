package ali

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/service"
	"os"
	"strings"
	"time"

	"numind-server/internal/pkg/httpclient"
	pkglog "numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

type BailianConfig struct {
	AccessKeyId     string
	AccessKeySecret string
	Endpoint        string
	ApiVersion      string
	Model           string
	Temperature     float64
	MaxTokens       int
	TopP            float64
}

type AliBiz interface {
	QianwenTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, *billing.TokenUsage, error)
	QianwenEmbedding(ctx context.Context, text string) ([]float32, *billing.EmbeddingUsage, error)
	QianwenVision(ctx context.Context, imageURL string, prompt string, model string) (string, *billing.TokenUsage, error)
	QianwenVisionStream(ctx context.Context, imageURL string, prompt string, model string, onToken func(token string) error) (string, *billing.TokenUsage, error)
	GetFileUploadLease(fileName string) (string, map[string]string, string, error)
	AddFile(leaseId string) (string, error)
	GetPromptManager() *PromptManager
}

type aliBiz struct {
	ds            store.IStore
	pm            *PromptManager
	bailianClient *service.BailianHTTPClient
	textClient    *httpclient.Client
	visionClient  *httpclient.Client
}

func NewAliBiz(ds store.IStore) AliBiz {
	return &aliBiz{
		ds: ds,
		pm: NewPromptManager(),
		bailianClient: service.NewBailianHTTPClient(
			getAliConfig("common", "access_key_id"),
			getAliConfig("common", "access_key_secret"),
			getAliConfig("common", "workspace_id"),
		),
		textClient:   httpclient.NewClientFromConfig("ali.text"),
		visionClient: httpclient.NewClientFromConfig("ali.vision"),
	}
}

// getAliConfig 获取Ali配置，支持服务特定配置和通用配置回退
func getAliConfig(service string, key string) string {
	// 先尝试服务特定配置
	serviceKey := fmt.Sprintf("ali.%s.%s", service, key)
	if viper.IsSet(serviceKey) {
		return viper.GetString(serviceKey)
	}

	// 回退到通用配置
	commonKey := fmt.Sprintf("ali.%s", key)
	if viper.IsSet(commonKey) {
		return viper.GetString(commonKey)
	}

	// 如果都没有，返回空字符串
	return ""
}

// GenerateContent 支持多轮对话和参数扩展
func (a *aliBiz) GenerateContent(messages []map[string]string, cfg *BailianConfig) (string, error) {
	if cfg == nil {
		cfg = &BailianConfig{
			AccessKeyId:     os.Getenv("ALI_ACCESS_KEY_ID"),
			AccessKeySecret: os.Getenv("ALI_ACCESS_KEY_SECRET"),
			Endpoint:        "bailian.cn-beijing.aliyuncs.com",
			ApiVersion:      "2023-12-29",
			Model:           "qwen-turbo",
			Temperature:     0.5,
			MaxTokens:       2048,
			TopP:            0.8,
		}
	}
	url := fmt.Sprintf("https://%s/api/bailian/v1/chat/completions", cfg.Endpoint)

	bodyMap := map[string]interface{}{
		"model": cfg.Model,
		"input": map[string]interface{}{
			"messages": messages,
		},
		"parameters": map[string]interface{}{
			"max_tokens":  cfg.MaxTokens,
			"temperature": cfg.Temperature,
			"top_p":       cfg.TopP,
		},
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-DashScope-AccessKey", cfg.AccessKeySecret)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result struct {
		Output struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"output"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("API返回解析失败: %v, 原始: %s", err, string(respBody))
	}
	if len(result.Output.Choices) == 0 {
		return "", fmt.Errorf("API无返回内容: %s", string(respBody))
	}
	return result.Output.Choices[0].Message.Content, nil
}

// 千问（文字模型）流式API（兼容模式）
func (a *aliBiz) QianwenTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, *billing.TokenUsage, error) {
	url := "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	modelName := getAliConfig("text", "model")
	bodyMap := map[string]interface{}{
		"model":       modelName,
		"messages":    messages,
		"max_tokens":  maxTokens,
		"temperature": temperature,
		"stream":      true,
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	// 使用 persistent client
	client := a.textClient

	// 创建请求
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + getAliConfig("text", "api_key"),
		},
		RetryPolicy: &httpclient.RetryPolicy{
			MaxRetries:   3,
			RetryDelay:   1 * time.Second,
			RetryBackoff: 2.0,
		},
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		// 网络诊断信息
		if netErr, ok := err.(net.Error); ok {
			if netErr.Timeout() {
				return "", nil, fmt.Errorf("网络超时错误: %w", err)
			}
		}
		return "", nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	var result strings.Builder
	var usage *billing.TokenUsage
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// 过滤 SSE 注释行（以 : 开头）和空行，防止心跳等注释内容混入输出
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			// 尝试提取 usage（通常在最后一个 chunk 中）
			if u := billing.ExtractUsageFromSSEData(data); u != nil {
				usage = u
			}
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				if choices, ok := m["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							if content, ok := delta["content"].(string); ok {
								result.WriteString(content)
							}
						}
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", usage, err
	}

	// 自动计费
	if bc := billing.FromContext(ctx); bc != nil && usage != nil {
		billing.RecordLLM(bc.UserID, "ali", modelName, bc.Operation, usage, bc.Meta)
	}

	return result.String(), usage, nil
}

// QianwenEmbedding 调用阿里百炼 Embedding API 获取文本向量（使用 DashScope 原生接口）
func (a *aliBiz) QianwenEmbedding(ctx context.Context, text string) ([]float32, *billing.EmbeddingUsage, error) {
	// 使用 DashScope 原生接口，支持自定义维度参数
	url := "https://dashscope.aliyuncs.com/api/v1/services/embeddings/text-embedding/text-embedding"

	modelName := "text-embedding-v4"
	bodyMap := map[string]interface{}{
		"model": modelName,
		"input": map[string]interface{}{
			"texts": []string{text},
		},
		"parameters": map[string]interface{}{
			"dimension": 2048, // DashScope 原生接口使用 dimension（单数），支持 2048 维
		},
	}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 使用 persistent client
	client := a.textClient

	// 创建请求
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + getAliConfig("text", "api_key"),
		},
		RetryPolicy: &httpclient.RetryPolicy{
			MaxRetries:   3,
			RetryDelay:   1 * time.Second,
			RetryBackoff: 2.0,
		},
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		// 网络诊断信息
		if netErr, ok := err.(net.Error); ok {
			if netErr.Timeout() {
				return nil, nil, fmt.Errorf("网络超时错误: %w", err)
			}
		}
		return nil, nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取响应失败: %w", err)
	}

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP错误: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	// 解析响应（DashScope 原生接口格式）
	var result struct {
		Output struct {
			Embeddings []struct {
				Embedding []float32 `json:"embedding"`
				TextIndex int       `json:"text_index"`
			} `json:"embeddings"`
		} `json:"output"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
		RequestID string `json:"request_id"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, nil, fmt.Errorf("解析响应失败: %w, 原始响应: %s", err, string(respBody))
	}

	// 提取向量
	if len(result.Output.Embeddings) == 0 {
		return nil, nil, fmt.Errorf("API返回数据为空: %s", string(respBody))
	}

	if len(result.Output.Embeddings[0].Embedding) == 0 {
		return nil, nil, fmt.Errorf("API返回向量为空: %s", string(respBody))
	}

	// 返回向量和用量信息
	embUsage := &billing.EmbeddingUsage{TotalTokens: result.Usage.TotalTokens}

	// 自动计费
	if bc := billing.FromContext(ctx); bc != nil && embUsage != nil {
		billing.RecordEmbedding(bc.UserID, "ali", modelName, bc.Operation, embUsage, bc.Meta)
	}

	return result.Output.Embeddings[0].Embedding, embUsage, nil
}

// QianwenVision 调用视觉模型读取图片 (OpenAI 兼容模式)
func (a *aliBiz) QianwenVision(ctx context.Context, imageURL string, prompt string, model string) (string, *billing.TokenUsage, error) {
	if prompt == "" {
		prompt = "图中描绘的是什么景象?"
	}
	apiKey := getAliConfig("vision", "api_key")

	// 如果未指定模型，尝试从配置获取
	if model == "" {
		model = getAliConfig("vision", "model")
	}
	// 如果仍未指定，使用默认模型
	if model == "" {
		model = "qwen-vl-plus" // 保持原有默认值，避免影响其他模块
	}
	if apiKey == "" {
		return "", nil, fmt.Errorf("未配置ali.vision.api_key")
	}

	url := "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	bodyMap := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]string{
							"url": imageURL,
						},
					},
					map[string]string{
						"type": "text",
						"text": prompt,
					},
				},
			},
		},
		"stream": false,
	}

	// 针对 qwen3-vl 系列模型开启深度思考 (Thinking)
	if strings.Contains(strings.ToLower(model), "qwen3-vl") {
		bodyMap["enable_thinking"] = true
		bodyMap["thinking_budget"] = 81920
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 使用 persistent client
	client := a.visionClient

	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + apiKey,
		},
		RetryPolicy: httpclient.DefaultRetryPolicy(),
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("HTTP错误: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *billing.TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil, fmt.Errorf("解析响应失败: %w, 原始响应: %s", err, string(respBody))
	}

	if len(result.Choices) == 0 {
		return "", nil, fmt.Errorf("响应为空: %s", string(respBody))
	}
	if result.Usage != nil {
		result.Usage.Normalize()
	}

	// 自动计费
	if bc := billing.FromContext(ctx); bc != nil && result.Usage != nil {
		billing.RecordVision(bc.UserID, "ali", model, bc.Operation, result.Usage, bc.Meta)
	}

	return result.Choices[0].Message.Content, result.Usage, nil
}

// QianwenVisionStream 调用视觉模型读取图片并进行流式回答 (OpenAI 兼容模式)
func (a *aliBiz) QianwenVisionStream(ctx context.Context, imageURL string, prompt string, model string, onToken func(token string) error) (string, *billing.TokenUsage, error) {
	if prompt == "" {
		prompt = "图中描绘的是什么景象?"
	}
	apiKey := getAliConfig("vision", "api_key")

	if model == "" {
		model = getAliConfig("vision", "model")
	}
	if model == "" {
		model = "qwen-vl-plus"
	}
	if apiKey == "" {
		return "", nil, fmt.Errorf("未配置ali.vision.api_key")
	}

	url := "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	bodyMap := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]string{
							"url": imageURL,
						},
					},
					map[string]string{
						"type": "text",
						"text": prompt,
					},
				},
			},
		},
		"stream": true, // 开启流式
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	// 针对 qwen3-vl 系列模型开启深度思考
	if strings.Contains(strings.ToLower(model), "qwen3-vl") {
		bodyMap["enable_thinking"] = true
		bodyMap["thinking_budget"] = 81920
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	log.Printf("[QianwenVisionStream] Request: model=%s, imageURL=%s..., prompt=%s...", model, imageURL[:min(len(imageURL), 50)], prompt[:min(len(prompt), 50)])

	client := a.visionClient
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + apiKey,
		},
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[QianwenVisionStream] Response status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("HTTP错误: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	var fullContent strings.Builder
	var usage *billing.TokenUsage
	scanner := bufio.NewScanner(resp.Body)
	lineCount := 0
	tokenCount := 0

	for scanner.Scan() {
		lineCount++
		line := scanner.Text()

		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			log.Printf("[QianwenVisionStream] Received [DONE]")
			break
		}

		// 尝试提取 usage（通常在最后一个 chunk 中）
		if u := billing.ExtractUsageFromSSEData(data); u != nil {
			usage = u
		}

		// 解析为通用 map 以查看完整结构
		var rawData map[string]interface{}
		if err := json.Unmarshal([]byte(data), &rawData); err != nil {
			pkglog.Warnw("解析SSE数据为map失败", "error", err, "data", data)
			continue
		}

		// 打印每一行的关键信息
		if choices, ok := rawData["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := choice["delta"].(map[string]interface{}); ok {
					if content, ok := delta["content"].(string); ok && content != "" {
						tokenCount++
						fullContent.WriteString(content)
						if onToken != nil {
							if err := onToken(content); err != nil {
								log.Printf("[QianwenVisionStream] onToken error: %v", err)
								return fullContent.String(), usage, err
							}
						}
					}
				}
			}
		}

		// 检查是否有 reasoning_content (思考内容)
		if choices, ok := rawData["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := choice["delta"].(map[string]interface{}); ok {
					if _, hasReasoning := delta["reasoning_content"]; hasReasoning {
						log.Printf("[QianwenVisionStream] Found reasoning_content in delta")
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[QianwenVisionStream] Scanner error: %v", err)
		return fullContent.String(), usage, fmt.Errorf("读取流式响应失败: %w", err)
	}

	log.Printf("[QianwenVisionStream] Finished: lines=%d, tokens=%d, content_len=%d", lineCount, tokenCount, fullContent.Len())

	// 检查是否收到了任何内容
	if fullContent.Len() == 0 {
		return "", usage, fmt.Errorf("API返回内容为空，请检查: 1) API Key是否正确 2) 模型名称是否有效(%s) 3) 图片是否可访问", model)
	}

	// 自动计费
	if bc := billing.FromContext(ctx); bc != nil && usage != nil {
		billing.RecordVision(bc.UserID, "ali", model, bc.Operation, usage, bc.Meta)
	}

	return fullContent.String(), usage, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (a *aliBiz) GetFileUploadLease(fileName string) (string, map[string]string, string, error) {
	return a.bailianClient.GetLease(fileName)
}

func (a *aliBiz) AddFile(leaseId string) (string, error) {
	return a.bailianClient.ConfirmFile(leaseId)
}

func (a *aliBiz) GetPromptManager() *PromptManager {
	return a.pm
}
