package baidu

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// accessTokenCache 缓存百度 access_token（有效期 30 天）
type accessTokenCache struct {
	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

var tokenCache accessTokenCache

// OCRResult 百度 OCR 响应结构
type OCRResult struct {
	LogID          uint64       `json:"log_id"`
	WordsResultNum int          `json:"words_result_num"`
	WordsResult    []WordsItem  `json:"words_result"`
	ErrorCode      int          `json:"error_code,omitempty"`
	ErrorMsg       string       `json:"error_msg,omitempty"`
}

// WordsItem 单条识别结果
type WordsItem struct {
	Words string `json:"words"`
}

// getAccessToken 获取或刷新百度 access_token
func getAccessToken() (string, error) {
	tokenCache.mu.RLock()
	if tokenCache.token != "" && time.Now().Before(tokenCache.expiresAt) {
		token := tokenCache.token
		tokenCache.mu.RUnlock()
		return token, nil
	}
	tokenCache.mu.RUnlock()

	tokenCache.mu.Lock()
	defer tokenCache.mu.Unlock()

	// 双重检查
	if tokenCache.token != "" && time.Now().Before(tokenCache.expiresAt) {
		return tokenCache.token, nil
	}

	apiKey := viper.GetString("baidu.api_key")
	secretKey := viper.GetString("baidu.secret_key")
	if apiKey == "" || secretKey == "" {
		return "", fmt.Errorf("百度 OCR 未配置 api_key 或 secret_key")
	}

	tokenURL := fmt.Sprintf(
		"https://aip.baidubce.com/oauth/2.0/token?grant_type=client_credentials&client_id=%s&client_secret=%s",
		url.QueryEscape(apiKey), url.QueryEscape(secretKey),
	)

	resp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", nil)
	if err != nil {
		return "", fmt.Errorf("获取百度 access_token 失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取百度 token 响应失败: %w", err)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error,omitempty"`
		ErrorDesc   string `json:"error_description,omitempty"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("解析百度 token 响应失败: %w", err)
	}
	if tokenResp.Error != "" {
		return "", fmt.Errorf("百度 token 请求错误: %s - %s", tokenResp.Error, tokenResp.ErrorDesc)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("百度 token 响应中无 access_token")
	}

	// 提前 1 小时过期，避免边界情况
	tokenCache.token = tokenResp.AccessToken
	tokenCache.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn-3600) * time.Second)

	log.Infow("百度 access_token 已刷新", "expires_in", tokenResp.ExpiresIn)
	return tokenCache.token, nil
}

// RecognizeText 调用百度通用文字识别（高精度版），返回拼接后的全文文本
func RecognizeText(imageData []byte) (string, error) {
	token, err := getAccessToken()
	if err != nil {
		return "", err
	}

	ocrURL := "https://aip.baidubce.com/rest/2.0/ocr/v1/accurate_basic?access_token=" + url.QueryEscape(token)

	// Base64 编码图片
	b64 := base64.StdEncoding.EncodeToString(imageData)

	// 构建 form 请求体
	formData := url.Values{}
	formData.Set("image", b64)
	formData.Set("language_type", "CHN_ENG")
	formData.Set("detect_direction", "true")
	formData.Set("paragraph", "true")

	resp, err := http.Post(ocrURL, "application/x-www-form-urlencoded", strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("百度 OCR 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取百度 OCR 响应失败: %w", err)
	}

	var result OCRResult
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析百度 OCR 响应失败: %w", err)
	}

	if result.ErrorCode != 0 {
		return "", fmt.Errorf("百度 OCR 错误 %d: %s", result.ErrorCode, result.ErrorMsg)
	}

	// 拼接所有识别文字，每行一条
	lines := make([]string, 0, len(result.WordsResult))
	for _, item := range result.WordsResult {
		if item.Words != "" {
			lines = append(lines, item.Words)
		}
	}

	return strings.Join(lines, "\n"), nil
}
