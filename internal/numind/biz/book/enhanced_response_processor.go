package book

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
)

// EnhancedResponseProcessor 增强响应处理器
type EnhancedResponseProcessor struct {
	enableAdvancedRepair  bool
	enableSmartExtraction bool
}

// NewEnhancedResponseProcessor 创建增强响应处理器
func NewEnhancedResponseProcessor() *EnhancedResponseProcessor {
	return &EnhancedResponseProcessor{
		enableAdvancedRepair:  true,
		enableSmartExtraction: true,
	}
}

// ProcessAPIResponse 处理API响应
func (erp *EnhancedResponseProcessor) ProcessAPIResponse(
	ctx context.Context,
	rawResponse string,
	apiType string,
	bookID uint,
	chunkIndex int,
) (string, error) {
	log.C(ctx).Debugw("🔄 开始处理API响应",
		"book_id", bookID,
		"chunk_index", chunkIndex,
		"api_type", apiType,
		"raw_length", len(rawResponse))

	// 第一步：空响应快速检测
	if len(rawResponse) == 0 {
		return "", fmt.Errorf("API返回空响应")
	}

	// 第二步：基础清理
	cleaned := erp.performBasicCleaning(rawResponse)

	log.C(ctx).Debugw("🧹 基础清理完成",
		"book_id", bookID,
		"original_length", len(rawResponse),
		"cleaned_length", len(cleaned))

	// 第三步：尝试直接JSON解析
	if erp.isValidJSON(cleaned) {
		log.C(ctx).Debugw("✅ 直接JSON解析成功", "book_id", bookID)
		return cleaned, nil
	}

	// 第四步：智能JSON提取
	if erp.enableSmartExtraction {
		extracted := erp.performSmartJSONExtraction(ctx, cleaned, bookID)
		if extracted != "" && erp.isValidJSON(extracted) {
			log.C(ctx).Debugw("✅ 智能提取成功", "book_id", bookID)
			return extracted, nil
		}
	}

	// 第五步：高级修复引擎
	if erp.enableAdvancedRepair {
		repaired, err := erp.performAdvancedRepair(ctx, cleaned, bookID)
		if err == nil && repaired != "" && erp.isValidJSON(repaired) {
			log.C(ctx).Debugw("✅ 高级修复成功", "book_id", bookID)
			return repaired, nil
		}

		if err != nil {
			log.C(ctx).Debugw("⚠️ 高级修复失败", "book_id", bookID, "error", err.Error())
		}
	}

	// 第六步：手动结构修复
	manually := erp.performManualRepair(cleaned)
	if manually != "" && erp.isValidJSON(manually) {
		log.C(ctx).Debugw("✅ 手动修复成功", "book_id", bookID)
		return manually, nil
	}

	// 所有方法都失败
	log.C(ctx).Warnw("❌ 所有JSON处理方法都失败",
		"book_id", bookID,
		"chunk_index", chunkIndex,
		"api_type", apiType,
		"raw_preview", erp.truncateString(rawResponse, 200),
		"cleaned_preview", erp.truncateString(cleaned, 200))

	return "", fmt.Errorf("所有JSON处理方法都失败")
}

// performBasicCleaning 执行基础清理
func (erp *EnhancedResponseProcessor) performBasicCleaning(text string) string {
	// 移除BOM标记
	text = strings.TrimPrefix(text, "\uFEFF")

	// 移除前后空白
	text = strings.TrimSpace(text)

	// 修复常见的Unicode问题
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}

	// 移除markdown代码块包装
	codeBlockRegex := regexp.MustCompile("```(?:json)?\\s*([\\s\\S]*?)\\s*```")
	if matches := codeBlockRegex.FindStringSubmatch(text); len(matches) > 1 {
		text = strings.TrimSpace(matches[1])
	}

	// 移除可能的前缀说明文字
	prefixes := []string{
		"好的，这是整理后的内容：",
		"以下是处理结果：",
		"根据您的要求，整理如下：",
		"处理后的内容如下：",
		"整理完成，结果如下：",
		"Here is the result:",
		"The processed content is:",
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			text = strings.TrimSpace(text[len(prefix):])
			break
		}
	}

	// 移除可能的后缀说明文字
	suffixes := []string{
		"以上就是整理结果。",
		"希望对您有帮助。",
		"整理完成。",
		"That's the processed result.",
	}

	for _, suffix := range suffixes {
		if strings.HasSuffix(text, suffix) {
			text = strings.TrimSpace(text[:len(text)-len(suffix)])
			break
		}
	}

	return text
}

// performSmartJSONExtraction 执行智能JSON提取
func (erp *EnhancedResponseProcessor) performSmartJSONExtraction(
	ctx context.Context,
	text string,
	bookID uint,
) string {
	// 尝试提取第一个完整的JSON对象
	jsonObjectRegex := regexp.MustCompile(`\{[\s\S]*?\}`)
	matches := jsonObjectRegex.FindAllString(text, -1)

	for _, match := range matches {
		// 尝试平衡大括号
		balanced := erp.balanceBraces(match)
		if balanced != "" && erp.isValidJSON(balanced) {
			log.C(ctx).Debugw("📦 找到有效JSON对象", "book_id", bookID, "length", len(balanced))
			return balanced
		}
	}

	// 尝试提取JSON数组
	jsonArrayRegex := regexp.MustCompile(`\[[\s\S]*?\]`)
	arrayMatches := jsonArrayRegex.FindAllString(text, -1)

	for _, match := range arrayMatches {
		balanced := erp.balanceBrackets(match)
		if balanced != "" && erp.isValidJSON(balanced) {
			log.C(ctx).Debugw("📋 找到有效JSON数组", "book_id", bookID, "length", len(balanced))
			return balanced
		}
	}

	// 如果都没找到，尝试查找包含structured_text_array的部分
	structuredRegex := regexp.MustCompile(`"structured_text_array"\s*:\s*\[[\s\S]*?\]`)
	if structuredMatch := structuredRegex.FindString(text); structuredMatch != "" {
		// 构造完整的JSON对象
		constructed := fmt.Sprintf("{%s}", structuredMatch)
		if erp.isValidJSON(constructed) {
			log.C(ctx).Debugw("🎯 构造structured_text_array对象成功", "book_id", bookID)
			return constructed
		}
	}

	return ""
}

// performAdvancedRepair 执行高级修复
func (erp *EnhancedResponseProcessor) performAdvancedRepair(
	ctx context.Context,
	text string,
	bookID uint,
) (string, error) {
	log.C(ctx).Debugw("🔧 开始高级JSON修复", "book_id", bookID, "text_length", len(text))

	// 使用现有的高级JSON修复引擎
	extractor := httpclient.NewAdvancedJSONExtractor()
	repairedBytes, err := extractor.ExtractValidJSON([]byte(text))

	if err != nil {
		return "", fmt.Errorf("高级修复引擎失败: %w", err)
	}

	repaired := string(repairedBytes)
	log.C(ctx).Debugw("🔧 高级修复完成",
		"book_id", bookID,
		"original_length", len(text),
		"repaired_length", len(repaired))

	return repaired, nil
}

// performManualRepair 执行手动修复
func (erp *EnhancedResponseProcessor) performManualRepair(text string) string {
	// 查找JSON的开始
	startIdx := strings.Index(text, "{")
	if startIdx == -1 {
		// 尝试查找数组开始
		startIdx = strings.Index(text, "[")
		if startIdx == -1 {
			return ""
		}
	}

	text = text[startIdx:]

	// 平衡大括号或方括号
	if text[0] == '{' {
		return erp.balanceBraces(text)
	} else if text[0] == '[' {
		return erp.balanceBrackets(text)
	}

	return ""
}

// balanceBraces 平衡大括号
func (erp *EnhancedResponseProcessor) balanceBraces(text string) string {
	braceCount := 0
	var result strings.Builder

	for i, char := range text {
		result.WriteRune(char)

		switch char {
		case '{':
			braceCount++
		case '}':
			braceCount--
			if braceCount == 0 {
				// 找到完整的JSON对象
				candidate := result.String()
				if erp.isValidJSON(candidate) {
					return candidate
				}
			}
		}

		// 防止处理过长的文本
		if i > 100000 {
			break
		}
	}

	// 如果还有未闭合的大括号，尝试添加
	for braceCount > 0 {
		result.WriteRune('}')
		braceCount--
	}

	candidate := result.String()
	if erp.isValidJSON(candidate) {
		return candidate
	}

	return ""
}

// balanceBrackets 平衡方括号
func (erp *EnhancedResponseProcessor) balanceBrackets(text string) string {
	bracketCount := 0
	var result strings.Builder

	for i, char := range text {
		result.WriteRune(char)

		switch char {
		case '[':
			bracketCount++
		case ']':
			bracketCount--
			if bracketCount == 0 {
				// 找到完整的JSON数组
				candidate := result.String()
				if erp.isValidJSON(candidate) {
					return candidate
				}
			}
		}

		// 防止处理过长的文本
		if i > 100000 {
			break
		}
	}

	// 如果还有未闭合的方括号，尝试添加
	for bracketCount > 0 {
		result.WriteRune(']')
		bracketCount--
	}

	candidate := result.String()
	if erp.isValidJSON(candidate) {
		return candidate
	}

	return ""
}

// isValidJSON 检查是否为有效JSON
func (erp *EnhancedResponseProcessor) isValidJSON(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}

	var obj interface{}
	return json.Unmarshal([]byte(text), &obj) == nil
}

// truncateString 截断字符串
func (erp *EnhancedResponseProcessor) truncateString(text string, maxLength int) string {
	if len(text) <= maxLength {
		return text
	}
	return text[:maxLength] + "..."
}
