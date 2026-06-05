package agent

import (
	"context"
	"strings"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// denyHooks builds a RunHooks whose PreToolCall always returns PermissionDeny and (like
// the real permission/compliance wrappers) pre-records PermissionDeny into the registry
// AND populates the SoftDenyController's pending reason via SetPending. This lets the
// adapter test exercise the exact production registry-write ordering (R6-A).
func denyHooks(reg *HookActionRegistry, reason string) *RunHooks {
	return &RunHooks{
		PreToolCall: func(ctx context.Context, _ einotool.BaseTool, _ string) (HookAction, error) {
			if sd := SoftDenyFromCtx(ctx); sd != nil {
				sd.SetPending(&PermissionDenialDetail{ToolName: "bash_exec", Message: reason})
			}
			// mimic wrap_hooks.go:42 / compliancegate pre-record of the deny
			reg.Record(HookActionPermissionDeny)
			return HookActionPermissionDeny, nil
		},
		Registry: reg,
	}
}

// TestAdapter_SoftDeny_ContinuesAndKeepsRegistryClean: with an enabled controller, a
// permission deny returns a (tool-result, nil) carrying the reason AND leaves the
// registry at Continue (so the run-end applyHookOverride does not mis-terminate). This
// is the core R6-A registry-hygiene contract.
func TestAdapter_SoftDeny_ContinuesAndKeepsRegistryClean(t *testing.T) {
	ft := &fakeFullTool{name: "bash_exec", out: []byte(`should-not-run`)}
	reg := NewHookActionRegistry()
	ctrl := NewSoftDenyController(SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 10, MaxLifetime: 10})
	ctx := WithSoftDenyController(context.Background(), ctrl)
	eino := adaptFullToEinoTool(ft, denyHooks(reg, "命令含毁灭性 rm -rf /"))

	out, err := eino.InvokableRun(ctx, `{"command":"rm -rf /"}`)
	require.NoError(t, err, "soft deny must NOT return an error (loop continues)")
	assert.Contains(t, out, "被平台安全策略拦截", "soft result must carry the preamble")
	assert.Contains(t, out, "rm -rf /", "soft result must carry the reason")
	assert.Equal(t, HookActionContinue, reg.LastAction(),
		"soft deny MUST leave registry at Continue (overwriting the wrapper's pre-recorded PermissionDeny)")
}

// TestAdapter_SoftDeny_TripsAfterMaxSame: repeating the SAME blocked (tool+input) until
// the anti-loop guard trips returns an error AND records PermissionDeny (so the existing
// override drives TerminalPermissionDenied).
func TestAdapter_SoftDeny_TripsAfterMaxSame(t *testing.T) {
	ft := &fakeFullTool{name: "bash_exec", out: []byte(`x`)}
	reg := NewHookActionRegistry()
	ctrl := NewSoftDenyController(SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 100, MaxLifetime: 100})
	ctx := WithSoftDenyController(context.Background(), ctrl)
	eino := adaptFullToEinoTool(ft, denyHooks(reg, "blocked"))

	input := `{"command":"rm -rf /"}`
	for i := 1; i <= 2; i++ {
		_, err := eino.InvokableRun(ctx, input)
		require.NoError(t, err, "deny #%d should be soft", i)
	}
	_, err := eino.InvokableRun(ctx, input)
	require.Error(t, err, "deny #3 (MaxSame=3) should hard-terminate")
	assert.Contains(t, err.Error(), "stopped by hook")
	assert.Equal(t, HookActionPermissionDeny, reg.LastAction(),
		"tripped deny must record PermissionDeny so the override → TerminalPermissionDenied")
}

// TestAdapter_SoftDeny_OnSuccessResetsStreak: a successful tool call between denials
// resets the consecutive streak, proving the adapter calls OnSuccess on the success path.
func TestAdapter_SoftDeny_OnSuccessResetsStreak(t *testing.T) {
	reg := NewHookActionRegistry()
	ctrl := NewSoftDenyController(SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 100, MaxLifetime: 100})
	ctx := WithSoftDenyController(context.Background(), ctrl)
	input := `{"command":"rm -rf /"}`

	// Two denies (sameStreak 1,2 — both soft).
	denyTool := adaptFullToEinoTool(&fakeFullTool{name: "bash_exec", out: []byte(`x`)}, denyHooks(reg, "blocked"))
	for i := 1; i <= 2; i++ {
		_, err := denyTool.InvokableRun(ctx, input)
		require.NoError(t, err)
	}
	// A successful tool call (Continue hook) → adapter must call OnSuccess → streak reset.
	okHooks := &RunHooks{
		PreToolCall: func(context.Context, einotool.BaseTool, string) (HookAction, error) { return HookActionContinue, nil },
		Registry:    reg,
	}
	okTool := adaptFullToEinoTool(&fakeFullTool{name: "bash_exec", out: []byte(`done`)}, okHooks)
	_, err := okTool.InvokableRun(ctx, `{"command":"ls"}`)
	require.NoError(t, err)

	// Back to denies: with the streak reset, the NEXT same-fp deny is sameStreak=1 (soft),
	// not sameStreak=3 (which would trip). Assert it is soft.
	_, err = denyTool.InvokableRun(ctx, input)
	require.NoError(t, err, "after a successful call the streak must reset; this deny should be soft, not tripped")
}

// TestAdapter_SoftDeny_DisabledHardTerminates: enabled=false controller → legacy
// behavior (first deny hard-terminates).
func TestAdapter_SoftDeny_DisabledHardTerminates(t *testing.T) {
	ft := &fakeFullTool{name: "bash_exec", out: []byte(`x`)}
	reg := NewHookActionRegistry()
	ctrl := NewSoftDenyController(SoftDenyConfig{Enabled: false})
	ctx := WithSoftDenyController(context.Background(), ctrl)
	eino := adaptFullToEinoTool(ft, denyHooks(reg, "blocked"))

	_, err := eino.InvokableRun(ctx, `{"command":"rm -rf /"}`)
	require.Error(t, err, "disabled soft interception must hard-terminate on the first deny")
	assert.Equal(t, HookActionPermissionDeny, reg.LastAction())
}

// TestAdapter_SoftDeny_NoControllerInCtx_HardTerminates: when the runner did NOT inject
// a controller (SoftDenyFromCtx == nil) the adapter falls back to hard-terminate — pins
// that both runner paths must inject (reviewer R2-D).
func TestAdapter_SoftDeny_NoControllerInCtx_HardTerminates(t *testing.T) {
	ft := &fakeFullTool{name: "bash_exec", out: []byte(`x`)}
	reg := NewHookActionRegistry()
	eino := adaptFullToEinoTool(ft, denyHooks(reg, "blocked"))

	// no WithSoftDenyController on the ctx
	_, err := eino.InvokableRun(context.Background(), `{"command":"rm -rf /"}`)
	require.Error(t, err, "missing controller must hard-terminate (legacy behavior)")
	assert.Equal(t, HookActionPermissionDeny, reg.LastAction())
}

// TestAdapter_SoftDeny_OtherHardStopsUnaffected: Stop/BlockingStop/BudgetExceeded are NOT
// soft — they still hard-terminate even with an enabled controller (soft interception is
// scoped to PermissionDeny only).
func TestAdapter_SoftDeny_OtherHardStopsUnaffected(t *testing.T) {
	for _, action := range []HookAction{HookActionStop, HookActionBlockingStop, HookActionBudgetExceeded} {
		ft := &fakeFullTool{name: "bash_exec", out: []byte(`x`)}
		reg := NewHookActionRegistry()
		ctrl := NewSoftDenyController(SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 10, MaxLifetime: 10})
		ctx := WithSoftDenyController(context.Background(), ctrl)
		act := action
		hooks := &RunHooks{
			PreToolCall: func(context.Context, einotool.BaseTool, string) (HookAction, error) { return act, nil },
			Registry:    reg,
		}
		eino := adaptFullToEinoTool(ft, hooks)
		_, err := eino.InvokableRun(ctx, `{}`)
		require.Error(t, err, "hard stop action=%d must terminate even with soft interception enabled", act)
		assert.Equal(t, act, reg.LastAction(), "hard stop must record its own action")
	}
}

// sanity: the soft message escalates on repeated same-fp attempts.
func TestAdapter_SoftDeny_EscalatesWording(t *testing.T) {
	ft := &fakeFullTool{name: "bash_exec", out: []byte(`x`)}
	reg := NewHookActionRegistry()
	ctrl := NewSoftDenyController(SoftDenyConfig{Enabled: true, MaxSame: 5, MaxTotal: 100, MaxLifetime: 100})
	ctx := WithSoftDenyController(context.Background(), ctrl)
	eino := adaptFullToEinoTool(ft, denyHooks(reg, "blocked"))
	input := `{"command":"rm -rf /"}`

	out1, _ := eino.InvokableRun(ctx, input)
	assert.False(t, strings.Contains(out1, "已拦截"), "first attempt should not escalate")
	out2, _ := eino.InvokableRun(ctx, input)
	assert.True(t, strings.Contains(out2, "请立即停止重试"), "second same-fp attempt should escalate")
}

// denyHooksNoSetPending denies WITHOUT populating the controller's pending reason
// (a deny source that fired before SetPending, or didn't set it). The adapter must still
// soft-intercept with a generic message and not panic (plan T3 RED 1(b)).
func denyHooksNoSetPending(reg *HookActionRegistry) *RunHooks {
	return &RunHooks{
		PreToolCall: func(_ context.Context, _ einotool.BaseTool, _ string) (HookAction, error) {
			reg.Record(HookActionPermissionDeny)
			return HookActionPermissionDeny, nil
		},
		Registry: reg,
	}
}

// TestAdapter_SoftDeny_NilPendingFallback: a deny with no SetPending must still produce a
// generic (non-empty) soft message, not panic, and keep the registry at Continue.
func TestAdapter_SoftDeny_NilPendingFallback(t *testing.T) {
	ft := &fakeFullTool{name: "bash_exec", out: []byte(`x`)}
	reg := NewHookActionRegistry()
	ctrl := NewSoftDenyController(SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 10, MaxLifetime: 10})
	ctx := WithSoftDenyController(context.Background(), ctrl)
	eino := adaptFullToEinoTool(ft, denyHooksNoSetPending(reg))

	require.NotPanics(t, func() {
		out, err := eino.InvokableRun(ctx, `{"command":"rm -rf /"}`)
		require.NoError(t, err)
		assert.Contains(t, out, "被平台安全策略拦截", "nil-pending must still yield the generic soft message")
	})
	assert.Equal(t, HookActionContinue, reg.LastAction())
}

// TestAdapter_SoftDeny_ComplianceSourceReason: a compliance-origin deny (SetPending with a
// compliance reason) must surface that reason to the LLM in the soft message.
func TestAdapter_SoftDeny_ComplianceSourceReason(t *testing.T) {
	ft := &fakeFullTool{name: "web_search", out: []byte(`x`)}
	reg := NewHookActionRegistry()
	ctrl := NewSoftDenyController(SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 10, MaxLifetime: 10})
	ctx := WithSoftDenyController(context.Background(), ctrl)
	hooks := &RunHooks{
		PreToolCall: func(c context.Context, _ einotool.BaseTool, _ string) (HookAction, error) {
			if sd := SoftDenyFromCtx(c); sd != nil {
				sd.SetPending(&PermissionDenialDetail{ToolName: "web_search", ValidatorID: "compliance", Message: "违禁话题被合规策略拦截"})
			}
			reg.Record(HookActionPermissionDeny)
			return HookActionPermissionDeny, nil
		},
		Registry: reg,
	}
	eino := adaptFullToEinoTool(ft, hooks)
	out, err := eino.InvokableRun(ctx, `{"query":"x"}`)
	require.NoError(t, err)
	assert.Contains(t, out, "违禁话题被合规策略拦截", "compliance-source reason must reach the LLM")
}
