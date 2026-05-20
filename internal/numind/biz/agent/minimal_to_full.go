package agent

import (
	"context"
	"encoding/json"
)

// MinimalToFullAdapter 把 MinimalTool 包装为 FullTool。
// 用于向后兼容 #2 的 MinimalTool 实现，使其可以在 FullTool 上下文中使用。
type MinimalToFullAdapter struct {
	BaseTool
	impl MinimalTool
}

// WrapMinimal 把 MinimalTool 升级为 FullTool。
func WrapMinimal(m MinimalTool) FullTool {
	return &MinimalToFullAdapter{impl: m}
}

// 编译期断言：MinimalToFullAdapter 满足 FullTool
var _ FullTool = (*MinimalToFullAdapter)(nil)

// 5 个必须重写的方法

func (a *MinimalToFullAdapter) Name() string           { return a.impl.Name() }
func (a *MinimalToFullAdapter) Description() string    { return a.impl.Description() }
func (a *MinimalToFullAdapter) UserFacingName() string { return a.impl.Name() } // 用 Name 兜底
func (a *MinimalToFullAdapter) NarrationVerb() string  { return "执行" }
func (a *MinimalToFullAdapter) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	out, err := a.impl.Run(ctx, json.RawMessage(input))
	return ToolResult(out), err
}
