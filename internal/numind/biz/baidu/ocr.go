package baidu

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"regexp"
	"sort"
	"strings"
	"sync"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"

	"github.com/disintegration/imaging"
)

// 百度 OCR 高精度含位置版图片限制
const (
	maxLongEdge   = 8192             // 最长边 ≤ 8192px
	maxBase64Size = 10 * 1024 * 1024 // base64 编码后 ≤ 10MB

	// 分段参数
	segmentHeight = 7000 // 每段高度（留余量，< 8192）
	overlapHeight = 300  // 相邻段重叠高度（避免切断气泡）
)

// OCRResult 百度 OCR 响应结构（高精度含位置版）
type OCRResult struct {
	LogID          uint64      `json:"log_id"`
	WordsResultNum int         `json:"words_result_num"`
	WordsResult    []WordsItem `json:"words_result"`
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

// callOCRAPI 通过 AI Gateway 调用百度 OCR，返回识别结果。
// ctx 应已注入 aismw.WithUserID + aiservice.WithSkipLegacyBilling（由上层调用方负责注入）。
func callOCRAPI(ctx context.Context, imageData []byte) (*OCRResult, error) {
	resp, err := aiservice.OCR(ctx, profile.OcrBaidu, aiservice.OCRRequest{
		ImageBytes: imageData,
	})
	if err != nil {
		return nil, fmt.Errorf("百度 OCR 请求失败: %w", err)
	}

	// 将 aiservice.OCRResponse 转换为本包内的 OCRResult（保留位置信息）
	result := &OCRResult{
		WordsResultNum: len(resp.Words),
	}
	for _, w := range resp.Words {
		item := WordsItem{Words: w.Word}
		if len(w.BoundingBox) == 4 {
			item.Location = Location{
				Left:   w.BoundingBox[0],
				Top:    w.BoundingBox[1],
				Width:  w.BoundingBox[2] - w.BoundingBox[0],
				Height: w.BoundingBox[3] - w.BoundingBox[1],
			}
		}
		result.WordsResult = append(result.WordsResult, item)
	}
	return result, nil
}

// segmentResult 存储单个分段的 OCR 结果
type segmentResult struct {
	index int
	items []WordsItem
	err   error
}

// recognizeSegments 并发调用 OCR 识别多个分段（受限于百度 API 2 QPS），合并结果并将坐标映射回原图
func recognizeSegments(ctx context.Context, segments [][]byte, yOffsets []int) ([]WordsItem, error) {
	results := make([]segmentResult, len(segments))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2) // 百度 OCR 限制 2 QPS

	for i, segData := range segments {
		wg.Add(1)
		sem <- struct{}{} // 获取信号量
		go func(idx int, data []byte) {
			defer wg.Done()
			defer func() { <-sem }() // 释放信号量
			result, err := callOCRAPI(ctx, data)
			if err != nil {
				results[idx] = segmentResult{index: idx, err: err}
				return
			}
			var items []WordsItem
			for _, item := range result.WordsResult {
				if item.Words == "" {
					continue
				}
				item.Location.Top += yOffsets[idx]
				items = append(items, item)
			}
			results[idx] = segmentResult{index: idx, items: items}
			log.Infow("[BaiduOCR] Segment OCR done", "index", idx, "words_count", result.WordsResultNum)
		}(i, segData)
	}

	wg.Wait()

	// 按分段顺序合并结果
	var allItems []WordsItem
	for _, r := range results {
		if r.err != nil {
			log.Infow("[BaiduOCR] Segment OCR failed, skipping", "index", r.index, "error", r.err)
			continue
		}
		allItems = append(allItems, r.items...)
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

// RecognizeText 调用百度 OCR（经由 AI Gateway）并返回纯文本（无说话人标注）。
// 当前由 ocr.go 内部使用；保留为公开 API 以便其他模块调用 baidu OCR 单页。
// ctx 应已注入 aismw.WithUserID + aiservice.WithSkipLegacyBilling（由调用方负责）。
func RecognizeText(ctx context.Context, imageData []byte) (string, error) {
	items, _, err := recognizeImage(ctx, imageData)
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

// RecognizeChatText 调用百度 OCR（经由 AI Gateway）识别微信聊天截图，根据文字位置自动标注说话人。
// 返回格式: "左侧消息\n右侧消息\n..."
// ctx 应已注入 aismw.WithUserID + aiservice.WithSkipLegacyBilling（由调用方负责）。
func RecognizeChatText(ctx context.Context, imageData []byte, imageWidth int) (string, error) {
	items, width, err := recognizeImage(ctx, imageData)
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
func recognizeImage(ctx context.Context, imageData []byte) ([]WordsItem, int, error) {
	img, err := imaging.Decode(bytes.NewReader(imageData))
	if err != nil {
		// 解码失败，直接发原图（可能是 OCR 支持但 imaging 不支持的格式）
		result, ocrErr := callOCRAPI(ctx, imageData)
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

		result, ocrErr := callOCRAPI(ctx, imageData)
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

	items, mergeErr := recognizeSegments(ctx, segments, yOffsets)
	if mergeErr != nil {
		return nil, 0, mergeErr
	}

	return items, width, nil
}

// 微信时间分隔符正则（匹配整行）
// 匹配格式：下午 2:30 / 昨天 下午 3:45 / 星期三 上午 9:00 / 10月15日 下午 2:30 等
var wechatTimeRegex = regexp.MustCompile(
	`^((\d{4}年)?\d{1,2}月\d{1,2}日\s*)?` + // 可选日期
		`(昨天|前天|今天|星期[一二三四五六日天])?\s*` + // 可选相对日期/星期
		`(上午|下午|凌晨|中午|晚上)?\s*` + // 可选时段
		`\d{1,2}:\d{2}\s*$`, // 时间 HH:MM
)

// 语音消息时长正则（匹配 5" / 15” / 1'23" / 0:15 等）
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

// isVoiceDuration 判断文本是否为语音消息时长（如 5"、15”、1'23"）
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

// formatChatMessages 根据文字位置信息将 OCR 结果格式化为纯对话文本
// 算法：内容去噪 → 气泡分组（按 Y 间距合并同一气泡的多行文字）→ 每个气泡输出一行
// 不标注说话人角色（客户/销售），因为无法仅凭位置判断截图来自哪一方
func formatChatMessages(items []WordsItem, imageWidth int) string {
	if len(items) == 0 || imageWidth <= 0 {
		return ""
	}

	// === Phase 1: 内容去噪 ===
	var filtered []WordsItem
	for _, item := range items {
		text := strings.TrimSpace(item.Words)
		if text == "" {
			continue
		}
		if isWechatTimestamp(text) {
			continue
		}
		if isVoiceDuration(text) {
			continue
		}
		centerX := float64(item.Location.Left) + float64(item.Location.Width)/2.0
		if isSystemMessage(text, centerX, imageWidth) {
			continue
		}
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
	// 同一气泡内的多行文字（Y 间距小）合并为一条消息
	heights := make([]int, len(filtered))
	for i, item := range filtered {
		h := item.Location.Height
		if h <= 0 {
			h = 30
		}
		heights[i] = h
	}
	sort.Ints(heights)
	medianHeight := heights[len(heights)/2]
	groupGap := medianHeight * 3 / 2
	if groupGap < 30 {
		groupGap = 30
	}

	type bubble struct {
		texts  []string
		bottom int
	}
	var bubbles []bubble
	for _, item := range filtered {
		itemTop := item.Location.Top
		itemBottom := item.Location.Top + item.Location.Height

		if len(bubbles) > 0 {
			last := &bubbles[len(bubbles)-1]
			if itemTop-last.bottom < groupGap {
				last.texts = append(last.texts, item.Words)
				if itemBottom > last.bottom {
					last.bottom = itemBottom
				}
				continue
			}
		}

		bubbles = append(bubbles, bubble{
			texts:  []string{item.Words},
			bottom: itemBottom,
		})
	}

	// === Phase 3: 格式化输出 ===
	var result strings.Builder
	for i, b := range bubbles {
		if i > 0 {
			result.WriteByte('\n')
		}
		result.WriteString(strings.Join(b.texts, ""))
	}

	return result.String()
}
