package agent

import (
	"context"
	"encoding/json"
)

// ToolFactory 是工具注册的插件抽象（蓝本 §4.2.2）。
// v1 仅 PlatformToolFactory；mcp/cli/webhook 留给后续 feature。
type ToolFactory interface {
	// FactoryID 唯一标识（用于 tool_factory_registry.factory_id）
	FactoryID() string

	// Source 工具来源类型："platform" | "mcp" | "cli" | "webhook"
	Source() string

	// DisplayName 给运营看的工厂名
	DisplayName() string

	// LoadTools 加载工厂内所有工具，返回 FullTool 列表 + metadata（用于 seed tool_definition）。
	// 调用方（AgentToolRegistry.LoadAll）应保证 tools[i].Name() == metadata[i].ToolName。
	LoadTools(ctx context.Context) ([]FullTool, []ToolMetadata, error)

	// Watch 监听工厂内工具变化（v1 noop；#10 dynamic CRUD 用）
	Watch(ctx context.Context, onChange func(diff ToolDiff)) error
}

// ToolMetadata 工厂报告给 tool_definition 的元信息。
type ToolMetadata struct {
	ToolName                string
	DisplayName             string
	Description             string
	Source                  string
	RiskLevel               string // safe/moderate/dangerous
	RequiresSandbox         bool
	RequiresTenantWhitelist bool
	InputSchema             json.RawMessage
	Category                string
}

// ToolDiff 描述工具集合的变更（Watch 回调用）。
type ToolDiff struct {
	Added   []ToolMetadata
	Removed []string // tool names
	Updated []ToolMetadata
}
