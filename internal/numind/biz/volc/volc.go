package volc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"numind-server/internal/numind/store"
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
