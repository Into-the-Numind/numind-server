// Package permission 提供 Agent 模式工具调用前的权限判别流水线。
// 蓝本 §4.4。
package permission

// DecisionReasonType — 11 种 canonical（蓝本 §4.4.5）
type DecisionReasonType string

const (
	DecisionReasonRule                 DecisionReasonType = "rule"
	DecisionReasonMode                 DecisionReasonType = "mode"
	DecisionReasonSubcommandResults    DecisionReasonType = "subcommandResults"
	DecisionReasonPermissionPromptTool DecisionReasonType = "permissionPromptTool"
	DecisionReasonHook                 DecisionReasonType = "hook"
	DecisionReasonAsyncAgent           DecisionReasonType = "asyncAgent"
	DecisionReasonSandboxOverride      DecisionReasonType = "sandboxOverride"
	DecisionReasonClassifier           DecisionReasonType = "classifier"
	DecisionReasonWorkingDir           DecisionReasonType = "workingDir"
	DecisionReasonSafetyCheck          DecisionReasonType = "safetyCheck"
	DecisionReasonOther                DecisionReasonType = "other"
)

// Behavior 常量
const (
	BehaviorAllow       = "allow"
	BehaviorAsk         = "ask"
	BehaviorDeny        = "deny"
	BehaviorPassthrough = "passthrough"
)

// PermissionResult — pipeline Validator 的返回结构（蓝本 §4.4.2）
type PermissionResult struct {
	Behavior       string
	DecisionReason DecisionReasonType
	ValidatorID    string
	Message        string
	UpdatedInput   map[string]any
	Pending        *PendingClassifierCheck
	Suggestions    []PermissionUpdate
}

// PendingClassifierCheck — 异步 LLM 分类器（v1 占位，#14 落地）
type PendingClassifierCheck struct {
	ClassifierID string
	TimeoutMs    int
	OnApprove    string
	OnReject     string
}

// PermissionUpdate — 给 #10 的规则调整建议（v1 永远 nil）
type PermissionUpdate struct {
	RuleID     uint64
	Suggestion string
}
