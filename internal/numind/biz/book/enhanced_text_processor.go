package book

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"numind-server/internal/pkg/log"
)

// EnhancedTextProcessor 增强文本处理器 - 专门处理长文本和JSON完整性问题
type EnhancedTextProcessor struct {
	maxChunkLength int     // 单次处理的最大文本长度
	maxTokens      int     // API调用的最大token数
	temperature    float64 // API调用温度
	maxRetries     int     // 最大重试次数
	enableChunking bool    // 是否启用分块处理
	enhancedPrompt bool    // 是否使用增强提示词
}

// TextChunk 文本块
type TextChunk struct {
	Content   string `json:"content"`
	Index     int    `json:"index"`
	IsPartial bool   `json:"is_partial"`
}

// ChunkProcessResult 块处理结果
type ChunkProcessResult struct {
	ChunkIndex    int           `json:"chunk_index"`
	Success       bool          `json:"success"`
	JSONContent   string        `json:"json_content"`
	Error         string        `json:"error,omitempty"`
	ProcessTime   time.Duration `json:"process_time"`
	RetryCount    int           `json:"retry_count"`
	ResponseStats ResponseStats `json:"response_stats"`
}

// ResponseStats 响应统计
type ResponseStats struct {
	RawLength       int  `json:"raw_length"`
	ExtractedLength int  `json:"extracted_length"`
	IsValidJSON     bool `json:"is_valid_json"`
	RequiredRepair  bool `json:"required_repair"`
}

// MergedResult 合并结果
type MergedResult struct {
	FinalJSON    string               `json:"final_json"`
	ChunkResults []ChunkProcessResult `json:"chunk_results"`
	MergeStats   MergeStats           `json:"merge_stats"`
	TotalTime    time.Duration        `json:"total_time"`
}

// MergeStats 合并统计
type MergeStats struct {
	TotalChunks   int  `json:"total_chunks"`
	SuccessChunks int  `json:"success_chunks"`
	FailedChunks  int  `json:"failed_chunks"`
	TotalRetries  int  `json:"total_retries"`
	RequiredMerge bool `json:"required_merge"`
}

// NewEnhancedTextProcessor 创建增强文本处理器
func NewEnhancedTextProcessor() *EnhancedTextProcessor {
	return &EnhancedTextProcessor{
		maxChunkLength: 3000, // 每个块最大3000字符
		maxTokens:      8192, // 增加token限制
		temperature:    0.3,  // 降低温度提高稳定性
		maxRetries:     5,    // 最大重试5次
		enableChunking: true, // 启用分块处理
		enhancedPrompt: true, // 启用增强提示词
	}
}

// ProcessLongText 处理长文本，确保JSON完整性
func (etp *EnhancedTextProcessor) ProcessLongText(
	ctx context.Context,
	text string,
	apiCaller func(context.Context, []map[string]string, int, float64, uint) (string, error),
	bookID uint,
) (*MergedResult, error) {
	startTime := time.Now()

	log.C(ctx).Infow("🚀 开始增强文本处理",
		"book_id", bookID,
		"text_length", len(text),
		"max_chunk_length", etp.maxChunkLength,
		"enable_chunking", etp.enableChunking)

	// 检查是否需要分块处理
	if !etp.enableChunking || len(text) <= etp.maxChunkLength {
		log.C(ctx).Infow("📄 文本较短，使用单次处理", "book_id", bookID)
		return etp.processSingleText(ctx, text, apiCaller, bookID, startTime)
	}

	// 分块处理长文本
	log.C(ctx).Infow("✂️ 文本较长，启用分块处理", "book_id", bookID)
	return etp.processChunkedText(ctx, text, apiCaller, bookID, startTime)
}

// processSingleText 单次处理文本
func (etp *EnhancedTextProcessor) processSingleText(
	ctx context.Context,
	text string,
	apiCaller func(context.Context, []map[string]string, int, float64, uint) (string, error),
	bookID uint,
	startTime time.Time,
) (*MergedResult, error) {
	// 构建增强提示词
	enhancedPrompt := etp.buildEnhancedPrompt(text, false, 1, 1)
	messages := []map[string]string{
		{"role": "user", "content": enhancedPrompt},
	}

	// 使用重试机制处理
	result, err := etp.callAPIWithEnhancedRetry(ctx, messages, apiCaller, bookID, 0)
	if err != nil {
		return nil, fmt.Errorf("单次处理失败: %w", err)
	}

	return &MergedResult{
		FinalJSON:    result.JSONContent,
		ChunkResults: []ChunkProcessResult{*result},
		MergeStats: MergeStats{
			TotalChunks:   1,
			SuccessChunks: 1,
			FailedChunks:  0,
			TotalRetries:  result.RetryCount,
			RequiredMerge: false,
		},
		TotalTime: time.Since(startTime),
	}, nil
}

// processChunkedText 分块处理文本
func (etp *EnhancedTextProcessor) processChunkedText(
	ctx context.Context,
	text string,
	apiCaller func(context.Context, []map[string]string, int, float64, uint) (string, error),
	bookID uint,
	startTime time.Time,
) (*MergedResult, error) {
	// 智能分块
	chunks := etp.smartSplitText(text)
	log.C(ctx).Infow("📦 文本分块完成",
		"book_id", bookID,
		"total_chunks", len(chunks),
		"chunk_lengths", etp.getChunkLengths(chunks))

	// 并发处理所有块
	chunkResults := make([]ChunkProcessResult, len(chunks))
	totalRetries := 0

	for i, chunk := range chunks {
		log.C(ctx).Infow("🔄 处理文本块",
			"book_id", bookID,
			"chunk_index", i+1,
			"total_chunks", len(chunks),
			"chunk_length", len(chunk.Content))

		// 构建针对当前块的增强提示词
		enhancedPrompt := etp.buildEnhancedPrompt(chunk.Content, chunk.IsPartial, i+1, len(chunks))
		messages := []map[string]string{
			{"role": "user", "content": enhancedPrompt},
		}

		// 处理当前块
		result, err := etp.callAPIWithEnhancedRetry(ctx, messages, apiCaller, bookID, i)
		if err != nil {
			log.C(ctx).Errorw("❌ 文本块处理失败",
				"book_id", bookID,
				"chunk_index", i+1,
				"error", err.Error())

			chunkResults[i] = ChunkProcessResult{
				ChunkIndex:  i,
				Success:     false,
				Error:       err.Error(),
				ProcessTime: 0,
			}
		} else {
			log.C(ctx).Infow("✅ 文本块处理成功",
				"book_id", bookID,
				"chunk_index", i+1,
				"json_length", len(result.JSONContent),
				"retry_count", result.RetryCount)

			chunkResults[i] = *result
			totalRetries += result.RetryCount
		}
	}

	// 合并结果
	mergedJSON, mergeStats := etp.mergeChunkResults(ctx, chunkResults, bookID)
	mergeStats.TotalRetries = totalRetries

	return &MergedResult{
		FinalJSON:    mergedJSON,
		ChunkResults: chunkResults,
		MergeStats:   mergeStats,
		TotalTime:    time.Since(startTime),
	}, nil
}

// smartSplitText 智能分割文本
func (etp *EnhancedTextProcessor) smartSplitText(text string) []TextChunk {
	var chunks []TextChunk

	// 按段落分割
	paragraphs := strings.Split(text, "\n\n")

	var currentChunk strings.Builder
	chunkIndex := 0

	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}

		// 检查添加当前段落是否会超过限制
		if currentChunk.Len() > 0 && currentChunk.Len()+len(paragraph)+2 > etp.maxChunkLength {
			// 保存当前块
			chunks = append(chunks, TextChunk{
				Content:   currentChunk.String(),
				Index:     chunkIndex,
				IsPartial: true,
			})

			// 开始新块
			currentChunk.Reset()
			chunkIndex++
		}

		// 添加段落到当前块
		if currentChunk.Len() > 0 {
			currentChunk.WriteString("\n\n")
		}
		currentChunk.WriteString(paragraph)
	}

	// 添加最后一个块
	if currentChunk.Len() > 0 {
		chunks = append(chunks, TextChunk{
			Content:   currentChunk.String(),
			Index:     chunkIndex,
			IsPartial: chunkIndex > 0, // 如果不是第一个块，则标记为部分
		})
	}

	// 标记最后一个块为完整
	if len(chunks) > 0 {
		chunks[len(chunks)-1].IsPartial = false
	}

	return chunks
}

// buildEnhancedPrompt 构建增强提示词
func (etp *EnhancedTextProcessor) buildEnhancedPrompt(text string, isPartial bool, chunkIndex, totalChunks int) string {
	if !etp.enhancedPrompt {
		return text // 如果未启用增强提示词，直接返回原文本
	}

	var promptBuilder strings.Builder

	// 核心要求
	promptBuilder.WriteString(`# 🚨 关键输出要求 - 必须严格遵守

## JSON完整性要求（最高优先级）
1. **你的输出必须是完整、可直接解析的JSON格式**
2. **严禁任何形式的截断、省略或不完整输出**
3. **所有JSON结构必须正确闭合（{}、[]、""）**
4. **即使内容极长也必须完整输出所有内容**
5. **最终输出必须能通过严格的JSON校验**

## 输出格式规范
必须输出标准的结构化JSON，格式如下：
`)

	// 添加期望的JSON结构示例
	promptBuilder.WriteString(`
{
  "structured_text_array": [
    {
      "type": "title",
      "content": "主标题内容"
    },
    {
      "type": "subtitle", 
      "content": "子标题内容"
    },
    {
      "type": "body",
      "content": "正文内容"
    },
    {
      "type": "list",
      "content": ["列表项1", "列表项2", "列表项3"]
    },
    {
      "type": "quote",
      "content": "引用内容"
    }
  ],
  "image_prompt": "根据内容生成的图片描述提示词"
}`)

	// 分块处理的特殊说明
	if totalChunks > 1 {
		promptBuilder.WriteString(fmt.Sprintf(`

## 分块处理说明
- 这是第 %d/%d 个文本块
- 请处理当前块的内容，保持结构完整
- 确保JSON格式完整，不要省略任何结构元素
`, chunkIndex, totalChunks))

		if isPartial {
			promptBuilder.WriteString(`- 这是部分内容，请处理当前块并保持逻辑连贯`)
		} else {
			promptBuilder.WriteString(`- 这是最后一个块，请确保内容完整`)
		}
	}

	// 质量保证要求
	promptBuilder.WriteString(`

## 质量保证
1. **语法检查**: 输出前请自查JSON语法正确性
2. **结构验证**: 确保所有开始的标记都有对应的结束标记
3. **内容完整**: 不遗漏任何重要信息
4. **格式统一**: 严格按照示例格式输出

## 禁止事项
❌ 不要输出任何解释性文字
❌ 不要使用markdown代码块包装
❌ 不要截断或省略内容
❌ 不要输出不完整的JSON结构

---

# 需要处理的文本内容：

`)

	// 添加实际文本内容
	promptBuilder.WriteString(text)

	return promptBuilder.String()
}

// callAPIWithEnhancedRetry 使用增强重试机制调用API
func (etp *EnhancedTextProcessor) callAPIWithEnhancedRetry(
	ctx context.Context,
	messages []map[string]string,
	apiCaller func(context.Context, []map[string]string, int, float64, uint) (string, error),
	bookID uint,
	chunkIndex int,
) (*ChunkProcessResult, error) {
	startTime := time.Now()
	var lastErr error

	// 创建API参数优化器和响应处理器
	paramOptimizer := NewAPIParametersOptimizer()
	responseProcessor := NewEnhancedResponseProcessor()

	// 估算输入文本长度
	inputLength := 0
	for _, msg := range messages {
		if content, exists := msg["content"]; exists {
			inputLength += len(content)
		}
	}

	for attempt := 1; attempt <= etp.maxRetries; attempt++ {
		// 🔧 优化API参数（根据重试次数动态调整）
		optimizedMaxTokens, optimizedTemperature, paramErr := paramOptimizer.OptimizeParametersForAPI(
			ctx, "ali", inputLength, attempt, bookID) // 假设优先使用阿里API

		if paramErr != nil {
			log.C(ctx).Warnw("⚠️ 参数优化失败，使用默认值", "error", paramErr.Error())
			optimizedMaxTokens = 4096
			optimizedTemperature = 0.5
		}

		log.C(ctx).Infow("🔄 API调用尝试",
			"book_id", bookID,
			"chunk_index", chunkIndex,
			"attempt", attempt,
			"max_attempts", etp.maxRetries,
			"optimized_max_tokens", optimizedMaxTokens,
			"optimized_temperature", optimizedTemperature,
			"input_length", inputLength)

		// 调用API（使用优化后的参数）
		response, err := apiCaller(ctx, messages, optimizedMaxTokens, optimizedTemperature, bookID)
		if err != nil {
			lastErr = err
			log.C(ctx).Warnw("⚠️ API调用失败",
				"book_id", bookID,
				"chunk_index", chunkIndex,
				"attempt", attempt,
				"error", err.Error())

			// 如果不是最后一次尝试，等待后重试
			if attempt < etp.maxRetries {
				waitTime := time.Duration(paramOptimizer.GetRecommendedRetryDelay(attempt, "ali")) * time.Second
				log.C(ctx).Infow("⏳ 等待重试", "wait_time", waitTime)
				time.Sleep(waitTime)
			}
			continue
		}

		// 📊 记录响应统计
		stats := ResponseStats{
			RawLength: len(response),
		}

		// 🚨 快速检测空响应
		if stats.RawLength == 0 {
			lastErr = fmt.Errorf("API返回空响应")
			log.C(ctx).Warnw("🚨 检测到空响应，立即重试",
				"book_id", bookID,
				"chunk_index", chunkIndex,
				"attempt", attempt)

			// 空响应直接重试，不进入JSON修复流程
			if attempt < etp.maxRetries {
				waitTime := time.Duration(2*attempt) * time.Second
				log.C(ctx).Infow("⏳ 空响应快速重试", "wait_time", waitTime)
				time.Sleep(waitTime)
			}
			continue
		}

		// ✅ 响应有效性验证
		isValid, reason := paramOptimizer.ValidateAPIResponse(ctx, response, "ali", bookID)
		if !isValid {
			lastErr = fmt.Errorf("API响应无效: %s", reason)
			log.C(ctx).Warnw("⚠️ API响应验证失败",
				"book_id", bookID,
				"chunk_index", chunkIndex,
				"attempt", attempt,
				"reason", reason,
				"response_preview", etp.truncateString(response, 200))

			// 无效响应也直接重试
			if attempt < etp.maxRetries {
				waitTime := time.Duration(2*attempt) * time.Second
				time.Sleep(waitTime)
			}
			continue
		}

		// 🔄 使用增强响应处理器处理JSON
		jsonContent, jsonErr := responseProcessor.ProcessAPIResponse(ctx, response, "ali", bookID, chunkIndex)

		stats.ExtractedLength = len(jsonContent)
		stats.IsValidJSON = (jsonErr == nil && jsonContent != "")
		stats.RequiredRepair = (jsonContent != response) // 是否需要修复

		if jsonErr == nil && jsonContent != "" && stats.IsValidJSON {
			log.C(ctx).Infow("✅ API调用和JSON处理成功",
				"book_id", bookID,
				"chunk_index", chunkIndex,
				"attempt", attempt,
				"response_length", stats.RawLength,
				"json_length", stats.ExtractedLength,
				"required_repair", stats.RequiredRepair)

			return &ChunkProcessResult{
				ChunkIndex:    chunkIndex,
				Success:       true,
				JSONContent:   jsonContent,
				ProcessTime:   time.Since(startTime),
				RetryCount:    attempt - 1,
				ResponseStats: stats,
			}, nil
		}

		// JSON处理失败，记录详细错误信息
		lastErr = fmt.Errorf("JSON处理失败: %v", jsonErr)
		log.C(ctx).Warnw("⚠️ JSON处理失败",
			"book_id", bookID,
			"chunk_index", chunkIndex,
			"attempt", attempt,
			"json_error", jsonErr,
			"response_length", stats.RawLength,
			"response_preview", etp.truncateString(response, 300))

		// 如果不是最后一次尝试，等待后重试
		if attempt < etp.maxRetries {
			waitTime := time.Duration(paramOptimizer.GetRecommendedRetryDelay(attempt, "ali")) * time.Second
			log.C(ctx).Infow("⏳ 等待JSON重试", "wait_time", waitTime)
			time.Sleep(waitTime)
		}
	}

	return nil, fmt.Errorf("所有重试都失败: %w", lastErr)
}

// extractAndRepairJSON 提取和修复JSON（保留向后兼容性）
func (etp *EnhancedTextProcessor) extractAndRepairJSON(ctx context.Context, response string, bookID uint, chunkIndex int) string {
	log.C(ctx).Debugw("🔍 开始JSON提取和修复（兼容模式）",
		"book_id", bookID,
		"chunk_index", chunkIndex,
		"response_length", len(response))

	// 使用新的增强响应处理器
	processor := NewEnhancedResponseProcessor()
	result, err := processor.ProcessAPIResponse(ctx, response, "unknown", bookID, chunkIndex)

	if err != nil {
		log.C(ctx).Warnw("❌ 增强响应处理失败", "book_id", bookID, "error", err.Error())
		return ""
	}

	return result
}

// basicJSONClean 基础JSON清理
func (etp *EnhancedTextProcessor) basicJSONClean(text string) string {
	// 移除BOM
	text = strings.TrimPrefix(text, "\uFEFF")

	// 移除前后空白
	text = strings.TrimSpace(text)

	// 移除markdown代码块标记
	text = regexp.MustCompile("```json\\s*").ReplaceAllString(text, "")
	text = regexp.MustCompile("```\\s*$").ReplaceAllString(text, "")

	// 移除无效的Unicode字符
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}

	return text
}

// manualJSONRepair 手动JSON修复
func (etp *EnhancedTextProcessor) manualJSONRepair(text string) string {
	// 查找JSON的开始
	startIdx := strings.Index(text, "{")
	if startIdx == -1 {
		return ""
	}

	text = text[startIdx:]

	// 尝试修复不完整的JSON结构
	braceCount := 0
	inString := false
	escaped := false
	var result strings.Builder

	for i, char := range text {
		if escaped {
			escaped = false
			result.WriteRune(char)
			continue
		}

		if char == '\\' {
			escaped = true
			result.WriteRune(char)
			continue
		}

		if char == '"' {
			inString = !inString
			result.WriteRune(char)
			continue
		}

		if !inString {
			if char == '{' {
				braceCount++
			} else if char == '}' {
				braceCount--
			}
		}

		result.WriteRune(char)

		// 如果找到了完整的JSON对象，尝试验证
		if !inString && braceCount == 0 && i > 0 {
			candidate := result.String()
			if etp.isValidJSON(candidate) {
				return candidate
			}
		}
	}

	// 如果还没有完整的JSON，尝试补全
	jsonStr := result.String()
	for braceCount > 0 {
		jsonStr += "}"
		braceCount--
	}

	// 修复常见的结尾问题
	if !strings.HasSuffix(jsonStr, "}") && !strings.HasSuffix(jsonStr, "]") {
		// 如果看起来像在字符串中截断
		if strings.Count(jsonStr, "\"")%2 == 1 {
			jsonStr += "\""
		}
		jsonStr += "}"
	}

	return jsonStr
}

// mergeChunkResults 合并块处理结果
func (etp *EnhancedTextProcessor) mergeChunkResults(ctx context.Context, results []ChunkProcessResult, bookID uint) (string, MergeStats) {
	stats := MergeStats{
		TotalChunks: len(results),
	}

	var successResults []ChunkProcessResult
	for _, result := range results {
		if result.Success {
			successResults = append(successResults, result)
			stats.SuccessChunks++
		} else {
			stats.FailedChunks++
		}
	}

	log.C(ctx).Infow("📊 合并统计",
		"book_id", bookID,
		"total_chunks", stats.TotalChunks,
		"success_chunks", stats.SuccessChunks,
		"failed_chunks", stats.FailedChunks)

	if len(successResults) == 0 {
		log.C(ctx).Errorw("❌ 没有成功的块可以合并", "book_id", bookID)
		return "", stats
	}

	if len(successResults) == 1 {
		// 只有一个成功的结果，直接返回
		stats.RequiredMerge = false
		return successResults[0].JSONContent, stats
	}

	// 需要合并多个结果
	stats.RequiredMerge = true
	return etp.mergeJSONContents(ctx, successResults, bookID), stats
}

// mergeJSONContents 合并JSON内容
func (etp *EnhancedTextProcessor) mergeJSONContents(ctx context.Context, results []ChunkProcessResult, bookID uint) string {
	var allElements []interface{}
	var imagePrompts []string

	for _, result := range results {
		var chunkData map[string]interface{}
		if err := json.Unmarshal([]byte(result.JSONContent), &chunkData); err != nil {
			log.C(ctx).Warnw("⚠️ 跳过无效的JSON块",
				"book_id", bookID,
				"chunk_index", result.ChunkIndex,
				"error", err.Error())
			continue
		}

		// 合并structured_text_array
		if elements, ok := chunkData["structured_text_array"].([]interface{}); ok {
			allElements = append(allElements, elements...)
		}

		// 收集image_prompt
		if prompt, ok := chunkData["image_prompt"].(string); ok && prompt != "" {
			imagePrompts = append(imagePrompts, prompt)
		}
	}

	// 构建合并后的JSON
	mergedData := map[string]interface{}{
		"structured_text_array": allElements,
	}

	// 合并image_prompt
	if len(imagePrompts) > 0 {
		mergedData["image_prompt"] = strings.Join(imagePrompts, "; ")
	}

	// 转换为JSON字符串
	mergedJSON, err := json.Marshal(mergedData)
	if err != nil {
		log.C(ctx).Errorw("❌ 合并JSON失败", "book_id", bookID, "error", err.Error())
		// 如果合并失败，返回第一个有效的结果
		return results[0].JSONContent
	}

	log.C(ctx).Infow("✅ JSON合并成功",
		"book_id", bookID,
		"total_elements", len(allElements),
		"image_prompts", len(imagePrompts),
		"merged_length", len(mergedJSON))

	return string(mergedJSON)
}

// 辅助方法

func (etp *EnhancedTextProcessor) isValidJSON(s string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}

func (etp *EnhancedTextProcessor) getChunkLengths(chunks []TextChunk) []int {
	lengths := make([]int, len(chunks))
	for i, chunk := range chunks {
		lengths[i] = len(chunk.Content)
	}
	return lengths
}

func (etp *EnhancedTextProcessor) truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// GetProcessorStats 获取处理器统计信息
func (etp *EnhancedTextProcessor) GetProcessorStats() map[string]interface{} {
	return map[string]interface{}{
		"max_chunk_length": etp.maxChunkLength,
		"max_tokens":       etp.maxTokens,
		"temperature":      etp.temperature,
		"max_retries":      etp.maxRetries,
		"enable_chunking":  etp.enableChunking,
		"enhanced_prompt":  etp.enhancedPrompt,
	}
}

// UpdateSettings 更新处理器设置
func (etp *EnhancedTextProcessor) UpdateSettings(settings map[string]interface{}) {
	if maxChunkLength, ok := settings["max_chunk_length"].(int); ok {
		etp.maxChunkLength = maxChunkLength
	}
	if maxTokens, ok := settings["max_tokens"].(int); ok {
		etp.maxTokens = maxTokens
	}
	if temperature, ok := settings["temperature"].(float64); ok {
		etp.temperature = temperature
	}
	if maxRetries, ok := settings["max_retries"].(int); ok {
		etp.maxRetries = maxRetries
	}
	if enableChunking, ok := settings["enable_chunking"].(bool); ok {
		etp.enableChunking = enableChunking
	}
	if enhancedPrompt, ok := settings["enhanced_prompt"].(bool); ok {
		etp.enhancedPrompt = enhancedPrompt
	}
}
