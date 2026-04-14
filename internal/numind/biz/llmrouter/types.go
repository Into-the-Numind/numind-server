package llmrouter

// ThinkingFormat 定义不同 LLM 供应商的思考模式激活方式
const (
	// ThinkingNone 不发送任何 thinking 参数（模型不支持或不需要）
	ThinkingNone = ""
	// ThinkingEnableField 使用 enable_thinking: true（Gemini、Qwen 等通过 DMXAPI）
	ThinkingEnableField = "enable_thinking"
	// ThinkingAnthropic Claude 专用，走 /v1/messages 端点 + thinking:{type:"adaptive"}
	ThinkingAnthropic = "anthropic"
	// ThinkingDoubao 豆包：走 /v1/chat/completions + thinking:{type:"enabled"}
	ThinkingDoubao = "doubao"
)

// ResolvedRoute 解析后的路由信息，包含调用 LLM 所需的完整凭据和参数
type ResolvedRoute struct {
	BaseURL         string
	APIKey          string
	ProviderModelID string
	ProviderName    string
	ThinkingFormat  string // ThinkingNone / ThinkingEnableField / ThinkingAnthropic
}
