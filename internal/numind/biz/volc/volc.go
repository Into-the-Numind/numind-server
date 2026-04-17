package volc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type OpenAIConfig struct {
	APIKey      string
	APIBase     string
	Model       string
	Temperature float64
	MaxTokens   int
}

type VolcBiz interface {
	// GenerateArticleContent 仅供本地 CLI 测试工具使用，无线上路由；
	// 如需扩展为线上接口，应走 aiservice Gateway 路径（见实现注释）。
	GenerateArticleContent(ctx context.Context, content string, contentType string, maxLength int, cfg *OpenAIConfig, prompt string) (string, error)
	// 新增流式文本生成方法，与ali的QianwenTextStream保持一致
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, *billing.TokenUsage, error)
	// 火山方舟 Embedding 向量化
	DoubaoEmbedding(ctx context.Context, text string) ([]float32, *billing.EmbeddingUsage, error)
	// StreamChat 真正的流式聊天，通过回调函数逐 token 或思维链内容推送
	// onEvent: 收到事件内容时调用，event 类型为 "thinking" 或 "message"
	// 返回: 完整内容（所有 token 拼接）、usage 和错误
	StreamChat(ctx context.Context, messages []map[string]interface{}, maxTokens int, temperature float64, deepThinking bool, onEvent func(event string, token string) error) (string, *billing.TokenUsage, error)
	// VisionAnalyze 调用火山方舟视觉模型分析图片
	VisionAnalyze(ctx context.Context, imageURL string, prompt string, model string, maxTokens int, reasoningEffort string) (string, *billing.TokenUsage, error)
	// VisionAnalyzeStream 流式分析图片，支持思维链输出
	VisionAnalyzeStream(ctx context.Context, imageURL string, prompt string, model string, maxTokens int, reasoningEffort string, onToken func(token string) error) (string, *billing.TokenUsage, error)

	// ChatWithModel 非流式聊天，支持指定模型
	ChatWithModel(ctx context.Context, messages []map[string]interface{}, model string, maxTokens int, temperature float64) (string, *billing.TokenUsage, error)
	// StreamChatWithModel 流式聊天，支持指定模型和思考程度
	// reasoningEffort: doubao-seed 系列使用 "minimal"/"low"/"medium"/"high"；其他模型非空时开启 thinking
	StreamChatWithModel(ctx context.Context, messages []map[string]interface{}, model string, maxTokens int, temperature float64, reasoningEffort string, onEvent func(event string, token string) error) (string, *billing.TokenUsage, error)
}

type volcBiz struct {
	ds     store.IStore
	client *httpclient.Client
}

func NewVolcBiz(ds store.IStore) VolcBiz {
	return &volcBiz{
		ds:     ds,
		client: httpclient.NewClientFromConfig("volc"),
	}
}

// GenerateArticleContent 通用内容生成函数
// 仅供 cmd/main.go 本地 CLI 测试工具使用，无线上路由。因此无 billing 记账逻辑
// 并非遗漏 —— 本地测试不应扣用户额度。如需扩展为线上接口，应走
// aiservice Gateway 路径（会统一处理 billing + tracing + fallback）。
func (v *volcBiz) GenerateArticleContent(ctx context.Context, content string, contentType string, maxLength int, cfg *OpenAIConfig, prompt string) (string, error) {
	if cfg == nil {
		cfg = &OpenAIConfig{
			APIKey:      viper.GetString("volc.api_key"),
			APIBase:     viper.GetString("volc.base_url"),
			Model:       viper.GetString("volc.model"), // Gateway 已接管；此为非 Gateway 调用路径兜底
			Temperature: viper.GetFloat64("volc.temperature"),
			MaxTokens:   viper.GetInt("volc.tokens"),
		}
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://ark.cn-beijing.volces.com/api/v3"
	}
	if cfg.Model == "" {
		cfg.Model = "deepseek-v3-250324"
	}
	if cfg.Temperature == 0 {
		cfg.Temperature = 0.5
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 2000
	}

	var systemPrompt string
	var temperature float64
	var maxTokens int

	if contentType == "summary" {
		systemPrompt = prompt
		if systemPrompt == "" {
			systemPrompt = fmt.Sprintf("你是一个专业的文章摘要生成器。请生成不超过%d字的中文摘要，准确概括文章的核心内容。", maxLength)
		}
		temperature = cfg.Temperature
		maxTokens = maxLength * 2
	} else {
		systemPrompt = prompt
		if systemPrompt == "" {
			systemPrompt = `你是一个专业的文章标注系统。请分析以下文章，并添加适当的标注：\n\n1. 用 {{{ }}} 包围文章中的重要段落和关键信息（黄色高亮）\n2. 用 [[[ ]]] 包围文章中的次要但有用的信息（下划线）\n\n请保持原文的完整性，只添加标注符号。不要修改原文内容，不要添加任何解释或评论。\n返回带有标注的完整文章。`
		}
		temperature = 0.3
		maxTokens = 0 // 不限制
	}

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": content},
	}

	// 构建API参数
	params := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": temperature,
	}
	if maxTokens > 0 {
		params["max_tokens"] = maxTokens
	}

	bodyBytes, _ := json.Marshal(params)
	url := cfg.APIBase + "/chat/completions"

	// 使用 persistent client
	client := v.client

	// 创建请求
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + cfg.APIKey,
		},
		RetryPolicy: &httpclient.RetryPolicy{
			MaxRetries:   3,
			RetryDelay:   1 * time.Second,
			RetryBackoff: 2.0,
		},
	}

	// 使用新的JSON响应处理方法，从根源上解决JSON解析失败问题
	respBody, err := client.DoWithJSONResponse(httpReq)
	if err != nil {
		return "", fmt.Errorf("HTTP请求或JSON处理失败: %w", err)
	}

	// 解析返回
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		// 使用高级JSON修复引擎
		extractor := httpclient.NewAdvancedJSONExtractor()
		repairedBody, repairErr := extractor.ExtractValidJSON(respBody)

		if repairErr != nil {
			return "", fmt.Errorf("JSON解析和修复都失败: 原始错误=%w, 修复错误=%v, 响应长度=%d", err, repairErr, len(respBody))
		}

		// 尝试解析修复后的JSON
		if err := json.Unmarshal(repairedBody, &result); err != nil {
			return "", fmt.Errorf("修复后JSON解析失败: %w, 修复后响应长度: %d", err, len(repairedBody))
		}
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("API无返回内容: %s", string(respBody))
	}
	return result.Choices[0].Message.Content, nil
}

// VolcTextStream 火山引擎文字模型API（兼容模式，非流式）
func (v *volcBiz) VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, *billing.TokenUsage, error) {
	url := viper.GetString("volc.base_url") + "/chat/completions"
	bodyMap := map[string]interface{}{
		"model":       viper.GetString("volc.model"), // Gateway 已接管；此为非 Gateway 调用路径兜底
		"messages":    messages,
		"max_tokens":  maxTokens,
		"temperature": temperature,
		// 移除stream参数，使用非流式调用
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	// 添加调试日志
	log.C(ctx).Debugw("调用volc API", "url", url, "request_params", string(bodyBytes))

	// 使用 persistent client
	client := v.client

	// 创建请求
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + viper.GetString("volc.api_key"),
			"User-Agent":    "numind-server/1.0",
		},
		RetryPolicy: &httpclient.RetryPolicy{
			MaxRetries:   viper.GetInt("volc.max_retries"),
			RetryDelay:   1 * time.Second,
			RetryBackoff: 2.0,
		},
	}

	log.C(ctx).Debugw("开始HTTP请求", "timeout", "120s")

	// 使用新的JSON响应处理方法，从根源上解决JSON解析失败问题
	respBody, err := client.DoWithJSONResponse(httpReq)
	if err != nil {
		log.C(ctx).Errorw("HTTP请求或JSON处理失败", "error", err.Error())
		return "", nil, fmt.Errorf("HTTP请求或JSON处理失败: %w", err)
	}

	// 检查响应完整性
	respLength := len(respBody)
	log.C(ctx).Debugw("处理后的响应体长度", "length", respLength)

	// 检查响应是否为空
	if respLength == 0 {
		log.C(ctx).Errorw("响应体为空")
		return "", nil, fmt.Errorf("API响应体为空")
	}

	previewLength := respLength
	if previewLength > 500 {
		previewLength = 500
	}
	log.C(ctx).Debugw("API响应", "response_length", respLength, "response_preview", string(respBody[:previewLength]))

	// 解析响应JSON
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *billing.TokenUsage `json:"usage,omitempty"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		log.C(ctx).Warnw("JSON解析失败，尝试使用高级修复引擎", "error", err.Error())

		// 使用高级JSON修复引擎
		extractor := httpclient.NewAdvancedJSONExtractor()
		repairedBody, repairErr := extractor.ExtractValidJSON(respBody)

		if repairErr != nil {
			log.C(ctx).Errorw("JSON修复失败", "repair_error", repairErr.Error(), "original_error", err.Error())
			return "", nil, fmt.Errorf("JSON解析和修复都失败: 原始错误=%w, 修复错误=%v, 响应长度=%d", err, repairErr, respLength)
		}

		// 尝试解析修复后的JSON
		if err := json.Unmarshal(repairedBody, &result); err != nil {
			log.C(ctx).Errorw("修复后JSON解析仍然失败", "error", err.Error(), "repaired_length", len(repairedBody))
			// 输出修复后的响应的前后部分用于调试
			debugLen := 200
			if len(repairedBody) > debugLen*2 {
				log.C(ctx).Debugw("修复后响应调试信息",
					"repaired_start", string(repairedBody[:debugLen]),
					"repaired_end", string(repairedBody[len(repairedBody)-debugLen:]))
			} else {
				log.C(ctx).Debugw("修复后响应", "repaired_response", string(repairedBody))
			}
			return "", nil, fmt.Errorf("修复后JSON解析失败: %w, 修复后响应长度: %d", err, len(repairedBody))
		}
		log.C(ctx).Infow("JSON解析成功（经过高级修复）", "repaired_length", len(repairedBody))
	}

	// 检查是否有错误
	if result.Error != nil {
		log.C(ctx).Debugw("API返回错误", "error_code", result.Error.Code, "error_message", result.Error.Message, "error_type", result.Error.Type)
		return "", nil, fmt.Errorf("API错误: %s - %s", result.Error.Code, result.Error.Message)
	}

	// 检查是否有choices
	if len(result.Choices) == 0 {
		log.C(ctx).Debugw("没有返回choices")
		return "", nil, fmt.Errorf("API未返回choices")
	}

	// 提取内容
	content := result.Choices[0].Message.Content
	if content == "" {
		log.C(ctx).Debugw("返回内容为空")
		return "", nil, fmt.Errorf("API返回内容为空")
	}

	if result.Usage != nil {
		result.Usage.Normalize()
	}

	// 自动计费
	if !aiservice.ShouldSkipLegacyBilling(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && result.Usage != nil {
			billing.RecordLLM(bc.UserID, "volc", viper.GetString("volc.model"), bc.Operation, result.Usage, bc.Meta)
		}
	}

	// Langfuse generation 追踪
	if tc := langfuse.FromContext(ctx); tc != nil {
		genID := langfuse.SpanID()
		opts := []langfuse.GenOption{
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("volc-text"),
			langfuse.WithGenModel(viper.GetString("volc.model")),
			langfuse.WithGenOutput(content),
		}
		if result.Usage != nil {
			opts = append(opts, langfuse.WithGenUsage(result.Usage.PromptTokens, result.Usage.CompletionTokens))
		}
		langfuse.CreateGeneration(tc.TraceID, genID, opts...)
		langfuse.EndGeneration(genID)
	}

	log.C(ctx).Debugw("成功获取内容", "content_length", len(content))
	return content, result.Usage, nil
}

// DoubaoEmbedding 调用火山方舟 Embedding API 获取文本向量
// 使用 doubao-embedding-vision 模型，支持多模态输入
func (v *volcBiz) DoubaoEmbedding(ctx context.Context, text string) ([]float32, *billing.EmbeddingUsage, error) {
	// 构建 API URL
	baseURL := viper.GetString("volc.base_url")
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	url := baseURL + "/embeddings/multimodal"

	// 读取 embedding 模型配置
	embeddingModel := viper.GetString("volc.embedding.model")
	if embeddingModel == "" {
		embeddingModel = "doubao-embedding-vision-250615"
	}

	// 读取 embedding 专用 API Key（如果没有则回退到通用 api_key）
	apiKey := viper.GetString("volc.embedding.api_key")
	if apiKey == "" {
		apiKey = viper.GetString("volc.api_key")
	}

	// 构建请求体（多模态格式，仅使用 text 类型）
	bodyMap := map[string]interface{}{
		"model": embeddingModel,
		"input": []map[string]interface{}{
			{
				"type": "text",
				"text": text,
			},
		},
	}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	log.C(ctx).Debugw("调用火山Embedding API", "url", url, "model", embeddingModel, "text_length", len(text))

	// 创建请求
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + apiKey,
		},
		RetryPolicy: &httpclient.RetryPolicy{
			MaxRetries:   3,
			RetryDelay:   1 * time.Second,
			RetryBackoff: 2.0,
		},
	}

	// 发送请求
	respBody, err := v.client.DoWithJSONResponse(httpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("Embedding API请求失败: %w", err)
	}

	// 解析响应
	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Usage *struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage,omitempty"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, nil, fmt.Errorf("解析Embedding响应失败: %w, 响应内容: %s", err, string(respBody[:min(len(respBody), 500)]))
	}

	// 检查错误
	if result.Error != nil {
		return nil, nil, fmt.Errorf("Embedding API错误: %s - %s", result.Error.Code, result.Error.Message)
	}

	// 检查数据
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, nil, fmt.Errorf("Embedding API未返回向量数据")
	}

	// 转换 float64 到 float32
	embedding := result.Data[0].Embedding
	vector := make([]float32, len(embedding))
	for i, v := range embedding {
		vector[i] = float32(v)
	}

	var embUsage *billing.EmbeddingUsage
	if result.Usage != nil {
		embUsage = &billing.EmbeddingUsage{TotalTokens: result.Usage.TotalTokens}
	}

	// 自动计费
	if !aiservice.ShouldSkipLegacyBilling(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && embUsage != nil {
			billing.RecordEmbedding(bc.UserID, "volc", embeddingModel, bc.Operation, embUsage, bc.Meta)
		}
	}

	log.C(ctx).Debugw("Embedding成功", "vector_dim", len(vector))
	return vector, embUsage, nil
}

// StreamChat 真正的流式聊天方法
// 通过回调函数 onEvent 逐 token 或思维链内容推送
// 火山方舟 API 使用 SSE 格式，每行格式为 "data: {json}\n\n"
func (v *volcBiz) StreamChat(ctx context.Context, messages []map[string]interface{}, maxTokens int, temperature float64, deepThinking bool, onEvent func(event string, token string) error) (string, *billing.TokenUsage, error) {
	url := viper.GetString("volc.base_url") + "/chat/completions"

	thinkingType := "disabled"
	if deepThinking {
		thinkingType = "enabled"
	}

	bodyMap := map[string]interface{}{
		"model":       viper.GetString("volc.model"), // Gateway 已接管；此为非 Gateway 调用路径兜底
		"messages":    messages,
		"max_tokens":  maxTokens,
		"temperature": temperature,
		"stream":      true, // 启用流式传输
		"thinking": map[string]interface{}{
			"type": thinkingType, // 使用参数控制深度思考
		},
		"stream_options": map[string]interface{}{
			"include_usage": true, // 包含 token 使用统计
		},
	}
	// 如果是推理系列模型（doubao-seed），设置思维链预算和较大的输出上限
	if modelName, ok := bodyMap["model"].(string); ok && strings.Contains(modelName, "doubao-seed") {
		// 思考过程限制 2000 token，总输出放宽至 32k（提示词会控制实际输出）
		bodyMap["thinking"] = map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": 2000,
		}
		bodyMap["max_completion_tokens"] = 32768
		delete(bodyMap, "max_tokens")
		// 移除互斥或较低精度的参数
		delete(bodyMap, "reasoning_effort")
	}

	bodyBytes, _ := json.Marshal(bodyMap)

	log.C(ctx).Debugw("调用volc流式API", "url", url, "stream", true)

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viper.GetString("volc.api_key"))
	req.Header.Set("Accept", "text/event-stream")

	// 使用带处理响应头的 HTTP 客户端
	client := &http.Client{
		Timeout: 0, // 流式传输由 context 控制
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("API返回错误状态码: %d, body: %s", resp.StatusCode, string(body))
	}

	// 逐行读取 SSE 响应
	var fullContent strings.Builder
	var thinkingContent strings.Builder
	var usage *billing.TokenUsage
	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fullContent.String(), usage, fmt.Errorf("读取响应失败: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		// 提取 usage（最后一个 chunk 包含 usage）
		if u := billing.ExtractUsageFromSSEData(data); u != nil {
			usage = u
		}

		// 使用通用的 map 解析，因为可能有多种字段（content, reasoning_content）
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.C(ctx).Warnw("解析SSE JSON失败", "data", data, "error", err)
			continue
		}

		// 检查是否有错误
		if errObj, ok := chunk["error"].(map[string]interface{}); ok {
			return fullContent.String(), usage, fmt.Errorf("API流式错误: %v", errObj["message"])
		}

		// 提取 choices
		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}

		choice := choices[0].(map[string]interface{})
		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		// 1. 处理思维链内容 (reasoning_content)
		if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
			thinkingContent.WriteString(rc)
			if onEvent != nil {
				if err := onEvent("thinking", rc); err != nil {
					return fullContent.String(), usage, err
				}
			}
		}

		// 2. 处理普通回答内容 (content)
		if content, ok := delta["content"].(string); ok && content != "" {
			fullContent.WriteString(content)
			if onEvent != nil {
				if err := onEvent("message", content); err != nil {
					return fullContent.String(), usage, err
				}
			}
		}

		// 3. 检查结束
		if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
			if finishReason == "stop" {
				break
			}
		}
	}

	// 自动计费
	if !aiservice.ShouldSkipLegacyBilling(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && usage != nil {
			billing.RecordLLM(bc.UserID, "volc", viper.GetString("volc.model"), bc.Operation, usage, bc.Meta)
		}
	}

	// Langfuse generation 追踪
	if tc := langfuse.FromContext(ctx); tc != nil {
		genID := langfuse.SpanID()
		opts := []langfuse.GenOption{
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("volc-stream"),
			langfuse.WithGenModel(viper.GetString("volc.model")),
			langfuse.WithGenOutput(fullContent.String()),
		}
		if usage != nil {
			opts = append(opts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
		}
		langfuse.CreateGeneration(tc.TraceID, genID, opts...)
		langfuse.EndGeneration(genID)
	}

	log.C(ctx).Debugw("流式聊天完成", "content_len", fullContent.Len(), "thinking_len", thinkingContent.Len())
	return fullContent.String(), usage, nil
}

// VisionAnalyze 调用火山方舟视觉模型分析图片
func (v *volcBiz) VisionAnalyze(ctx context.Context, imageURL string, prompt string, model string, maxTokens int, reasoningEffort string) (string, *billing.TokenUsage, error) {
	// 设置默认模型
	if model == "" {
		model = "doubao-seed-1-8-251228"
	}

	baseURL := viper.GetString("volc.base_url")
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	url := baseURL + "/chat/completions"

	// 构建多模态消息格式
	messages := []map[string]interface{}{
		{
			"role": "user",
			"content": []map[string]interface{}{
				{
					"type": "image_url",
					"image_url": map[string]string{
						"url": imageURL,
					},
				},
				{
					"type": "text",
					"text": prompt,
				},
			},
		},
	}

	bodyMap := map[string]interface{}{
		"model":    model,
		"messages": messages,
	}

	// 推理模型特殊参数
	if strings.Contains(model, "doubao-seed") {
		// 设置思考程度: minimal, low, medium, high
		if reasoningEffort == "" {
			reasoningEffort = "medium"
		}
		bodyMap["reasoning_effort"] = reasoningEffort
		bodyMap["max_completion_tokens"] = 65535
	} else {
		if maxTokens > 0 {
			bodyMap["max_tokens"] = maxTokens
		} else {
			bodyMap["max_tokens"] = 2000 // 视觉模型默认给较多 token
		}
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	log.C(ctx).Debugw("调用火山方舟视觉模型", "url", url, "model", model, "image_url", imageURL)

	// 创建请求
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + viper.GetString("volc.api_key"),
		},
		RetryPolicy: &httpclient.RetryPolicy{
			MaxRetries:   3,
			RetryDelay:   2 * time.Second,
			RetryBackoff: 2.0,
		},
	}

	// 发送请求
	respBody, err := v.client.DoWithJSONResponse(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("视觉模型API请求失败: %w", err)
	}

	// 解析响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *billing.TokenUsage `json:"usage,omitempty"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil, fmt.Errorf("解析视觉模型响应失败: %w", err)
	}

	// 检查错误
	if result.Error != nil {
		return "", nil, fmt.Errorf("视觉模型API错误: %s - %s", result.Error.Code, result.Error.Message)
	}

	// 检查是否有choices
	if len(result.Choices) == 0 {
		return "", nil, fmt.Errorf("视觉模型API未返回结果")
	}

	if result.Usage != nil {
		result.Usage.Normalize()
	}

	// 自动计费
	if !aiservice.ShouldSkipLegacyBilling(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && result.Usage != nil {
			billing.RecordVision(bc.UserID, "volc", model, bc.Operation, result.Usage, bc.Meta)
		}
	}

	content := result.Choices[0].Message.Content

	// Langfuse generation 追踪
	if tc := langfuse.FromContext(ctx); tc != nil {
		genID := langfuse.SpanID()
		opts := []langfuse.GenOption{
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("volc-vision"),
			langfuse.WithGenModel(model),
			langfuse.WithGenOutput(content),
		}
		if result.Usage != nil {
			opts = append(opts, langfuse.WithGenUsage(result.Usage.PromptTokens, result.Usage.CompletionTokens))
		}
		langfuse.CreateGeneration(tc.TraceID, genID, opts...)
		langfuse.EndGeneration(genID)
	}

	log.C(ctx).Infow("视觉模型分析完成", "content_length", len(content))
	return content, result.Usage, nil
}

// VisionAnalyzeStream 流式分析图片，支持思维链输出
func (v *volcBiz) VisionAnalyzeStream(ctx context.Context, imageURL string, prompt string, model string, maxTokens int, reasoningEffort string, onToken func(token string) error) (string, *billing.TokenUsage, error) {
	// 设置默认模型
	if model == "" {
		model = "doubao-seed-1-8-251228"
	}

	baseURL := viper.GetString("volc.base_url")
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	url := baseURL + "/chat/completions"

	// 构建多模态消息格式
	messages := []map[string]interface{}{
		{
			"role": "user",
			"content": []map[string]interface{}{
				{
					"type": "image_url",
					"image_url": map[string]string{
						"url": imageURL,
					},
				},
				{
					"type": "text",
					"text": prompt,
				},
			},
		},
	}

	bodyMap := map[string]interface{}{
		"model":    model,
		"messages": messages,
		"stream":   true,
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	// 推理模型特殊参数
	if strings.Contains(model, "doubao-seed") {
		if reasoningEffort == "" {
			reasoningEffort = "medium"
		}
		bodyMap["reasoning_effort"] = reasoningEffort
		bodyMap["max_completion_tokens"] = 65535
	} else {
		if maxTokens > 0 {
			bodyMap["max_tokens"] = maxTokens
		} else {
			bodyMap["max_tokens"] = 2000 // 视觉模型默认给较多 token
		}
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	log.C(ctx).Debugw("调用火山方舟流式视觉模型", "url", url, "model", model)

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viper.GetString("volc.api_key"))
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("API返回错误状态码: %d, body: %s", resp.StatusCode, string(body))
	}

	var fullContent strings.Builder
	var usage *billing.TokenUsage
	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fullContent.String(), usage, fmt.Errorf("读取响应失败: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
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

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if errObj, ok := chunk["error"].(map[string]interface{}); ok {
			return fullContent.String(), usage, fmt.Errorf("API流式错误: %v", errObj["message"])
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}

		choice := choices[0].(map[string]interface{})
		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		// 处理普通回答内容 (content)
		if content, ok := delta["content"].(string); ok && content != "" {
			fullContent.WriteString(content)
			if onToken != nil {
				if err := onToken(content); err != nil {
					return fullContent.String(), usage, err
				}
			}
		}
	}

	// 自动计费
	if !aiservice.ShouldSkipLegacyBilling(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && usage != nil {
			billing.RecordVision(bc.UserID, "volc", model, bc.Operation, usage, bc.Meta)
		}
	}

	log.C(ctx).Debugw("流式视觉分析完成", "content_len", fullContent.Len())
	return fullContent.String(), usage, nil
}

// ChatWithModel 非流式聊天，支持指定模型
func (v *volcBiz) ChatWithModel(ctx context.Context, messages []map[string]interface{}, model string, maxTokens int, temperature float64) (string, *billing.TokenUsage, error) {
	if model == "" {
		// Gateway 已接管模型选择；此处为未经 Gateway 的直接调用兜底
		model = viper.GetString("volc.model")
	}
	if model == "" {
		model = "deepseek-v3-250324"
	}

	baseURL := viper.GetString("volc.base_url")
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	url := baseURL + "/chat/completions"

	bodyMap := map[string]interface{}{
		"model":       model,
		"messages":    messages,
		"temperature": temperature,
	}

	if strings.Contains(model, "doubao-seed") {
		bodyMap["thinking"] = map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": 2000,
		}
		bodyMap["max_completion_tokens"] = 32768
	} else if maxTokens > 0 {
		bodyMap["max_tokens"] = maxTokens
	}

	bodyBytes, _ := json.Marshal(bodyMap)

	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + viper.GetString("volc.api_key"),
		},
	}

	respBody, err := v.client.DoWithJSONResponse(httpReq)
	if err != nil {
		return "", nil, err
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *billing.TokenUsage `json:"usage,omitempty"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil, err
	}

	if result.Error != nil {
		return "", nil, fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", nil, fmt.Errorf("no choices returned")
	}

	if result.Usage != nil {
		result.Usage.Normalize()
	}

	// 自动计费
	if !aiservice.ShouldSkipLegacyBilling(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && result.Usage != nil {
			billing.RecordLLM(bc.UserID, "volc", model, bc.Operation, result.Usage, bc.Meta)
		}
	}

	return result.Choices[0].Message.Content, result.Usage, nil
}

// StreamChatWithModel 流式聊天，支持指定模型和思考程度
func (v *volcBiz) StreamChatWithModel(ctx context.Context, messages []map[string]interface{}, model string, maxTokens int, temperature float64, reasoningEffort string, onEvent func(event string, token string) error) (string, *billing.TokenUsage, error) {
	if model == "" {
		// Gateway 已接管模型选择；此处为未经 Gateway 的直接调用兜底
		model = viper.GetString("volc.model")
	}

	bodyMap := map[string]interface{}{
		"model":       model,
		"messages":    messages,
		"temperature": temperature,
		"stream":      true,
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}

	// doubao-seed 系列模型使用 reasoning_effort 参数控制思考程度
	// minimal: 不思考; low/medium/high: 开启思考并控制程度
	if strings.Contains(model, "doubao-seed") {
		if reasoningEffort == "" {
			reasoningEffort = "medium"
		}
		bodyMap["reasoning_effort"] = reasoningEffort
		bodyMap["max_completion_tokens"] = 65535
		delete(bodyMap, "max_tokens")
		delete(bodyMap, "thinking")
	} else {
		if maxTokens <= 0 {
			maxTokens = 8192
		}
		bodyMap["max_tokens"] = maxTokens
		thinkingType := "disabled"
		if reasoningEffort != "" && reasoningEffort != "minimal" {
			thinkingType = "enabled"
		}
		bodyMap["thinking"] = map[string]interface{}{
			"type": thinkingType,
		}
	}

	bodyBytes, _ := json.Marshal(bodyMap)

	baseURL := viper.GetString("volc.base_url")
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	// Log request details
	log.C(ctx).Infow("StreamChatWithModel request", "url", baseURL+"/chat/completions", "model", model, "body_map", bodyMap)

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viper.GetString("volc.api_key"))

	client := &http.Client{
		Timeout: 300 * time.Second, // 增加超时时间以适应 deeply thinking
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("Volc API error: %d - %s", resp.StatusCode, string(body))
	}

	var fullContent strings.Builder
	var thinkingContent strings.Builder
	var usage *billing.TokenUsage
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fullContent.String(), usage, err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		log.C(ctx).Debugw("StreamChatWithModel raw line", "line", line)

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

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			log.C(ctx).Warnw("StreamChatWithModel unmarshal failed", "data", data, "error", err)
			continue
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			// Check if there is an error field at root
			if errObj, ok := chunk["error"].(map[string]interface{}); ok {
				return fullContent.String(), usage, fmt.Errorf("API Error: %v", errObj)
			}
			log.C(ctx).Warnw("StreamChatWithModel no choices", "data", data)
			continue
		}

		choice := choices[0].(map[string]interface{})
		if delta, ok := choice["delta"].(map[string]interface{}); ok {
			if rc, ok := delta["reasoning_content"].(string); ok && rc != "" {
				thinkingContent.WriteString(rc)
				if onEvent != nil {
					if err := onEvent("thinking", rc); err != nil {
						return fullContent.String(), usage, err
					}
				}
			}

			if content, ok := delta["content"].(string); ok && content != "" {
				fullContent.WriteString(content)
				if onEvent != nil {
					if err := onEvent("message", content); err != nil {
						return fullContent.String(), usage, err
					}
				}
			}
		}

		if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
			log.C(ctx).Infow("StreamChatWithModel finish_reason", "reason", finishReason)
		}
	}

	// 自动计费
	if !aiservice.ShouldSkipLegacyBilling(ctx) {
		if bc := billing.FromContext(ctx); bc != nil && usage != nil {
			billing.RecordLLM(bc.UserID, "volc", model, bc.Operation, usage, bc.Meta)
		}
	}

	// Langfuse generation 追踪
	if tc := langfuse.FromContext(ctx); tc != nil {
		genID := langfuse.SpanID()
		opts := []langfuse.GenOption{
			langfuse.WithGenParent(tc.ParentObservationID),
			langfuse.WithGenName("volc-stream-" + model),
			langfuse.WithGenModel(model),
			langfuse.WithGenOutput(fullContent.String()),
		}
		if usage != nil {
			opts = append(opts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
		}
		langfuse.CreateGeneration(tc.TraceID, genID, opts...)
		langfuse.EndGeneration(genID)
	}

	result := fullContent.String()
	log.C(ctx).Infow("StreamChatWithModel completed", "content_len", len(result), "thinking_len", thinkingContent.Len())

	if result == "" {
		if thinkingContent.Len() > 0 {
			log.C(ctx).Warnw("StreamChatWithModel response empty but has thinking content", "thinking_len", thinkingContent.Len())
		} else {
			log.C(ctx).Warnw("StreamChatWithModel returned empty content and empty thinking")
		}
	}

	return result, usage, nil
}
