package llmrouter

// ResolvedRoute 解析后的路由信息，包含调用 LLM 所需的完整凭据和参数
type ResolvedRoute struct {
	BaseURL         string
	APIKey          string
	ProviderModelID string
	ProviderName    string
	EnableThinking  bool
}
