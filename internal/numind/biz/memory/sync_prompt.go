package memory

// SyncTurnSystemPrompt drives the per-turn memory extraction LLM call.
// Used by compositeProvider.SyncTurn after every successful agent run.
const SyncTurnSystemPrompt = `你是一个对话观察员。读以下用户/助手对话，提取 0-3 条**只对未来对话有用**的"事实"或"偏好"。

- 事实：用户主动声明的稳定信息（如"我在做销售"/"我的客户是 B2B SaaS"）
- 偏好：用户表达的风格喜好（如"我喜欢看图表不喜欢看长文字"）
- 不提取：临时问题、一次性请求、闲聊

输出 JSON: {"items": [{"kind": "fact|preference", "content": "<≤80字>", "confidence": 0.0-1.0}]}
不输出其他内容。`

// SyncTurnItem is one extracted memory item from the LLM response.
type SyncTurnItem struct {
	Kind       string  `json:"kind"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
}

// SyncTurnResult wraps the LLM JSON response.
type SyncTurnResult struct {
	Items []SyncTurnItem `json:"items"`
}
