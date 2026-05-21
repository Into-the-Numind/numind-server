package compliance

import (
	"context"

	"numind-server/internal/pkg/model"
)

// ToolInfo — compliance-local 工具元数据（不依赖 biz/agent）
// compliancegate.buildRequest 从 agent.FullTool 取值填充
type ToolInfo struct {
	Name          string
	IsDestructive bool
}

// ComplianceResult — 每次合规判定的返回
type ComplianceResult struct {
	Decision      string
	RuleLayer     string
	RuleID        *uint64
	Reason        string
	TriggeredText string
	NarrationMsg  string
	Metadata      map[string]any
}

// ComplianceRequest — PreToolCall hook 检查工具调用时的请求结构
type ComplianceRequest struct {
	AgentRunID        uint64
	UserID            uint
	ParentUserID      uint
	AgentDefinitionID uint64
	Tool              ToolInfo
	InputJSON         string
}

// ComplianceGate — 三层合规框架的顶层 interface（gate.go 实现）
type ComplianceGate interface {
	SystemPromptBlock(ctx context.Context, ad *model.AgentDefinition) (string, error)
	CheckUserInput(ctx context.Context, parentUserID uint, input string) (ComplianceResult, error)
	CheckLLMOutput(ctx context.Context, parentUserID uint, output string) (ComplianceResult, error)
	CheckToolCall(ctx context.Context, req ComplianceRequest) (ComplianceResult, error)
}

const DefaultOutOfScopeNarration = "这个问题有点超出我的范围，我更擅长帮你解决学习相关事项。"

// truncate 字符串截断到指定长度（用于 TriggeredText 限长 500）
// 共享 helper；gate.go / scope_validator.go / injection_detector.go 多处用
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
