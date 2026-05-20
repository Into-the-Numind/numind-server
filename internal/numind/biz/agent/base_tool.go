package agent

import (
	"context"
	"encoding/json"
)

// BaseTool 提供 FullTool 36 方法中 **31** 个非必需方法的默认实现（36 - 5 必须重写 = 31）。
// 工具 impl 嵌入 BaseTool 后只需重写 5 个必须方法：
//
//	Name / Description / UserFacingName / NarrationVerb / Execute
//
// 注意：agent.BaseTool (struct) 与 Eino 的 tool.BaseTool (interface) 同名不同包。
// 同时 import 时用 alias：import einotool "github.com/cloudwego/eino/components/tool"
type BaseTool struct{}

// ── 基础元数据（默认值）──

func (BaseTool) Aliases() []string            { return nil }
func (BaseTool) SearchHint() []string         { return nil }
func (BaseTool) Prompt() string               { return "" }
func (BaseTool) InputSchema() json.RawMessage { return nil }

// ── 行为标志（默认值）──

func (BaseTool) IsEnabled(_ ToolConfig) bool        { return true }
func (BaseTool) IsConcurrencySafe(_ ToolInput) bool { return true }
func (BaseTool) IsReadOnly() bool                   { return true } // 默认安全
func (BaseTool) IsDestructive() bool                { return false }
func (BaseTool) InterruptBehavior() string          { return "cancel" }
func (BaseTool) IsSearchOrReadCommand() bool        { return false }

// ── 来源标记（默认值）──

func (BaseTool) IsMCP() bool           { return false }
func (BaseTool) IsCLI() bool           { return false }
func (BaseTool) MCPInfo() *MCPToolInfo { return nil }
func (BaseTool) CLIInfo() *CLIToolInfo { return nil }

// ── 加载策略（默认值）──

func (BaseTool) ShouldDefer() bool { return false }
func (BaseTool) AlwaysLoad() bool  { return false }

// ── 输出控制（默认值）──

func (BaseTool) MaxResultSizeChars() int { return 0 } // 0 = 无限制

// ── 输入处理（默认值）──

func (BaseTool) BackfillObservableInput(input ToolInput) ToolInput  { return input }
func (BaseTool) ValidateInput(_ context.Context, _ ToolInput) error { return nil }
func (BaseTool) InputsEquivalent(a, b ToolInput) bool               { return string(a) == string(b) }

// ── 权限控制（默认值）──

func (BaseTool) CheckPermissions(_ context.Context, _ ToolInput) error  { return nil }
func (BaseTool) GetPath(_ ToolInput) string                             { return "" }
func (BaseTool) PreparePermissionMatcher(_ ToolInput) PermissionMatcher { return nil }

// ── 执行 + 结果序列化（默认值）──

func (BaseTool) MapToolResultToBlock(result ToolResult) []ContentBlock {
	return []ContentBlock{{Type: "text", Content: string(result)}}
}
func (BaseTool) ToAutoClassifierInput(input ToolInput) map[string]interface{} {
	return map[string]interface{}{"raw_input": string(input)}
}

// ── Narration 层（默认值）──

func (BaseTool) GetActivityDescription(_ ToolInput) string         { return "" }
func (BaseTool) RenderToolUseMessage(_ ToolInput) NarrationMessage { return NarrationMessage{} }
func (BaseTool) RenderToolResultMessage(_ ToolInput, _ ToolResult) NarrationMessage {
	return NarrationMessage{}
}
func (BaseTool) RenderToolErrorMessage(_ ToolInput, _ error) NarrationMessage {
	return NarrationMessage{}
}
func (BaseTool) ShouldShowResultInNarration() bool   { return true }
func (BaseTool) NarrationDetail(_ ToolResult) string { return "" }

// 必须 impl 重写（无默认）：
// Name() string
// Description() string
// UserFacingName() string
// NarrationVerb() string
// Execute(ctx context.Context, input ToolInput) (ToolResult, error)
