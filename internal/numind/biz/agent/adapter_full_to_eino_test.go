package agent

import (
	"context"
	"encoding/json"
	"errors"
	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/narration"
	"strings"
	"sync"
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
	name     string
	desc     string
	out      []byte
	err      error
	panicVal any // when non-nil, Execute panics with it (simulates a tool bug)
}

func (f *fakeFullTool) Name() string           { return f.name }
func (f *fakeFullTool) Description() string    { return f.desc }
func (f *fakeFullTool) UserFacingName() string { return f.name }
func (f *fakeFullTool) NarrationVerb() string  { return "执行" }

func (f *fakeFullTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	if f.panicVal != nil {
		panic(f.panicVal)
	}
	if f.err != nil {
		return nil, f.err
	}
	return ToolResult(f.out), nil
}

type toolCallIDCapturingFullTool struct {
	BaseTool
	mu  sync.Mutex
	ids []string
}

func (t *toolCallIDCapturingFullTool) Name() string { return "tool_call_id_probe" }
func (t *toolCallIDCapturingFullTool) Description() string {
	return "capture adapter tool call identity"
}
func (t *toolCallIDCapturingFullTool) UserFacingName() string { return "工具调用标识探针" }
func (t *toolCallIDCapturingFullTool) NarrationVerb() string  { return "检查" }
func (t *toolCallIDCapturingFullTool) Execute(ctx context.Context, _ ToolInput) (ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ids = append(t.ids, ToolCallIDFromContext(ctx))
	return ToolResult(`{"ok":true}`), nil
}

func (t *toolCallIDCapturingFullTool) snapshotIDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.ids...)
}

func TestToolCallID_AdapterThreadsSyntheticIDAcrossExecuteNarrationAndSSE(t *testing.T) {
	const runID uint64 = 810
	provider, err := narration.NewProvider(narration.Config{
		YAMLBytes:  []byte(narrationFixtureYAML),
		BufferSize: 16,
	})
	require.NoError(t, err)
	narrationCh, cleanup := provider.Subscribe(runID)
	defer cleanup()

	probe := &toolCallIDCapturingFullTool{}
	adapter := adaptFullToEinoTool(probe, &RunHooks{NarrationProvider: provider})
	streamCh := make(chan stream.Event, 16)
	ctx := WithStreamState(WithRunID(context.Background(), runID), &StreamSessionState{Ch: streamCh, RunID: runID})

	invokeAndCollect := func() (string, []narration.Event, []stream.Event) {
		t.Helper()
		_, invokeErr := adapter.InvokableRun(ctx, `{}`)
		require.NoError(t, invokeErr)
		ids := probe.snapshotIDs()
		require.NotEmpty(t, ids)
		executeID := ids[len(ids)-1]
		require.NotEmpty(t, executeID, "adapter must inject the already-generated synthetic ID into Execute context")

		narrationEvents := drainEvents(narrationCh, 100*time.Millisecond)
		var streamEvents []stream.Event
		for {
			select {
			case event := <-streamCh:
				streamEvents = append(streamEvents, event)
			default:
				return executeID, narrationEvents, streamEvents
			}
		}
	}

	firstID, firstNarration, firstStream := invokeAndCollect()
	require.Len(t, firstNarration, 2, "successful invocation emits use and result narration")
	for _, event := range firstNarration {
		assert.Equal(t, firstID, event.ToolCallID)
	}
	require.Len(t, firstStream, 2, "successful invocation emits start and result SSE")
	for _, event := range firstStream {
		switch event.Type {
		case stream.EventToolCallStart:
			var payload stream.ToolCallStartPayload
			require.NoError(t, json.Unmarshal(event.Data, &payload))
			assert.Equal(t, firstID, payload.ToolCallID)
		case stream.EventToolCallResult:
			var payload stream.ToolCallResultPayload
			require.NoError(t, json.Unmarshal(event.Data, &payload))
			assert.Equal(t, firstID, payload.ToolCallID)
		default:
			t.Fatalf("unexpected stream event %q", event.Type)
		}
	}

	secondID, secondNarration, secondStream := invokeAndCollect()
	assert.NotEqual(t, firstID, secondID, "different invocations must receive different synthetic IDs")
	for _, event := range secondNarration {
		assert.Equal(t, secondID, event.ToolCallID)
	}
	for _, event := range secondStream {
		var payload struct {
			ToolCallID string `json:"tool_call_id"`
		}
		require.NoError(t, json.Unmarshal(event.Data, &payload))
		assert.Equal(t, secondID, payload.ToolCallID)
	}
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

// TestAdaptFullToEinoTool_ExecutePanic_BecomesSoftError reproduces the run-killer
// where a tool's Execute panics (nil-deref in a parser, image decode, etc.). The
// panic would otherwise unwind through the eino graph and crash the detached run
// goroutine — Gin's recovery middleware does not cover spawned goroutines. The
// adapter must contain it and convert it to a SOFT error so the run survives and
// the LLM sees the failure.
func TestAdaptFullToEinoTool_ExecutePanic_BecomesSoftError(t *testing.T) {
	ft := &fakeFullTool{name: "boom", panicVal: "simulated nil-map write"}
	eino := adaptFullToEinoTool(ft, nil)
	out, err := eino.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err, "a tool panic must be contained as a soft error, not propagate")
	assert.Contains(t, out, "ERROR", "panic should surface as a soft tool error payload")
	assert.Contains(t, out, "boom", "soft error should name the tool")
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

// Bug repro (customer-reported, dev 2026-06-14): on the POLLING path (an
// ask_user_question answer-resume), a single tool call's StateUse and StateResult
// narration events surfaced as TWO separate UI cards — the 'use' one stuck in
// 执行中 forever (its live timer ticked for 10+ minutes) plus a separate 已完成
// card. Root cause: emitNarration passed an EMPTY ToolCallID, so
// narration.Provider.nextCallID minted a FRESH "<runID>-<seq>" for EACH emit
// (use → "<id>-1", result → "<id>-2"). The polling UI groups by tool_call_id, so
// the two never merged. The SSE path was unaffected because it threads ONE
// synthetic toolCallID through start/result. Fix: emitNarration must thread that
// same stable toolCallID into the narration payload so all states of one call
// share an id.
func TestAdaptFullToEinoTool_Narration_UseAndResultShareToolCallID(t *testing.T) {
	prov, err := narration.NewProvider(narration.Config{YAMLBytes: []byte(`
tools:
  x:
    verb: "执行"
    use_template: "{{ .verb }}"
    result_template: "完成"
    error_template: "出错"
    rejected_template: "拦截"
defaults:
  verb: "处理"
  use_template: "{{ .verb }}"
  result_template: "完成"
  error_template: "出错"
  rejected_template: "拦截"
`)})
	require.NoError(t, err)

	ft := &fakeFullTool{name: "x", out: []byte(`{"ok":true}`)}
	eino := adaptFullToEinoTool(ft, &RunHooks{NarrationProvider: prov})
	ctx := narration.WithCollector(WithRunID(context.Background(), 999))

	_, err = eino.InvokableRun(ctx, `{"input":"hi"}`)
	require.NoError(t, err)

	var useID, resultID string
	for _, ev := range narration.CollectorFrom(ctx).Events() {
		switch ev.State {
		case narration.StateUse:
			useID = ev.ToolCallID
		case narration.StateResult:
			resultID = ev.ToolCallID
		}
	}
	require.NotEmpty(t, useID, "expected a StateUse narration event")
	require.NotEmpty(t, resultID, "expected a StateResult narration event")
	assert.Equal(t, useID, resultID,
		"a tool call's use & result narration MUST share one tool_call_id — otherwise the polling UI splits them and the 'use' card sticks in 执行中 forever (customer-reported timer-never-stops bug)")
}

// TestAdaptFullToEinoTool_Narration_SoftErrorEmitsStateError reproduces the
// customer-reported bug (dev run 169, 2026-06-18): a tool that returns a SOFT
// error — a successful ToolResult (nil Go error) whose JSON body carries the
// "ERROR: " contract (softToolError / each tool's returnSoftError), e.g. an
// image_gen timeout — was narrated as StateResult. So the UI showed a green
// "✓ 图片已生成" success badge while the model itself said "生成过程中遇到了超时".
// A soft error is a FAILURE: it must narrate StateError, never StateResult.
func TestAdaptFullToEinoTool_Narration_SoftErrorEmitsStateError(t *testing.T) {
	prov, err := narration.NewProvider(narration.Config{YAMLBytes: []byte(`
tools:
  x:
    verb: "执行"
    use_template: "{{ .verb }}"
    result_template: "完成"
    error_template: "出错"
    rejected_template: "拦截"
defaults:
  verb: "处理"
  use_template: "{{ .verb }}"
  result_template: "完成"
  error_template: "出错"
  rejected_template: "拦截"
`)})
	require.NoError(t, err)

	// Soft-error contract: nil Go error, body carries the "ERROR: " marker in the
	// dedicated "error" field (the shape image_gen / softToolError use).
	ft := &fakeFullTool{name: "x", out: []byte(`{"error":"ERROR: x: 生成过程中遇到了超时"}`)}
	eino := adaptFullToEinoTool(ft, &RunHooks{NarrationProvider: prov})
	ctx := narration.WithCollector(WithRunID(context.Background(), 999))

	_, err = eino.InvokableRun(ctx, `{"input":"hi"}`)
	require.NoError(t, err, "a soft error keeps the Go error nil so the ReAct loop continues")

	var sawResult, sawError bool
	for _, ev := range narration.CollectorFrom(ctx).Events() {
		switch ev.State {
		case narration.StateResult:
			sawResult = true
		case narration.StateError:
			sawError = true
		}
	}
	assert.False(t, sawResult,
		"a soft-error tool result MUST NOT narrate StateResult — that renders a false ✓ success badge over a failed call (customer-reported: image_gen timeout showed 图片已生成 ✓)")
	assert.True(t, sawError,
		"a soft-error tool result MUST narrate StateError so the UI shows failure (✗), matching what the model tells the user")
}

func TestAdaptFullToEinoTool_RecoverableSoftErrorEmitsProgress(t *testing.T) {
	prov, err := narration.NewProvider(narration.Config{YAMLBytes: []byte(`
tools:
  lark_execute:
    verb: "操作"
    use_template: "操作中"
    result_template: "完成"
    error_template: "出错"
defaults:
  verb: "处理"
  use_template: "处理中"
  result_template: "完成"
  error_template: "出错"
`)})
	require.NoError(t, err)
	ft := &fakeFullTool{name: "lark_execute", out: []byte(`{"error":"ERROR: command rejected","code":"command_rejected","recoverable":true,"retryable":false}`)}
	eino := adaptFullToEinoTool(ft, &RunHooks{NarrationProvider: prov})
	ch := make(chan stream.Event, 8)
	ctx := narration.WithCollector(WithRunID(context.Background(), 226))
	ctx = WithStreamState(ctx, &StreamSessionState{Ch: ch, RunID: 226})

	_, err = eino.InvokableRun(ctx, `{"argv":["drive","+search"]}`)
	require.NoError(t, err)

	var sawProgress, sawError bool
	for _, ev := range narration.CollectorFrom(ctx).Events() {
		if ev.State == narration.StateProgress {
			sawProgress = true
			assert.Equal(t, "正在调整执行方式", ev.Message)
		}
		if ev.State == narration.StateError {
			sawError = true
		}
	}
	assert.True(t, sawProgress)
	assert.False(t, sawError, "a safe correction must not be narrated as terminal failure")

	var payload stream.ToolCallErrorPayload
	for len(ch) > 0 {
		ev := <-ch
		if ev.Type == stream.EventToolCallError {
			require.NoError(t, json.Unmarshal(ev.Data, &payload))
		}
	}
	assert.True(t, payload.Recoverable)
}

func TestAdaptFullToEinoTool_LarkTerminalFailureNeverEmitsFalseSuccess(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		output string
	}{
		{
			name:   "unknown started write",
			output: `{"ok":false,"state":"unknown","operation_id":"op-unknown","failure":{"code":"feishu_unknown_result","category":"unknown_result","retryable":false,"business_started":true}}`,
		},
		{
			name:   "hard operation failure",
			output: `{"ok":false,"state":"failed","operation_id":"op-failed","failure":{"code":"feishu_operation_failed","category":"failed","retryable":false,"business_started":false}}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			prov, err := narration.NewProvider(narration.Config{YAMLBytes: []byte(narrationFixtureYAML)})
			require.NoError(t, err)
			ft := &fakeFullTool{name: "lark_execute", out: []byte(testCase.output)}
			eino := adaptFullToEinoTool(ft, &RunHooks{NarrationProvider: prov})
			ch := make(chan stream.Event, 8)
			ctx := narration.WithCollector(WithRunID(context.Background(), 227))
			ctx = WithStreamState(ctx, &StreamSessionState{Ch: ch, RunID: 227})

			out, err := eino.InvokableRun(ctx, `{"argv":["docs","+update"]}`)
			require.NoError(t, err, "the redacted failure result must still reach the model")
			assert.JSONEq(t, testCase.output, out)

			var sawResult, sawError bool
			for _, event := range narration.CollectorFrom(ctx).Events() {
				sawResult = sawResult || event.State == narration.StateResult
				sawError = sawError || event.State == narration.StateError
			}
			assert.False(t, sawResult, "a terminal Feishu failure must never render as green success")
			assert.True(t, sawError)

			var sawStreamResult bool
			var errorPayload *stream.ToolCallErrorPayload
			for len(ch) > 0 {
				event := <-ch
				if event.Type == stream.EventToolCallResult {
					sawStreamResult = true
				}
				if event.Type == stream.EventToolCallError {
					var payload stream.ToolCallErrorPayload
					require.NoError(t, json.Unmarshal(event.Data, &payload))
					errorPayload = &payload
				}
			}
			assert.False(t, sawStreamResult)
			if assert.NotNil(t, errorPayload) {
				assert.False(t, errorPayload.Recoverable)
			}
		})
	}
}

func TestAdaptFullToEinoTool_RetryableLarkFailureEmitsRecoverableProgress(t *testing.T) {
	prov, err := narration.NewProvider(narration.Config{YAMLBytes: []byte(narrationFixtureYAML)})
	require.NoError(t, err)
	ft := &fakeFullTool{name: "lark_execute", out: []byte(`{"ok":false,"state":"failed","operation_id":"op-temporary","failure":{"code":"feishu_temporary_error","category":"temporary","retryable":true,"business_started":false}}`)}
	eino := adaptFullToEinoTool(ft, &RunHooks{NarrationProvider: prov})
	ch := make(chan stream.Event, 8)
	ctx := narration.WithCollector(WithRunID(context.Background(), 228))
	ctx = WithStreamState(ctx, &StreamSessionState{Ch: ch, RunID: 228})

	_, err = eino.InvokableRun(ctx, `{"argv":["docs","+fetch"]}`)
	require.NoError(t, err)

	var sawProgress, sawResult, sawError bool
	for _, event := range narration.CollectorFrom(ctx).Events() {
		if event.State == narration.StateProgress {
			sawProgress = true
			assert.Equal(t, "正在调整执行方式", event.Message)
		}
		sawResult = sawResult || event.State == narration.StateResult
		sawError = sawError || event.State == narration.StateError
	}
	assert.True(t, sawProgress)
	assert.False(t, sawResult)
	assert.False(t, sawError)

	var payload stream.ToolCallErrorPayload
	for len(ch) > 0 {
		event := <-ch
		if event.Type == stream.EventToolCallError {
			require.NoError(t, json.Unmarshal(event.Data, &payload))
		}
	}
	assert.True(t, payload.Recoverable)
}

func TestAdaptFullToEinoTool_ValidationLarkFailureEmitsRecoverableProgress(t *testing.T) {
	prov, err := narration.NewProvider(narration.Config{YAMLBytes: []byte(narrationFixtureYAML)})
	require.NoError(t, err)
	ft := &fakeFullTool{name: "lark_execute", out: []byte(`{"ok":false,"state":"failed","operation_id":"op-validation","failure":{"code":"feishu_validation_error","category":"validation","retryable":false,"business_started":false}}`)}
	eino := adaptFullToEinoTool(ft, &RunHooks{NarrationProvider: prov})
	ch := make(chan stream.Event, 8)
	ctx := narration.WithCollector(WithRunID(context.Background(), 230))
	ctx = WithStreamState(ctx, &StreamSessionState{Ch: ch, RunID: 230})

	_, err = eino.InvokableRun(ctx, `{"argv":["docs","+fetch"]}`)
	require.NoError(t, err)

	var sawProgress, sawResult, sawError bool
	for _, event := range narration.CollectorFrom(ctx).Events() {
		if event.State == narration.StateProgress {
			sawProgress = true
			assert.Equal(t, "正在调整执行方式", event.Message)
		}
		sawResult = sawResult || event.State == narration.StateResult
		sawError = sawError || event.State == narration.StateError
	}
	assert.True(t, sawProgress)
	assert.False(t, sawResult)
	assert.False(t, sawError)

	var payload stream.ToolCallErrorPayload
	for len(ch) > 0 {
		event := <-ch
		if event.Type == stream.EventToolCallError {
			require.NoError(t, json.Unmarshal(event.Data, &payload))
		}
	}
	assert.True(t, payload.Recoverable)
}

// TestSoftToolErrorMessage locks the detector's false-positive safety: it matches
// ONLY a dedicated "error" field with the "ERROR: " prefix, never a successful
// result whose content merely contains the text "ERROR:".
func TestSoftToolErrorMessage(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
		ok     bool
	}{
		{"image_gen soft error", `{"error":"ERROR: x: 生成过程中遇到了超时"}`, "ERROR: x: 生成过程中遇到了超时", true},
		{"softToolError shape", `{"error":"ERROR: image_gen: boom"}`, "ERROR: image_gen: boom", true},
		{"success result", `{"ok":true}`, "", false},
		{"empty error field", `{"error":""}`, "", false},
		{"error field without ERROR prefix", `{"error":"just a note"}`, "", false},
		{"content contains ERROR but no error field", `{"content":"the log said ERROR: x","ok":true}`, "", false},
		{"file_read soft error (error + content)", `{"file_name":"x.pdf","mime_type":"application/octet-stream","content":"ERROR: 文件不存在","error":"ERROR: 文件不存在"}`, "ERROR: 文件不存在", true},
		{"run_python friendly error (no ERROR prefix)", `{"error":"run_python: traceback ..."}`, "", false},
		{"non-string error field", `{"error":42}`, "", false},
		{"non-json", `not json`, "", false},
		{"json array", `["ERROR: x"]`, "", false},
		{"empty", ``, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := softToolErrorMessage(c.output)
			assert.Equal(t, c.ok, ok, "ok mismatch")
			assert.Equal(t, c.want, got, "message mismatch")
		})
	}
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

// TestAdapter_NarrationEmits_YieldNotErrorOnNonStreamPath reproduces a
// customer-reported bug: when a RESUMED run (answer.go → runner.Run, the
// non-streaming path with NO StreamState in ctx) reaches a 2nd
// ask_user_question, the yield sentinel previously fell through to the error
// branch and emitted a false StateError narration. The learner saw
// "失败⚠️问题处理中断，稍后再试一下" even though the run was healthily paused and
// answerable. A yield is a PAUSE, not a tool failure: StateError must NOT fire
// on either path. Only the StateUse ("等你回答…") narration may be emitted.
func TestAdapter_NarrationEmits_YieldNotErrorOnNonStreamPath(t *testing.T) {
	yieldErr := &yieldError{Payload: YieldPayload{Questions: []YieldQuestion{
		{Question: "陪跑周期多长？", Options: []YieldOption{{Key: "90", Label: "90天"}}},
	}}}
	ft := &fakeFullTool{name: "ask_user_question", err: yieldErr}
	rec := newHookRecorder()
	prov, ch, cleanup := setupNarration(t, 1010)
	defer cleanup()

	hooks := rec.asRunHooks()
	hooks.NarrationProvider = prov
	// Non-stream resume path: WithRunID only, NO StreamState injected into ctx.
	ctx := WithRunID(context.Background(), 1010)

	eino := adaptFullToEinoTool(ft, hooks)
	_, err := eino.InvokableRun(ctx, `{}`)
	require.Error(t, err, "yield sentinel still propagates so the eino graph stops")
	require.ErrorIs(t, err, ErrYieldForUserQuestion)

	evs := drainEvents(ch, 100*time.Millisecond)
	require.Len(t, evs, 1, "only the StateUse narration may fire for a yield; NO StateError")
	assert.Equal(t, narration.StateUse, evs[0].State,
		"a yield is a pause, not a failure: the only event must be StateUse, never StateError")
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
	st := &StreamSessionState{Ch: ch, RunID: 42}
	st.StepIdx.Store(1)
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

// TestAdapter_InvokableRun_EmitsArtifactURL reproduces the User-reported bug
// (dev 2026-06-08): image_gen said "图片已生成" but no image appeared. The image
// was generated and uploaded to COS, but its URL was never delivered to the
// frontend — ToolCallResultPayload.ArtifactURL was defined yet never populated.
// A file-producing tool (image_gen / create_*) returns a fileCreateOutput JSON;
// its url must surface as ArtifactURL. Pre-fix this FAILS (ArtifactURL empty).
func TestAdapter_InvokableRun_EmitsArtifactURL(t *testing.T) {
	const url = "https://cos.example/agent-outputs/1/x.png?sign=abc"
	out := `{"url":"` + url + `","filename":"x.png","size_bytes":1024,"format":"png"}`
	ft := &fakeFullTool{name: "image_gen", out: []byte(out)}
	eino := adaptFullToEinoTool(ft, nil)

	ch := make(chan stream.Event, 16)
	ctx := WithStreamState(context.Background(), &StreamSessionState{Ch: ch, RunID: 1})

	_, err := eino.InvokableRun(ctx, `{"prompt":"a cat"}`)
	require.NoError(t, err)

	var result *stream.ToolCallResultPayload
	for {
		select {
		case ev := <-ch:
			if ev.Type == stream.EventToolCallResult {
				var p stream.ToolCallResultPayload
				require.NoError(t, json.Unmarshal(ev.Data, &p))
				result = &p
			}
			continue
		default:
		}
		break
	}
	require.NotNil(t, result, "expected a tool_call_result event")
	assert.Equal(t, url, result.ArtifactURL, "generated file URL must be delivered as ArtifactURL")
	assert.Equal(t, "image/png", result.ArtifactMime, "image artifact must carry its MIME type")
	assert.Equal(t, "x.png", result.ArtifactFilename)
}

// TestArtifactFromToolResult covers artifact extraction + MIME derivation.
func TestArtifactFromToolResult(t *testing.T) {
	url, name, mime := artifactFromToolResult(`{"url":"https://c/x.png","filename":"x.png","format":"png"}`)
	assert.Equal(t, "https://c/x.png", url)
	assert.Equal(t, "x.png", name)
	assert.Equal(t, "image/png", mime)

	// create_text passes format="text" with an extension-less filename → text/plain
	// (the format fallback must recognise "text", not just "txt"/"md").
	_, _, mime = artifactFromToolResult(`{"url":"https://c/out","filename":"out","format":"text"}`)
	assert.Equal(t, "text/plain", mime)

	// Non-file tool output → no artifact.
	url, _, _ = artifactFromToolResult(`{"results":["a","b"]}`)
	assert.Empty(t, url)
	// Plain text output → no artifact.
	url, _, _ = artifactFromToolResult(`not json`)
	assert.Empty(t, url)
}

// TestAdapter_InvokableRun_CollectsGeneratedImage reproduces the User-reported
// follow-up (dev 2026-06-08): image_gen ran and the URL was emitted on the SSE
// artifact channel, but the bubble is transient — on reload loadSessionSnapshot
// replaces messages from agent_run.messages (which never persists artifacts), so
// the image vanishes. The durable fix collects generated images so they can be
// embedded as markdown in the PERSISTED final answer. Pre-fix InvokableRun does
// not collect → generatedImageMarkdown() is empty → this FAILS.
func TestAdapter_InvokableRun_CollectsGeneratedImage(t *testing.T) {
	const url = "https://cos.example/agent-outputs/1/y.png?sign=x"
	out := `{"url":"` + url + `","filename":"y.png","size_bytes":10,"format":"png"}`
	ft := &fakeFullTool{name: "image_gen", out: []byte(out)}
	eino := adaptFullToEinoTool(ft, nil)

	ctx := withArtifactCollector(context.Background())

	_, err := eino.InvokableRun(ctx, `{"prompt":"a cat"}`)
	require.NoError(t, err)

	md := artifactCollectorFrom(ctx).finalizeInto("")
	if !strings.Contains(md, url) || !strings.Contains(md, "![") {
		t.Fatalf("generated image must be collected as markdown for durable render, got %q", md)
	}
}

func TestAdapter_InvokableRun_EmitsToolCallError(t *testing.T) {
	ft := &fakeFullTool{name: "invoke_skill", err: errors.New("sandbox boom")}
	eino := adaptFullToEinoTool(ft, nil)

	ch := make(chan stream.Event, 16)
	ctx := WithStreamState(context.Background(), &StreamSessionState{Ch: ch, RunID: 7})

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
	ctx := WithStreamState(context.Background(), &StreamSessionState{Ch: ch, RunID: 1})

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
