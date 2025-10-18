package volc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
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
	GenerateArticleContent(content string, contentType string, maxLength int, cfg *OpenAIConfig, prompt string) (string, error)
	// 新增流式文本生成方法，与ali的QianwenTextStream保持一致
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error)
}

type volcBiz struct {
	ds store.IStore
}

func NewVolcBiz(ds store.IStore) VolcBiz {
	return &volcBiz{ds: ds}
}

// GenerateArticleContent 通用内容生成函数
func (v *volcBiz) GenerateArticleContent(content string, contentType string, maxLength int, cfg *OpenAIConfig, prompt string) (string, error) {
	if cfg == nil {
		cfg = &OpenAIConfig{
			APIKey:      viper.GetString("volc.api_key"),
			APIBase:     viper.GetString("volc.base_url"),
			Model:       viper.GetString("volc.model"),
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

	// 使用优化的HTTP客户端
	client := httpclient.NewClientFromConfig("volc")
	defer client.Close()

	// 创建请求
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: context.Background(),
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
func (v *volcBiz) VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error) {
	url := viper.GetString("volc.base_url") + "/chat/completions"
	bodyMap := map[string]interface{}{
		"model":       viper.GetString("volc.model"),
		"messages":    messages,
		"max_tokens":  maxTokens,
		"temperature": temperature,
		// 移除stream参数，使用非流式调用
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	// 添加调试日志
	log.C(ctx).Debugw("调用volc API", "url", url, "request_params", string(bodyBytes))

	// 使用优化的HTTP客户端
	client := httpclient.NewClientFromConfig("volc")
	defer client.Close()

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
		return "", fmt.Errorf("HTTP请求或JSON处理失败: %w", err)
	}

	// 检查响应完整性
	respLength := len(respBody)
	log.C(ctx).Debugw("处理后的响应体长度", "length", respLength)

	// 检查响应是否为空
	if respLength == 0 {
		log.C(ctx).Errorw("响应体为空")
		return "", fmt.Errorf("API响应体为空")
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
			return "", fmt.Errorf("JSON解析和修复都失败: 原始错误=%w, 修复错误=%v, 响应长度=%d", err, repairErr, respLength)
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
			return "", fmt.Errorf("修复后JSON解析失败: %w, 修复后响应长度: %d", err, len(repairedBody))
		}
		log.C(ctx).Infow("JSON解析成功（经过高级修复）", "repaired_length", len(repairedBody))
	}

	// 检查是否有错误
	if result.Error != nil {
		log.C(ctx).Debugw("API返回错误", "error_code", result.Error.Code, "error_message", result.Error.Message, "error_type", result.Error.Type)
		return "", fmt.Errorf("API错误: %s - %s", result.Error.Code, result.Error.Message)
	}

	// 检查是否有choices
	if len(result.Choices) == 0 {
		log.C(ctx).Debugw("没有返回choices")
		return "", fmt.Errorf("API未返回choices")
	}

	// 提取内容
	content := result.Choices[0].Message.Content
	if content == "" {
		log.C(ctx).Debugw("返回内容为空")
		return "", fmt.Errorf("API返回内容为空")
	}

	log.C(ctx).Debugw("成功获取内容", "content_length", len(content))
	return content, nil
}

// 注意：原来的cleanJSONResponse函数已被弃用，现在使用更强大的httpclient.AdvancedJSONExtractor
