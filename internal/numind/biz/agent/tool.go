package agent

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Tool 是 agent-mode runtime 的最小工具 interface（feature #2）。
// feature #3 tool-registry 将扩展到 38 字段的完整版（IsDestructive / Permission / Prompt / etc.）。
type Tool interface {
	Name() string
	Description() string
	Run(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// einoToolAdapter 把内部 Tool 适配为 Eino 的 tool.InvokableTool。
type einoToolAdapter struct {
	impl Tool
}

// AdaptTool 把内部 Tool 包装为 Eino tool.InvokableTool。
func AdaptTool(t Tool) tool.InvokableTool {
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
