package book

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// AsyncBookProcessor 异步book处理器
type AsyncBookProcessor struct {
	biz BizInterface
}

// BizInterface 业务接口
type BizInterface interface {
	Books() AsyncBookBiz
	Cards() AsyncCardBiz
	Users() AsyncUserBiz
	Ali() AsyncAliBiz
	Volc() AsyncVolcBiz // 新增volc支持
	Templates() AsyncTemplateBiz
	Store() AsyncStoreBiz // 新增store层访问
}

// AsyncBookBiz 书籍业务接口
type AsyncBookBiz interface {
	Create(ctx context.Context, book *model.BookM) error
	Update(ctx context.Context, book *model.BookM) error
	GetByID(ctx context.Context, id uint) (*model.BookM, error)
	UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error
}

// AsyncCardBiz 卡片业务接口
type AsyncCardBiz interface {
	Create(ctx context.Context, card *model.CardM) error
	Update(ctx context.Context, card *model.CardM) error
}

// AsyncUserBiz 用户业务接口
type AsyncUserBiz interface {
	IncrementUserBookNum(ctx context.Context, userID uint) error
	IncrementUserCardNum(ctx context.Context, userID uint) error
}

// AsyncAliBiz 阿里业务接口
type AsyncAliBiz interface {
	QianwenTextStream(messages []map[string]string, maxTokens int, temperature float64) (string, error)
	WanxiangImageAsync(prompt, style, size string) (string, error)
	StableDiffusionImageAsync(prompt, size string) (string, error)
	GetPromptManager() AsyncPromptManager
}

// AsyncVolcBiz 火山引擎业务接口
type AsyncVolcBiz interface {
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error)
}

// AsyncTemplateBiz 模板业务接口
type AsyncTemplateBiz interface {
	GetByID(ctx context.Context, id uint) (*model.Template, error)
}

// AsyncPromptManager 提示词管理器接口
type AsyncPromptManager interface {
	GetTextProcessingPrompt() string
}

// AsyncStoreBiz store层业务接口
type AsyncStoreBiz interface {
	UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error
}

// NewAsyncBookProcessor 创建异步book处理器
func NewAsyncBookProcessor(biz BizInterface) *AsyncBookProcessor {
	return &AsyncBookProcessor{
		biz: biz,
	}
}

// CreateBookAsync 异步创建book
func (p *AsyncBookProcessor) CreateBookAsync(ctx context.Context, userID uint, text, templateID string) (*model.BookM, error) {
	// 立即创建book记录，状态为creating
	now := time.Now()
	book := &model.BookM{
		UserID:     userID,
		Title:      fmt.Sprintf("AI生成卡册 - %s", now.Format("2006-01-02 15:04:05")),
		TemplateID: templateID,
		ViewTime:   &now,
		Status:     model.BookStatusCreating,
	}

	if err := p.biz.Books().Create(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to create initial book record", "error", err.Error())
		return nil, err
	}

	// 创建book后立即更新用户统计
	if err := p.biz.Users().IncrementUserBookNum(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment user book num", "error", err.Error())
		// 统计更新失败不影响主要流程，但记录错误
	}

	// 在后台异步处理book创建
	go func() {
		p.processBookCreationInBackground(ctx, book.ID, userID, text, templateID)
	}()

	return book, nil
}

// processBookCreationInBackground 在后台处理book创建
func (p *AsyncBookProcessor) processBookCreationInBackground(ctx context.Context, bookID uint, userID uint, text, templateID string) {
	startTime := time.Now()
	log.C(ctx).Infow("Starting async book creation", "book_id", bookID, "user_id", userID)

	// 获取book记录
	book, err := p.biz.Books().GetByID(ctx, bookID)
	if err != nil {
		log.C(ctx).Errorw("Failed to get book for async processing", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to get book record")
		return
	}

	// 获取模板背景信息
	var templateBackground string
	if templateID != "" {
		// 将string类型的templateID转换为uint
		if tid, err := strconv.ParseUint(templateID, 10, 64); err == nil {
			template, err := p.biz.Templates().GetByID(ctx, uint(tid))
			if err != nil {
				log.C(ctx).Warnw("Failed to get template, using default white background", "template_id", templateID, "error", err.Error())
				templateBackground = "" // 使用默认白色背景
			} else if template.File != "" {
				templateBackground = template.File
				log.C(ctx).Infow("Template background loaded", "template_id", templateID, "background", templateBackground)
			} else {
				log.C(ctx).Warnw("Template has no file, using default white background", "template_id", templateID)
				templateBackground = "" // 使用默认白色背景
			}
		} else {
			log.C(ctx).Warnw("Invalid template ID format, using default white background", "template_id", templateID, "error", err.Error())
			templateBackground = "" // 使用默认白色背景
		}
	} else {
		log.C(ctx).Infow("No template ID provided, using default white background")
		templateBackground = "" // 使用默认白色背景
	}

	// 🚀 使用增强文本处理器处理长文本（解决JSON截断问题）
	log.C(ctx).Infow("🚀 启动增强文本处理流程", "book_id", bookID, "text_length", len(text))

	// 创建增强文本处理器
	enhancedProcessor := NewEnhancedTextProcessor()

	// 记录处理器配置
	processorStats := enhancedProcessor.GetProcessorStats()
	log.C(ctx).Infow("📊 增强处理器配置", "book_id", bookID, "config", processorStats)

	// 定义API调用函数（增强版，支持参数优化和空响应快速检测）
	apiCaller := func(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64, bookID uint) (string, error) {
		log.C(ctx).Debugw("🚀 开始API调用",
			"book_id", bookID,
			"requested_max_tokens", maxTokens,
			"requested_temperature", temperature)

		// 创建参数优化器
		paramOptimizer := NewAPIParametersOptimizer()

		// 优先尝试阿里千问API
		result, err := p.callQianwenWithEnhancedRetry(ctx, messages, maxTokens, temperature, bookID, paramOptimizer)
		if err != nil {
			log.C(ctx).Warnw("⚠️ 阿里千问API失败，尝试火山引擎降级", "book_id", bookID, "error", err.Error())

			// 降级到火山引擎API
			volcResult, volcErr := p.callVolcWithEnhancedRetry(ctx, messages, maxTokens, temperature, bookID, paramOptimizer)
			if volcErr != nil {
				return "", fmt.Errorf("所有API都失败: qianwen=%w, volc=%w", err, volcErr)
			}

			log.C(ctx).Infow("✅ 火山引擎API降级成功", "book_id", bookID)
			return volcResult, nil
		}

		log.C(ctx).Infow("✅ 阿里千问API调用成功", "book_id", bookID)
		return result, nil
	}

	// 使用增强处理器处理长文本
	result, err := enhancedProcessor.ProcessLongText(ctx, text, apiCaller, bookID)
	if err != nil {
		log.C(ctx).Errorw("❌ 增强文本处理失败", "book_id", bookID, "error", err.Error())
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, fmt.Sprintf("Enhanced text processing failed: %v", err))
		return
	}

	// 记录处理结果统计
	log.C(ctx).Infow("📈 增强处理完成",
		"book_id", bookID,
		"total_time", result.TotalTime,
		"total_chunks", result.MergeStats.TotalChunks,
		"success_chunks", result.MergeStats.SuccessChunks,
		"failed_chunks", result.MergeStats.FailedChunks,
		"total_retries", result.MergeStats.TotalRetries,
		"required_merge", result.MergeStats.RequiredMerge,
		"final_json_length", len(result.FinalJSON))

	// 使用处理结果
	jsonContent := result.FinalJSON

	// 验证最终JSON
	if jsonContent == "" {
		log.C(ctx).Errorw("❌ 增强处理器返回空JSON", "book_id", bookID)
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Enhanced processor returned empty JSON")
		return
	}

	// 解析AI返回的JSON结果
	var aiResponse QianwenResponse
	if err := json.Unmarshal([]byte(jsonContent), &aiResponse); err != nil {
		log.C(ctx).Warnw("AI response JSON解析失败，尝试使用高级修复引擎",
			"book_id", bookID,
			"error", err.Error(),
			"json_content_length", len(jsonContent))

		// 使用高级JSON修复引擎
		extractor := httpclient.NewAdvancedJSONExtractor()
		repairedJSON, repairErr := extractor.ExtractValidJSON([]byte(jsonContent))

		if repairErr != nil {
			log.C(ctx).Errorw("JSON修复也失败",
				"book_id", bookID,
				"original_error", err.Error(),
				"repair_error", repairErr.Error(),
				"json_content_preview", jsonContent[:minInt(len(jsonContent), 500)])
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to parse AI response after repair: "+repairErr.Error())
			return
		}

		// 尝试解析修复后的JSON
		if err := json.Unmarshal(repairedJSON, &aiResponse); err != nil {
			log.C(ctx).Errorw("修复后JSON解析仍然失败",
				"book_id", bookID,
				"error", err.Error(),
				"repaired_json_length", len(repairedJSON),
				"repaired_json_preview", string(repairedJSON[:minInt(len(repairedJSON), 500)]))
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "Failed to parse repaired AI response: "+err.Error())
			return
		}

		log.C(ctx).Infow("JSON解析成功（经过高级修复）",
			"book_id", bookID,
			"original_length", len(jsonContent),
			"repaired_length", len(repairedJSON))
	}

	// 验证解析后的数据结构
	if aiResponse.Text == "" {
		log.C(ctx).Errorw("AI response has no text content",
			"book_id", bookID,
			"response", aiResponse)
		p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "AI response has no text content")
		return
	}

	// 分页引擎将在processBookWithMarkdownRenderer中使用

	// 从markdown文本中提取title作为book的标题
	bookTitle := p.extractTitleFromMarkdown(aiResponse.Text)
	if bookTitle == "" {
		bookTitle = fmt.Sprintf("AI生成卡册 - %s", time.Now().Format("2006-01-02 15:04:05"))
	}

	// 使用解析出的image_prompt调用stable-diffusion生成图片
	var imageUrl string
	if aiResponse.ImagePrompt != "" {
		// 直接使用原始提示词调用stable-diffusion API
		remoteImageUrl, err := p.biz.Ali().StableDiffusionImageAsync(aiResponse.ImagePrompt, "1024*1024")
		if err != nil {
			log.C(ctx).Errorw("StableDiffusionImageAsync failed", "book_id", bookID, "error", err.Error())
			// 图片生成失败不影响整体流程
		} else {
			// 下载并保存图片到本地
			localImagePath, err := downloadAndSaveImage(remoteImageUrl, bookID)
			if err != nil {
				log.C(ctx).Errorw("Failed to download and save image", "book_id", bookID, "error", err.Error())
			} else {
				imageUrl = localImagePath
			}
		}
	}

	// 更新book记录
	book.Title = bookTitle
	book.ImageUrl = imageUrl
	if err := p.biz.Books().Update(ctx, book); err != nil {
		log.C(ctx).Errorw("Failed to update book with title and image", "book_id", bookID, "error", err.Error())
	}

	// 获取模板背景信息
	var coverBackground string
	if templateID != "" {
		if tid, err := strconv.ParseUint(templateID, 10, 64); err == nil {
			if tmpl, err := p.biz.Templates().GetByID(ctx, uint(tid)); err == nil && tmpl != nil {
				// 这里假设 template.File 字段保存的是背景图的绝对路径
				coverBackground = tmpl.File
				log.C(ctx).Infow("获取到模板背景图", "book_id", bookID, "template_id", templateID, "background_path", coverBackground)
			}
		}
	}

	// 使用简化的markdown渲染器处理
	// 直接使用aiResponse.Text作为markdown内容，自动分页和渲染
	if aiResponse.Text != "" {
		log.C(ctx).Infow("使用AI返回的markdown文本", "book_id", bookID, "text_length", len(aiResponse.Text))

		// 直接使用markdown渲染器处理
		if err := p.processBookWithMarkdownRenderer(ctx, book, userID, aiResponse.Text, coverBackground); err != nil {
			log.C(ctx).Errorw("markdown渲染器处理失败", "book_id", bookID, "error", err.Error())
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "markdown渲染器处理失败: "+err.Error())
			return
		}
	} else {
		// 后备方案：使用原始文本
		log.C(ctx).Infow("AI未返回markdown内容，使用原始文本作为后备方案", "book_id", bookID, "original_text_length", len(text))

		if err := p.processBookWithMarkdownRenderer(ctx, book, userID, text, coverBackground); err != nil {
			log.C(ctx).Errorw("markdown渲染器处理失败", "book_id", bookID, "error", err.Error())
			p.updateBookStatus(ctx, bookID, model.BookStatusFailed, "markdown渲染器处理失败: "+err.Error())
			return
		}
	}

	// 直接完成，不需要其他渲染流程
	log.C(ctx).Infow("🎉 markdown渲染器处理完成，跳过其他渲染流程", "book_id", bookID)
	// 直接进行最后的状态更新并返回
	p.finalizeBookCreation(ctx, bookID, startTime)
	return
}

// updateBookStatus 更新book状态
func (p *AsyncBookProcessor) updateBookStatus(ctx context.Context, bookID uint, status, errorMsg string) error {
	book, err := p.biz.Books().GetByID(ctx, bookID)
	if err != nil {
		return err
	}

	oldStatus := book.Status
	book.Status = status

	if err := p.biz.Books().Update(ctx, book); err != nil {
		return err
	}

	// 如果状态发生变化，需要更新用户统计
	if oldStatus != status {
		// 调用store层的方法来更新用户统计
		if err := p.biz.Store().UpdateUserBookStatsOnStatusChange(ctx, book.UserID, oldStatus, status); err != nil {
			// 记录错误但不影响状态更新操作
			// 这里可以考虑记录日志
		}
	}

	return nil
}

// QianwenResponse 通义千问返回的结构化数据
type QianwenResponse struct {
	Text        string `json:"text"`         // 带markdown格式的文字内容
	ImagePrompt string `json:"image_prompt"` // 文生图提示词
}

// extractJSONWithRetry 带重试的JSON提取（更激进的修复策略）
func extractJSONWithRetry(response string) string {
	fmt.Printf("=== 重试JSON提取（激进策略）===\n")

	// 策略1: 尝试修复不完整的JSON结构
	fixedJSON := fixIncompleteJSON(response)
	if fixedJSON != "" && isValidJSON(fixedJSON) {
		fmt.Printf("重试成功：修复了不完整的JSON结构\n")
		return fixedJSON
	}

	// 策略2: 查找包含关键字段的部分JSON并强制修复
	keyFields := []string{"structured_text_array", "image_prompt"}
	for _, field := range keyFields {
		fieldIndex := strings.Index(response, fmt.Sprintf(`"%s"`, field))
		if fieldIndex != -1 {
			fmt.Printf("重试策略：找到字段 '%s'，尝试强制修复\n", field)

			// 向前查找最近的 { 开始
			braceStart := -1
			for i := fieldIndex; i >= 0; i-- {
				if response[i] == '{' {
					braceStart = i
					break
				}
			}

			if braceStart != -1 {
				partialJSON := response[braceStart:]
				// 使用更激进的修复策略
				aggressiveFixed := aggressiveJSONFix(partialJSON)
				if aggressiveFixed != "" && isValidJSON(aggressiveFixed) {
					fmt.Printf("重试成功：使用激进策略修复了JSON\n")
					return aggressiveFixed
				}
			}
		}
	}

	fmt.Printf("重试失败：所有激进策略都失败了\n")
	return ""
}

// cleanJSONWithSmartFilter 使用智能过滤清理JSON，保留中文字符
func cleanJSONWithSmartFilter(jsonStr string) string {
	var result strings.Builder
	removedCount := 0

	fmt.Printf("开始智能字符过滤，原始长度: %d\n", len(jsonStr))

	for i, char := range jsonStr {
		// 1. 检查是否是无效的Unicode字符
		if char == utf8.RuneError || char == 0xFFFD {
			fmt.Printf("移除无效Unicode字符，位置: %d, 字符: 0x%02x\n", i, char)
			removedCount++
			continue
		}

		// 2. 检查是否是控制字符（除了换行符和制表符）
		if char >= 0 && char <= 31 && char != '\n' && char != '\t' {
			fmt.Printf("移除控制字符，位置: %d, 字符: 0x%02x (rune: %q)\n", i, char, char)
			removedCount++
			continue
		}

		// 3. 检查是否是扩展ASCII字符（128-255）
		if char >= 128 && char <= 255 {
			fmt.Printf("移除扩展ASCII字符，位置: %d, 字符: 0x%02x (rune: %q)\n", i, char, char)
			removedCount++
			continue
		}

		// 4. 检查是否是JSON结构中的问题字符
		if isJSONStructureProblemChar(char, jsonStr, i) {
			fmt.Printf("移除JSON结构问题字符，位置: %d, 字符: 0x%02x (rune: %q)\n", i, char, char)
			removedCount++
			continue
		}

		// 5. 保留所有其他字符（包括中文字符）
		result.WriteRune(char)
	}

	cleaned := result.String()
	fmt.Printf("智能字符过滤完成: %d -> %d 字符，移除了 %d 个字符\n", len(jsonStr), len(cleaned), removedCount)

	if removedCount > 0 {
		// 显示清理后的预览
		if len(cleaned) > 0 {
			preview := cleaned
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			fmt.Printf("清理后预览: %q\n", preview)
		}
	}

	return cleaned
}

// isJSONStructureProblemChar 检查是否是JSON结构中的问题字符
func isJSONStructureProblemChar(char rune, jsonStr string, position int) bool {
	// 检查是否是JSON结构中的常见问题字符
	problemChars := []rune{'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z'}

	// 检查是否是问题字符
	for _, problemChar := range problemChars {
		if char == problemChar {
			// 进一步检查上下文，判断是否真的是问题字符
			return isContextuallyProblematicChar(jsonStr, position, char)
		}
	}

	return false
}

// isContextuallyProblematicChar 检查字符在上下文中是否真的有问题
func isContextuallyProblematicChar(jsonStr string, position int, char rune) bool {
	// 检查字符前后的上下文
	before := ""
	after := ""

	if position > 0 {
		before = string(jsonStr[position-1])
	}
	if position < len(jsonStr)-1 {
		after = string(jsonStr[position+1])
	}

	// 如果字符前后都是有效的JSON字符，那么它可能不是问题字符
	validBefore := isValidJSONContextChar(before)
	validAfter := isValidJSONContextChar(after)

	// 如果前后都是有效的，那么这个字符可能不是问题
	if validBefore && validAfter {
		return false
	}

	// 检查是否在JSON字符串中
	inString := isInJSONString(jsonStr, position)
	if inString {
		// 在JSON字符串中的字符通常是有效的
		return false
	}

	// 检查是否在JSON对象或数组的键值对中
	if isInJSONKeyValue(jsonStr, position) {
		// 在键值对中的字符可能是问题字符
		return true
	}

	return false
}

// isValidJSONContextChar 检查字符是否是有效的JSON上下文
func isValidJSONContextChar(char string) bool {
	if char == "" {
		return true
	}

	validChars := []string{`"`, `{`, `}`, `[`, `]`, `:`, `,`, ` `, `\n`, `\t`}
	for _, valid := range validChars {
		if char == valid {
			return true
		}
	}

	return false
}

// isInJSONString 检查位置是否在JSON字符串中
func isInJSONString(jsonStr string, position int) bool {
	// 计算当前位置之前的引号数量
	quoteCount := 0
	escaped := false

	for i := 0; i < position; i++ {
		if jsonStr[i] == '\\' && !escaped {
			escaped = true
			continue
		}

		if jsonStr[i] == '"' && !escaped {
			quoteCount++
		}

		escaped = false
	}

	// 如果引号数量是奇数，说明在字符串中
	return quoteCount%2 == 1
}

// isInJSONKeyValue 检查位置是否在JSON键值对中
func isInJSONKeyValue(jsonStr string, position int) bool {
	// 查找最近的冒号
	colonPos := -1
	for i := position; i >= 0; i-- {
		if jsonStr[i] == ':' {
			colonPos = i
			break
		}
	}

	if colonPos == -1 {
		return false
	}

	// 查找冒号后的下一个引号或大括号
	for i := colonPos + 1; i < len(jsonStr); i++ {
		if jsonStr[i] == '"' || jsonStr[i] == '{' || jsonStr[i] == '[' {
			// 如果当前位置在这个范围内，说明在键值对中
			return position > colonPos && position < i
		}
	}

	return false
}

// aggressiveJSONFix 激进的JSON修复策略
func aggressiveJSONFix(jsonStr string) string {
	fmt.Printf("使用激进策略修复JSON，原始长度: %d\n", len(jsonStr))

	// 策略1: 使用智能字符过滤，保留中文字符
	cleaned := cleanJSONWithSmartFilter(jsonStr)

	// 策略2: 强制修复JSON结构
	repaired := repairJSONStructure(cleaned)

	// 策略3: 如果仍然无效，尝试添加默认值
	if !isValidJSON(repaired) {
		fmt.Printf("激进修复后仍然无效，尝试添加默认值...\n")
		repaired = addDefaultValues(repaired)
	}

	return repaired
}

// addDefaultValues 为不完整的JSON添加默认值
func addDefaultValues(jsonStr string) string {
	// 检查是否包含 structured_text_array
	if !strings.Contains(jsonStr, "structured_text_array") {
		// 在最后一个 } 之前添加默认的 structured_text_array
		lastBrace := strings.LastIndex(jsonStr, "}")
		if lastBrace != -1 {
			defaultArray := `,"structured_text_array":[{"type":"body","content":"内容解析失败，请重试"}]`
			jsonStr = jsonStr[:lastBrace] + defaultArray + jsonStr[lastBrace:]
		}
	}

	// 检查是否包含 image_prompt
	if !strings.Contains(jsonStr, "image_prompt") {
		// 在最后一个 } 之前添加默认的 image_prompt
		lastBrace := strings.LastIndex(jsonStr, "}")
		if lastBrace != -1 {
			defaultPrompt := `,"image_prompt":"默认图片描述"`
			jsonStr = jsonStr[:lastBrace] + defaultPrompt + jsonStr[lastBrace:]
		}
	}

	return jsonStr
}

// extractJSONFromResponse 从响应中提取JSON内容
func extractJSONFromResponse(response string) string {
	// 记录原始响应用于调试
	fmt.Printf("Raw response length: %d\n", len(response))
	if len(response) > 1000 {
		fmt.Printf("Raw response preview (first 500 chars): %q\n", response[:500])
		fmt.Printf("Raw response preview (last 500 chars): %q\n", response[len(response)-500:])
	} else {
		fmt.Printf("Raw response: %q\n", response)
	}

	// 策略1: 尝试直接解析（如果响应本身就是有效的JSON）
	if isValidJSON(response) {
		fmt.Printf("Response is already valid JSON\n")
		return response
	}

	// 策略2: 使用新的JSON响应处理器进行深度修复
	fmt.Printf("使用新的JSON响应处理器进行深度修复...\n")

	// 创建模拟的HTTP响应，使用新的JSON响应处理器
	processor := httpclient.NewJSONResponseProcessor()

	// 模拟HTTP响应结构
	mockResp := &http.Response{
		Body: io.NopCloser(strings.NewReader(response)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	// 使用新的处理器处理响应
	processedBody, err := processor.ProcessResponse(mockResp)
	if err == nil && len(processedBody) > 0 {
		fmt.Printf("新的JSON响应处理器处理成功，长度: %d\n", len(processedBody))

		// 验证处理后的JSON是否有效
		if isValidJSON(string(processedBody)) {
			fmt.Printf("处理后的JSON验证成功\n")
			return string(processedBody)
		} else {
			fmt.Printf("处理后的JSON验证失败，继续使用旧方法\n")
		}
	} else {
		fmt.Printf("新的JSON响应处理器处理失败: %v，继续使用旧方法\n", err)
	}

	// 策略3: 深度清理响应内容（旧方法作为备选）
	cleanedResponse := deepCleanResponse(response)
	fmt.Printf("Deep cleaned response length: %d\n", len(cleanedResponse))

	// 尝试解析深度清理后的响应
	if isValidJSON(cleanedResponse) {
		fmt.Printf("Deep cleaned response is valid JSON\n")
		return cleanedResponse
	}

	// 策略3: 智能提取JSON内容
	extractedJSON := smartExtractJSON(cleanedResponse)
	if extractedJSON != "" && isValidJSON(extractedJSON) {
		fmt.Printf("Successfully extracted valid JSON, length: %d\n", len(extractedJSON))
		return extractedJSON
	}

	// 策略4: 回退到最基础的提取方法
	fallbackJSON := fallbackExtractJSON(cleanedResponse)
	if fallbackJSON != "" && isValidJSON(fallbackJSON) {
		fmt.Printf("Using fallback JSON extraction, length: %d\n", len(fallbackJSON))
		return fallbackJSON
	}

	// 策略5: 最后尝试修复常见问题
	fixedJSON := fixCommonJSONIssues(response)
	if fixedJSON != "" && isValidJSON(fixedJSON) {
		fmt.Printf("Fixed common JSON issues, length: %d\n", len(fixedJSON))
		return fixedJSON
	}

	// 如果所有方法都失败，记录错误并返回空字符串
	fmt.Printf("All JSON extraction methods failed\n")
	return ""
}

// deepCleanResponse 深度清理响应内容
func deepCleanResponse(response string) string {
	// 第一步：移除所有HTML标签及其内容
	cleaned := response

	// 移除 <think> 标签及其内容
	cleaned = removeTagContent(cleaned, "think")

	// 移除其他可能的HTML标签
	cleaned = removeTagContent(cleaned, "html")
	cleaned = removeTagContent(cleaned, "body")
	cleaned = removeTagContent(cleaned, "div")
	cleaned = removeTagContent(cleaned, "p")
	cleaned = removeTagContent(cleaned, "span")
	cleaned = removeTagContent(cleaned, "script")
	cleaned = removeTagContent(cleaned, "style")
	cleaned = removeTagContent(cleaned, "head")
	cleaned = removeTagContent(cleaned, "title")
	cleaned = removeTagContent(cleaned, "meta")
	cleaned = removeTagContent(cleaned, "link")

	// 第二步：标准化换行符和空格
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\n\n", "\n")
	cleaned = strings.TrimSpace(cleaned)

	// 第三步：移除BOM标记
	if len(cleaned) > 3 && cleaned[0] == 0xEF && cleaned[1] == 0xBB && cleaned[2] == 0xBF {
		cleaned = cleaned[3:]
	}

	// 第四步：移除控制字符，但保留必要的字符
	var result strings.Builder
	for _, r := range cleaned {
		// 保留：字母、数字、标点符号、空格、换行、制表符
		// 移除：控制字符、零宽字符、其他不可见字符
		if (r >= 32 && r <= 126) || r == '\n' || r == '\t' || r == '\r' {
			result.WriteRune(r)
		}
	}

	// 第五步：移除多余的空白字符
	cleaned = result.String()
	cleaned = strings.ReplaceAll(cleaned, "  ", " ")    // 双空格变单空格
	cleaned = strings.ReplaceAll(cleaned, "\n\n", "\n") // 双换行变单换行
	cleaned = strings.TrimSpace(cleaned)

	return cleaned
}

// removeTagContent 移除指定标签及其内容
func removeTagContent(content, tagName string) string {
	// 移除开始标签
	startTag := fmt.Sprintf("<%s", tagName)
	endTag := fmt.Sprintf("</%s>", tagName)

	// 查找开始标签位置
	startPos := strings.Index(content, startTag)
	if startPos == -1 {
		return content
	}

	// 查找结束标签位置
	endPos := strings.Index(content, endTag)
	if endPos == -1 {
		// 如果没有结束标签，只移除开始标签
		return content[:startPos] + content[startPos+len(startTag):]
	}

	// 移除整个标签及其内容
	return content[:startPos] + content[endPos+len(endTag):]
}

// isValidJSON 验证字符串是否为有效的JSON
func isValidJSON(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}

	var js json.RawMessage
	err := json.Unmarshal([]byte(s), &js)
	if err != nil {
		fmt.Printf("JSON validation failed: %v\n", err)
		return false
	}
	return true
}

// fixCommonJSONIssues 修复常见的JSON问题（保留原有功能）
func fixCommonJSONIssues(response string) string {
	cleaned := response

	// 修复1: 移除JSON末尾的额外内容
	// 查找最后一个有效的 } 或 ]
	lastBrace := strings.LastIndex(cleaned, "}")
	lastBracket := strings.LastIndex(cleaned, "]")

	var endPos int
	if lastBrace > lastBracket {
		endPos = lastBrace + 1
	} else if lastBracket > lastBrace {
		endPos = lastBracket + 1
	} else {
		return cleaned // 没有找到结束符
	}

	// 移除JSON末尾的额外内容
	cleaned = cleaned[:endPos]

	// 修复2: 处理可能的编码问题
	// 移除常见的无效字符序列
	cleaned = strings.ReplaceAll(cleaned, "\\'", "'")
	cleaned = strings.ReplaceAll(cleaned, "\\\"", "\"")

	// 修复3: 确保JSON结构完整
	// 如果以 { 开始但没有对应的 }，尝试添加
	if strings.HasPrefix(cleaned, "{") && !strings.HasSuffix(cleaned, "}") {
		// 计算大括号的平衡
		braceCount := 0
		for _, char := range cleaned {
			if char == '{' {
				braceCount++
			} else if char == '}' {
				braceCount--
			}
		}

		// 如果缺少结束大括号，添加它们
		for i := 0; i < braceCount; i++ {
			cleaned += "}"
		}
	}

	// 修复4: 处理可能的Unicode转义问题
	// 移除无效的Unicode转义序列
	cleaned = removeInvalidUnicodeEscapes(cleaned)

	// 修复5: 修复常见的JSON结构问题
	cleaned = fixJSONStructureIssues(cleaned)

	return cleaned
}

// fixJSONStructureIssues 修复JSON结构问题
func fixJSONStructureIssues(jsonStr string) string {
	// 修复缺失的逗号
	jsonStr = strings.ReplaceAll(jsonStr, "}\n{", "},\n{")
	jsonStr = strings.ReplaceAll(jsonStr, "}\n \"", "},\n \"")
	jsonStr = strings.ReplaceAll(jsonStr, "]\n{", "],\n{")
	jsonStr = strings.ReplaceAll(jsonStr, "]\n \"", "],\n \"")

	// 修复缺失的引号
	jsonStr = strings.ReplaceAll(jsonStr, "content\": \"", "content\": \"")
	jsonStr = strings.ReplaceAll(jsonStr, "type\": \"", "type\": \"")

	// 修复数组元素之间的分隔
	jsonStr = strings.ReplaceAll(jsonStr, "\"}\n{", "\"},\n{")
	jsonStr = strings.ReplaceAll(jsonStr, "\"]\n[", "\"],\n[")

	// 修复对象属性之间的分隔
	jsonStr = strings.ReplaceAll(jsonStr, "\"\n \"", "\",\n \"")

	fmt.Printf("修复了常见的JSON结构问题\n")
	return jsonStr
}

// removeInvalidUnicodeEscapes 移除无效的Unicode转义序列
func removeInvalidUnicodeEscapes(s string) string {
	// 查找并移除无效的 \u 转义序列
	var result strings.Builder
	i := 0
	for i < len(s) {
		if i+5 < len(s) && s[i] == '\\' && s[i+1] == 'u' {
			// 检查接下来的4个字符是否为有效的十六进制
			hexStr := s[i+2 : i+6]
			if isValidHexString(hexStr) {
				result.WriteString(s[i : i+6])
				i += 6
			} else {
				// 无效的Unicode转义，跳过
				result.WriteByte(s[i])
				i++
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

// isValidHexString 检查字符串是否为有效的十六进制
func isValidHexString(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, char := range s {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

// smartExtractJSON 智能提取JSON内容
func smartExtractJSON(response string) string {
	// 策略1: 优先查找包含关键字段的JSON（最重要的策略）
	fieldBasedJSON := findJSONByFields(response)
	if fieldBasedJSON != "" {
		fmt.Printf("Found JSON by fields (PRIORITY), length: %d\n", len(fieldBasedJSON))
		return fieldBasedJSON
	}

	// 策略2: 查找最长的JSON对象
	longestJSON := findLongestJSON(response)
	if longestJSON != "" {
		fmt.Printf("Found longest JSON object, length: %d\n", len(longestJSON))
		return longestJSON
	}

	// 策略3: 查找JSON数组
	arrayJSON := findJSONArray(response)
	if arrayJSON != "" {
		fmt.Printf("Found JSON array, length: %d\n", len(arrayJSON))
		return arrayJSON
	}

	// 策略4: 回退到原始提取方法
	return fallbackExtractJSON(response)
}

// findLongestJSON 查找最长的有效JSON对象
func findLongestJSON(response string) string {
	var longestJSON string
	maxLength := 0

	// 查找所有可能的JSON对象
	braceCount := 0
	start := -1

	for i, char := range response {
		if char == '{' {
			if braceCount == 0 {
				start = i
			}
			braceCount++
		} else if char == '}' {
			braceCount--
			if braceCount == 0 && start != -1 {
				// 找到一个完整的JSON对象
				jsonCandidate := response[start : i+1]
				if isValidJSON(jsonCandidate) && len(jsonCandidate) > maxLength {
					longestJSON = jsonCandidate
					maxLength = len(jsonCandidate)
				}
				start = -1
			}
		}
	}

	// 如果没有找到完整的JSON对象，尝试修复不完整的JSON
	if longestJSON == "" {
		fmt.Printf("No complete JSON found, attempting to fix incomplete JSON...\n")
		longestJSON = fixIncompleteJSON(response)
	}

	return longestJSON
}

// findJSONArray 查找JSON数组
func findJSONArray(response string) string {
	var longestArray string
	maxLength := 0

	// 查找所有可能的JSON数组
	bracketCount := 0
	start := -1

	for i, char := range response {
		if char == '[' {
			if bracketCount == 0 {
				start = i
			}
			bracketCount++
		} else if char == ']' {
			bracketCount--
			if bracketCount == 0 && start != -1 {
				// 找到一个完整的JSON数组
				jsonCandidate := response[start : i+1]
				if isValidJSON(jsonCandidate) && len(jsonCandidate) > maxLength {
					longestArray = jsonCandidate
					maxLength = len(jsonCandidate)
				}
				start = -1
			}
		}
	}

	return longestArray
}

// findJSONByFields 根据字段查找JSON
func findJSONByFields(response string) string {
	// 查找包含关键字段的JSON
	keyFields := []string{"structured_text_array", "image_prompt"}

	// 策略1: 查找包含所有关键字段的完整JSON对象
	braceCount := 0
	start := -1

	for i, char := range response {
		if char == '{' {
			if braceCount == 0 {
				start = i
			}
			braceCount++
		} else if char == '}' {
			braceCount--
			if braceCount == 0 && start != -1 {
				// 检查是否包含所有关键字段
				jsonCandidate := response[start : i+1]
				if containsAllFields(jsonCandidate, keyFields) && isValidJSON(jsonCandidate) {
					fmt.Printf("Found complete JSON with all key fields\n")
					return jsonCandidate
				}
				start = -1
			}
		}
	}

	// 策略2: 如果没有找到完整的JSON，尝试查找包含关键字段的部分JSON
	fmt.Printf("No complete JSON with all fields found, searching for partial JSON...\n")

	// 查找包含关键字段的部分JSON
	for _, field := range keyFields {
		fieldIndex := strings.Index(response, fmt.Sprintf(`"%s"`, field))
		if fieldIndex != -1 {
			fmt.Printf("Found field '%s' at position %d\n", field, fieldIndex)

			// 向前查找最近的 { 开始
			braceStart := -1
			for i := fieldIndex; i >= 0; i-- {
				if response[i] == '{' {
					braceStart = i
					break
				}
			}

			if braceStart != -1 {
				// 尝试修复这部分JSON
				partialJSON := response[braceStart:]
				fmt.Printf("Attempting to fix partial JSON starting with field '%s'...\n", field)

				fixedJSON := fixIncompleteJSON(partialJSON)
				if fixedJSON != "" && isValidJSON(fixedJSON) {
					fmt.Printf("Successfully fixed partial JSON containing field '%s'\n", field)
					return fixedJSON
				}
			}
		}
	}

	// 策略3: 如果仍然没有找到，尝试查找包含至少一个关键字段的JSON
	fmt.Printf("No partial JSON found, searching for JSON with at least one key field...\n")

	// 查找包含至少一个关键字段的JSON
	for _, field := range keyFields {
		fieldIndex := strings.Index(response, fmt.Sprintf(`"%s"`, field))
		if fieldIndex != -1 {
			fmt.Printf("Found field '%s' at position %d, attempting to extract surrounding JSON...\n", field, fieldIndex)

			// 向前查找最近的 { 开始
			braceStart := -1
			for i := fieldIndex; i >= 0; i-- {
				if response[i] == '{' {
					braceStart = i
					break
				}
			}

			if braceStart != -1 {
				// 尝试从 { 开始提取到响应末尾，然后修复
				partialJSON := response[braceStart:]
				fmt.Printf("Extracting partial JSON from position %d to end, length: %d\n", braceStart, len(partialJSON))

				// 使用更激进的修复策略
				aggressiveFixed := aggressiveJSONFix(partialJSON)
				if aggressiveFixed != "" && isValidJSON(aggressiveFixed) {
					fmt.Printf("Successfully extracted and fixed JSON containing field '%s'\n", field)
					return aggressiveFixed
				}
			}
		}
	}

	return ""
}

// containsAllFields 检查JSON字符串是否包含所有指定字段
func containsAllFields(jsonStr string, fields []string) bool {
	for _, field := range fields {
		if !strings.Contains(jsonStr, fmt.Sprintf(`"%s"`, field)) {
			return false
		}
	}
	return true
}

// fixIncompleteJSON 修复不完整的JSON
func fixIncompleteJSON(response string) string {
	fmt.Printf("Attempting to fix incomplete JSON...\n")

	// 查找最后一个 { 开始的位置
	lastBraceStart := strings.LastIndex(response, "{")
	if lastBraceStart == -1 {
		fmt.Printf("No opening brace found\n")
		return ""
	}

	// 从最后一个 { 开始，尝试构建完整的JSON
	partialJSON := response[lastBraceStart:]
	fmt.Printf("Found partial JSON starting at position %d, length: %d\n", lastBraceStart, len(partialJSON))

	// 尝试修复常见的JSON结构问题
	fixedJSON := fixCommonJSONIssues(partialJSON)

	// 如果修复后仍然无效，尝试添加缺失的结束符
	if !isValidJSON(fixedJSON) {
		fmt.Printf("JSON still invalid after common fixes, attempting structural repair...\n")
		fixedJSON = repairJSONStructure(fixedJSON)
	}

	// 验证修复后的JSON
	if isValidJSON(fixedJSON) {
		fmt.Printf("Successfully fixed incomplete JSON, length: %d\n", len(fixedJSON))
		return fixedJSON
	}

	fmt.Printf("Failed to fix incomplete JSON\n")
	return ""
}

// repairJSONStructure 修复JSON结构问题
func repairJSONStructure(jsonStr string) string {
	// 首先清理JSON字符串，移除无效字符
	cleaned := cleanJSONStringForStructure(jsonStr)

	// 尝试修复常见的JSON结构问题
	cleaned = fixCommonJSONIssues(cleaned)

	// 计算大括号和方括号的平衡
	braceCount := 0
	bracketCount := 0

	for _, char := range cleaned {
		switch char {
		case '{':
			braceCount++
		case '}':
			braceCount--
		case '[':
			bracketCount++
		case ']':
			bracketCount--
		}
	}

	// 添加缺失的结束符
	var result strings.Builder
	result.WriteString(cleaned)

	// 添加缺失的方括号结束符
	for i := 0; i < bracketCount; i++ {
		result.WriteString("]")
	}

	// 添加缺失的大括号结束符
	for i := 0; i < braceCount; i++ {
		result.WriteString("}")
	}

	fmt.Printf("Repaired JSON structure: added %d brackets and %d braces\n", bracketCount, braceCount)
	return result.String()
}

// cleanJSONStringForStructure 清理JSON字符串，专门用于结构修复
func cleanJSONStringForStructure(jsonStr string) string {
	var result strings.Builder
	removedCount := 0

	fmt.Printf("开始清理JSON结构，原始长度: %d\n", len(jsonStr))

	for i, char := range jsonStr {
		// 1. 保留所有有效的JSON结构字符
		if char == '{' || char == '}' || char == '[' || char == ']' || char == ':' || char == ',' || char == '"' {
			result.WriteRune(char)
			continue
		}

		// 2. 保留所有空白字符
		if char == ' ' || char == '\n' || char == '\t' || char == '\r' {
			result.WriteRune(char)
			continue
		}

		// 3. 保留所有字母数字字符和常用符号（用于键名和字符串值）
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' || char == '!' || char == '?' || char == ';' || char == '(' || char == ')' {
			result.WriteRune(char)
			continue
		}

		// 4. 保留中文字符和其他Unicode字符
		if char > 127 {
			result.WriteRune(char)
			continue
		}

		// 5. 只移除真正有问题的控制字符
		if char >= 0 && char <= 31 && char != '\n' && char != '\t' && char != '\r' {
			fmt.Printf("移除控制字符，位置: %d, 字符: 0x%02x (rune: %q)\n", i, char, char)
			removedCount++
			continue
		}

		// 6. 保留其他字符（包括下划线、感叹号等）
		result.WriteRune(char)
	}

	cleaned := result.String()
	fmt.Printf("JSON结构清理完成: %d -> %d 字符，移除了 %d 个字符\n", len(jsonStr), len(cleaned), removedCount)

	return cleaned
}

// fallbackExtractJSON 回退提取方法
func fallbackExtractJSON(response string) string {
	// 查找第一个 { 和最后一个 }
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")

	if start != -1 && end != -1 && end > start {
		candidate := response[start : end+1]
		fmt.Printf("Fallback extraction: found JSON candidate from %d to %d\n", start, end)
		return candidate
	}

	// 如果没有找到 { }，尝试查找 [ ]
	start = strings.Index(response, "[")
	end = strings.LastIndex(response, "]")

	if start != -1 && end != -1 && end > start {
		candidate := response[start : end+1]
		fmt.Printf("Fallback extraction: found JSON array candidate from %d to %d\n", start, end)
		return candidate
	}

	fmt.Printf("Fallback extraction: no JSON structure found\n")
	return ""
}

// downloadAndSaveImage 下载并保存图片
func downloadAndSaveImage(remoteURL string, bookID uint) (string, error) {
	// 计算本地保存目录：{image_path}/book/{book_id}
	localDir := util.GetBookImagePath(bookID)

	// 确保目录存在
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory %s: %w", localDir, err)
	}

	// 固定文件名：book_{id}.webp
	localFilePath := filepath.Join(localDir, fmt.Sprintf("book_%d.webp", bookID))

	// 下载远程图片
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(remoteURL)
	if err != nil {
		return "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download image, status: %d", resp.StatusCode)
	}

	// 创建本地文件并写入
	file, err := os.Create(localFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create local file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", fmt.Errorf("failed to save image: %w", err)
	}

	return localFilePath, nil
}

// finalizeBookCreation 完成book创建的最终步骤
func (p *AsyncBookProcessor) finalizeBookCreation(ctx context.Context, bookID uint, startTime time.Time) {
	// 更新book状态为成功
	if err := p.updateBookStatus(ctx, bookID, model.BookStatusSuccess, ""); err != nil {
		log.C(ctx).Errorw("Failed to update book status to success", "book_id", bookID, "error", err.Error())
		return
	}

	log.C(ctx).Infow("Async book creation completed", "book_id", bookID, "duration", time.Since(startTime).Seconds())
}

// callVolcWithRetry 带重试的火山引擎API调用（支持动态参数调整）
func (p *AsyncBookProcessor) callVolcWithRetry(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64, bookID uint) (string, error) {
	maxRetries := 5
	baseDelay := 2 * time.Second
	maxDelay := 30 * time.Second

	// 动态参数
	currentMaxTokens := maxTokens
	currentTemperature := temperature

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.C(ctx).Infow("🔄 尝试火山引擎API",
			"book_id", bookID,
			"attempt", attempt,
			"max_attempts", maxRetries,
			"max_tokens", currentMaxTokens,
			"temperature", currentTemperature)

		result, err := p.biz.Volc().VolcTextStream(ctx, messages, currentMaxTokens, currentTemperature)
		if err == nil {
			log.C(ctx).Infow("✅ 火山引擎API成功", "book_id", bookID, "attempt", attempt)
			return result, nil
		}

		lastErr = err
		log.C(ctx).Warnw("⚠️ 火山引擎API失败", "book_id", bookID, "attempt", attempt, "error", err.Error())

		// 动态调整参数（在重试时）
		if attempt < maxRetries {
			// 增加max_tokens以应对长文本
			if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "length") {
				currentMaxTokens = int(float64(currentMaxTokens) * 1.5)
				log.C(ctx).Infow("🔧 检测到token相关错误，增加max_tokens",
					"book_id", bookID,
					"old_tokens", maxTokens,
					"new_tokens", currentMaxTokens)
			}

			// 降低temperature提高稳定性
			currentTemperature = currentTemperature * 0.8
			if currentTemperature < 0.1 {
				currentTemperature = 0.1
			}

			delay := time.Duration(attempt-1) * baseDelay
			if delay > maxDelay {
				delay = maxDelay
			}

			log.C(ctx).Infow("⏳ 等待重试",
				"book_id", bookID,
				"delay", delay,
				"next_attempt", attempt+1,
				"adjusted_tokens", currentMaxTokens,
				"adjusted_temperature", currentTemperature)

			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
				// 继续重试
			}
		}
	}

	return "", fmt.Errorf("火山引擎API重试%d次后仍失败: %v", maxRetries, lastErr)
}

// callQianwenWithRetry 带重试的阿里千问API调用（支持动态参数调整）
func (p *AsyncBookProcessor) callQianwenWithRetry(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64, bookID uint) (string, error) {
	maxRetries := 3
	baseDelay := 2 * time.Second

	// 动态参数
	currentMaxTokens := maxTokens
	currentTemperature := temperature

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		log.C(ctx).Infow("🔄 尝试阿里千问API",
			"book_id", bookID,
			"attempt", attempt,
			"max_attempts", maxRetries,
			"max_tokens", currentMaxTokens,
			"temperature", currentTemperature)

		result, err := p.biz.Ali().QianwenTextStream(messages, currentMaxTokens, currentTemperature)
		if err == nil {
			log.C(ctx).Infow("✅ 阿里千问API成功", "book_id", bookID, "attempt", attempt)
			return result, nil
		}

		lastErr = err
		log.C(ctx).Warnw("⚠️ 阿里千问API失败", "book_id", bookID, "attempt", attempt, "error", err.Error())

		// 动态调整参数（在重试时）
		if attempt < maxRetries {
			// 增加max_tokens以应对长文本
			if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "length") || strings.Contains(err.Error(), "too long") {
				currentMaxTokens = int(float64(currentMaxTokens) * 1.5)
				log.C(ctx).Infow("🔧 检测到token相关错误，增加max_tokens",
					"book_id", bookID,
					"old_tokens", maxTokens,
					"new_tokens", currentMaxTokens)
			}

			// 降低temperature提高稳定性
			currentTemperature = currentTemperature * 0.8
			if currentTemperature < 0.1 {
				currentTemperature = 0.1
			}

			delay := time.Duration(attempt) * baseDelay

			log.C(ctx).Infow("⏳ 等待重试",
				"book_id", bookID,
				"delay", delay,
				"next_attempt", attempt+1,
				"adjusted_tokens", currentMaxTokens,
				"adjusted_temperature", currentTemperature)

			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
				// 继续重试
			}
		}
	}

	return "", fmt.Errorf("阿里千问API重试%d次后仍失败: %v", maxRetries, lastErr)
}

// extractTitleFromMarkdown 从markdown文本中提取标题
func (p *AsyncBookProcessor) extractTitleFromMarkdown(markdown string) string {
	lines := strings.Split(markdown, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			// 找到一级标题，返回标题内容（去掉# 前缀）
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// convertMarkdownToElements 将markdown文本转换为分页元素
func (p *AsyncBookProcessor) convertMarkdownToElements(markdown string) []pagination.Element {
	var elements []pagination.Element
	lines := strings.Split(markdown, "\n")

	var currentContent strings.Builder
	var currentType pagination.ElementType = pagination.ElementTypeBody

	for i, line := range lines {
		line = strings.TrimSpace(line)

		// 跳过空行
		if line == "" {
			// 如果当前有内容，先保存当前元素
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}
			continue
		}

		// 检查是否是标题
		if strings.HasPrefix(line, "# ") {
			// 保存之前的内容
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}

			// 跳过一级标题（已经在book title中处理）
			if !strings.HasPrefix(line, "# ") {
				// 二级标题
				title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
				if title != "" {
					elements = append(elements, pagination.Element{
						Type:    pagination.ElementTypeSubtitle,
						Content: title,
					})
				}
			}
			continue
		}

		// 检查是否是列表项
		if strings.HasPrefix(line, "- ") {
			// 保存之前的内容
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}

			// 收集列表项
			var listItems []string
			listItems = append(listItems, strings.TrimSpace(strings.TrimPrefix(line, "- ")))

			// 继续收集后续的列表项
			for j := i + 1; j < len(lines); j++ {
				nextLine := strings.TrimSpace(lines[j])
				if strings.HasPrefix(nextLine, "- ") {
					listItems = append(listItems, strings.TrimSpace(strings.TrimPrefix(nextLine, "- ")))
				} else if nextLine == "" {
					// 空行表示列表结束
					break
				} else {
					// 非列表项，停止收集
					break
				}
			}

			if len(listItems) > 0 {
				elements = append(elements, pagination.Element{
					Type:    pagination.ElementTypeList,
					Content: listItems,
				})
			}
			continue
		}

		// 检查是否是引用
		if strings.HasPrefix(line, "> ") {
			// 保存之前的内容
			if currentContent.Len() > 0 {
				content := strings.TrimSpace(currentContent.String())
				if content != "" {
					elements = append(elements, pagination.Element{
						Type:    currentType,
						Content: content,
					})
				}
				currentContent.Reset()
			}

			// 收集引用内容
			quote := strings.TrimSpace(strings.TrimPrefix(line, "> "))
			if quote != "" {
				elements = append(elements, pagination.Element{
					Type:    pagination.ElementTypeQuote,
					Content: quote,
				})
			}
			continue
		}

		// 普通段落内容
		if currentContent.Len() > 0 {
			currentContent.WriteString("\n")
		}
		currentContent.WriteString(line)
	}

	// 保存最后的内容
	if currentContent.Len() > 0 {
		content := strings.TrimSpace(currentContent.String())
		if content != "" {
			elements = append(elements, pagination.Element{
				Type:    currentType,
				Content: content,
			})
		}
	}

	return elements
}

// processBookWithMarkdownRenderer 使用markdown渲染器处理书籍
// 直接使用aiResponse.Text作为markdown内容，自动分页和渲染
func (p *AsyncBookProcessor) processBookWithMarkdownRenderer(
	ctx context.Context,
	book *model.BookM,
	userID uint,
	markdownText string,
	coverBackground string,
) error {
	log.C(ctx).Infow("开始使用markdown渲染器处理", "book_id", book.ID, "text_length", len(markdownText))

	// 1. 直接使用markdown文本创建单个卡片记录
	cardRecord := &model.CardM{
		UserID:        userID,
		BookID:        book.ID,
		ProcessedText: markdownText, // 直接存储markdown内容
		SortOrder:     1,            // 内容卡片排序为1
	}

	// 2. 保存卡片记录
	if err := p.biz.Cards().Create(ctx, cardRecord); err != nil {
		return fmt.Errorf("创建卡片记录失败: %v", err)
	}

	log.C(ctx).Infow("卡片记录创建成功", "book_id", book.ID, "card_id", cardRecord.ID, "content_length", len(markdownText))

	// 3. 使用现有的轻量级渲染器进行渲染
	// 这里我们使用现有的渲染流程，但传入markdown文本
	if card.IsLightweightRendererEnabled() {
		log.C(ctx).Infow("🚀 使用轻量级渲染器渲染markdown内容", "book_id", book.ID)

		// 创建轻量级渲染器集成器
		paginationBiz := pagination.NewPaginationBiz()
		lightweightIntegration, err := NewLightweightRendererIntegration(p.biz, paginationBiz.GetConfig())
		if err != nil {
			log.C(ctx).Errorw("轻量级渲染器创建失败", "book_id", book.ID, "error", err.Error())
			return fmt.Errorf("轻量级渲染器创建失败: %v", err)
		}
		defer lightweightIntegration.Cleanup()

		// 将markdown文本转换为分页元素
		elements := p.convertMarkdownToElements(markdownText)

		// 使用轻量级渲染器进行整体处理
		if err := lightweightIntegration.ProcessBookWithLightweightRendering(ctx, book, userID, elements, book.ImageUrl); err != nil {
			log.C(ctx).Errorw("轻量级渲染器处理失败", "book_id", book.ID, "error", err.Error())
			return fmt.Errorf("轻量级渲染器处理失败: %v", err)
		}

		// 创建封面卡片
		_, err = lightweightIntegration.CreateCoverCardWithLightweight(ctx, book, userID, coverBackground)
		if err != nil {
			log.C(ctx).Errorw("轻量级封面卡片创建失败", "book_id", book.ID, "error", err.Error())
		} else {
			log.C(ctx).Infow("📚 轻量级封面卡片创建成功", "book_id", book.ID)
		}

		log.C(ctx).Infow("✅ 轻量级渲染器处理完成", "book_id", book.ID)
	} else {
		// 如果没有启用轻量级渲染器，使用传统渲染流程
		log.C(ctx).Infow("使用传统渲染流程", "book_id", book.ID)

		// 将markdown文本转换为分页元素
		elements := p.convertMarkdownToElements(markdownText)

		// 使用传统分页和渲染流程
		paginationBiz := pagination.NewPaginationBiz()
		paginatedContent, err := paginationBiz.PaginateElements(elements)
		if err != nil {
			return fmt.Errorf("分页处理失败: %v", err)
		}

		// 为每个分页后的卡片创建数据库记录
		for i, cardContent := range paginatedContent.Cards {
			// 将卡片内容转换为JSON格式
			var cardElements []map[string]interface{}
			for _, element := range cardContent.Elements {
				cardElements = append(cardElements, map[string]interface{}{
					"type":    element.Type,
					"content": element.Content,
				})
			}

			// 将JSON数据转换为字符串
			cardJSONStr, err := json.Marshal(cardElements)
			if err != nil {
				log.C(ctx).Errorw("Failed to marshal card JSON", "book_id", book.ID, "card_index", i, "error", err.Error())
				continue
			}

			// 创建卡片记录
			contentCardRecord := &model.CardM{
				UserID:        userID,
				BookID:        book.ID,
				ProcessedText: string(cardJSONStr),
				SortOrder:     i + 1, // 从1开始计数，0是封面卡片
			}

			if err := p.biz.Cards().Create(ctx, contentCardRecord); err != nil {
				log.C(ctx).Errorw("Failed to create content card", "book_id", book.ID, "card_index", i, "error", err.Error())
				continue
			}

			log.C(ctx).Infow("内容卡片创建成功", "book_id", book.ID, "card_id", contentCardRecord.ID, "sort_order", contentCardRecord.SortOrder)
		}

		// 更新用户卡片统计
		for range paginatedContent.Cards {
			if err := p.biz.Users().IncrementUserCardNum(ctx, userID); err != nil {
				log.C(ctx).Errorw("更新用户卡片统计失败", "book_id", book.ID, "user_id", userID, "error", err.Error())
			}
		}
	}

	// 4. 更新用户卡片统计
	if err := p.biz.Users().IncrementUserCardNum(ctx, userID); err != nil {
		log.C(ctx).Errorw("更新用户卡片统计失败", "book_id", book.ID, "user_id", userID, "error", err.Error())
	}

	log.C(ctx).Infow("markdown渲染器处理完成", "book_id", book.ID)
	return nil
}
