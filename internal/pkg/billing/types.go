package billing

import "encoding/json"

// TokenUsage Token使用统计信息（统一类型，供所有模块使用）
type TokenUsage struct {
	PromptTokens          int `json:"prompt_tokens"`     // 输入 tokens
	CompletionTokens      int `json:"completion_tokens"` // 输出 tokens
	TotalTokens           int `json:"total_tokens"`      // 总 tokens
	ReasoningTokens       int `json:"reasoning_tokens"`  // 思考过程 tokens（某些模型直接返回）
	EstimatedPromptTokens int    `json:"-"`                 // 预估输入 tokens（内部使用）
	ModelName             string `json:"-"`                 // 实际调用的模型名称（内部使用，不序列化）

	// Volcengine/OpenAI 兼容的嵌套结构
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// Normalize 同步嵌套字段到扁平字段
func (u *TokenUsage) Normalize() {
	if u == nil {
		return
	}
	if u.ReasoningTokens == 0 && u.CompletionTokensDetails.ReasoningTokens > 0 {
		u.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
}

// EmbeddingUsage Embedding API 使用统计
type EmbeddingUsage struct {
	TotalTokens int `json:"total_tokens"`
}

// ExtractUsageFromSSEData 从 SSE data JSON 中提取 TokenUsage（通用方法）
// 适用于 OpenAI 兼容格式的流式响应，usage 在最后一个 chunk 中返回
func ExtractUsageFromSSEData(data string) *TokenUsage {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil
	}
	rawUsage, ok := m["usage"]
	if !ok || rawUsage == nil {
		return nil
	}
	usageBytes, err := json.Marshal(rawUsage)
	if err != nil {
		return nil
	}
	var usage TokenUsage
	if err := json.Unmarshal(usageBytes, &usage); err != nil {
		return nil
	}
	if usage.TotalTokens > 0 || usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
		usage.Normalize()
		return &usage
	}
	return nil
}
