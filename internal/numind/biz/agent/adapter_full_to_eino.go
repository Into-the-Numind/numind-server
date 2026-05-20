package agent

import (
	"context"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// adaptFullToEinoTool wraps a FullTool as Eino's tool.InvokableTool so that
// AgentRunner can pass it to react.AgentConfig.ToolsConfig.Tools.
func adaptFullToEinoTool(ft FullTool) einotool.InvokableTool {
	return &fullToolEinoAdapter{ft: ft}
}

type fullToolEinoAdapter struct {
	ft FullTool
}

// Compile-time assertion.
var _ einotool.InvokableTool = (*fullToolEinoAdapter)(nil)

// Info returns the Eino ToolInfo derived from the wrapped FullTool's metadata.
// ParamsOneOf is left empty for now; a future task can populate it from ft.InputSchema().
func (a *fullToolEinoAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: a.ft.Name(),
		Desc: a.ft.Description(),
		// ParamsOneOf: empty map placeholder; Task #InputSchema will wire ft.InputSchema().
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun delegates to the wrapped FullTool.Execute and converts the
// ToolResult (json.RawMessage) to the string that Eino expects.
func (a *fullToolEinoAdapter) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
	result, err := a.ft.Execute(ctx, ToolInput(args))
	if err != nil {
		return "", err
	}
	return string(result), nil
}
