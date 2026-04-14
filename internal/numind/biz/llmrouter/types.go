package llmrouter

// ThinkingFormat 定义不同 LLM 供应商的思考模式激活方式
const (
	// ThinkingNone 不发送任何 thinking 参数（模型不支持或不需要）
	ThinkingNone = ""
	// ThinkingEnableField 使用 enable_thinking: true（Qwen 等通过 DMXAPI OpenAI 兼容端点）
	ThinkingEnableField = "enable_thinking"
	// ThinkingDoubao 豆包：走 /v1/chat/completions + thinking:{type:"enabled"}
	ThinkingDoubao = "doubao"
	// ThinkingGemini Gemini：走原生 /v1beta/models/{model}:streamGenerateContent 端点
	ThinkingGemini = "gemini"
	// ThinkingGPT GPT-5/O 系列：走 /v1/responses 端点 + reasoning:{effort}
	ThinkingGPT = "gpt"
)

// ResolvedRoute 解析后的路由信息，包含调用 LLM 所需的完整凭据和参数
type ResolvedRoute struct {
	BaseURL         string
	APIKey          string
	ProviderModelID string
	ProviderName    string
	ThinkingFormat  string // ThinkingNone / ThinkingEnableField / ThinkingDoubao / ThinkingGemini
}
