package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	einotool "github.com/cloudwego/eino/components/tool"
)

// ── fake FullTool for adapter tests ─────────────────────────────────────────

type fakeFullTool struct {
	BaseTool
	name string
	desc string
	out  []byte
	err  error
}

func (f *fakeFullTool) Name() string           { return f.name }
func (f *fakeFullTool) Description() string    { return f.desc }
func (f *fakeFullTool) UserFacingName() string { return f.name }
func (f *fakeFullTool) NarrationVerb() string  { return "执行" }

func (f *fakeFullTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return ToolResult(f.out), nil
}

// ── tests: nil hooks back-compat ─────────────────────────────────────────────

func TestAdaptFullToEinoTool_Info(t *testing.T) {
	ft := &fakeFullTool{name: "echo", desc: "echoes input"}
	eino := adaptFullToEinoTool(ft, nil)
	info, err := eino.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "echo", info.Name)
	assert.Equal(t, "echoes input", info.Desc)
}

func TestAdaptFullToEinoTool_NilHooks_InvokableRun_Success(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`{"ok":true}`)}
	eino := adaptFullToEinoTool(ft, nil)
	out, err := eino.InvokableRun(context.Background(), `{"input":"hi"}`)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, out)
}

func TestAdaptFullToEinoTool_NilHooks_InvokableRun_Error(t *testing.T) {
	ft := &fakeFullTool{name: "x", err: errors.New("boom")}
	eino := adaptFullToEinoTool(ft, nil)
	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.EqualError(t, err, "boom")
}

func TestAdaptFullToEinoTool_NilHooks_InvokableRun_EmptyResult(t *testing.T) {
	ft := &fakeFullTool{name: "empty", out: []byte(nil)}
	eino := adaptFullToEinoTool(ft, nil)
	out, err := eino.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, "", out)
}

// ── tests: non-nil hooks ─────────────────────────────────────────────────────

type hookRecorder struct {
	preCalls   atomic.Int64
	postCalls  atomic.Int64
	preAction  HookAction
	preErr     error
	postAction HookAction
	postErr    error
	lastOutput string
	lastErr    error
}

func (h *hookRecorder) preToolCall(_ context.Context, _ einotool.BaseTool, _ string) (HookAction, error) {
	h.preCalls.Add(1)
	return h.preAction, h.preErr
}

func (h *hookRecorder) postToolCall(_ context.Context, _ einotool.BaseTool, output string, execErr error) (HookAction, error) {
	h.postCalls.Add(1)
	h.lastOutput = output
	h.lastErr = execErr
	return h.postAction, h.postErr
}

func newHookRecorder() *hookRecorder {
	return &hookRecorder{
		preAction:  HookActionContinue,
		postAction: HookActionContinue,
	}
}

func (h *hookRecorder) asRunHooks() *RunHooks {
	return &RunHooks{
		PreToolCall:  h.preToolCall,
		PostToolCall: h.postToolCall,
	}
}

func TestAdaptFullToEinoTool_Hooks_HappyPath(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`{"ok":true}`)}
	rec := newHookRecorder()
	eino := adaptFullToEinoTool(ft, rec.asRunHooks())

	out, err := eino.InvokableRun(context.Background(), `{"input":"hi"}`)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, out)
	assert.Equal(t, int64(1), rec.preCalls.Load(), "PreToolCall must fire once")
	assert.Equal(t, int64(1), rec.postCalls.Load(), "PostToolCall must fire once")
	assert.Equal(t, `{"ok":true}`, rec.lastOutput, "PostToolCall sees output")
	assert.Nil(t, rec.lastErr, "PostToolCall sees no execErr on success")
}

func TestAdaptFullToEinoTool_Hooks_PreStopShortCircuits(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`{"ok":true}`)}
	rec := newHookRecorder()
	rec.preAction = HookActionStop
	eino := adaptFullToEinoTool(ft, rec.asRunHooks())

	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped by hook")
	assert.Equal(t, int64(1), rec.preCalls.Load())
	// PostToolCall MUST NOT fire when Pre short-circuits
	assert.Equal(t, int64(0), rec.postCalls.Load(), "Post should not fire on Pre stop")
}

func TestAdaptFullToEinoTool_Hooks_PreError(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`{"ok":true}`)}
	rec := newHookRecorder()
	rec.preErr = errors.New("synthetic pre err")
	eino := adaptFullToEinoTool(ft, rec.asRunHooks())

	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PreToolCall")
	assert.Equal(t, int64(0), rec.postCalls.Load(), "Post should not fire on Pre error")
}

func TestAdaptFullToEinoTool_Hooks_PostFiresOnExecError(t *testing.T) {
	ft := &fakeFullTool{name: "x", err: errors.New("exec boom")}
	rec := newHookRecorder()
	eino := adaptFullToEinoTool(ft, rec.asRunHooks())

	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.EqualError(t, err, "exec boom")
	assert.Equal(t, int64(1), rec.preCalls.Load())
	assert.Equal(t, int64(1), rec.postCalls.Load(), "Post must fire even on exec error (cleanup)")
	assert.NotNil(t, rec.lastErr, "Post should see the execErr")
}

func TestAdaptFullToEinoTool_Hooks_PostErrSurfacedWhenNoExecErr(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`ok`)}
	rec := newHookRecorder()
	rec.postErr = errors.New("post boom")
	eino := adaptFullToEinoTool(ft, rec.asRunHooks())

	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PostToolCall")
	assert.Contains(t, err.Error(), "post boom")
}

func TestAdaptFullToEinoTool_Hooks_PostErrShadowedByExecErr(t *testing.T) {
	// If both execErr and postErr exist, execErr wins (Post is just logged).
	ft := &fakeFullTool{name: "x", err: errors.New("exec boom")}
	rec := newHookRecorder()
	rec.postErr = errors.New("post boom")
	eino := adaptFullToEinoTool(ft, rec.asRunHooks())

	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.EqualError(t, err, "exec boom", "execErr takes priority over postErr")
}

// ── M10: Registry recording tests ────────────────────────────────────────────

// asRunHooksWithRegistry returns a *RunHooks wired to the given registry.
func (h *hookRecorder) asRunHooksWithRegistry(reg *HookActionRegistry) *RunHooks {
	rh := h.asRunHooks()
	rh.Registry = reg
	return rh
}

func TestAdapter_PreToolCallStop_recordsToRegistry(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`ok`)}
	rec := newHookRecorder()
	rec.preAction = HookActionStop
	reg := NewHookActionRegistry()
	eino := adaptFullToEinoTool(ft, rec.asRunHooksWithRegistry(reg))

	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped by hook")
	assert.Equal(t, HookActionStop, reg.LastAction(), "Stop should be recorded in registry")
}

func TestAdapter_PreToolCallContinue_doesNotRecord(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`ok`)}
	rec := newHookRecorder()
	rec.preAction = HookActionContinue // default
	reg := NewHookActionRegistry()
	eino := adaptFullToEinoTool(ft, rec.asRunHooksWithRegistry(reg))

	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, HookActionContinue, reg.LastAction(), "Continue should leave registry at default")
}

func TestAdapter_PostToolCallStop_recordsToRegistry(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`ok`)}
	rec := newHookRecorder()
	rec.postAction = HookActionStop
	reg := NewHookActionRegistry()
	eino := adaptFullToEinoTool(ft, rec.asRunHooksWithRegistry(reg))

	_, _ = eino.InvokableRun(context.Background(), `{}`)
	assert.Equal(t, HookActionStop, reg.LastAction(), "PostToolCall Stop should be recorded in registry")
}

func TestAdapter_PostToolCallContinue_doesNotRecord(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`ok`)}
	rec := newHookRecorder()
	rec.postAction = HookActionContinue // default
	reg := NewHookActionRegistry()
	eino := adaptFullToEinoTool(ft, rec.asRunHooksWithRegistry(reg))

	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, HookActionContinue, reg.LastAction(), "Continue should leave registry at default")
}

func TestAdapter_RegistryNil_doesNotPanic(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`ok`)}
	rec := newHookRecorder()
	rec.preAction = HookActionStop
	// Registry is nil — must not panic
	hooks := rec.asRunHooks() // Registry field is nil
	eino := adaptFullToEinoTool(ft, hooks)

	require.NotPanics(t, func() {
		_, _ = eino.InvokableRun(context.Background(), `{}`)
	})
}
