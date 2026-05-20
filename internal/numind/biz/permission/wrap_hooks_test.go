package permission

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	einoschema "github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/pkg/middleware"
)

// fakeEinoTool 实现 einotool.BaseTool 接口（Info + InvokableRun，但 Run 不会被调）。
type fakeEinoTool struct {
	name string
}

func (f *fakeEinoTool) Info(_ context.Context) (*einoschema.ToolInfo, error) {
	return &einoschema.ToolInfo{Desc: "fake", Name: f.name}, nil
}

// fakeFullTool 嵌入 agent.BaseTool 拿 31 个默认方法 + override 4 个关键方法。
type fakeFullTool struct {
	agent.BaseTool
	name          string
	isDestructive bool
}

func (f *fakeFullTool) Name() string           { return f.name }
func (f *fakeFullTool) Description() string    { return "" }
func (f *fakeFullTool) UserFacingName() string { return f.name }
func (f *fakeFullTool) NarrationVerb() string  { return "exec" }
func (f *fakeFullTool) Execute(_ context.Context, _ agent.ToolInput) (agent.ToolResult, error) {
	return nil, nil
}
func (f *fakeFullTool) IsDestructive() bool { return f.isDestructive }

var _ agent.FullTool = (*fakeFullTool)(nil)

// 通用 ctx builder：注入 sink + agentDef + fullToolMap
func ctxWithAll(sink chan *agent.PermissionDenialDetail, agentDefID uint64, parentUserID uint, fullTools map[string]agent.FullTool) context.Context {
	ctx := context.Background()
	if sink != nil {
		ctx = agent.WithPermissionSink(ctx, sink)
	}
	if agentDefID > 0 {
		ctx = agent.WithAgentDefCtx(ctx, agentDefID, parentUserID)
	}
	if fullTools != nil {
		ctx = agent.WithFullToolMap(ctx, fullTools)
	}
	ctx = middleware.NewContextWithUserID(ctx, 5)
	return ctx
}

func TestWrap_PreToolCall_DenyShortCircuits(t *testing.T) {
	gate := NewPermissionGate(
		WithValidators(&stubValidator{id: "Deny", result: Deny("Deny", DecisionReasonRule, "blocked")}),
	)
	defer gate.Close()

	baseCalled := false
	base := &agent.RunHooks{
		PreToolCall: func(_ context.Context, _ einotool.BaseTool, _ string) (agent.HookAction, error) {
			baseCalled = true
			return agent.HookActionContinue, nil
		},
		Registry: agent.NewHookActionRegistry(),
	}

	wrapped := WrapHooks(base, gate)

	sink := make(chan *agent.PermissionDenialDetail, 1)
	tool := &fakeEinoTool{name: "bash_exec"}
	ctx := ctxWithAll(sink, 42, 1, map[string]agent.FullTool{"bash_exec": &fakeFullTool{name: "bash_exec"}})

	action, err := wrapped.PreToolCall(ctx, tool, `{"command":"ls"}`)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if action != agent.HookActionPermissionDeny {
		t.Errorf("action = %d, want HookActionPermissionDeny (%d)", action, agent.HookActionPermissionDeny)
	}
	if baseCalled {
		t.Errorf("base.PreToolCall must NOT be called when permission deny")
	}
	if base.Registry.LastAction() != agent.HookActionPermissionDeny {
		t.Errorf("Registry.LastAction = %d, want HookActionPermissionDeny", base.Registry.LastAction())
	}
	select {
	case d := <-sink:
		if d.ValidatorID != "Deny" {
			t.Errorf("sink detail ValidatorID = %s, want Deny", d.ValidatorID)
		}
		if d.Behavior != BehaviorDeny {
			t.Errorf("sink detail Behavior = %s, want deny", d.Behavior)
		}
	default:
		t.Errorf("sink did not receive detail")
	}
}

func TestWrap_PreToolCall_AllowForwardsToBase(t *testing.T) {
	gate := NewPermissionGate(
		WithValidators(&stubValidator{id: "Allow", result: Allow("Allow", DecisionReasonOther, "ok")}),
	)
	defer gate.Close()

	baseCalled := false
	receivedInput := ""
	base := &agent.RunHooks{
		PreToolCall: func(_ context.Context, _ einotool.BaseTool, in string) (agent.HookAction, error) {
			baseCalled = true
			receivedInput = in
			return agent.HookActionContinue, nil
		},
		Registry: agent.NewHookActionRegistry(),
	}

	wrapped := WrapHooks(base, gate)

	sink := make(chan *agent.PermissionDenialDetail, 1)
	tool := &fakeEinoTool{name: "web_search"}
	ctx := ctxWithAll(sink, 42, 1, map[string]agent.FullTool{"web_search": &fakeFullTool{name: "web_search"}})

	action, err := wrapped.PreToolCall(ctx, tool, `{"query":"hi"}`)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if action != agent.HookActionContinue {
		t.Errorf("action = %d, want HookActionContinue", action)
	}
	if !baseCalled {
		t.Errorf("base.PreToolCall must be called when allow")
	}
	if receivedInput != `{"query":"hi"}` {
		t.Errorf("base received different input: %s", receivedInput)
	}
	select {
	case <-sink:
		t.Errorf("sink unexpectedly received detail on allow")
	default:
	}
}

func TestWrap_PreToolCall_UpdatedInputMarshalled(t *testing.T) {
	gate := NewPermissionGate(
		WithValidators(&stubValidator{
			id: "Override",
			result: PermissionResult{
				Behavior:       BehaviorAllow,
				DecisionReason: DecisionReasonSandboxOverride,
				ValidatorID:    "Override",
				UpdatedInput:   map[string]any{"path": "/sandbox/safe"},
			},
		}),
	)
	defer gate.Close()

	var receivedInput string
	base := &agent.RunHooks{
		PreToolCall: func(_ context.Context, _ einotool.BaseTool, in string) (agent.HookAction, error) {
			receivedInput = in
			return agent.HookActionContinue, nil
		},
	}

	wrapped := WrapHooks(base, gate)
	tool := &fakeEinoTool{name: "file_read"}
	ctx := ctxWithAll(nil, 0, 0, map[string]agent.FullTool{"file_read": &fakeFullTool{name: "file_read"}})

	_, _ = wrapped.PreToolCall(ctx, tool, `{"path":"/etc/passwd"}`)

	if !strings.Contains(receivedInput, "/sandbox/safe") {
		t.Errorf("base received original input %s; expected UpdatedInput marshalled", receivedInput)
	}
	// Also verify it is valid JSON
	var m map[string]any
	if err := json.Unmarshal([]byte(receivedInput), &m); err != nil {
		t.Errorf("received input not valid JSON: %v", err)
	}
}

func TestWrap_PreToolCall_NoBaseHooks(t *testing.T) {
	gate := NewPermissionGate(
		WithValidators(&stubValidator{id: "Allow", result: Allow("Allow", DecisionReasonOther, "")}),
	)
	defer gate.Close()

	wrapped := WrapHooks(nil, gate)
	tool := &fakeEinoTool{name: "web_search"}
	ctx := ctxWithAll(nil, 0, 0, map[string]agent.FullTool{"web_search": &fakeFullTool{name: "web_search"}})

	action, err := wrapped.PreToolCall(ctx, tool, `{}`)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if action != agent.HookActionContinue {
		t.Errorf("with nil base + allow, action = %d, want Continue", action)
	}
}

func TestWrap_PostToolCall_ForwardsToBase(t *testing.T) {
	gate := NewPermissionGate()
	defer gate.Close()

	baseCalled := false
	base := &agent.RunHooks{
		PostToolCall: func(_ context.Context, _ einotool.BaseTool, _ string, _ error) (agent.HookAction, error) {
			baseCalled = true
			return agent.HookActionContinue, nil
		},
	}

	wrapped := WrapHooks(base, gate)
	tool := &fakeEinoTool{name: "web_search"}

	_, _ = wrapped.PostToolCall(context.Background(), tool, "result", errors.New("none"))

	if !baseCalled {
		t.Errorf("base.PostToolCall must be called via wrapper")
	}
}

func TestWrap_PostToolCall_NoBaseHook(t *testing.T) {
	gate := NewPermissionGate()
	defer gate.Close()

	wrapped := WrapHooks(nil, gate)
	tool := &fakeEinoTool{name: "web_search"}
	action, err := wrapped.PostToolCall(context.Background(), tool, "ok", nil)
	if err != nil || action != agent.HookActionContinue {
		t.Errorf("nil base PostToolCall should return Continue, no err; got %d %v", action, err)
	}
}

func TestWrap_RegistryTranspose(t *testing.T) {
	reg := agent.NewHookActionRegistry()
	base := &agent.RunHooks{Registry: reg}
	wrapped := WrapHooks(base, NewPermissionGate())
	if wrapped.Registry != reg {
		t.Errorf("wrapper Registry not equal to base Registry")
	}

	wrappedNilBase := WrapHooks(nil, NewPermissionGate())
	if wrappedNilBase.Registry != nil {
		t.Errorf("wrapper with nil base should have nil Registry, got %v", wrappedNilBase.Registry)
	}
}

func TestWrap_PreToolCall_UnknownBehavior_FailOpen(t *testing.T) {
	gate := NewPermissionGate(
		WithValidators(&stubValidator{
			id: "Weird",
			result: PermissionResult{
				Behavior:    "weird-unknown",
				ValidatorID: "Weird",
			},
		}),
	)
	defer gate.Close()

	baseCalled := false
	base := &agent.RunHooks{
		PreToolCall: func(_ context.Context, _ einotool.BaseTool, _ string) (agent.HookAction, error) {
			baseCalled = true
			return agent.HookActionContinue, nil
		},
	}

	wrapped := WrapHooks(base, gate)
	tool := &fakeEinoTool{name: "web_search"}
	ctx := ctxWithAll(nil, 0, 0, map[string]agent.FullTool{"web_search": &fakeFullTool{name: "web_search"}})

	_, _ = wrapped.PreToolCall(ctx, tool, `{}`)
	if !baseCalled {
		t.Errorf("unknown behavior should fail-open to base.PreToolCall")
	}
}

// TestWrap_ConcurrentInvocation_RaceSafe verifies wrapper is reusable across goroutines.
func TestWrap_ConcurrentInvocation_RaceSafe(t *testing.T) {
	gate := NewPermissionGate(
		WithValidators(&stubValidator{id: "Allow", result: Allow("Allow", DecisionReasonOther, "")}),
		WithAuditChannelSize(2048),
	)
	defer gate.Close()

	base := &agent.RunHooks{
		PreToolCall: func(_ context.Context, _ einotool.BaseTool, _ string) (agent.HookAction, error) {
			return agent.HookActionContinue, nil
		},
		Registry: agent.NewHookActionRegistry(),
	}

	wrapped := WrapHooks(base, gate)
	tool := &fakeEinoTool{name: "web_search"}
	ctx := ctxWithAll(nil, 0, 0, map[string]agent.FullTool{"web_search": &fakeFullTool{name: "web_search"}})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_, _ = wrapped.PreToolCall(ctx, tool, `{}`)
			}
		}()
	}
	wg.Wait()
}

// TestWrapHooks_PreservesNarrationFields verifies that NarrationProvider and
// NarrationRunID set on the base hooks survive through permission.WrapHooks.
// #12 agent-mode-billing-integration P1-2 fix — without this, the budget
// wrapper (when chained `permission(budget(sandbox))`) would lose narration
// fields at the outermost layer.
func TestWrapHooks_PreservesNarrationFields(t *testing.T) {
	reg := agent.NewHookActionRegistry()
	base := &agent.RunHooks{
		Registry:       reg,
		NarrationRunID: 42424242,
		// NarrationProvider intentionally nil — verifying RunID copy is sufficient
		// (Provider is *narration.Provider, same field-copy logic).
	}
	gate := NewPermissionGate()
	defer gate.Close()
	wrapped := WrapHooks(base, gate)
	if wrapped.NarrationRunID != 42424242 {
		t.Errorf("NarrationRunID lost: want 42424242 got %d", wrapped.NarrationRunID)
	}
	if wrapped.Registry != reg {
		t.Error("Registry lost through wrap")
	}
}

func TestWrapHooks_PreservesNarrationFields_NilBase(t *testing.T) {
	gate := NewPermissionGate()
	defer gate.Close()
	wrapped := WrapHooks(nil, gate)
	if wrapped.NarrationRunID != 0 {
		t.Errorf("nil base should yield RunID=0, got %d", wrapped.NarrationRunID)
	}
	if wrapped.NarrationProvider != nil {
		t.Error("nil base should yield NarrationProvider nil")
	}
}
