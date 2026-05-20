package agent

import (
	"context"
	"sync/atomic"

	"github.com/cloudwego/eino/components/tool"

	"numind-server/internal/numind/biz/narration"
)

// HookAction is the return value enum from RunHooks, determining how the state machine transitions.
type HookAction int

const (
	HookActionContinue       HookAction = iota // 0 — continue normally
	HookActionStop                             // 1 — trigger TerminalHookStopped
	HookActionBlockingStop                     // 2 — trigger TerminalStopHookPrevented
	HookActionPermissionDeny                   // 3 — trigger TerminalPermissionDenied (#6 agent-mode-permission-pipeline)
)

// HookActionRegistry is a thread-safe recorder of the last HookAction emitted during a Run.
// adapter PreToolCall + PostToolCall both call Record; runner.Run reads LastAction at the end
// to propagate Stop/BlockingStop/PermissionDeny into the correct TerminalReason via state.Transition.
type HookActionRegistry struct {
	last atomic.Int32 // 0=Continue 1=Stop 2=BlockingStop 3=PermissionDeny
}

// NewHookActionRegistry creates a registry with zero value (HookActionContinue).
func NewHookActionRegistry() *HookActionRegistry {
	return &HookActionRegistry{}
}

// Record stores the action atomically.
func (r *HookActionRegistry) Record(action HookAction) {
	r.last.Store(int32(action))
}

// LastAction returns the last recorded action.
func (r *HookActionRegistry) LastAction() HookAction {
	return HookAction(r.last.Load())
}

// Reset sets the registry back to HookActionContinue (used between runs or in tests).
func (r *HookActionRegistry) Reset() {
	r.last.Store(int32(HookActionContinue))
}

// RunHooks is the extension point for sandbox and future features (#4) to inject into Runtime.
// Blueprint §4.1.9 contract; this feature only exposes the interface, real sandbox implementation is in #4.
type RunHooks struct {
	PreToolCall  func(ctx context.Context, t tool.BaseTool, input string) (HookAction, error)
	PostToolCall func(ctx context.Context, t tool.BaseTool, output string, err error) (HookAction, error)
	Registry     *HookActionRegistry // M10: receives Record() calls from adapter; runner reads LastAction()

	// NarrationProvider is the shared narration singleton; runner.Run attaches it
	// per-Run from r.narrationProvider. nil = legacy adapter behavior (no narration
	// events emitted). #8 agent-mode-narration-layer.
	NarrationProvider *narration.Provider

	// NarrationRunID is the per-Run identifier (= agent_run.ID); runner.Run sets
	// this so the adapter can route narration events to the correct stream.
	// Zero is invalid; only set when NarrationProvider is non-nil.
	NarrationRunID uint64
}

// HookActionToLoopEvent maps a HookAction to a state machine LoopEvent.
func HookActionToLoopEvent(action HookAction) LoopEvent {
	switch action {
	case HookActionStop:
		return LoopEventHookActionStop
	case HookActionBlockingStop:
		return LoopEventHookActionBlockStop
	case HookActionPermissionDeny:
		return LoopEventPermissionDenied
	default:
		// HookActionContinue does not map to an event
		return LoopEventInvalid
	}
}
