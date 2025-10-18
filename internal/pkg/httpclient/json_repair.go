package httpclient

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// JSONRepairEngine JSON修复引擎 - 专门处理截断和编码问题
type JSONRepairEngine struct {
	EnableLogging bool
	LogPrefix     string
}

// NewJSONRepairEngine 创建JSON修复引擎
func NewJSONRepairEngine() *JSONRepairEngine {
	return &JSONRepairEngine{
		EnableLogging: true,
		LogPrefix:     "[JSONRepair]",
	}
}

// RepairTruncatedJSON 修复被截断的JSON响应
func (r *JSONRepairEngine) RepairTruncatedJSON(rawResponse string) (string, error) {
	if r.EnableLogging {
		fmt.Printf("%s Starting JSON repair, input length: %d\n", r.LogPrefix, len(rawResponse))
	}

	// 1. 清理UTF-8编码问题
	cleaned := r.cleanUTF8Issues(rawResponse)
	if r.EnableLogging {
		fmt.Printf("%s After UTF-8 cleanup, length: %d\n", r.LogPrefix, len(cleaned))
	}

	// 2. 尝试直接验证
	if r.isValidJSON(cleaned) {
		if r.EnableLogging {
			fmt.Printf("%s JSON is already valid after cleanup\n", r.LogPrefix)
		}
		return cleaned, nil
	}

	// 3. 智能截断恢复
	repaired := r.smartTruncationRecovery(cleaned)
	if r.EnableLogging {
		fmt.Printf("%s After smart recovery, length: %d\n", r.LogPrefix, len(repaired))
	}

	// 4. 验证修复结果
	if r.isValidJSON(repaired) {
		if r.EnableLogging {
			fmt.Printf("%s JSON repair successful\n", r.LogPrefix)
		}
		return repaired, nil
	}

	// 5. 最后尝试：提取完整的JSON对象
	extracted := r.extractCompleteJSON(cleaned)
	if extracted != "" && r.isValidJSON(extracted) {
		if r.EnableLogging {
			fmt.Printf("%s JSON extraction successful, length: %d\n", r.LogPrefix, len(extracted))
		}
		return extracted, nil
	}

	return "", fmt.Errorf("unable to repair JSON after all attempts")
}

// cleanUTF8Issues 清理UTF-8编码问题
func (r *JSONRepairEngine) cleanUTF8Issues(input string) string {
	if !utf8.ValidString(input) {
		if r.EnableLogging {
			fmt.Printf("%s Invalid UTF-8 detected, cleaning...\n", r.LogPrefix)
		}

		// 转换为byte数组并清理
		bytes := []byte(input)
		cleaned := make([]byte, 0, len(bytes))
		
		for len(bytes) > 0 {
			r, size := utf8.DecodeRune(bytes)
			if r == utf8.RuneError && size == 1 {
				// 跳过无效的字节
				bytes = bytes[1:]
			} else {
				// 添加有效的rune
				cleaned = append(cleaned, bytes[:size]...)
				bytes = bytes[size:]
			}
		}

		return string(cleaned)
	}

	return input
}

// smartTruncationRecovery 智能截断恢复
func (r *JSONRepairEngine) smartTruncationRecovery(input string) string {
	trimmed := strings.TrimSpace(input)
	
	// 检查是否是对象或数组的开始
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		// 尝试找到第一个{或[
		objectStart := strings.Index(trimmed, "{")
		arrayStart := strings.Index(trimmed, "[")
		
		var start int = -1
		if objectStart != -1 && (arrayStart == -1 || objectStart < arrayStart) {
			start = objectStart
		} else if arrayStart != -1 {
			start = arrayStart
		}
		
		if start > 0 {
			trimmed = trimmed[start:]
		}
	}

	// 分析结构并尝试补全
	if strings.HasPrefix(trimmed, "{") {
		return r.repairJSONObject(trimmed)
	} else if strings.HasPrefix(trimmed, "[") {
		return r.repairJSONArray(trimmed)
	}

	return trimmed
}

// repairJSONObject 修复JSON对象
func (r *JSONRepairEngine) repairJSONObject(input string) string {
	if r.EnableLogging {
		fmt.Printf("%s Repairing JSON object...\n", r.LogPrefix)
	}

	// 分析括号平衡
	braceStack := []rune{}
	inString := false
	escaped := false
	lastValidPos := 0

	for i, char := range input {
		if escaped {
			escaped = false
			continue
		}

		if char == '\\' {
			escaped = true
			continue
		}

		if char == '"' {
			inString = !inString
			continue
		}

		if !inString {
			switch char {
			case '{':
				braceStack = append(braceStack, '{')
				lastValidPos = i
			case '}':
				if len(braceStack) > 0 && braceStack[len(braceStack)-1] == '{' {
					braceStack = braceStack[:len(braceStack)-1]
					lastValidPos = i
				}
			case '[':
				braceStack = append(braceStack, '[')
				lastValidPos = i
			case ']':
				if len(braceStack) > 0 && braceStack[len(braceStack)-1] == '[' {
					braceStack = braceStack[:len(braceStack)-1]
					lastValidPos = i
				}
			}
		}
	}

	// 如果栈为空，说明结构完整
	if len(braceStack) == 0 {
		return input
	}

	// 截取到最后一个有效位置
	if lastValidPos > 0 && lastValidPos < len(input) {
		truncated := input[:lastValidPos+1]
		
		// 尝试补全缺失的结构
		for len(braceStack) > 0 {
			last := braceStack[len(braceStack)-1]
			if last == '{' {
				truncated += "}"
			} else if last == '[' {
				truncated += "]"
			}
			braceStack = braceStack[:len(braceStack)-1]
		}

		return truncated
	}

	// 如果找不到有效位置，尝试其他策略
	return r.fallbackRepair(input)
}

// repairJSONArray 修复JSON数组
func (r *JSONRepairEngine) repairJSONArray(input string) string {
	if r.EnableLogging {
		fmt.Printf("%s Repairing JSON array...\n", r.LogPrefix)
	}

	// 类似于对象修复，但针对数组
	return r.repairJSONObject(input) // 复用对象修复逻辑
}

// fallbackRepair 后备修复策略
func (r *JSONRepairEngine) fallbackRepair(input string) string {
	if r.EnableLogging {
		fmt.Printf("%s Using fallback repair strategy...\n", r.LogPrefix)
	}

	// 尝试找到最后一个完整的JSON结构
	input = strings.TrimSpace(input)
	
	// 找到最后一个完整的对象或值
	lastComplete := r.findLastCompleteStructure(input)
	if lastComplete != "" {
		return lastComplete
	}

	// 如果都失败了，尝试修复字符串末尾
	return r.repairStringEnding(input)
}

// findLastCompleteStructure 找到最后一个完整的结构
func (r *JSONRepairEngine) findLastCompleteStructure(input string) string {
	// 从后往前找，尝试找到完整的JSON结构
	for i := len(input) - 1; i >= 0; i-- {
		substring := input[:i+1]
		if r.isValidJSON(substring) {
			return substring
		}
	}
	return ""
}

// repairStringEnding 修复字符串结尾
func (r *JSONRepairEngine) repairStringEnding(input string) string {
	// 检查是否在字符串中截断
	lastQuote := strings.LastIndex(input, "\"")
	if lastQuote > 0 {
		// 检查引号前的字符
		beforeQuote := input[:lastQuote]
		
		// 如果看起来像是在字符串值中截断，尝试修复
		if strings.HasSuffix(beforeQuote, ": \"") || strings.HasSuffix(beforeQuote, ", \"") {
			// 这看起来像是在字符串值中截断了
			repaired := beforeQuote + "\""
			
			// 尝试补全结构
			openBraces := strings.Count(repaired, "{") - strings.Count(repaired, "}")
			openBrackets := strings.Count(repaired, "[") - strings.Count(repaired, "]")
			
			for i := 0; i < openBraces; i++ {
				repaired += "}"
			}
			for i := 0; i < openBrackets; i++ {
				repaired += "]"
			}
			
			return repaired
		}
	}

	return input
}

// extractCompleteJSON 提取完整的JSON对象
func (r *JSONRepairEngine) extractCompleteJSON(input string) string {
	if r.EnableLogging {
		fmt.Printf("%s Extracting complete JSON...\n", r.LogPrefix)
	}

	// 使用正则表达式找到可能的JSON结构
	patterns := []string{
		`\{[^{}]*(?:\{[^{}]*\}[^{}]*)*\}`, // 简单对象
		`\[[^\[\]]*(?:\[[^\[\]]*\][^\[\]]*)*\]`, // 简单数组
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllString(input, -1)
		
		for _, match := range matches {
			if r.isValidJSON(match) {
				return match
			}
		}
	}

	return ""
}

// isValidJSON 检查字符串是否为有效JSON
func (r *JSONRepairEngine) isValidJSON(s string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}

// AdvancedJSONExtractor 高级JSON提取器
type AdvancedJSONExtractor struct {
	RepairEngine *JSONRepairEngine
}

// NewAdvancedJSONExtractor 创建高级JSON提取器
func NewAdvancedJSONExtractor() *AdvancedJSONExtractor {
	return &AdvancedJSONExtractor{
		RepairEngine: NewJSONRepairEngine(),
	}
}

// ExtractValidJSON 从响应中提取有效的JSON
func (e *AdvancedJSONExtractor) ExtractValidJSON(response []byte) ([]byte, error) {
	responseStr := string(response)
	
	// 1. 尝试修复
	repaired, err := e.RepairEngine.RepairTruncatedJSON(responseStr)
	if err == nil {
		return []byte(repaired), nil
	}

	// 2. 如果修复失败，尝试其他提取策略
	return e.extractByPatternMatching(response)
}

// extractByPatternMatching 通过模式匹配提取JSON
func (e *AdvancedJSONExtractor) extractByPatternMatching(response []byte) ([]byte, error) {
	responseStr := string(response)
	
	// 寻找structured_text_array模式
	pattern := `"structured_text_array"\s*:\s*\[`
	re := regexp.MustCompile(pattern)
	
	loc := re.FindStringIndex(responseStr)
	if loc == nil {
		return nil, fmt.Errorf("structured_text_array not found")
	}

	// 从找到的位置开始，尝试提取完整的数组
	start := loc[0]
	
	// 向前找到对象的开始
	for i := start - 1; i >= 0; i-- {
		if responseStr[i] == '{' {
			start = i
			break
		}
	}

	// 从开始位置提取，并尝试修复
	candidate := responseStr[start:]
	repaired, err := e.RepairEngine.RepairTruncatedJSON(candidate)
	if err == nil {
		return []byte(repaired), nil
	}

	return nil, fmt.Errorf("failed to extract valid JSON")
}
