package volc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"numind-server/internal/numind/store"
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
	request, _ := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	// 解析返回
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("API返回解析失败: %v, 原始: %s", err, string(respBody))
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

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viper.GetString("volc.api_key"))

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.C(ctx).Debugw("HTTP请求失败", "error", err.Error())
		return "", fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.C(ctx).Debugw("API调用失败", "status_code", resp.StatusCode, "response", string(respBody))
		return "", fmt.Errorf("API调用失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.C(ctx).Debugw("读取响应体失败", "error", err.Error())
		return "", fmt.Errorf("读取响应体失败: %w", err)
	}

	log.C(ctx).Debugw("API响应", "response", string(respBody))

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
		log.C(ctx).Debugw("JSON解析失败", "error", err.Error(), "response", string(respBody))
		return "", fmt.Errorf("JSON解析失败: %w, 响应: %s", err, string(respBody))
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
