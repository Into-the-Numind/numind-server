package agent

import (
	"context"
	"fmt"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"numind-server/internal/pkg/log"
)

// adaptFullToEinoTool wraps a FullTool as Eino's tool.InvokableTool so that
// AgentRunner can pass it to react.AgentConfig.ToolsConfig.Tools.
//
// hooks may be nil — then Pre/PostToolCall is skipped (#3 compatible).
// When non-nil, PreToolCall fires before Execute and PostToolCall fires
// after, in line with #2 RunHooks contract (#4 wiring path).
func adaptFullToEinoTool(ft FullTool, hooks *RunHooks) einotool.InvokableTool {
	return &fullToolEinoAdapter{ft: ft, hooks: hooks}
}

type fullToolEinoAdapter struct {
	ft    FullTool
	hooks *RunHooks
}

// Compile-time assertion.
var _ einotool.InvokableTool = (*fullToolEinoAdapter)(nil)

// Info returns the Eino ToolInfo derived from the wrapped FullTool's metadata.
// ParamsOneOf is left empty for now; a future task can populate it from ft.InputSchema().
func (a *fullToolEinoAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        a.ft.Name(),
		Desc:        a.ft.Description(),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

// InvokableRun delegates to the wrapped FullTool.Execute, with optional
// PreToolCall and PostToolCall hooks injected before/after. The PostToolCall
// hook ALWAYS fires (even on Execute error), so cleanup hooks like
// sandbox session destruction don't leak.
func (a *fullToolEinoAdapter) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
	input := ToolInput(args)

	// PreToolCall: may short-circuit via HookActionStop / HookActionBlockingStop
	if a.hooks != nil && a.hooks.PreToolCall != nil {
		action, err := a.hooks.PreToolCall(ctx, a, args)
		if err != nil {
			return "", fmt.Errorf("PreToolCall: %w", err)
		}
		if action != HookActionContinue {
			// M10: record the stopping action to registry so runner.Run can propagate TerminalReason
			if a.hooks.Registry != nil {
				a.hooks.Registry.Record(action)
			}
			return "", fmt.Errorf("tool execution stopped by hook: action=%d", action)
		}
	}

	// Execute the underlying tool
	result, execErr := a.ft.Execute(ctx, input)
	var output string
	if result != nil {
		output = string(result)
	}

	// PostToolCall always fires (cleanup semantic). Errors from PostToolCall
	// are logged + only surface to the caller when no execErr already exists.
	if a.hooks != nil && a.hooks.PostToolCall != nil {
		postAction, postErr := a.hooks.PostToolCall(ctx, a, output, execErr)
		// M10: record non-Continue actions to registry so runner.Run can propagate TerminalReason
		if a.hooks.Registry != nil && postAction != HookActionContinue {
			a.hooks.Registry.Record(postAction)
		}
		if postErr != nil {
			log.Warnw("PostToolCall failed",
				"tool", a.ft.Name(),
				"post_err", postErr,
				"exec_err", execErr)
			if execErr == nil {
				return output, fmt.Errorf("PostToolCall: %w", postErr)
			}
		}
	}

	if execErr != nil {
		return output, execErr
	}
	return output, nil
}
