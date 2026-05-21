package compliance

import "strings"

// outputForbiddenFences — LLM 输出不应出现的 fence tags
// S1 reviewer P2-3 补：tool_call / function_call
var outputForbiddenFences = []string{
	"<system>", "<system_prompt>",
	"<platform_hard_rules>", "<tenant_hard_rules>",
	"<memory>", "<memory_context>", "<memory-context>",
	"<compliance>", "<external_data>",
	"<tool_call>", "<function_call>",
}

// ValidateOutput — 检查 LLM 输出是否含禁用 fence tag
// 返回 (hit, matchedTag)
// 大小写不敏感
func ValidateOutput(output string) (bool, string) {
	lower := strings.ToLower(output)
	for _, fence := range outputForbiddenFences {
		if strings.Contains(lower, fence) {
			return true, fence
		}
	}
	return false, ""
}
