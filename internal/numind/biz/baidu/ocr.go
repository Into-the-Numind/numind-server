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
	"regexp"
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
	speakerSystem                  // 系统消息（居中/时间戳/通知）
)

// 微信时间分隔符正则（匹配整行）
// 匹配格式：下午 2:30 / 昨天 下午 3:45 / 星期三 上午 9:00 / 10月15日 下午 2:30 等
var wechatTimeRegex = regexp.MustCompile(
	`^((\d{4}年)?\d{1,2}月\d{1,2}日\s*)?` + // 可选日期
		`(昨天|前天|今天|星期[一二三四五六日天])?\s*` + // 可选相对日期/星期
		`(上午|下午|凌晨|中午|晚上)?\s*` + // 可选时段
		`\d{1,2}:\d{2}\s*$`, // 时间 HH:MM
)

// 语音消息时长正则（匹配 5" / 15'' / 1'23" / 0:15 等）
// 注意：纯 H:MM 格式已被 wechatTimeRegex 覆盖，此处补充秒数+引号格式
var voiceDurationRegex = regexp.MustCompile(
	`^\d{1,3}(["″]|'{1,2})\s*$|` + // 5" / 15'' / 60″ / 5'
		`^\d{1,2}'\d{2}["''″]?\s*$`, // 1'23" / 2'00
)

// 微信系统通知关键词
var systemMessageKeywords = []string{
	"已添加", "好友验证", "撤回了一条消息", "邀请你加入",
	"拍了拍", "以下是新消息", "消息已发出", "开启了朋友验证",
	"发起了语音通话", "发起了视频通话", "领取了", "发出了红包", "通过了你的",
}

// isWechatTimestamp 判断文本是否为微信时间分隔符
func isWechatTimestamp(text string) bool {
	return wechatTimeRegex.MatchString(strings.TrimSpace(text))
}

// isVoiceDuration 判断文本是否为语音消息时长（如 5"、15''、1'23"）
func isVoiceDuration(text string) bool {
	return voiceDurationRegex.MatchString(strings.TrimSpace(text))
}

// isSystemMessage 判断文本是否为微信系统通知（需同时满足：包含关键词 + 居中 + 短文本）
func isSystemMessage(text string, centerX float64, imageWidth int) bool {
	if len([]rune(text)) > 30 {
		return false
	}
	// 文字中心在图片宽度 30%-70% 范围内视为居中
	if centerX < float64(imageWidth)*0.30 || centerX > float64(imageWidth)*0.70 {
		return false
	}
	for _, kw := range systemMessageKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// isUIElement 判断文本是否为微信 UI 元素（导航按钮、输入栏等）
func isUIElement(text string, loc Location, imageWidth int) bool {
	runeLen := len([]rune(text))

	// 1-2 个字符的常见 UI 符号直接过滤（这些不可能是正常聊天内容）
	if runeLen <= 2 {
		switch text {
		case "+", "<", ">", "×", "…", "⋯", "···", "...", "←":
			return true
		}
	}

	// 极短文本（≤3字符）且在屏幕极端位置（最左10%或最右10%）→ UI 按钮
	// 这里覆盖了 "1"（未读计数）、"返回" 等位置相关的 UI 元素
	if runeLen <= 3 {
		leftRatio := float64(loc.Left) / float64(imageWidth)
		rightRatio := float64(loc.Left+loc.Width) / float64(imageWidth)
		if leftRatio > 0.90 || rightRatio < 0.10 {
			return true
		}
	}

	return false
}

// itemSide 判断一个文字项在屏幕的哪一侧
// 返回: -1=左侧, 0=中间, 1=右侧
func itemSide(item WordsItem, imageWidth int) int {
	midPoint := float64(imageWidth) / 2.0
	itemCenter := float64(item.Location.Left) + float64(item.Location.Width)/2.0
	if itemCenter < midPoint*0.8 {
		return -1 // 左侧
	}
	if itemCenter > midPoint*1.2 {
		return 1 // 右侧
	}
	return 0 // 中间
}

// bubbleSide 判断气泡当前内容主要在屏幕哪一侧（用第一个 item 的位置决定）
func bubbleSide(b *bubble, imageWidth int) int {
	if len(b.items) == 0 {
		return 0
	}
	return itemSide(b.items[0], imageWidth)
}

// bubble 聊天气泡（一组垂直相邻的文字行）
type bubble struct {
	items     []WordsItem
	leftEdge  int // 所有行中最小的 Left
	rightEdge int // 所有行中最大的 Left+Width
	bottom    int // 最后一行的底部 Y 坐标
}

// formatChatMessages 根据文字位置信息将 OCR 结果格式化为聊天记录
// 算法：内容去噪 → 气泡分组 → 气泡级说话人判断 → 格式化输出
func formatChatMessages(items []WordsItem, imageWidth int) string {
	if len(items) == 0 || imageWidth <= 0 {
		return ""
	}

	// === Phase 1: 内容去噪 ===
	// 过滤时间分隔符、系统通知和 UI 元素，防止干扰后续气泡分组
	var filtered []WordsItem
	for _, item := range items {
		text := strings.TrimSpace(item.Words)
		if text == "" {
			continue
		}
		// 过滤时间分隔符
		if isWechatTimestamp(text) {
			continue
		}
		// 过滤语音消息时长
		if isVoiceDuration(text) {
			continue
		}
		// 过滤系统通知
		centerX := float64(item.Location.Left) + float64(item.Location.Width)/2.0
		if isSystemMessage(text, centerX, imageWidth) {
			continue
		}
		// 过滤 UI 元素（导航按钮、输入栏按钮等）
		if isUIElement(text, item.Location, imageWidth) {
			continue
		}
		filtered = append(filtered, item)
	}

	if len(filtered) == 0 {
		return ""
	}

	// 按 top 坐标排序（从上到下）
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Location.Top < filtered[j].Location.Top
	})

	// === Phase 2: 气泡分组 ===
	// 计算中位数行高，用作自适应分组阈值
	heights := make([]int, len(filtered))
	for i, item := range filtered {
		h := item.Location.Height
		if h <= 0 {
			h = 30 // fallback
		}
		heights[i] = h
	}
	sort.Ints(heights)
	medianHeight := heights[len(heights)/2]
	groupGap := medianHeight * 3 / 2 // 1.5 倍中位数行高
	if groupGap < 30 {
		groupGap = 30 // 最小阈值
	}

	// 按 Y 间距分组为气泡（同时检查水平一致性）
	// 关键：Y 相近但在屏幕不同侧的文字不能合并（如客户文字和销售图片缩略图同行）
	var bubbles []bubble
	for _, item := range filtered {
		itemTop := item.Location.Top
		itemBottom := item.Location.Top + item.Location.Height
		itemRight := item.Location.Left + item.Location.Width

		merged := false
		if len(bubbles) > 0 {
			last := &bubbles[len(bubbles)-1]
			gap := itemTop - last.bottom
			if gap < groupGap {
				// Y 间距够近，再检查水平一致性
				// 如果新 item 和气泡在屏幕的不同侧（一个左一个右），不合并
				bSide := bubbleSide(last, imageWidth)
				iSide := itemSide(item, imageWidth)
				if bSide == 0 || iSide == 0 || bSide == iSide {
					// 同侧或有一方居中 → 允许合并
					last.items = append(last.items, item)
					if item.Location.Left < last.leftEdge {
						last.leftEdge = item.Location.Left
					}
					if itemRight > last.rightEdge {
						last.rightEdge = itemRight
					}
					if itemBottom > last.bottom {
						last.bottom = itemBottom
					}
					merged = true
				}
			}
		}

		if !merged {
			// 新气泡
			bubbles = append(bubbles, bubble{
				items:     []WordsItem{item},
				leftEdge:  item.Location.Left,
				rightEdge: itemRight,
				bottom:    itemBottom,
			})
		}
	}

	// === Phase 3: 气泡级说话人判断 ===
	// 客户气泡锚定在左侧：leftEdge < 25% imageWidth
	// 销售气泡锚定在右侧：rightEdge > 75% imageWidth
	// 阈值需要足够宽松以适应不同分辨率（实测 1440px 宽图中气泡 leftEdge ≈ 18.4%）
	leftAnchor := float64(imageWidth) * 0.25
	rightAnchor := float64(imageWidth) * 0.75

	type message struct {
		speaker speaker
		texts   []string
		bottom  int
	}

	var messages []message
	for _, b := range bubbles {
		// 判断说话人
		isLeft := float64(b.leftEdge) < leftAnchor
		isRight := float64(b.rightEdge) > rightAnchor
		var sp speaker
		if isLeft && isRight {
			// 长文本同时触及两侧边界 → 用距离判断：leftEdge 离左边近 = 客户，rightEdge 离右边近 = 销售
			distToLeft := float64(b.leftEdge)
			distToRight := float64(imageWidth) - float64(b.rightEdge)
			if distToLeft <= distToRight {
				sp = speakerCustomer
			} else {
				sp = speakerSales
			}
		} else if isRight {
			sp = speakerSales
		} else if isLeft {
			sp = speakerCustomer
		} else {
			// 既不贴左也不贴右 → 系统消息/噪音，跳过
			continue
		}

		// 收集气泡内所有文字
		var texts []string
		for _, item := range b.items {
			texts = append(texts, item.Words)
		}

		// 合并相邻同一说话人的气泡（仅间距很小时合并，可能是 OCR 把一个气泡拆成了两个）
		// 间距较大说明是同一人连发的多条消息，应保持独立
		if len(messages) > 0 {
			last := &messages[len(messages)-1]
			if last.speaker == sp && (b.items[0].Location.Top-last.bottom) < groupGap {
				last.texts = append(last.texts, texts...)
				last.bottom = b.bottom
				continue
			}
		}

		messages = append(messages, message{
			speaker: sp,
			texts:   texts,
			bottom:  b.bottom,
		})
	}

	// === Phase 4: 格式化输出 ===
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
