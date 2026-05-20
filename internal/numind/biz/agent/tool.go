package agent

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// MinimalTool 是 agent-mode runtime 的最小工具 interface（feature #2 引入，#3 重命名）。
// 新代码应使用 FullTool（#3 引入的 36 方法完整接口）。
// MinimalTool 保留用于向后兼容 #2 现有 mock/测试；可通过 WrapMinimal 升级为 FullTool。
type MinimalTool interface {
	Name() string
	Description() string
	Run(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// einoToolAdapter 把内部 MinimalTool 适配为 Eino 的 tool.InvokableTool。
// Task 6 将改造为接受 FullTool。
type einoToolAdapter struct {
	impl MinimalTool
}

// AdaptTool 把内部 MinimalTool 包装为 Eino tool.InvokableTool。
func AdaptTool(t MinimalTool) tool.InvokableTool {
	return &einoToolAdapter{impl: t}
}

// 编译期断言
var _ tool.InvokableTool = (*einoToolAdapter)(nil)

// Info 返回工具元数据给 Eino 的 LLM。
func (a *einoToolAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: a.impl.Name(),
		Desc: a.impl.Description(),
		// ParamsOneOf: 在 #3 tool-registry 时通过 JSON Schema 自动推导；#2 暂留空（Eino 允许）
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun 把 Eino 的 string args 转换为 json.RawMessage 调用内部 Tool.Run。
func (a *einoToolAdapter) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	out, err := a.impl.Run(ctx, json.RawMessage(args))
	if err != nil {
		return "", err
	}
	return string(out), nil
}
