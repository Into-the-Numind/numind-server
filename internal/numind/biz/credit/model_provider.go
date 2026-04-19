package credit

import "strings"

// ProviderFromModel best-effort 根据 model 名前缀推断 provider。
// 未知时返回空字符串——credit 层消费方应降级到 global fallback coefficient
// 或 pricing_rule（ai_service_route 表的规范化查询是 future work，暂时用
// prefix 规则覆盖 prod 模型集）。
//
// 规则（与 llm_provider.name 对齐）：
//   - qwen* / text-embedding-v*                  → "ali-dashscope"
//   - deepseek* / doubao* / glm-*                → "volc-ark"
//   - claude-* / gemini-*                        → "dmxapi"
//   - 其它 / 空                                   → ""
func ProviderFromModel(modelName string) string {
	switch {
	case modelName == "":
		return ""
	case strings.HasPrefix(modelName, "qwen") || strings.HasPrefix(modelName, "text-embedding-v"):
		return "ali-dashscope"
	case strings.HasPrefix(modelName, "deepseek") || strings.HasPrefix(modelName, "doubao") ||
		strings.HasPrefix(modelName, "glm-"):
		return "volc-ark"
	case strings.HasPrefix(modelName, "claude-") || strings.HasPrefix(modelName, "gemini-"):
		return "dmxapi"
	default:
		return ""
	}
}
