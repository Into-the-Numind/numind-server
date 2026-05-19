package main

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// currentDateTool implements tool.InvokableTool and returns today's date in ISO 8601 format.
type currentDateTool struct{}

// Compile-time assertion: currentDateTool must satisfy tool.InvokableTool.
var _ tool.InvokableTool = (*currentDateTool)(nil)

// newGetCurrentDateTool creates a new currentDateTool.
func newGetCurrentDateTool() tool.BaseTool {
	return &currentDateTool{}
}

// Info returns the tool's metadata used by the ChatModel to decide when to call it.
func (t *currentDateTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        "get_current_date",
		Desc:        "Returns today's date in ISO 8601 format (YYYY-MM-DD). Use this when the user asks about the current date, today's date, the day of the week, or any question that requires knowing what day it is.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			// No parameters required.
		}),
	}, nil
}

// InvokableRun executes the tool, wrapped in a Langfuse span (spec §3.6).
// Arguments are ignored (no parameters needed).
func (t *currentDateTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	return instrumentedToolCall(ctx, "get_current_date", func() (string, error) {
		return time.Now().UTC().Format("2006-01-02"), nil
	})
}
