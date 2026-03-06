package baidu

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/log"

	"github.com/disintegration/imaging"
	"github.com/spf13/viper"
)

// 百度 OCR 高精度含位置版图片限制
const (
	maxLongEdge   = 8192             // 最长边 ≤ 8192px
	maxBase64Size = 10 * 1024 * 1024 // base64 编码后 ≤ 10MB
)

// accessTokenCache 缓存百度 access_token（有效期 30 天）
type accessTokenCache struct {
	mu        sync.RWMutex
	token     string
	expiresAt time.Time
}

var tokenCache accessTokenCache

// OCRResult 百度 OCR 响应结构（高精度含位置版）
type OCRResult struct {
	LogID          uint64      `json:"log_id"`
	WordsResultNum int         `json:"words_result_num"`
	WordsResult    []WordsItem `json:"words_result"`
	ErrorCode      int         `json:"error_code,omitempty"`
	ErrorMsg       string      `json:"error_msg,omitempty"`
}

// WordsItem 单条识别结果（含位置信息）
type WordsItem struct {
	Words    string   `json:"words"`
	Location Location `json:"location"`
}

// Location 文字位置信息
type Location struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Width  int `json:"width"`
	Height int `json:"height"`
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

// resizeForOCR 将图片缩放到百度 OCR 限制范围内（最长边 ≤ 8192px，base64 ≤ 10MB）
// 返回处理后的图片数据和缩放比例（用于坐标还原）
func resizeForOCR(imageData []byte) ([]byte, float64, error) {
	img, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, 1, fmt.Errorf("解码图片失败: %w", err)
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	scale := 1.0

	// 检查是否需要缩放（最长边超限）
	longEdge := width
	if height > longEdge {
		longEdge = height
	}

	if longEdge > maxLongEdge {
		scale = float64(maxLongEdge) / float64(longEdge)
		newWidth := int(float64(width) * scale)
		newHeight := int(float64(height) * scale)
		log.Infow("[BaiduOCR] Resizing image", "original", fmt.Sprintf("%dx%d", width, height), "target", fmt.Sprintf("%dx%d", newWidth, newHeight))
		img = imaging.Resize(img, newWidth, newHeight, imaging.Lanczos)
	}

	// 编码为 JPEG，逐步降低质量直到 base64 大小 ≤ 10MB
	quality := 90
	for quality >= 20 {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, 1, fmt.Errorf("编码 JPEG 失败: %w", err)
		}
		encoded := buf.Bytes()
		b64Len := base64.StdEncoding.EncodedLen(len(encoded))
		if b64Len <= maxBase64Size {
			return encoded, scale, nil
		}
		quality -= 10
	}

	return nil, 1, fmt.Errorf("图片过大，压缩后仍超过百度 OCR 10MB 限制")
}

// callOCRAPI 调用百度 OCR API，返回原始结果
func callOCRAPI(imageData []byte) (*OCRResult, error) {
	token, err := getAccessToken()
	if err != nil {
		return nil, err
	}

	ocrURL := "https://aip.baidubce.com/rest/2.0/ocr/v1/accurate?access_token=" + url.QueryEscape(token)

	b64 := base64.StdEncoding.EncodeToString(imageData)

	formData := url.Values{}
	formData.Set("image", b64)
	formData.Set("language_type", "CHN_ENG")
	formData.Set("detect_direction", "true")
	formData.Set("paragraph", "true")

	resp, err := http.Post(ocrURL, "application/x-www-form-urlencoded", strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("百度 OCR 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取百度 OCR 响应失败: %w", err)
	}

	var result OCRResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析百度 OCR 响应失败: %w", err)
	}

	if result.ErrorCode != 0 {
		return nil, fmt.Errorf("百度 OCR 错误 %d: %s", result.ErrorCode, result.ErrorMsg)
	}

	return &result, nil
}

// RecognizeText 调用百度 OCR 并返回纯文本（无说话人标注）
func RecognizeText(imageData []byte) (string, error) {
	imageData, err := prepareImage(imageData)
	if err != nil {
		return "", err
	}

	result, err := callOCRAPI(imageData)
	if err != nil {
		return "", err
	}

	lines := make([]string, 0, len(result.WordsResult))
	for _, item := range result.WordsResult {
		if item.Words != "" {
			lines = append(lines, item.Words)
		}
	}

	return strings.Join(lines, "\n"), nil
}

// RecognizeChatText 调用百度 OCR 识别微信聊天截图，根据文字位置自动标注说话人
// 返回格式: "客户：xxx\n销售：xxx\n..."
func RecognizeChatText(imageData []byte, imageWidth int) (string, error) {
	processedData, err := prepareImage(imageData)
	if err != nil {
		return "", err
	}

	// 如果图片被缩放过，需要用缩放后的宽度来判断位置
	// 这里重新解码获取实际发送给 OCR 的图片宽度
	if img, decErr := imaging.Decode(bytes.NewReader(processedData)); decErr == nil {
		imageWidth = img.Bounds().Dx()
	}

	result, err := callOCRAPI(processedData)
	if err != nil {
		return "", err
	}

	if len(result.WordsResult) == 0 {
		return "", nil
	}

	return formatChatMessages(result.WordsResult, imageWidth), nil
}

// prepareImage 检查并预处理图片（缩放/压缩）以满足百度 OCR 限制
func prepareImage(imageData []byte) ([]byte, error) {
	b64Len := base64.StdEncoding.EncodedLen(len(imageData))
	needResize := b64Len > maxBase64Size

	if !needResize {
		img, decErr := imaging.Decode(bytes.NewReader(imageData))
		if decErr == nil {
			bounds := img.Bounds()
			longEdge := bounds.Dx()
			if bounds.Dy() > longEdge {
				longEdge = bounds.Dy()
			}
			needResize = longEdge > maxLongEdge
		}
	}

	if needResize {
		resized, _, resizeErr := resizeForOCR(imageData)
		if resizeErr != nil {
			return nil, fmt.Errorf("图片预处理失败: %w", resizeErr)
		}
		return resized, nil
	}

	return imageData, nil
}

// speaker 说话人类型
type speaker int

const (
	speakerCustomer speaker = iota // 客户（左侧）
	speakerSales                   // 销售（右侧）
	speakerSystem                  // 系统消息（居中，如时间戳）
)

// formatChatMessages 根据文字位置信息将 OCR 结果格式化为聊天记录
func formatChatMessages(items []WordsItem, imageWidth int) string {
	if len(items) == 0 {
		return ""
	}

	// 按 top 坐标排序（从上到下）
	sort.Slice(items, func(i, j int) bool {
		return items[i].Location.Top < items[j].Location.Top
	})

	// 微信聊天布局判断阈值
	// 客户气泡靠左（center_x < 图片宽度 * 0.45）
	// 销售气泡靠右（center_x > 图片宽度 * 0.55）
	// 系统消息居中（介于两者之间）
	leftThreshold := float64(imageWidth) * 0.45
	rightThreshold := float64(imageWidth) * 0.55

	type chatLine struct {
		speaker speaker
		top     int
		text    string
	}

	// 为每行文字标注说话人
	var lines []chatLine
	for _, item := range items {
		if item.Words == "" {
			continue
		}
		centerX := float64(item.Location.Left) + float64(item.Location.Width)/2.0

		var sp speaker
		if centerX < leftThreshold {
			sp = speakerCustomer
		} else if centerX > rightThreshold {
			sp = speakerSales
		} else {
			sp = speakerSystem
		}

		lines = append(lines, chatLine{
			speaker: sp,
			top:     item.Location.Top,
			text:    item.Words,
		})
	}

	// 合并相邻的同一说话人文字为一条消息
	// 微信一条消息可能被 OCR 拆成多行，判断标准：同一说话人 + 垂直距离较近
	const mergeGap = 60 // 同一消息内行间距通常 < 60px

	type message struct {
		speaker speaker
		texts   []string
	}

	var messages []message
	for _, line := range lines {
		// 跳过系统消息（时间戳、日期分隔线等）
		if line.speaker == speakerSystem {
			continue
		}

		if len(messages) > 0 {
			last := &messages[len(messages)-1]
			// 同一说话人 + 垂直距离近 → 合并
			if last.speaker == line.speaker {
				lastBottom := lines[0].top // 简单取当前消息的上一行底部
				for i, l := range lines {
					if l.top == line.top && i > 0 {
						prevItem := items[i-1]
						lastBottom = prevItem.Location.Top + prevItem.Location.Height
						break
					}
				}
				gap := line.top - lastBottom
				if gap < mergeGap {
					last.texts = append(last.texts, line.text)
					continue
				}
			}
		}

		messages = append(messages, message{
			speaker: line.speaker,
			texts:   []string{line.text},
		})
	}

	// 格式化输出
	var result strings.Builder
	for i, msg := range messages {
		if i > 0 {
			result.WriteByte('\n')
		}
		prefix := "客户"
		if msg.speaker == speakerSales {
			prefix = "销售"
		}
		result.WriteString(prefix)
		result.WriteString("：")
		result.WriteString(strings.Join(msg.texts, ""))
	}

	return result.String()
}
