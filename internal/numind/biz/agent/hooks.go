package agent

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
)

// HookAction is the return value enum from RunHooks, determining how the state machine transitions.
type HookAction int

const (
	HookActionContinue     HookAction = iota // 0 — continue normally
	HookActionStop                           // 1 — trigger TerminalHookStopped
	HookActionBlockingStop                   // 2 — trigger TerminalStopHookPrevented
)

// RunHooks is the extension point for sandbox and future features (#4) to inject into Runtime.
// Blueprint §4.1.9 contract; this feature only exposes the interface, real sandbox implementation is in #4.
type RunHooks struct {
	PreToolCall  func(ctx context.Context, t tool.BaseTool, input string) (HookAction, error)
	PostToolCall func(ctx context.Context, t tool.BaseTool, output string, err error) (HookAction, error)
}

// HookActionToLoopEvent maps a HookAction to a state machine LoopEvent.
func HookActionToLoopEvent(action HookAction) LoopEvent {
	switch action {
	case HookActionStop:
		return LoopEventHookActionStop
	case HookActionBlockingStop:
		return LoopEventHookActionBlockStop
	default:
		// HookActionContinue does not map to an event
		return LoopEventInvalid
	}
}
