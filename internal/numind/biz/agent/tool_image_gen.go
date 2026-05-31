package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// imageGenTool is the image_gen FullTool.
// Implements prompt-based image generation via the 'dmxapi' provider's
// gemini-2.5-flash-image model and uploads it through uploadGeneratedFile.
type imageGenTool struct {
	BaseTool
	ds store.IStore
}

var _ FullTool = (*imageGenTool)(nil)

func (t *imageGenTool) Name() string { return "image_gen" }
func (t *imageGenTool) Description() string {
	return "Generate an image from a text prompt using the Gemini image model."
}
func (t *imageGenTool) UserFacingName() string        { return "图像生成" }
func (t *imageGenTool) NarrationVerb() string         { return "生成" }
func (t *imageGenTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableImageGen }

func (t *imageGenTool) returnSoftError(format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(map[string]string{
		"error": "ERROR: " + msg,
	})
	return ToolResult(out), nil
}

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *imageGenTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "Text description of the image to generate."}
		},
		"required": ["prompt"]
	}`)
}

func (t *imageGenTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	// 1. 解析 Tool 输入
	var inp struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &inp); err != nil {
		return nil, fmt.Errorf("invalid image_gen input format: %w", err)
	}
	if strings.TrimSpace(inp.Prompt) == "" {
		return nil, errors.New("prompt is required for image generation")
	}

	// 2. 确定 Store 实例
	ds := t.ds
	if ds == nil {
		ds = store.S
	}
	var db *gorm.DB
	if ds != nil {
		func() {
			defer func() { _ = recover() }()
			db = ds.DB()
		}()
	}
	if db == nil {
		return t.returnSoftError("database store context is not configured")
	}

	// 3. 从数据库中查询 dmxapi 供应源配置
	var provider model.LLMProvider
	if err := db.WithContext(ctx).Where("name = ?", "dmxapi").First(&provider).Error; err != nil {
		return t.returnSoftError("failed to retrieve 'dmxapi' provider config: %v", err)
	}
	if provider.APIKey == "" {
		return t.returnSoftError("dmxapi provider API key is not configured in DB")
	}

	// 4. 计算并装配标准的 API Endpoint URL
	baseURL := strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		// 默认兜底官方地址
		baseURL = "https://www.dmxapi.cn"
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}

	// 兼容 dmxapi 基础路径含有 /v1 或是 /v1/ 的拼接规律，替换为 v1beta 调用文生图端点
	var apiURL string
	if strings.Contains(baseURL, "/v1/") {
		apiURL = strings.Replace(baseURL, "/v1/", "/v1beta/models/gemini-2.5-flash-image:generateContent", 1)
	} else if strings.Contains(baseURL, "/v1") {
		apiURL = strings.Replace(baseURL, "/v1", "/v1beta/models/gemini-2.5-flash-image:generateContent", 1)
	} else {
		apiURL = baseURL + "v1beta/models/gemini-2.5-flash-image:generateContent"
	}

	// 5. 构筑官方定义的生图请求体 payload
	reqBody := map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{
				"parts": []interface{}{
					map[string]interface{}{
						"text": inp.Prompt,
					},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"imageConfig": map[string]interface{}{
				"aspectRatio": "1:1",
			},
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// 6. 发起 HTTP 图像生成请求
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("x-goog-api-key", provider.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 90 * time.Second, // 图像生成通常稍慢，给足 90s 超时时间
	}
	resp, err := client.Do(req)
	if err != nil {
		return t.returnSoftError("http request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBuffer bytes.Buffer
		_, _ = errBuffer.ReadFrom(resp.Body)
		return t.returnSoftError("API request failed with status %d: %s", resp.StatusCode, errBuffer.String())
	}

	// 7. 解析包含 Base64 数据的 JSON 响应结构
	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						Data string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return t.returnSoftError("failed to decode response payload: %v", err)
	}

	// 8. 过滤并提取 Base64 原始数据
	var base64Data string
	if len(result.Candidates) > 0 && len(result.Candidates[0].Content.Parts) > 0 {
		for _, part := range result.Candidates[0].Content.Parts {
			if part.InlineData.Data != "" {
				base64Data = part.InlineData.Data
				break
			}
		}
	}

	if base64Data == "" {
		return t.returnSoftError("no image base64 data returned from API response (possibly blocked by safety filters)")
	}

	// 9. 解码 Base64 为二进制图像流
	imgBytes, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return t.returnSoftError("failed to decode base64 image data: %v", err)
	}

	// 10. 上传文件并将其以标准的 fileCreateOutput 结构体回传给大模型工具链作为 Result
	filename := fmt.Sprintf("gemini-image-%d.png", time.Now().Unix())
	res, uploadErr := uploadGeneratedFile(ctx, imgBytes, "image/png", filename, "png")
	if uploadErr != nil {
		return t.returnSoftError("failed to upload generated image: %v", uploadErr)
	}
	return res, nil
}
