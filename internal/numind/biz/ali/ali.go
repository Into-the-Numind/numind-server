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
	"os"
	"strings"
	"time"

	"numind-server/internal/pkg/httpclient"

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
	QianwenTextStream(messages []map[string]string, maxTokens int, temperature float64) (string, error)
	WanxiangImageStream(prompt string, style string, size string) (string, error)
	WanxiangImageAsync(prompt, style, size string) (string, error)
	StableDiffusionImageAsync(prompt, size string) (string, error)
	GetPromptManager() *PromptManager
}

type aliBiz struct {
	ds store.IStore
	pm *PromptManager
}

func NewAliBiz(ds store.IStore) AliBiz {
	return &aliBiz{
		ds: ds,
		pm: NewPromptManager(),
	}
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
func (a *aliBiz) QianwenTextStream(messages []map[string]string, maxTokens int, temperature float64) (string, error) {
	url := "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	bodyMap := map[string]interface{}{
		"model":       viper.GetString("ali.text.model"),
		"messages":    messages,
		"max_tokens":  maxTokens,
		"temperature": temperature,
		"stream":      true,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+viper.GetString("ali.text.api_key"))
	req.Header.Set("User-Agent", "numind-server/1.0")

	// 使用优化的HTTP客户端
	client := httpclient.NewClientFromConfig("ali.text")
	defer client.Close()

	// 创建请求
	httpReq := &httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewBuffer(bodyBytes),
		Context: context.Background(),
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + viper.GetString("ali.text.api_key"),
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
				return "", fmt.Errorf("网络超时错误: %w", err)
			} else if netErr.Temporary() {
				return "", fmt.Errorf("临时网络错误: %w", err)
			}
		}
		return "", fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	var result strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				//fmt.Println(m)
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
		return "", err
	}
	return result.String(), nil
}

// 万象（图像模型）流式API（官方接口）
func (a *aliBiz) WanxiangImageStream(prompt string, style string, size string) (string, error) {
	url := "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"
	bodyMap := map[string]interface{}{
		"model": "wanx2.0-t2i-turbo",
		"input": map[string]interface{}{
			"prompt": prompt,
			"style":  style,
			"size":   size,
		},
		"stream": true,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-4b081bdaaa14454ca19d1ed5d031cd10")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var imgUrl string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				fmt.Println(m)
				if output, ok := m["output"].(map[string]interface{}); ok {
					if results, ok := output["results"].([]interface{}); ok && len(results) > 0 {
						if resultMap, ok := results[0].(map[string]interface{}); ok {
							if url, ok := resultMap["url"].(string); ok {
								imgUrl = url
							}
						}
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if imgUrl == "" {
		return "", fmt.Errorf("未获取到图片URL")
	}
	return imgUrl, nil
}

// WanxiangImageAsync 异步生成图片，自动轮询获取结果
func (a *aliBiz) WanxiangImageAsync(prompt, style, size string) (string, error) {

	model := viper.GetString("ali.image.model")
	apiKey := viper.GetString("ali.image.api_key")

	const (
		createURL = "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"
		getURL    = "https://dashscope.aliyuncs.com/api/v1/tasks/"
		maxTries  = 20
		interval  = 3 * time.Second
	)

	// 1. 提交异步任务
	bodyMap := map[string]interface{}{
		"model": model,
		"input": map[string]interface{}{
			"prompt": prompt,
			// "style":  style, // wanx2.1暂不支持style参数，可根据实际API调整
		},
		"parameters": map[string]interface{}{
			"size": size,
			"n":    1,
		},
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	req, _ := http.NewRequest("POST", createURL, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-DashScope-Async", "enable")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var createResp struct {
		Output struct {
			TaskID string `json:"task_id"`
			Status string `json:"task_status"`
		} `json:"output"`
	}
	if err := json.Unmarshal(respBody, &createResp); err != nil {
		return "", fmt.Errorf("提交任务解析失败: %v, 原始: %s", err, string(respBody))
	}
	if createResp.Output.TaskID == "" {
		return "", fmt.Errorf("未获取到任务ID: %s", string(respBody))
	}

	taskID := createResp.Output.TaskID

	// 2. 轮询查询任务状态
	var imgUrl string
	for i := 0; i < maxTries; i++ {
		getReq, _ := http.NewRequest("GET", getURL+taskID, nil)
		getReq.Header.Set("Authorization", "Bearer "+apiKey)
		getResp, err := client.Do(getReq)
		if err != nil {
			return "", err
		}
		getRespBody, _ := io.ReadAll(getResp.Body)
		getResp.Body.Close()

		var getResult struct {
			Output struct {
				TaskStatus string `json:"task_status"`
				Results    []struct {
					URL string `json:"url"`
				} `json:"results"`
			} `json:"output"`
		}
		if err := json.Unmarshal(getRespBody, &getResult); err != nil {
			return "", fmt.Errorf("查询任务解析失败: %v, 原始: %s", err, string(getRespBody))
		}
		if getResult.Output.TaskStatus == "SUCCEEDED" && len(getResult.Output.Results) > 0 {
			imgUrl = getResult.Output.Results[0].URL
			break
		} else if getResult.Output.TaskStatus == "FAILED" {
			return "", fmt.Errorf("图片生成失败: %s", string(getRespBody))
		}
		time.Sleep(interval)
	}
	if imgUrl == "" {
		return "", fmt.Errorf("超时未获取到图片URL，请稍后重试")
	}
	return imgUrl, nil
}

// StableDiffusionImageAsync 使用stable-diffusion-3.5-large-turbo模型异步生成图片
func (a *aliBiz) StableDiffusionImageAsync(prompt, size string) (string, error) {
	apiKey := viper.GetString("ali.stable_diffusion.api_key")
	model := viper.GetString("ali.stable_diffusion.model")

	// 修复size参数格式，确保使用乘号分隔的格式
	// API期望的是 "1024*1024" 格式，而不是 "1024x1024"
	formattedSize := strings.ReplaceAll(size, "x", "*")
	formattedSize = strings.ReplaceAll(formattedSize, "X", "*")

	// 验证并规范化size格式
	if !strings.Contains(formattedSize, "*") {
		// 如果没有分隔符，假设是正方形尺寸
		if formattedSize == "1024" || formattedSize == "" {
			formattedSize = "1024*1024"
		} else {
			formattedSize = formattedSize + "*" + formattedSize
		}
	}

	log.Printf("🎨 开始Stable Diffusion图片生成")
	log.Printf("📋 模型: %s", model)
	log.Printf("📏 原始尺寸: %s", size)
	log.Printf("📏 格式化尺寸: %s", formattedSize)
	log.Printf("🔑 API Key: %s", apiKey[:10]+"..."+apiKey[len(apiKey)-4:])
	log.Printf("💬 提示词: %s", prompt)

	const (
		createURL = "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"
		getURL    = "https://dashscope.aliyuncs.com/api/v1/tasks/"
		maxTries  = 30
		interval  = 3 * time.Second
	)

	// 1. 提交异步任务
	bodyMap := map[string]interface{}{
		"model": model,
		"input": map[string]interface{}{
			"prompt": prompt,
		},
		"parameters": map[string]interface{}{
			"size":  formattedSize,
			"n":     1,
			"steps": 40,
			"cfg":   4.5,
			"seed":  42,
			"shift": 3.0,
		},
	}

	bodyBytes, _ := json.Marshal(bodyMap)
	log.Printf("📤 请求URL: %s", createURL)
	log.Printf("📦 请求体: %s", string(bodyBytes))
	log.Printf("🔍 Size参数详细检查: 原始='%s', 格式化='%s'", size, formattedSize)

	req, _ := http.NewRequest("POST", createURL, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-DashScope-Async", "enable")

	log.Printf("🚀 发送请求...")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("❌ 提交stable-diffusion任务失败: %v", err)
		return "", fmt.Errorf("提交stable-diffusion任务失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	log.Printf("📥 响应状态码: %d", resp.StatusCode)
	log.Printf("📄 响应头: %v", resp.Header)
	log.Printf("📋 响应体: %s", string(respBody))

	var createResp struct {
		Output struct {
			TaskID     string `json:"task_id"`
			TaskStatus string `json:"task_status"`
		} `json:"output"`
		RequestID string `json:"request_id"`
		Code      string `json:"code"`
		Message   string `json:"message"`
	}

	if err := json.Unmarshal(respBody, &createResp); err != nil {
		log.Printf("❌ 提交任务解析失败: %v", err)
		return "", fmt.Errorf("提交任务解析失败: %v, 原始: %s", err, string(respBody))
	}

	// 检查API错误
	if createResp.Code != "" {
		log.Printf("❌ API返回错误: %s - %s", createResp.Code, createResp.Message)
		return "", fmt.Errorf("API错误: %s - %s", createResp.Code, createResp.Message)
	}

	if createResp.Output.TaskID == "" {
		log.Printf("❌ 未获取到任务ID")
		return "", fmt.Errorf("未获取到任务ID: %s", string(respBody))
	}

	taskID := createResp.Output.TaskID
	log.Printf("✅ Stable-diffusion任务已提交，任务ID: %s", taskID)

	// 2. 轮询查询任务状态
	var imgUrl string
	for i := 0; i < maxTries; i++ {
		log.Printf("🔄 第 %d/%d 次查询任务状态...", i+1, maxTries)

		getReq, _ := http.NewRequest("GET", getURL+taskID, nil)
		getReq.Header.Set("Authorization", "Bearer "+apiKey)
		getResp, err := client.Do(getReq)
		if err != nil {
			log.Printf("❌ 查询任务状态失败: %v", err)
			return "", fmt.Errorf("查询任务状态失败: %w", err)
		}
		getRespBody, _ := io.ReadAll(getResp.Body)
		getResp.Body.Close()

		log.Printf("📥 查询响应状态: %d", getResp.StatusCode)
		log.Printf("📋 查询响应体: %s", string(getRespBody))

		var getResult struct {
			Output struct {
				TaskID        string `json:"task_id"`
				TaskStatus    string `json:"task_status"`
				SubmitTime    string `json:"submit_time"`
				ScheduledTime string `json:"scheduled_time"`
				EndTime       string `json:"end_time"`
				Results       []struct {
					URL string `json:"url"`
				} `json:"results"`
				TaskMetrics struct {
					TOTAL     int `json:"TOTAL"`
					SUCCEEDED int `json:"SUCCEEDED"`
					FAILED    int `json:"FAILED"`
				} `json:"task_metrics"`
			} `json:"output"`
			Usage struct {
				ImageCount int `json:"image_count"`
			} `json:"usage"`
			RequestID string `json:"request_id"`
			Code      string `json:"code"`
			Message   string `json:"message"`
		}

		if err := json.Unmarshal(getRespBody, &getResult); err != nil {
			log.Printf("❌ 查询任务解析失败: %v", err)
			return "", fmt.Errorf("查询任务解析失败: %v, 原始: %s", err, string(getRespBody))
		}

		// 检查API错误
		if getResult.Code != "" {
			log.Printf("❌ 查询API返回错误: %s - %s", getResult.Code, getResult.Message)
			return "", fmt.Errorf("查询API错误: %s - %s", getResult.Code, getResult.Message)
		}

		log.Printf("📊 任务状态: %s, 进度: %d/%d", getResult.Output.TaskStatus, getResult.Output.TaskMetrics.SUCCEEDED, getResult.Output.TaskMetrics.TOTAL)

		if getResult.Output.TaskStatus == "SUCCEEDED" && len(getResult.Output.Results) > 0 {
			imgUrl = getResult.Output.Results[0].URL
			log.Printf("🎉 Stable-diffusion图片生成成功: %s", imgUrl)
			break
		} else if getResult.Output.TaskStatus == "FAILED" {
			log.Printf("❌ 图片生成失败")
			return "", fmt.Errorf("stable-diffusion图片生成失败: %s", string(getRespBody))
		} else if getResult.Output.TaskStatus == "UNKNOWN" {
			log.Printf("❌ 任务不存在或状态未知")
			return "", fmt.Errorf("任务不存在或状态未知: %s", taskID)
		}

		log.Printf("⏳ 等待 %v 后重试...", interval)
		time.Sleep(interval)
	}

	if imgUrl == "" {
		log.Printf("⏰ 超时未获取到图片URL")
		return "", fmt.Errorf("超时未获取到stable-diffusion图片URL，请稍后重试")
	}

	log.Printf("✅ Stable Diffusion图片生成完成: %s", imgUrl)
	return imgUrl, nil
}

func (a *aliBiz) GetPromptManager() *PromptManager {
	return a.pm
}
