package domain

// MetaStrategy 综合策略（大类）
// 例如：信任建立系统、价值塑造系统等
type MetaStrategy struct {
	ID              string   `json:"id"`               // 策略ID，如 "M-T01"
	Name            string   `json:"name"`             // 策略名称
	Description     string   `json:"description"`      // 路由专用描述（简短）
	DecisionTree    string   `json:"decision_tree"`    // 核心决策树逻辑（Prompt）
	TriggerKeywords []string `json:"trigger_keywords"` // 触发关键词
	BasicIDs        []string `json:"basic_ids"`        // 包含的基础策略ID列表
}

// BasicStrategy 基础策略（原子卡片）
// 例如：P-001 交付边界与位势重构
type BasicStrategy struct {
	ID              string   `json:"id"`               // 策略ID，如 "P-001"
	MetaID          string   `json:"meta_id"`          // 所属综合策略ID
	Name            string   `json:"name"`             // 策略名称
	Description     string   `json:"description"`      // 路由专用描述（简短）
	Content         string   `json:"content"`          // 全量注入内容（Markdown）
	TriggerKeywords []string `json:"trigger_keywords"` // 触发关键词
}
