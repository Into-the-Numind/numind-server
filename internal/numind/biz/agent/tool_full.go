package agent

import (
	"context"
	"encoding/json"
)

// FullTool 是 #3 引入的完整 Tool 接口，蓝本 §4.2.3。
// 共 36 方法，分 9 组：基础元数据 / 行为标志 / 来源标记 / 加载策略 /
// 输出控制 / 输入处理 / 权限控制 / 执行+结果序列化 / Narration 层。
type FullTool interface {
	// ── 基础元数据（6） ──
	Name() string
	Aliases() []string
	SearchHint() []string
	Description() string
	Prompt() string
	InputSchema() json.RawMessage

	// ── 行为标志（6） ──
	IsEnabled(cfg ToolConfig) bool
	IsConcurrencySafe(input ToolInput) bool
	IsReadOnly() bool
	IsDestructive() bool
	InterruptBehavior() string // "cancel" | "wait" | "noop"
	IsSearchOrReadCommand() bool

	// ── 来源标记（4） ──
	IsMCP() bool
	IsCLI() bool
	MCPInfo() *MCPToolInfo
	CLIInfo() *CLIToolInfo

	// ── 加载策略（2） ──
	ShouldDefer() bool
	AlwaysLoad() bool

	// ── 输出控制（1） ──
	MaxResultSizeChars() int

	// ── 输入处理（3） ──
	BackfillObservableInput(input ToolInput) ToolInput
	ValidateInput(ctx context.Context, input ToolInput) error
	InputsEquivalent(a, b ToolInput) bool

	// ── 权限控制（3） ──
	CheckPermissions(ctx context.Context, input ToolInput) error
	GetPath(input ToolInput) string
	PreparePermissionMatcher(input ToolInput) PermissionMatcher

	// ── 执行 + 结果序列化（3） ──
	Execute(ctx context.Context, input ToolInput) (ToolResult, error)
	MapToolResultToBlock(result ToolResult) []ContentBlock
	ToAutoClassifierInput(input ToolInput) map[string]interface{}

	// ── Narration 层（8） ──
	UserFacingName() string
	GetActivityDescription(input ToolInput) string
	RenderToolUseMessage(input ToolInput) NarrationMessage
	RenderToolResultMessage(input ToolInput, result ToolResult) NarrationMessage
	RenderToolErrorMessage(input ToolInput, err error) NarrationMessage
	ShouldShowResultInNarration() bool
	NarrationVerb() string
	NarrationDetail(result ToolResult) string
}

// ToolConfig：FullTool.IsEnabled 的轻量参数，替代蓝本 AgentConfig
// （AgentConfig 是 #10 引入的 agent_definition model，#3 范围未到）
type ToolConfig struct {
	EnableSandbox  bool
	EnableImageGen bool
	// 后续 feature 按需扩展
}

// ToolInput / ToolResult：通用 JSON 结构
type ToolInput = json.RawMessage
type ToolResult = json.RawMessage

// PermissionMatcher：权限缓存匹配器，#6 permission-pipeline 用
type PermissionMatcher interface {
	Matches(other PermissionMatcher) bool
	Hash() string
}

// MCPToolInfo / CLIToolInfo：占位（MCP/CLI 在后续 feature）
type MCPToolInfo struct {
	ServerName string
	ToolName   string
}
type CLIToolInfo struct {
	Command string
	Args    []string
}

// ContentBlock / NarrationMessage：占位，#8 narration-layer 真实实装
type ContentBlock struct {
	Type    string // "text" | "image" | "document"
	Content string
}
type NarrationMessage struct {
	Verb   string
	Detail string
	Icon   string
}
