package compliancegate

import (
	"context"
	"errors"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/compliance"
	"numind-server/internal/pkg/model"
)

// fakeGate implements compliance.ComplianceGate with configurable behavior.
type fakeGate struct {
	checkToolCallResult compliance.ComplianceResult
	checkToolCallErr    error
}

func (f *fakeGate) SystemPromptBlock(ctx context.Context, ad *model.AgentDefinition) (string, error) {
	return "", nil
}
func (f *fakeGate) CheckUserInput(ctx context.Context, p uint, in string) (compliance.ComplianceResult, error) {
	return compliance.ComplianceResult{}, nil
}
func (f *fakeGate) CheckLLMOutput(ctx context.Context, p uint, out string) (compliance.ComplianceResult, error) {
	return compliance.ComplianceResult{}, nil
}
func (f *fakeGate) CheckToolCall(ctx context.Context, req compliance.ComplianceRequest) (compliance.ComplianceResult, error) {
	return f.checkToolCallResult, f.checkToolCallErr
}

// fakeTool — minimal einotool.BaseTool stub
type fakeTool struct{ name string }

func (f *fakeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: f.name}, nil
}

func newPassthroughBase() *agent.RunHooks {
	return &agent.RunHooks{
		Registry: agent.NewHookActionRegistry(),
		PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
			return agent.HookActionContinue, nil
		},
	}
}

func TestWrapHooks_NilGate_ReturnsBase(t *testing.T) {
	base := newPassthroughBase()
	got := WrapHooks(base, nil)
	assert.Same(t, base, got)
}

func TestWrapHooks_Allow_ForwardsToBase(t *testing.T) {
	gate := &fakeGate{checkToolCallResult: compliance.ComplianceResult{Decision: model.DecisionAllow}}
	base := newPassthroughBase()
	wrapped := WrapHooks(base, gate)
	action, err := wrapped.PreToolCall(context.Background(), &fakeTool{name: "kb_search"}, `{}`)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)
}

func TestWrapHooks_Deny_ShortCircuits(t *testing.T) {
	gate := &fakeGate{checkToolCallResult: compliance.ComplianceResult{
		Decision:     model.DecisionDeny,
		RuleLayer:    model.RuleLayerL1,
		NarrationMsg: "blocked",
	}}
	base := newPassthroughBase()
	wrapped := WrapHooks(base, gate)
	action, err := wrapped.PreToolCall(context.Background(), &fakeTool{name: "web_search"}, `{}`)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionPermissionDeny, action)
	// Registry should record the deny
	require.NotNil(t, wrapped.Registry)
	assert.Equal(t, agent.HookActionPermissionDeny, wrapped.Registry.LastAction())
}

func TestWrapHooks_CheckError_FailOpen(t *testing.T) {
	gate := &fakeGate{checkToolCallErr: errors.New("compliance internal error")}
	base := newPassthroughBase()
	wrapped := WrapHooks(base, gate)
	action, err := wrapped.PreToolCall(context.Background(), &fakeTool{name: "kb_search"}, `{}`)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action, "fail-open: forwards to base")
}

func TestWrapHooks_PostToolCall_Forwarded(t *testing.T) {
	gate := &fakeGate{}
	postCalled := false
	base := &agent.RunHooks{
		Registry: agent.NewHookActionRegistry(),
		PostToolCall: func(ctx context.Context, t einotool.BaseTool, output string, err error) (agent.HookAction, error) {
			postCalled = true
			return agent.HookActionContinue, nil
		},
	}
	wrapped := WrapHooks(base, gate)
	_, _ = wrapped.PostToolCall(context.Background(), &fakeTool{name: "x"}, "out", nil)
	assert.True(t, postCalled, "PostToolCall should forward to base")
}

func TestWrapHooks_PreservesRegistryAndNarration(t *testing.T) {
	gate := &fakeGate{}
	reg := agent.NewHookActionRegistry()
	base := &agent.RunHooks{
		Registry:       reg,
		NarrationRunID: 999,
	}
	wrapped := WrapHooks(base, gate)
	assert.Same(t, reg, wrapped.Registry)
	assert.Equal(t, uint64(999), wrapped.NarrationRunID)
}

func TestWrapHooks_NilBase_DoesNotPanic(t *testing.T) {
	gate := &fakeGate{checkToolCallResult: compliance.ComplianceResult{Decision: model.DecisionAllow}}
	wrapped := WrapHooks(nil, gate)
	action, err := wrapped.PreToolCall(context.Background(), &fakeTool{name: "x"}, `{}`)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionContinue, action)
}

func TestWrapHooks_Deny_SinkReceivesDetail(t *testing.T) {
	gate := &fakeGate{checkToolCallResult: compliance.ComplianceResult{
		Decision:     model.DecisionDeny,
		RuleLayer:    model.RuleLayerL1,
		NarrationMsg: "forbidden brand",
	}}
	base := newPassthroughBase()
	wrapped := WrapHooks(base, gate)

	sink := make(chan *agent.PermissionDenialDetail, 1)
	ctx := agent.WithPermissionSink(context.Background(), sink)

	action, err := wrapped.PreToolCall(ctx, &fakeTool{name: "bash_exec"}, `{"cmd":"echo"}`)
	require.NoError(t, err)
	assert.Equal(t, agent.HookActionPermissionDeny, action)

	select {
	case detail := <-sink:
		require.NotNil(t, detail)
		assert.Equal(t, "bash_exec", detail.ToolName)
		assert.Equal(t, "deny", detail.Behavior)
		assert.Contains(t, detail.DecisionReason, "compliance:")
		assert.Equal(t, "compliance", detail.ValidatorID)
		assert.Equal(t, "forbidden brand", detail.Message)
	default:
		t.Fatal("expected detail on sink channel, got none")
	}
}
