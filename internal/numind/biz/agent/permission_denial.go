package agent

import "encoding/json"

// PermissionDenialDetail — 工具调用被 permission gate 拒绝后的详情；
// 由 biz/permission/wrap_hooks 构造，通过 PermissionSink 注入 ctx，runner.Run 末尾收并填 RunResult.PermissionDenial。
//
// 字段全 string 类型；不引用 HookAction / TerminalReason 枚举，避免与 M9 同包并行编译冲突。
type PermissionDenialDetail struct {
	ToolName       string `json:"tool_name"`
	Behavior       string `json:"behavior"`        // "deny" | "ask"
	DecisionReason string `json:"decision_reason"` // 11 种 canonical 之一（字符串值，不引用 permission.DecisionReasonType 类型）
	ValidatorID    string `json:"validator_id"`
	Message        string `json:"message"`
}

// String 便于日志输出
func (d *PermissionDenialDetail) String() string {
	if d == nil {
		return "<nil PermissionDenialDetail>"
	}
	b, _ := json.Marshal(d)
	return string(b)
}
