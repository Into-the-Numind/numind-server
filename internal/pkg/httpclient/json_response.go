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
	// 配置选项
	MaxResponseSize int64  // 最大响应大小
	EnableLogging   bool   // 是否启用日志
	LogPrefix       string // 日志前缀
}

// NewJSONResponseProcessor 创建新的JSON响应处理器
func NewJSONResponseProcessor() *JSONResponseProcessor {
	return &JSONResponseProcessor{
		MaxResponseSize: 10 * 1024 * 1024, // 10MB
		EnableLogging:   true,
		LogPrefix:       "[JSONProcessor]",
	}
}

// ProcessResponse 处理HTTP响应，确保返回完整的JSON
func (p *JSONResponseProcessor) ProcessResponse(resp *http.Response) ([]byte, error) {
	if resp == nil {
		return nil, fmt.Errorf("response is nil")
	}

	// 1. 检查Content-Length
	expectedLength := resp.ContentLength
	if p.EnableLogging {
		fmt.Printf("%s Expected content length: %d\n", p.LogPrefix, expectedLength)
	}

	// 2. 读取响应体
	body, err := p.readResponseBody(resp.Body, expectedLength)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if p.EnableLogging {
		fmt.Printf("%s Actual response length: %d\n", p.LogPrefix, len(body))
	}

	// 3. 验证响应完整性
	if !p.isResponseComplete(body, expectedLength) {
		if p.EnableLogging {
			fmt.Printf("%s Response appears to be incomplete, attempting recovery...\n", p.LogPrefix)
		}
		
		// 尝试恢复不完整的响应
		recoveredBody, err := p.recoverIncompleteResponse(body, resp)
		if err != nil {
			return nil, fmt.Errorf("failed to recover incomplete response: %w", err)
		}
		body = recoveredBody
	}

	// 4. 清理和修复JSON
	cleanedBody, err := p.cleanAndFixJSON(body)
	if err != nil {
		return nil, fmt.Errorf("failed to clean and fix JSON: %w", err)
	}

	// 5. 最终验证
	if !p.isValidJSON(cleanedBody) {
		if p.EnableLogging {
			fmt.Printf("%s Final JSON validation failed, attempting smart extraction...\n", p.LogPrefix)
		}
		
		// 如果清理和修复失败，尝试智能提取
		extractedBody := p.extractValidJSON(body)
		if extractedBody != nil {
			if p.EnableLogging {
				fmt.Printf("%s Smart extraction successful, length: %d\n", p.LogPrefix, len(extractedBody))
			}
			return extractedBody, nil
		}
		
		return nil, fmt.Errorf("failed to produce valid JSON after processing")
	}

	if p.EnableLogging {
		fmt.Printf("%s Final JSON validation successful, length: %d\n", p.LogPrefix, len(cleanedBody))
	}

	return cleanedBody, nil
}

// readResponseBody 读取响应体，确保完整性
func (p *JSONResponseProcessor) readResponseBody(body io.ReadCloser, expectedLength int64) ([]byte, error) {
	defer body.Close()

	// 使用bytes.Buffer来构建响应
	var buffer bytes.Buffer
	
	// 如果知道预期长度，预分配缓冲区
	if expectedLength > 0 {
		buffer.Grow(int(expectedLength))
	}

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
			if totalRead > p.MaxResponseSize {
				return nil, fmt.Errorf("response too large: %d bytes (max: %d)", totalRead, p.MaxResponseSize)
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
func (p *JSONResponseProcessor) isResponseComplete(body []byte, expectedLength int64) bool {
	// 如果不知道预期长度，尝试通过JSON结构判断
	if expectedLength <= 0 {
		return p.isJSONStructurallyComplete(body)
	}
	
	// 检查实际长度是否匹配预期长度
	actualLength := int64(len(body))
	if p.EnableLogging {
		fmt.Printf("%s Length check: actual=%d, expected=%d\n", p.LogPrefix, actualLength, expectedLength)
	}
	
	// 允许一定的误差（比如压缩、编码等）
	lengthDiff := actualLength - expectedLength
	if lengthDiff < 0 {
		lengthDiff = -lengthDiff
	}
	
	// 如果差异超过1%，认为可能不完整
	if expectedLength > 0 && float64(lengthDiff)/float64(expectedLength) > 0.01 {
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
func (p *JSONResponseProcessor) recoverIncompleteResponse(body []byte, resp *http.Response) ([]byte, error) {
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
			return extractedBody, nil
		}
	}
	
	return repairedBody, nil
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
		return p.conservativeJSONFix(content)
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
	extractedJSON := content[braceStart:braceEnd+1]
	
	if p.EnableLogging {
		fmt.Printf("%s Extracted JSON length: %d\n", p.LogPrefix, len(extractedJSON))
	}
	
	return extractedJSON
}

// extractValidJSON 智能提取有效的JSON
func (p *JSONResponseProcessor) extractValidJSON(body []byte) []byte {
	content := string(body)
	
	// 1. 优先查找包含关键字段的JSON（最重要的策略）
	if strings.Contains(content, "structured_text_array") {
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
					jsonCandidate := content[braceStart:braceEnd+1]
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
	
	// 更严格的JSON字符过滤
	for i, char := range jsonStr {
		// 1. 检查是否是无效的Unicode字符
		if char == utf8.RuneError || char == 0xFFFD {
			if p.EnableLogging {
				fmt.Printf("%s Removing invalid Unicode at position %d: 0x%02x\n", p.LogPrefix, i, char)
			}
			removedCount++
			continue
		}
		
		// 2. 检查是否是控制字符（除了换行符和制表符）
		if char >= 0 && char <= 31 && char != '\n' && char != '\t' {
			if p.EnableLogging {
				fmt.Printf("%s Removing control character at position %d: 0x%02x (rune: %q)\n", p.LogPrefix, i, char, char)
			}
			removedCount++
			continue
		}
		
		// 3. 检查是否是扩展ASCII字符（128-255）
		if char >= 128 && char <= 255 {
			if p.EnableLogging {
				fmt.Printf("%s Removing extended ASCII at position %d: 0x%02x (rune: %q)\n", p.LogPrefix, i, char, char)
			}
			removedCount++
			continue
		}
		
		// 4. 检查是否是JSON结构中的问题字符
		if p.isJSONStructureProblem(char, jsonStr, i) {
			if p.EnableLogging {
				fmt.Printf("%s Removing JSON structure problem character at position %d: 0x%02x (rune: %q)\n", p.LogPrefix, i, char, char)
			}
			removedCount++
			continue
		}
		
		// 5. 检查是否是有效的字符
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

// isJSONStructureProblem 检查是否是JSON结构中的问题字符
func (p *JSONResponseProcessor) isJSONStructureProblem(char rune, jsonStr string, position int) bool {
	// 检查是否是JSON结构中的常见问题字符
	problemChars := []rune{'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z'}
	
	// 检查是否是问题字符
	for _, problemChar := range problemChars {
		if char == problemChar {
			// 进一步检查上下文，判断是否真的是问题字符
			return p.isContextuallyProblematic(jsonStr, position, char)
		}
	}
	
	return false
}

// isContextuallyProblematic 检查字符在上下文中是否真的有问题
func (p *JSONResponseProcessor) isContextuallyProblematic(jsonStr string, position int, char rune) bool {
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
	validBefore := p.isValidJSONContext(before)
	validAfter := p.isValidJSONContext(after)
	
	// 如果前后都是有效的，那么这个字符可能不是问题
	if validBefore && validAfter {
		return false
	}
	
	// 检查是否在JSON字符串中
	inString := p.isInJSONString(jsonStr, position)
	if inString {
		// 在JSON字符串中的字符通常是有效的
		return false
	}
	
	// 检查是否在JSON对象或数组的键值对中
	if p.isInJSONKeyValue(jsonStr, position) {
		// 在键值对中的字符可能是问题字符
		return true
	}
	
	return false
}

// isValidJSONContext 检查字符是否是有效的JSON上下文
func (p *JSONResponseProcessor) isValidJSONContext(char string) bool {
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
func (p *JSONResponseProcessor) isInJSONString(jsonStr string, position int) bool {
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
func (p *JSONResponseProcessor) isInJSONKeyValue(jsonStr string, position int) bool {
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

// isValidCharacter 判断字符是否有效
func (p *JSONResponseProcessor) isValidCharacter(char rune) bool {
	// 1. 严格过滤控制字符（0-31，除了换行符和制表符）
	if char >= 0 && char <= 31 {
		// 只允许换行符和制表符
		if char == '\n' || char == '\t' {
			return true
		}
		// 其他控制字符都是无效的
		return false
	}
	
	// 2. ASCII可打印字符（32-126）
	if char >= 32 && char <= 126 {
		return true
	}
	
	// 3. 换行符和制表符（已在上面的控制字符检查中处理）
	// 这里作为双重保险
	if char == '\n' || char == '\t' {
		return true
	}
	
	// 4. 中文字符（Unicode范围：4E00-9FFF）
	if char >= 0x4E00 && char <= 0x9FFF {
		return true
	}
	
	// 5. 中文标点符号（3000-303F）
	if char >= 0x3000 && char <= 0x303F {
		return true
	}
	
	// 6. 全角字符（FF00-FFEF）
	if char >= 0xFF00 && char <= 0xFFEF {
		return true
	}
	
	// 7. 其他常见的Unicode字符
	// 拉丁字母扩展（00C0-00FF, 0100-017F, 0180-024F）
	if (char >= 0x00C0 && char <= 0x00FF) ||
	   (char >= 0x0100 && char <= 0x017F) ||
	   (char >= 0x0180 && char <= 0x024F) {
		return true
	}
	
	// 8. 其他有效的Unicode字符（大于255但有效的字符）
	// 这些字符在JSON中通常是有效的
	if char > 255 {
		// 检查是否是其他常见的Unicode范围
		// 阿拉伯文（0600-06FF）
		if char >= 0x0600 && char <= 0x06FF {
			return true
		}
		// 西里尔文（0400-04FF）
		if char >= 0x0400 && char <= 0x04FF {
			return true
		}
		// 希腊文（0370-03FF）
		if char >= 0x0370 && char <= 0x03FF {
			return true
		}
		// 希伯来文（0590-05FF）
		if char >= 0x0590 && char <= 0x05FF {
			return true
		}
		// 泰文（0E00-0E7F）
		if char >= 0x0E00 && char <= 0x0E7F {
			return true
		}
		// 韩文（AC00-D7AF）
		if char >= 0xAC00 && char <= 0xD7AF {
			return true
		}
		// 日文平假名（3040-309F）
		if char >= 0x3040 && char <= 0x309F {
			return true
		}
		// 日文片假名（30A0-30FF）
		if char >= 0x30A0 && char <= 0x30FF {
			return true
		}
		// 日文汉字（4E00-9FFF，已包含在中文字符中）
		
		// 如果不在已知范围内，但大于255，通常也是有效的
		return true
	}
	
	// 9. 严格过滤扩展ASCII字符（128-255）
	// 这些字符在JSON中通常是有问题的，特别是控制字符
	if char >= 128 && char <= 255 {
		// 严格移除所有扩展ASCII字符，包括：
		// - 控制字符（0x80-0x9F）
		// - 扩展字符（0xA0-0xFF）
		// 这些字符在JSON中通常会导致解析问题
		return false
	}
	
	// 10. 默认情况下，只允许明确有效的字符
	return false
}

// cleanAndFixJSON 清理和修复JSON
func (p *JSONResponseProcessor) cleanAndFixJSON(body []byte) ([]byte, error) {
	// 1. 基本清理
	cleaned := p.basicCleanup(body)
	
	// 2. 尝试解析
	if p.isValidJSON(cleaned) {
		if p.EnableLogging {
			fmt.Printf("%s Basic cleanup successful\n", p.LogPrefix)
		}
		return cleaned, nil
	}
	
	// 3. 深度修复
	fixed := p.deepFixJSON(cleaned)
	
	// 4. 尝试解析修复后的内容
	if p.isValidJSON(fixed) {
		if p.EnableLogging {
			fmt.Printf("%s Deep fix successful\n", p.LogPrefix)
		}
		return fixed, nil
	}
	
	// 5. 如果深度修复失败，尝试智能提取
	if p.EnableLogging {
		fmt.Printf("%s Deep fix failed, attempting smart extraction\n", p.LogPrefix)
	}
	
	extracted := p.extractValidJSON(body)
	if extracted != nil {
		if p.EnableLogging {
			fmt.Printf("%s Smart extraction successful, length: %d\n", p.LogPrefix, len(extracted))
		}
		return extracted, nil
	}
	
	// 6. 如果所有方法都失败，返回错误
	return nil, fmt.Errorf("failed to clean and fix JSON")
}

// basicCleanup 基本清理
func (p *JSONResponseProcessor) basicCleanup(body []byte) []byte {
	content := string(body)
	
	// 移除BOM
	if strings.HasPrefix(content, "\uFEFF") {
		content = content[3:]
	}
	
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
	
	return []byte(result.String())
}

// deepFixJSON 深度修复JSON
func (p *JSONResponseProcessor) deepFixJSON(body []byte) []byte {
	content := string(body)
	
	// 1. 修复编码问题
	content = p.fixEncodingIssues(content)
	
	// 2. 修复结构问题
	content = p.fixStructuralIssues(content)
	
	// 3. 修复语法问题
	content = p.fixSyntaxIssues(content)
	
	return []byte(content)
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

