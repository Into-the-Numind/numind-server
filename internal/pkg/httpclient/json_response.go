package httpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// JSONResponseProcessor JSON响应处理器
type JSONResponseProcessor struct {
	EnableLogging bool
	LogPrefix     string
	Config        *JSONProcessingConfig
}

// NewJSONResponseProcessor 创建新的JSON响应处理器
func NewJSONResponseProcessor() *JSONResponseProcessor {
	config := LoadJSONProcessingConfig()
	return &JSONResponseProcessor{
		EnableLogging: config.JSONRepair.EnableLogging,
		LogPrefix:     "[JSONProcessor]",
		Config:        config,
	}
}

// ProcessResponse 处理HTTP响应，提取有效的JSON
func (p *JSONResponseProcessor) ProcessResponse(resp *http.Response) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("response is nil")
	}

	// 检查Content-Length（如果配置启用）
	if p.Config.ResponseProcessing.CheckContentLength {
		contentLength := resp.ContentLength
		if contentLength > 0 && contentLength > p.Config.ResponseProcessing.MaxResponseSize {
			return nil, fmt.Errorf("response too large: %d bytes (max: %d)", contentLength, p.Config.ResponseProcessing.MaxResponseSize)
		}
	}

	// 读取响应体
	body, err := p.readResponseBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// 检查响应是否完整
	if p.Config.ResponseProcessing.EnableResponseRecovery && !p.isResponseComplete(resp, body) {
		if p.EnableLogging {
			fmt.Printf("%s Response appears incomplete, attempting recovery\n", p.LogPrefix)
		}
		body = p.recoverIncompleteResponse(resp, body)
	}

	// 尝试直接解析JSON
	if p.isValidJSON(body) {
		if p.EnableLogging {
			fmt.Printf("%s Response is valid JSON, no processing needed\n", p.LogPrefix)
		}
		return body, nil
	}

	// 尝试修复和提取JSON
	if p.Config.JSONRepair.EnableDeepRepair {
		if p.EnableLogging {
			fmt.Printf("%s Attempting deep JSON repair\n", p.LogPrefix)
		}
		repaired := p.cleanAndFixJSON(string(body))
		if p.isValidJSON([]byte(repaired)) {
			if p.EnableLogging {
				fmt.Printf("%s Deep repair successful\n", p.LogPrefix)
			}
			return []byte(repaired), nil
		}
	}

	// 尝试智能提取
	if p.EnableLogging {
		fmt.Printf("%s Attempting intelligent JSON extraction\n", p.LogPrefix)
	}
	extracted := p.extractValidJSON(body)
	if extracted != nil {
		if p.EnableLogging {
			fmt.Printf("%s JSON extraction successful\n", p.LogPrefix)
		}
		return extracted, nil
	}

	return nil, fmt.Errorf("failed to extract valid JSON from response")
}

// readResponseBody 读取响应体，确保完整性
func (p *JSONResponseProcessor) readResponseBody(body io.ReadCloser) ([]byte, error) {
	defer body.Close()

	// 使用bytes.Buffer来构建响应
	var buffer bytes.Buffer

	// 分块读取，避免内存问题
	chunkSize := 32 * 1024 // 32KB chunks
	chunk := make([]byte, chunkSize)

	totalRead := int64(0)

	for {
		n, err := body.Read(chunk)
		if n > 0 {
			buffer.Write(chunk[:n])
			totalRead += int64(n)

			// 检查是否超过最大响应大小
			if totalRead > p.Config.ResponseProcessing.MaxResponseSize {
				return nil, fmt.Errorf("response too large: %d bytes (max: %d)", totalRead, p.Config.ResponseProcessing.MaxResponseSize)
			}
		}

		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("error reading response body: %w", err)
		}
	}

	return buffer.Bytes(), nil
}

// isResponseComplete 检查响应是否完整
func (p *JSONResponseProcessor) isResponseComplete(resp *http.Response, body []byte) bool {
	// 如果不知道预期长度，尝试通过JSON结构判断
	if resp.ContentLength <= 0 {
		return p.isJSONStructurallyComplete(body)
	}

	// 检查实际长度是否匹配预期长度
	actualLength := int64(len(body))
	if p.EnableLogging {
		fmt.Printf("%s Length check: actual=%d, expected=%d\n", p.LogPrefix, actualLength, resp.ContentLength)
	}

	// 允许一定的误差（比如压缩、编码等）
	lengthDiff := actualLength - resp.ContentLength
	if lengthDiff < 0 {
		lengthDiff = -lengthDiff
	}

	// 如果差异超过1%，认为可能不完整
	if resp.ContentLength > 0 && float64(lengthDiff)/float64(resp.ContentLength) > 0.01 {
		return false
	}

	return true
}

// isJSONStructurallyComplete 通过JSON结构判断是否完整
func (p *JSONResponseProcessor) isJSONStructurallyComplete(body []byte) bool {
	// 检查是否以完整的JSON结构结束
	trimmed := strings.TrimSpace(string(body))

	// 检查基本结构
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return false
	}

	// 检查括号平衡
	braceCount := 0
	bracketCount := 0

	for _, char := range trimmed {
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

	// 检查是否平衡
	if braceCount != 0 || bracketCount != 0 {
		if p.EnableLogging {
			fmt.Printf("%s JSON structure unbalanced: braces=%d, brackets=%d\n", p.LogPrefix, braceCount, bracketCount)
		}
		return false
	}

	return true
}

// recoverIncompleteResponse 尝试恢复不完整的响应
func (p *JSONResponseProcessor) recoverIncompleteResponse(resp *http.Response, body []byte) []byte {
	// 1. 尝试从响应头获取更多信息
	contentEncoding := resp.Header.Get("Content-Encoding")
	transferEncoding := resp.Header.Get("Transfer-Encoding")

	if p.EnableLogging {
		fmt.Printf("%s Content-Encoding: %s\n", p.LogPrefix, contentEncoding)
		fmt.Printf("%s Transfer-Encoding: %s\n", p.LogPrefix, transferEncoding)
	}

	// 2. 检查是否是分块传输
	if transferEncoding == "chunked" {
		if p.EnableLogging {
			fmt.Printf("%s Detected chunked transfer encoding\n", p.LogPrefix)
		}
		// 分块传输应该已经由HTTP客户端处理
		// 如果仍然不完整，可能是服务器端问题
	}

	// 3. 尝试修复JSON结构
	repairedBody := p.repairJSONStructure(body)

	// 4. 如果修复失败，尝试智能提取
	if !p.isValidJSON(repairedBody) {
		extractedBody := p.extractValidJSON(body)
		if extractedBody != nil {
			return extractedBody
		}
	}

	return repairedBody
}

// repairJSONStructure 修复JSON结构
func (p *JSONResponseProcessor) repairJSONStructure(body []byte) []byte {
	content := string(body)

	// 1. 移除无效的Unicode序列
	content = p.removeInvalidUnicode(content)

	// 2. 移除HTML标签
	content = p.removeHTMLTags(content)

	// 3. 修复常见的JSON问题
	content = p.fixCommonJSONIssues(content)

	// 4. 确保结构完整
	content = p.ensureJSONCompleteness(content)

	return []byte(content)
}

// removeInvalidUnicode 移除无效的Unicode序列
func (p *JSONResponseProcessor) removeInvalidUnicode(content string) string {
	var result strings.Builder
	i := 0

	for i < len(content) {
		r, size := utf8.DecodeRuneInString(content[i:])
		if r == utf8.RuneError {
			// 遇到无效的Unicode序列，跳过
			i++
			continue
		}
		result.WriteRune(r)
		i += size
	}

	return result.String()
}

// removeHTMLTags 移除HTML标签
func (p *JSONResponseProcessor) removeHTMLTags(content string) string {
	// 移除常见的HTML标签
	tags := []string{"<think>", "</think>", "<html>", "</html>", "<body>", "</body>",
		"<div>", "</div>", "<p>", "</p>", "<span>", "</span>", "<script>", "</script>"}

	for _, tag := range tags {
		content = strings.ReplaceAll(content, tag, "")
	}

	return content
}

// fixCommonJSONIssues 修复常见的JSON问题
func (p *JSONResponseProcessor) fixCommonJSONIssues(content string) string {
	// 1. 修复转义字符
	content = strings.ReplaceAll(content, "\\'", "'")
	content = strings.ReplaceAll(content, "\\\"", "\"")

	// 2. 修复换行符
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	// 3. 移除控制字符（保留换行符和制表符）
	var result strings.Builder
	for _, char := range content {
		if char >= 32 || char == '\n' || char == '\t' {
			result.WriteRune(char)
		}
	}

	return result.String()
}

// ensureJSONCompleteness 确保JSON结构完整
func (p *JSONResponseProcessor) ensureJSONCompleteness(content string) string {
	// 1. 首先检查是否包含关键字段，如果有则优先保护
	if strings.Contains(content, "structured_text_array") {
		if p.EnableLogging {
			fmt.Printf("%s Found structured_text_array, protecting content\n", p.LogPrefix)
		}
		// 对于包含关键字段的内容，使用更保守的修复策略
		if p.Config.JSONRepair.EnableConservativeFix {
			return p.conservativeJSONFix(content)
		}
	}

	// 2. 查找最后一个完整的JSON结构
	lastBrace := strings.LastIndex(content, "}")
	lastBracket := strings.LastIndex(content, "]")

	var endPos int
	if lastBrace > lastBracket {
		endPos = lastBrace + 1
	} else if lastBracket > lastBrace {
		endPos = lastBracket + 1
	} else {
		return content
	}

	// 截取到最后一个完整结构
	content = content[:endPos]

	// 3. 查找对应的开始位置
	braceCount := 0
	bracketCount := 0

	for i := len(content) - 1; i >= 0; i-- {
		switch content[i] {
		case '}':
			braceCount++
		case '{':
			braceCount--
		case ']':
			bracketCount++
		case '[':
			bracketCount--
		}

		if braceCount == 0 && bracketCount == 0 {
			content = content[i:]
			break
		}
	}

	return content
}

// conservativeJSONFix 保守的JSON修复策略，避免过度截取
func (p *JSONResponseProcessor) conservativeJSONFix(content string) string {
	if p.EnableLogging {
		fmt.Printf("%s Using conservative JSON fix strategy\n", p.LogPrefix)
	}

	// 1. 查找structured_text_array字段的开始位置
	fieldStart := strings.Index(content, `"structured_text_array"`)
	if fieldStart == -1 {
		if p.EnableLogging {
			fmt.Printf("%s structured_text_array field not found\n", p.LogPrefix)
		}
		return content
	}

	// 2. 向前查找最近的 { 开始
	braceStart := -1
	for i := fieldStart; i >= 0; i-- {
		if content[i] == '{' {
			braceStart = i
			break
		}
	}

	if braceStart == -1 {
		if p.EnableLogging {
			fmt.Printf("%s No opening brace found before structured_text_array\n", p.LogPrefix)
		}
		return content
	}

	// 3. 向后查找对应的结束大括号
	braceCount := 0
	braceEnd := -1

	for i := braceStart; i < len(content); i++ {
		switch content[i] {
		case '{':
			braceCount++
		case '}':
			braceCount--
			if braceCount == 0 {
				braceEnd = i
				break
			}
		}
	}

	if braceEnd == -1 {
		if p.EnableLogging {
			fmt.Printf("%s No matching closing brace found\n", p.LogPrefix)
		}
		// 如果没有找到匹配的结束大括号，尝试添加
		content += "}"
		return content
	}

	// 4. 提取完整的JSON对象
	extractedJSON := content[braceStart : braceEnd+1]

	if p.EnableLogging {
		fmt.Printf("%s Extracted JSON length: %d\n", p.LogPrefix, len(extractedJSON))
	}

	return extractedJSON
}

// extractValidJSON 智能提取有效的JSON
func (p *JSONResponseProcessor) extractValidJSON(body []byte) []byte {
	content := string(body)

	// 1. 优先查找包含关键字段的JSON（最重要的策略）
	if p.Config.JSONRepair.EnableFieldBasedExtraction && strings.Contains(content, "structured_text_array") {
		if p.EnableLogging {
			fmt.Printf("%s Found structured_text_array, using field-based extraction\n", p.LogPrefix)
		}
		fieldBasedJSON := p.findJSONByFields(content)
		if fieldBasedJSON != "" {
			if p.EnableLogging {
				fmt.Printf("%s Successfully extracted JSON with structured_text_array, length: %d\n", p.LogPrefix, len(fieldBasedJSON))
			}
			return []byte(fieldBasedJSON)
		}
	}

	// 2. 查找最长的有效JSON对象
	longestJSON := p.findLongestValidJSON(content)
	if longestJSON != "" {
		if p.EnableLogging {
			fmt.Printf("%s Found longest valid JSON, length: %d\n", p.LogPrefix, len(longestJSON))
		}
		return []byte(longestJSON)
	}

	// 3. 回退到基础提取
	fallbackJSON := p.fallbackExtractJSON(content)
	if fallbackJSON != "" {
		if p.EnableLogging {
			fmt.Printf("%s Using fallback extraction, length: %d\n", p.LogPrefix, len(fallbackJSON))
		}
		return []byte(fallbackJSON)
	}

	return nil
}

// findLongestValidJSON 查找最长的有效JSON对象
func (p *JSONResponseProcessor) findLongestValidJSON(content string) string {
	var longestJSON string
	maxLength := 0

	braceCount := 0
	start := -1

	for i, char := range content {
		if char == '{' {
			if braceCount == 0 {
				start = i
			}
			braceCount++
		} else if char == '}' {
			braceCount--
			if braceCount == 0 && start != -1 {
				jsonCandidate := content[start : i+1]
				if p.isValidJSON([]byte(jsonCandidate)) && len(jsonCandidate) > maxLength {
					longestJSON = jsonCandidate
					maxLength = len(jsonCandidate)
				}
				start = -1
			}
		}
	}

	return longestJSON
}

// findJSONByFields 根据字段查找JSON
func (p *JSONResponseProcessor) findJSONByFields(content string) string {
	// 查找包含关键字段的JSON
	keyFields := []string{"structured_text_array", "image_prompt", "choices", "content"}

	// 优先查找包含structured_text_array的JSON
	if strings.Contains(content, "structured_text_array") {
		if p.EnableLogging {
			fmt.Printf("%s Searching for JSON with structured_text_array\n", p.LogPrefix)
		}

		// 查找structured_text_array字段的位置
		fieldStart := strings.Index(content, `"structured_text_array"`)
		if fieldStart != -1 {
			// 向前查找最近的 { 开始
			braceStart := -1
			for i := fieldStart; i >= 0; i-- {
				if content[i] == '{' {
					braceStart = i
					break
				}
			}

			if braceStart != -1 {
				// 向后查找对应的结束大括号
				braceCount := 0
				braceEnd := -1

				for i := braceStart; i < len(content); i++ {
					switch content[i] {
					case '{':
						braceCount++
					case '}':
						braceCount--
						if braceCount == 0 {
							braceEnd = i
							break
						}
					}
				}

				if braceEnd != -1 {
					jsonCandidate := content[braceStart : braceEnd+1]
					if p.isValidJSON([]byte(jsonCandidate)) {
						if p.EnableLogging {
							fmt.Printf("%s Found valid JSON with structured_text_array, length: %d\n", p.LogPrefix, len(jsonCandidate))
						}
						return jsonCandidate
					}
				}
			}
		}
	}

	// 通用字段查找逻辑
	braceCount := 0
	start := -1

	for i, char := range content {
		if char == '{' {
			if braceCount == 0 {
				start = i
			}
			braceCount++
		} else if char == '}' {
			braceCount--
			if braceCount == 0 && start != -1 {
				jsonCandidate := content[start : i+1]
				if p.containsAllFields(jsonCandidate, keyFields) && p.isValidJSON([]byte(jsonCandidate)) {
					return jsonCandidate
				}
				start = -1
			}
		}
	}

	return ""
}

// containsAllFields 检查是否包含所有指定字段
func (p *JSONResponseProcessor) containsAllFields(jsonStr string, fields []string) bool {
	// 优先检查structured_text_array字段
	if strings.Contains(jsonStr, "structured_text_array") {
		// 如果包含structured_text_array，检查其值是否不为null
		if strings.Contains(jsonStr, `"structured_text_array":null`) {
			if p.EnableLogging {
				fmt.Printf("%s structured_text_array is null, rejecting\n", p.LogPrefix)
			}
			return false
		}

		// 检查structured_text_array是否有内容
		if strings.Contains(jsonStr, `"structured_text_array":[]`) {
			if p.EnableLogging {
				fmt.Printf("%s structured_text_array is empty array, rejecting\n", p.LogPrefix)
			}
			return false
		}

		// 如果structured_text_array有内容，认为这是有效的JSON
		if p.EnableLogging {
			fmt.Printf("%s structured_text_array has content, accepting\n", p.LogPrefix)
		}
		return true
	}

	// 对于其他字段，检查是否包含
	for _, field := range fields {
		if !strings.Contains(jsonStr, fmt.Sprintf(`"%s"`, field)) {
			return false
		}
	}

	return true
}

// fallbackExtractJSON 回退提取方法
func (p *JSONResponseProcessor) fallbackExtractJSON(content string) string {
	// 查找第一个 { 和最后一个 }
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")

	if start != -1 && end != -1 && end > start {
		extracted := content[start : end+1]

		// 对提取的JSON进行编码清理
		cleaned := p.cleanExtractedJSON(extracted)

		if p.EnableLogging {
			fmt.Printf("%s Fallback extraction: %d -> %d characters\n", p.LogPrefix, len(extracted), len(cleaned))
		}

		return cleaned
	}

	return ""
}

// cleanExtractedJSON 清理提取的JSON，移除无效字符
func (p *JSONResponseProcessor) cleanExtractedJSON(jsonStr string) string {
	var result strings.Builder
	removedCount := 0

	if p.EnableLogging {
		fmt.Printf("%s Starting JSON cleaning, original length: %d\n", p.LogPrefix, len(jsonStr))
	}

	// 使用配置的字符过滤规则
	for i, char := range jsonStr {
		// 检查是否是有效的字符
		if p.isValidCharacter(char) {
			result.WriteRune(char)
		} else {
			if p.EnableLogging {
				fmt.Printf("%s Removing invalid character at position %d: 0x%02x (rune: %q)\n", p.LogPrefix, i, char, char)
			}
			removedCount++
			continue
		}
	}

	cleaned := result.String()
	if p.EnableLogging {
		fmt.Printf("%s JSON cleaning completed: %d -> %d characters, removed %d characters\n", p.LogPrefix, len(jsonStr), len(cleaned), removedCount)

		// 如果移除了字符，显示清理前后的对比
		if removedCount > 0 {
			fmt.Printf("%s Cleaning summary:\n", p.LogPrefix)
			fmt.Printf("%s   - Original length: %d\n", p.LogPrefix, len(jsonStr))
			fmt.Printf("%s   - Cleaned length: %d\n", p.LogPrefix, len(cleaned))
			fmt.Printf("%s   - Characters removed: %d\n", p.LogPrefix, removedCount)

			// 显示清理后的前100个字符作为预览
			if len(cleaned) > 0 {
				preview := cleaned
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				fmt.Printf("%s   - Cleaned preview: %q\n", p.LogPrefix, preview)
			}
		}
	}

	return cleaned
}

// isValidCharacter 判断字符是否有效
func (p *JSONResponseProcessor) isValidCharacter(char rune) bool {
	config := p.Config.CharacterFiltering

	// 1. 检查是否是无效的Unicode字符
	if config.FilterUnicodeReplacement && (char == utf8.RuneError || char == 0xFFFD) {
		return false
	}

	// 2. 检查是否是控制字符
	if config.StrictControlChars {
		if char >= 0 && char <= 31 {
			// 检查是否在允许的控制字符列表中
			for _, allowedChar := range config.AllowedControlChars {
				if string(char) == allowedChar {
					return true
				}
			}
			// 其他控制字符都是无效的
			return false
		}
	}

	// 3. 检查是否是扩展ASCII字符
	if config.FilterExtendedASCII && char >= 128 && char <= 255 {
		return false
	}

	// 4. ASCII可打印字符（32-126）
	if char >= 32 && char <= 126 {
		return true
	}

	// 5. 检查是否在允许的Unicode范围内
	return p.isInAllowedUnicodeRange(char, config.AllowedUnicodeRanges)
}

// isInAllowedUnicodeRange 检查字符是否在允许的Unicode范围内
func (p *JSONResponseProcessor) isInAllowedUnicodeRange(char rune, ranges UnicodeRangesConfig) bool {
	charCode := int(char)

	// 检查中文字符范围
	if len(ranges.Chinese) == 2 && charCode >= ranges.Chinese[0] && charCode <= ranges.Chinese[1] {
		return true
	}

	// 检查中文标点符号范围
	if len(ranges.ChinesePunctuation) == 2 && charCode >= ranges.ChinesePunctuation[0] && charCode <= ranges.ChinesePunctuation[1] {
		return true
	}

	// 检查全角字符范围
	if len(ranges.Fullwidth) == 2 && charCode >= ranges.Fullwidth[0] && charCode <= ranges.Fullwidth[1] {
		return true
	}

	// 检查拉丁字母扩展范围
	for _, rangePair := range ranges.LatinExtended {
		if len(rangePair) == 2 && charCode >= rangePair[0] && charCode <= rangePair[1] {
			return true
		}
	}

	// 检查阿拉伯文范围
	if len(ranges.Arabic) == 2 && charCode >= ranges.Arabic[0] && charCode <= ranges.Arabic[1] {
		return true
	}

	// 检查西里尔文范围
	if len(ranges.Cyrillic) == 2 && charCode >= ranges.Cyrillic[0] && charCode <= ranges.Cyrillic[1] {
		return true
	}

	// 检查希腊文范围
	if len(ranges.Greek) == 2 && charCode >= ranges.Greek[0] && charCode <= ranges.Greek[1] {
		return true
	}

	// 检查希伯来文范围
	if len(ranges.Hebrew) == 2 && charCode >= ranges.Hebrew[0] && charCode <= ranges.Hebrew[1] {
		return true
	}

	// 检查泰文范围
	if len(ranges.Thai) == 2 && charCode >= ranges.Thai[0] && charCode <= ranges.Thai[1] {
		return true
	}

	// 检查韩文范围
	if len(ranges.Korean) == 2 && charCode >= ranges.Korean[0] && charCode <= ranges.Korean[1] {
		return true
	}

	// 检查日文平假名范围
	if len(ranges.JapaneseHiragana) == 2 && charCode >= ranges.JapaneseHiragana[0] && charCode <= ranges.JapaneseHiragana[1] {
		return true
	}

	// 检查日文片假名范围
	if len(ranges.JapaneseKatakana) == 2 && charCode >= ranges.JapaneseKatakana[0] && charCode <= ranges.JapaneseKatakana[1] {
		return true
	}

	// 如果不在已知范围内，但大于255，通常也是有效的
	if char > 255 {
		return true
	}

	// 默认情况下，只允许明确有效的字符
	return false
}

// cleanAndFixJSON 清理和修复JSON
func (p *JSONResponseProcessor) cleanAndFixJSON(body string) string {
	// 1. 基本清理
	cleaned := p.basicCleanup(body)

	// 2. 尝试解析
	if p.isValidJSON([]byte(cleaned)) {
		if p.EnableLogging {
			fmt.Printf("%s Basic cleanup successful\n", p.LogPrefix)
		}
		return cleaned
	}

	// 3. 深度修复
	fixed := p.deepFixJSON(cleaned)

	// 4. 尝试解析修复后的内容
	if p.isValidJSON([]byte(fixed)) {
		if p.EnableLogging {
			fmt.Printf("%s Deep fix successful\n", p.LogPrefix)
		}
		return fixed
	}

	// 5. 如果深度修复失败，尝试智能提取
	if p.EnableLogging {
		fmt.Printf("%s Deep fix failed, attempting smart extraction\n", p.LogPrefix)
	}

	extracted := p.extractValidJSON([]byte(body))
	if extracted != nil {
		if p.EnableLogging {
			fmt.Printf("%s Smart extraction successful, length: %d\n", p.LogPrefix, len(extracted))
		}
		return string(extracted)
	}

	// 6. 如果所有方法都失败，返回错误
	return ""
}

// basicCleanup 基本清理
func (p *JSONResponseProcessor) basicCleanup(body string) string {
	content := body

	// 移除BOM
	content = strings.TrimPrefix(content, "\uFEFF")

	// 移除前后空白
	content = strings.TrimSpace(content)

	// 移除多余的换行
	content = strings.ReplaceAll(content, "\n\n", "\n")

	// 移除无效的Unicode字符
	var result strings.Builder
	for _, char := range content {
		if char == utf8.RuneError || char == 0xFFFD {
			// 跳过无效的Unicode字符
			continue
		}
		// 只保留可打印字符、换行符和制表符
		if char >= 32 || char == '\n' || char == '\t' {
			result.WriteRune(char)
		}
	}

	return result.String()
}

// deepFixJSON 深度修复JSON
func (p *JSONResponseProcessor) deepFixJSON(body string) string {
	content := body

	// 1. 修复编码问题
	content = p.fixEncodingIssues(content)

	// 2. 修复结构问题
	content = p.fixStructuralIssues(content)

	// 3. 修复语法问题
	content = p.fixSyntaxIssues(content)

	return content
}

// fixEncodingIssues 修复编码问题
func (p *JSONResponseProcessor) fixEncodingIssues(content string) string {
	// 修复常见的编码问题
	replacements := map[string]string{
		"\\xe4": "ä",
		"\\xe8": "è",
		"\\xe9": "é",
		"\\xe0": "à",
		"\\xe2": "â",
		"\\xe7": "ç",
		"\\xef": "ï",
		"\\xee": "î",
		"\\x8a": "Š",
		"\\x8b": "‹",
		"\\x8c": "Œ",
		"\\x8d": "",
		"\\x8e": "Ž",
		"\\x8f": "",
		"\\x9a": "š",
		"\\x9b": "›",
		"\\x9c": "œ",
		"\\x9d": "",
		"\\x9e": "ž",
		"\\x9f": "Ÿ",
	}

	for old, new := range replacements {
		content = strings.ReplaceAll(content, old, new)
	}

	// 修复无效的Unicode字符
	var result strings.Builder
	for _, char := range content {
		if char == utf8.RuneError {
			// 跳过无效的Unicode字符
			continue
		}
		result.WriteRune(char)
	}

	return result.String()
}

// fixStructuralIssues 修复结构问题
func (p *JSONResponseProcessor) fixStructuralIssues(content string) string {
	// 确保JSON结构完整
	if strings.HasPrefix(content, "{") && !strings.HasSuffix(content, "}") {
		// 计算大括号平衡
		braceCount := 0
		for _, char := range content {
			if char == '{' {
				braceCount++
			} else if char == '}' {
				braceCount--
			}
		}

		// 添加缺失的结束大括号
		for i := 0; i < braceCount; i++ {
			content += "}"
		}
	}

	return content
}

// fixSyntaxIssues 修复语法问题
func (p *JSONResponseProcessor) fixSyntaxIssues(content string) string {
	// 修复常见的语法问题

	// 1. 修复多余的逗号
	content = strings.ReplaceAll(content, ",}", "}")
	content = strings.ReplaceAll(content, ",]", "]")

	// 2. 修复缺失的引号
	// 这里可以添加更复杂的引号修复逻辑

	return content
}

// isValidJSON 验证JSON是否有效
func (p *JSONResponseProcessor) isValidJSON(data []byte) bool {
	if len(data) == 0 {
		return false
	}

	var js json.RawMessage
	return json.Unmarshal(data, &js) == nil
}
