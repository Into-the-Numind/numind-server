package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestHookActionToLoopEvent(t *testing.T) {
	cases := []struct {
		action HookAction
		want   LoopEvent
	}{
		{HookActionContinue, LoopEventInvalid},
		{HookActionStop, LoopEventHookActionStop},
		{HookActionBlockingStop, LoopEventHookActionBlockStop},
	}
	for _, c := range cases {
		if got := HookActionToLoopEvent(c.action); got != c.want {
			t.Errorf("action=%v: got %v, want %v", c.action, got, c.want)
		}
	}
}

func TestRunHooks_NilSafe(t *testing.T) {
	// RunHooks fields are all funcs; nil invocation is caller's responsibility (runner handles nil check).
	// Here we only verify the struct can be zero-value instantiated.
	var hooks RunHooks
	if hooks.PreToolCall != nil || hooks.PostToolCall != nil {
		t.Error("RunHooks zero value should have nil hooks")
	}
}

// TestHookAction_StateTransitions verifies that hook-triggered reasons are consistent with state machine Transitions.
func TestHookAction_StateTransitions(t *testing.T) {
	s := &LoopState{}
	term, _, isTerm := s.Transition(LoopEventHookActionStop)
	if !isTerm || term != TerminalHookStopped {
		t.Errorf("HookActionStop: got term=%v isTerm=%v", term, isTerm)
	}

	s2 := &LoopState{}
	term2, _, isTerm2 := s2.Transition(LoopEventHookActionBlockStop)
	if !isTerm2 || term2 != TerminalStopHookPrevented {
		t.Errorf("HookActionBlockStop: got term=%v isTerm=%v", term2, isTerm2)
	}
}

// fakeTool is a minimal tool.BaseTool stub for testing hooks without Eino runtime deps.
type fakeTool struct{ name string }

func (f *fakeTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: f.name}, nil
}

// Compile-time assertion: fakeTool implements tool.BaseTool.
var _ tool.BaseTool = (*fakeTool)(nil)

func TestRunHooks_PreToolCall_Stops(t *testing.T) {
	hooks := &RunHooks{
		PreToolCall: func(_ context.Context, _ tool.BaseTool, _ string) (HookAction, error) {
			return HookActionStop, nil
		},
	}
	action, err := hooks.PreToolCall(context.Background(), &fakeTool{name: "t"}, "input")
	if err != nil {
		t.Fatal(err)
	}
	if action != HookActionStop {
		t.Errorf("got %v, want HookActionStop", action)
	}
}

func TestRunHooks_PostToolCall_BlockingStop(t *testing.T) {
	hooks := &RunHooks{
		PostToolCall: func(_ context.Context, _ tool.BaseTool, _ string, _ error) (HookAction, error) {
			return HookActionBlockingStop, nil
		},
	}
	action, err := hooks.PostToolCall(context.Background(), &fakeTool{name: "t"}, "output", nil)
	if err != nil {
		t.Fatal(err)
	}
	if action != HookActionBlockingStop {
		t.Errorf("got %v, want HookActionBlockingStop", action)
	}
}
