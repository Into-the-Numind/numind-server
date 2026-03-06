package baidu

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
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

	// 分段参数
	segmentHeight = 7000 // 每段高度（留余量，< 8192）
	overlapHeight = 300  // 相邻段重叠高度（避免切断气泡）
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

	tokenCache.token = tokenResp.AccessToken
	tokenCache.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn-3600) * time.Second)

	log.Infow("百度 access_token 已刷新", "expires_in", tokenResp.ExpiresIn)
	return tokenCache.token, nil
}

// encodeImageForOCR 将图片编码为 JPEG 并确保 base64 ≤ 10MB
func encodeImageForOCR(img image.Image) ([]byte, error) {
	quality := 90
	for quality >= 20 {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, fmt.Errorf("编码 JPEG 失败: %w", err)
		}
		encoded := buf.Bytes()
		b64Len := base64.StdEncoding.EncodedLen(len(encoded))
		if b64Len <= maxBase64Size {
			return encoded, nil
		}
		quality -= 10
	}
	return nil, fmt.Errorf("图片段过大，压缩后仍超过百度 OCR 10MB 限制")
}

// splitImage 将超高图片切割为多个分段，每段高度 ≤ segmentHeight，相邻段重叠 overlapHeight
// 返回每段的图片数据和该段在原图中的 y 偏移量
func splitImage(img image.Image) (segments [][]byte, yOffsets []int, err error) {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	log.Infow("[BaiduOCR] Splitting tall image", "dimensions", fmt.Sprintf("%dx%d", width, height))

	y := 0
	for y < height {
		segEnd := y + segmentHeight
		if segEnd > height {
			segEnd = height
		}

		// 裁剪当前段
		segImg := imaging.Crop(img, image.Rect(0, y, width, segEnd))

		segData, encErr := encodeImageForOCR(segImg)
		if encErr != nil {
			return nil, nil, fmt.Errorf("分段 %d 编码失败: %w", len(segments), encErr)
		}

		segments = append(segments, segData)
		yOffsets = append(yOffsets, y)

		log.Infow("[BaiduOCR] Segment created", "index", len(segments)-1, "yOffset", y, "segHeight", segEnd-y, "size", len(segData))

		// 下一段起点 = 当前段终点 - 重叠
		y = segEnd - overlapHeight
		if segEnd == height {
			break
		}
	}

	return segments, yOffsets, nil
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

// recognizeSegments 对多个图片分段调用 OCR，合并结果并将坐标映射回原图
func recognizeSegments(segments [][]byte, yOffsets []int) ([]WordsItem, error) {
	var allItems []WordsItem

	for i, segData := range segments {
		result, err := callOCRAPI(segData)
		if err != nil {
			log.Infow("[BaiduOCR] Segment OCR failed, skipping", "index", i, "error", err)
			continue
		}

		// 将分段内的 top 坐标映射回原图坐标
		for _, item := range result.WordsResult {
			if item.Words == "" {
				continue
			}
			item.Location.Top += yOffsets[i]
			allItems = append(allItems, item)
		}

		log.Infow("[BaiduOCR] Segment OCR done", "index", i, "words_count", result.WordsResultNum)
	}

	// 按原图 top 坐标排序
	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].Location.Top < allItems[j].Location.Top
	})

	// 去重：重叠区域的相同文字（top 相近 + 文字相同）
	allItems = deduplicateItems(allItems)

	return allItems, nil
}

// deduplicateItems 去除重叠区域产生的重复识别结果
func deduplicateItems(items []WordsItem) []WordsItem {
	if len(items) <= 1 {
		return items
	}

	var result []WordsItem
	for i, item := range items {
		isDup := false
		// 与已保留的最后几条对比（重叠区域只可能产生相邻重复）
		for j := len(result) - 1; j >= 0 && j >= len(result)-5; j-- {
			prev := result[j]
			topGap := item.Location.Top - prev.Location.Top
			if topGap < 0 {
				topGap = -topGap
			}
			// 同一行文字：top 差 < 30px 且文字相同
			if topGap < 30 && item.Words == prev.Words {
				isDup = true
				break
			}
		}
		if !isDup {
			result = append(result, items[i])
		}
	}

	return result
}

// RecognizeText 调用百度 OCR 并返回纯文本（无说话人标注）
func RecognizeText(imageData []byte) (string, error) {
	items, _, err := recognizeImage(imageData)
	if err != nil {
		return "", err
	}

	lines := make([]string, 0, len(items))
	for _, item := range items {
		if item.Words != "" {
			lines = append(lines, item.Words)
		}
	}

	return strings.Join(lines, "\n"), nil
}

// RecognizeChatText 调用百度 OCR 识别微信聊天截图，根据文字位置自动标注说话人
// 返回格式: "客户：xxx\n销售：xxx\n..."
func RecognizeChatText(imageData []byte, imageWidth int) (string, error) {
	items, width, err := recognizeImage(imageData)
	if err != nil {
		return "", err
	}
	if width > 0 {
		imageWidth = width
	}

	if len(items) == 0 {
		return "", nil
	}

	return formatChatMessages(items, imageWidth), nil
}

// recognizeImage 统一入口：解码图片 → 判断是否需要分段 → 调用 OCR → 返回结果
// 返回: (识别结果, 图片宽度, 错误)
func recognizeImage(imageData []byte) ([]WordsItem, int, error) {
	img, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		// 解码失败，直接发原图（可能是 OCR 支持但 imaging 不支持的格式）
		result, ocrErr := callOCRAPI(imageData)
		if ocrErr != nil {
			return nil, 0, ocrErr
		}
		return result.WordsResult, 0, nil
	}

	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// 短图（高度 ≤ 8192）：直接识别
	if height <= maxLongEdge {
		// 检查 base64 大小，必要时压缩质量
		b64Len := base64.StdEncoding.EncodedLen(len(imageData))
		if b64Len > maxBase64Size {
			imageData, err = encodeImageForOCR(img)
			if err != nil {
				return nil, 0, err
			}
		}

		result, ocrErr := callOCRAPI(imageData)
		if ocrErr != nil {
			return nil, 0, ocrErr
		}
		return result.WordsResult, width, nil
	}

	// 长图：分段切割 → 逐段 OCR → 合并去重
	log.Infow("[BaiduOCR] Tall image detected, splitting", "dimensions", fmt.Sprintf("%dx%d", width, height),
		"segments", (height-1)/segmentHeight+1)

	segments, yOffsets, splitErr := splitImage(img)
	if splitErr != nil {
		return nil, 0, splitErr
	}

	items, mergeErr := recognizeSegments(segments, yOffsets)
	if mergeErr != nil {
		return nil, 0, mergeErr
	}

	return items, width, nil
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
	leftThreshold := float64(imageWidth) * 0.45
	rightThreshold := float64(imageWidth) * 0.55

	type chatLine struct {
		speaker speaker
		top     int
		bottom  int
		text    string
	}

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
			bottom:  item.Location.Top + item.Location.Height,
			text:    item.Words,
		})
	}

	// 合并相邻同一说话人的文字为一条消息
	const mergeGap = 60

	type message struct {
		speaker speaker
		texts   []string
		bottom  int // 当前消息最后一行的底部
	}

	var messages []message
	for _, line := range lines {
		if line.speaker == speakerSystem {
			continue
		}

		if len(messages) > 0 {
			last := &messages[len(messages)-1]
			if last.speaker == line.speaker && (line.top-last.bottom) < mergeGap {
				last.texts = append(last.texts, line.text)
				last.bottom = line.bottom
				continue
			}
		}

		messages = append(messages, message{
			speaker: line.speaker,
			texts:   []string{line.text},
			bottom:  line.bottom,
		})
	}

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
