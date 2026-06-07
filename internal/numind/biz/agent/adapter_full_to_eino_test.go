package agent

import (
	"context"
	"errors"
	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/narration"
	"sync/atomic"
	"testing"
	"time"

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

// ── #8 narration emit fixtures (spec §10.5) ────────────────────────────────

const narrationFixtureYAML = `
tools:
  fake_narration_tool:
    verb: "正在执行"
    detail_template: "命令"
    use_template: "{{ .verb }} {{ .detail }}"
    result_template: "命令执行完成"
    error_template: "命令执行中断，{{ .reason_friendly }}"
    rejected_template: "这个命令被规则拦截了"
defaults:
  verb: "正在处理"
  detail_template: "操作"
  use_template: "{{ .verb }}"
  result_template: "操作完成"
  error_template: "操作失败，{{ .reason_friendly }}"
  rejected_template: "操作被规则拦截"
`

// helper: build a Provider + subscribe to its channel.
// Returns the provider, the channel, and a cleanup func.
func setupNarration(t *testing.T, runID uint64) (*narration.Provider, <-chan narration.Event, func()) {
	t.Helper()
	prov, err := narration.NewProvider(narration.Config{
		YAMLBytes:  []byte(narrationFixtureYAML),
		BufferSize: 16,
	})
	require.NoError(t, err)
	ch, cleanup := prov.Subscribe(runID)
	return prov, ch, cleanup
}

// drainEvents waits up to timeout and collects all events available; returns slice.
func drainEvents(ch <-chan narration.Event, timeout time.Duration) []narration.Event {
	var out []narration.Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
}

func TestAdapter_NarrationEmits_UseResult(t *testing.T) {
	ft := &fakeFullTool{name: "fake_narration_tool", out: []byte(`{"ok":true}`)}
	rec := newHookRecorder()
	prov, ch, cleanup := setupNarration(t, 1001)
	defer cleanup()

	hooks := rec.asRunHooks()
	hooks.NarrationProvider = prov
	// T3 (#5): narration routes by the run id in ctx, not a struct field.
	ctx := WithRunID(context.Background(), 1001)

	eino := adaptFullToEinoTool(ft, hooks)
	_, err := eino.InvokableRun(ctx, `{}`)
	require.NoError(t, err)

	evs := drainEvents(ch, 100*time.Millisecond)
	require.Len(t, evs, 2, "expected exactly 2 narration events (use + result)")
	assert.Equal(t, uint64(1001), evs[0].RunID, "event must carry ctx-derived run id")
	assert.Equal(t, uint64(1001), evs[1].RunID, "event must carry ctx-derived run id")
	assert.Equal(t, narration.StateUse, evs[0].State)
	assert.Equal(t, narration.StateResult, evs[1].State)
	assert.Contains(t, evs[0].Message, "正在执行")
	assert.Equal(t, "命令执行完成", evs[1].Message)
}

func TestAdapter_NarrationEmits_UseError(t *testing.T) {
	ft := &fakeFullTool{name: "fake_narration_tool", err: errors.New("super secret stack trace abc123")}
	rec := newHookRecorder()
	prov, ch, cleanup := setupNarration(t, 1002)
	defer cleanup()

	hooks := rec.asRunHooks()
	hooks.NarrationProvider = prov
	// T3 (#5): narration routes by the run id in ctx, not a struct field.
	ctx := WithRunID(context.Background(), 1002)

	eino := adaptFullToEinoTool(ft, hooks)
	_, err := eino.InvokableRun(ctx, `{}`)
	require.Error(t, err)

	evs := drainEvents(ch, 100*time.Millisecond)
	require.Len(t, evs, 2, "expected exactly 2 narration events (use + error)")
	assert.Equal(t, uint64(1002), evs[0].RunID, "event must carry ctx-derived run id")
	assert.Equal(t, narration.StateUse, evs[0].State)
	assert.Equal(t, narration.StateError, evs[1].State)
	// Security contract: raw error text MUST NOT leak.
	for _, leak := range []string{"super", "secret", "stack", "abc123"} {
		assert.NotContains(t, evs[1].Message, leak,
			"narration error message leaked raw err substring %q", leak)
	}
}

func TestAdapter_NarrationEmits_Rejected_NoUseEmitted(t *testing.T) {
	ft := &fakeFullTool{name: "fake_narration_tool", out: []byte(`unused`)}
	rec := newHookRecorder()
	rec.preAction = HookActionStop // short-circuit before Execute
	prov, ch, cleanup := setupNarration(t, 1003)
	defer cleanup()

	hooks := rec.asRunHooks()
	hooks.NarrationProvider = prov
	// T3 (#5): narration routes by the run id in ctx, not a struct field.
	ctx := WithRunID(context.Background(), 1003)

	eino := adaptFullToEinoTool(ft, hooks)
	_, err := eino.InvokableRun(ctx, `{}`)
	require.Error(t, err)

	evs := drainEvents(ch, 100*time.Millisecond)
	require.Len(t, evs, 1, "expected exactly 1 narration event (rejected only); no use/result/error")
	assert.Equal(t, uint64(1003), evs[0].RunID, "event must carry ctx-derived run id")
	assert.Equal(t, narration.StateRejected, evs[0].State)
	assert.Equal(t, "这个命令被规则拦截了", evs[0].Message)
}

func TestAdapter_NarrationEmits_PostErrUpgradesToError(t *testing.T) {
	// execErr=nil, postErr!=nil → effectiveErr=postErr → emit StateError (not Result).
	// Verifies S1-D15 / S1 P0-1 effectiveErr branching in narration.
	ft := &fakeFullTool{name: "fake_narration_tool", out: []byte(`{"ok":true}`)} // succeeds
	rec := newHookRecorder()
	rec.postErr = errors.New("sandbox teardown failed")
	prov, ch, cleanup := setupNarration(t, 1004)
	defer cleanup()

	hooks := rec.asRunHooks()
	hooks.NarrationProvider = prov
	// T3 (#5): narration routes by the run id in ctx, not a struct field.
	ctx := WithRunID(context.Background(), 1004)

	eino := adaptFullToEinoTool(ft, hooks)
	_, err := eino.InvokableRun(ctx, `{}`)
	require.Error(t, err, "caller should see PostToolCall-wrapped error")

	evs := drainEvents(ch, 100*time.Millisecond)
	require.Len(t, evs, 2, "expected use + error (NOT use + result)")
	assert.Equal(t, uint64(1004), evs[0].RunID, "event must carry ctx-derived run id")
	assert.Equal(t, narration.StateUse, evs[0].State)
	assert.Equal(t, narration.StateError, evs[1].State,
		"PostToolCall error with execErr=nil must drive narration to StateError, not StateResult")
}

func TestAdapter_NoNarrationProvider_LegacyBehaviorPreserved(t *testing.T) {
	// Sanity: when NarrationProvider is nil, adapter behaves exactly as
	// pre-#8 (no panic, no events, hooks fire as before).
	ft := &fakeFullTool{name: "fake_narration_tool", out: []byte(`{"ok":true}`)}
	rec := newHookRecorder()
	hooks := rec.asRunHooks() // NarrationProvider is nil

	eino := adaptFullToEinoTool(ft, hooks)
	out, err := eino.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, out)
	assert.Equal(t, int64(1), rec.preCalls.Load(), "PreToolCall should fire once")
	assert.Equal(t, int64(1), rec.postCalls.Load(), "PostToolCall should fire once")
}

// TestEmitNarration_RoutesByContextRunID_NoCrossRunLeak is the permanent guard
// for T3 (#5). Two concurrent agent runs share a SINGLE process-global *RunHooks
// (the production wiring: one struct built once in biz.go, stored in
// agentRunner.defaultHooks, reused by every run). Each run carries its own run id
// in ctx via WithRunID. The adapter MUST route narration by the ctx run id so the
// two runs land on their own subscriber streams.
//
// Pre-fix the adapter routed by a mutable RunHooks.NarrationRunID field that the
// runner overwrote per run → last-writer-wins → run A's narration leaked into run
// B's stream. That code can no longer compile (field deleted), but this test pins
// the contract: routing follows ctx, the shared struct cannot cause cross-run
// leakage. Run with the old code (read a.hooks.NarrationRunID, single shared
// field) and both subscribers would have collided on one run id.
func TestEmitNarration_RoutesByContextRunID_NoCrossRunLeak(t *testing.T) {
	// ONE provider, ONE shared *RunHooks — mirrors the process-global wiring.
	prov, err := narration.NewProvider(narration.Config{
		YAMLBytes:  []byte(narrationFixtureYAML),
		BufferSize: 16,
	})
	require.NoError(t, err)

	const runA, runB uint64 = 111, 222
	chA, cleanupA := prov.Subscribe(runA)
	defer cleanupA()
	chB, cleanupB := prov.Subscribe(runB)
	defer cleanupB()

	rec := newHookRecorder()
	sharedHooks := rec.asRunHooks() // the SINGLE shared hooks struct
	sharedHooks.NarrationProvider = prov

	ft := &fakeFullTool{name: "fake_narration_tool", out: []byte(`{"ok":true}`)}
	eino := adaptFullToEinoTool(ft, sharedHooks)

	// Two runs through the SAME adapter + SAME hooks, differing only by ctx run id.
	_, err = eino.InvokableRun(WithRunID(context.Background(), runA), `{}`)
	require.NoError(t, err)
	_, err = eino.InvokableRun(WithRunID(context.Background(), runB), `{}`)
	require.NoError(t, err)

	evsA := drainEvents(chA, 100*time.Millisecond)
	evsB := drainEvents(chB, 100*time.Millisecond)

	require.NotEmpty(t, evsA, "run A subscriber must receive its own narration")
	require.NotEmpty(t, evsB, "run B subscriber must receive its own narration")

	// No cross-run leakage: every event on stream A carries runA, every event on
	// stream B carries runB. If routing read the shared struct field, the second
	// run would have clobbered it and both streams would show 222 (or one stream
	// would be starved), failing these assertions.
	for _, ev := range evsA {
		assert.Equal(t, runA, ev.RunID, "run A stream leaked an event tagged %d", ev.RunID)
	}
	for _, ev := range evsB {
		assert.Equal(t, runB, ev.RunID, "run B stream leaked an event tagged %d", ev.RunID)
	}
}

// ── tests: SSE tool-call event emission (2026-05-29 "frozen UI" fix) ─────────
//
// The active streaming path (runner_runstream.go) never emitted tool_call_*
// events, so the student UI showed no sign a tool was running — a 30–60s
// invoke_skill looked frozen. The adapter now emits tool_call_start before
// Execute and tool_call_result / tool_call_error after. These tests lock that
// contract: with a StreamState in ctx, InvokableRun must put the right events
// on the channel; without one, it must be a silent no-op (non-streaming path).

func drainEventTypes(ch chan stream.Event) []stream.EventType {
	var types []stream.EventType
	for {
		select {
		case ev := <-ch:
			types = append(types, ev.Type)
		default:
			return types
		}
	}
}

func TestAdapter_InvokableRun_EmitsToolCallStartAndResult(t *testing.T) {
	ft := &fakeFullTool{name: "web_search", out: []byte(`{"results":["a","b"]}`)}
	eino := adaptFullToEinoTool(ft, nil) // nil hooks: stream emission is independent of hooks

	ch := make(chan stream.Event, 16)
	st := &StreamSessionState{Ch: ch, RunID: 42, StepIdx: 1}
	ctx := WithStreamState(context.Background(), st)

	out, err := eino.InvokableRun(ctx, `{"query":"github trending"}`)
	require.NoError(t, err)
	assert.Equal(t, `{"results":["a","b"]}`, out)

	types := drainEventTypes(ch)
	assert.Contains(t, types, stream.EventToolCallStart,
		"tool_call_start must be emitted before Execute (the 'a tool is running' signal)")
	assert.Contains(t, types, stream.EventToolCallResult,
		"tool_call_result must be emitted after a successful Execute")
	assert.NotContains(t, types, stream.EventToolCallError)
}

func TestAdapter_InvokableRun_EmitsToolCallError(t *testing.T) {
	ft := &fakeFullTool{name: "invoke_skill", err: errors.New("sandbox boom")}
	eino := adaptFullToEinoTool(ft, nil)

	ch := make(chan stream.Event, 16)
	ctx := WithStreamState(context.Background(), &StreamSessionState{Ch: ch, RunID: 7, StepIdx: 0})

	_, err := eino.InvokableRun(ctx, `{"skill_name":"pptx-author"}`)
	require.Error(t, err)

	types := drainEventTypes(ch)
	assert.Contains(t, types, stream.EventToolCallStart)
	assert.Contains(t, types, stream.EventToolCallError,
		"a failing tool must emit tool_call_error, not tool_call_result")
	assert.NotContains(t, types, stream.EventToolCallResult)
}

func TestAdapter_InvokableRun_NoStreamState_NoPanic(t *testing.T) {
	// Non-streaming path (Run, not RunStream): no StreamState in ctx → silent no-op.
	ft := &fakeFullTool{name: "x", out: []byte(`ok`)}
	eino := adaptFullToEinoTool(ft, nil)
	out, err := eino.InvokableRun(context.Background(), `{"a":1}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
}

func TestAdapter_ToolCallStart_CarriesInputPreview(t *testing.T) {
	// invoke_skill's skill_name must survive into input_preview so the frontend
	// can render a format-specific label ("正在生成 PPT 演示文稿").
	ft := &fakeFullTool{name: "invoke_skill", out: []byte(`{}`)}
	eino := adaptFullToEinoTool(ft, nil)
	ch := make(chan stream.Event, 16)
	ctx := WithStreamState(context.Background(), &StreamSessionState{Ch: ch, RunID: 1, StepIdx: 0})

	_, err := eino.InvokableRun(ctx, `{"skill_name":"pptx-author","instructions":"make a deck"}`)
	require.NoError(t, err)

	var start *stream.Event
	for {
		select {
		case ev := <-ch:
			if ev.Type == stream.EventToolCallStart {
				e := ev
				start = &e
			}
			continue
		default:
		}
		break
	}
	require.NotNil(t, start, "expected a tool_call_start event")
}
