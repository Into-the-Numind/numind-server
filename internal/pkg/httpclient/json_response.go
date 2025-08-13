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
		return nil, fmt.Errorf("failed to produce valid JSON after processing")
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
	// 1. 查找最后一个完整的JSON结构
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
	
	// 2. 查找对应的开始位置
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

// extractValidJSON 智能提取有效的JSON
func (p *JSONResponseProcessor) extractValidJSON(body []byte) []byte {
	content := string(body)
	
	// 1. 查找最长的有效JSON对象
	longestJSON := p.findLongestValidJSON(content)
	if longestJSON != "" {
		return []byte(longestJSON)
	}
	
	// 2. 查找包含关键字段的JSON
	fieldBasedJSON := p.findJSONByFields(content)
	if fieldBasedJSON != "" {
		return []byte(fieldBasedJSON)
	}
	
	// 3. 回退到基础提取
	fallbackJSON := p.fallbackExtractJSON(content)
	if fallbackJSON != "" {
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
		return content[start : end+1]
	}
	
	return ""
}

// cleanAndFixJSON 清理和修复JSON
func (p *JSONResponseProcessor) cleanAndFixJSON(body []byte) ([]byte, error) {
	// 1. 基本清理
	cleaned := p.basicCleanup(body)
	
	// 2. 尝试解析
	if p.isValidJSON(cleaned) {
		return cleaned, nil
	}
	
	// 3. 深度修复
	fixed := p.deepFixJSON(cleaned)
	
	// 4. 最终验证
	if p.isValidJSON(fixed) {
		return fixed, nil
	}
	
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
	
	return []byte(content)
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
	}
	
	for old, new := range replacements {
		content = strings.ReplaceAll(content, old, new)
	}
	
	return content
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
