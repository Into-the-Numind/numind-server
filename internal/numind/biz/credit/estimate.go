package credit

// 预估积分表 — 仅用于操作前的余额预检，不用于实际扣减
var estimatedCredits = map[string]int64{
	"sop_run":          20,
	"sop_chat":         6,
	"salesrag_chat":    6,
	"chatbot_chat":     6, // aligned with sop_chat (both are interactive chat operations)
	"profile_analysis": 2,
	"style_analysis":   1,
	"file_parse":       3,
	"ocr":              1,
	"agent_run":        6,  // agent ReAct LLM 调用，对齐 chat 类（实际走 pricing token 计价）
	"image_gen":        10, // 文生图 per-image 预检估算（实际走 pricing_rule flat per-call）
}

// GetEstimatedCredits 获取操作的预估积分消耗
func GetEstimatedCredits(operation string) int64 {
	if v, ok := estimatedCredits[operation]; ok {
		return v
	}
	return 1
}

// IsSopOperation 判断是否为 SOP 相关操作
func IsSopOperation(operation string) bool {
	return operation == "sop_run" || operation == "sop_chat"
}
