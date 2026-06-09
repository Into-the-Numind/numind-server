package billing

import "encoding/json"

// promptTokensDetails nests the cached_tokens field used by the OpenAI-standard
// wire path usage.prompt_tokens_details.cached_tokens — the Batch A
// auto-prefix-cache signal for DeepSeek / GPT served via the DMXAPI
// OpenAI-compatible endpoint. It is the portion of prompt_tokens served from the
// provider's prefix cache.
type promptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// TokenUsage Token使用统计信息（统一类型，供所有模块使用）
type TokenUsage struct {
	PromptTokens          int    `json:"prompt_tokens"`     // 输入 tokens
	CompletionTokens      int    `json:"completion_tokens"` // 输出 tokens
	TotalTokens           int    `json:"total_tokens"`      // 总 tokens
	ReasoningTokens       int    `json:"reasoning_tokens"`  // 思考过程 tokens（某些模型直接返回）
	EstimatedPromptTokens int    `json:"-"`                 // 预估输入 tokens（内部使用）
	ModelName             string `json:"-"`                 // 实际调用的模型名称（内部使用，不序列化）
	Provider              string `json:"-"`                 // 实际 provider（如 aihubmix / volc / ali；内部使用，不序列化）

	// CachedPromptTokens 是 PromptTokens 中由 provider 前缀缓存命中的子集（Batch A
	// 自动缓存：DeepSeek / GPT 经 DMXAPI OpenAI 兼容端点）。它是 PromptTokens 的一部分，
	// 不是额外增量——计费按折扣后的缓存输入价计这部分 token。
	// 故意不加 omitempty：该字段需在 billing.TokenUsage 的 JSON 往返序列化中稳定保留。
	// Additive：provider 不上报缓存命中时该值为 0，cost/usage 与启用缓存前字节一致。
	CachedPromptTokens int `json:"cached_prompt_tokens"`

	// Volcengine/OpenAI 兼容的嵌套结构
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`

	// PromptTokensDetails 嵌套 cached_tokens，OpenAI 标准缓存命中线格式
	// （usage.prompt_tokens_details.cached_tokens）。Normalize() 优先读它。
	PromptTokensDetails promptTokensDetails `json:"prompt_tokens_details"`

	// PromptCacheHitTokens 是 DeepSeek 原生的扁平缓存命中字段
	// （prompt_cache_hit_tokens）。当节点直连 DeepSeek 时使用；Normalize() 在嵌套
	// 字段缺失时回退到它。
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
}

// Normalize 同步嵌套/原生字段到扁平字段（never-overwrite 语义）。
// 缓存命中 token 的展平优先级：已设置的扁平字段 > 嵌套 prompt_tokens_details.cached_tokens
// > DeepSeek 原生扁平 prompt_cache_hit_tokens。三者皆缺时 CachedPromptTokens 保持 0，
// 与启用缓存前行为字节一致（零回归）。
func (u *TokenUsage) Normalize() {
	if u == nil {
		return
	}
	if u.ReasoningTokens == 0 && u.CompletionTokensDetails.ReasoningTokens > 0 {
		u.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	if u.CachedPromptTokens == 0 {
		if u.PromptTokensDetails.CachedTokens > 0 {
			u.CachedPromptTokens = u.PromptTokensDetails.CachedTokens
		} else if u.PromptCacheHitTokens > 0 {
			u.CachedPromptTokens = u.PromptCacheHitTokens
		}
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
