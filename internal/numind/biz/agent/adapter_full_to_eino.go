package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/log"
)

// adaptFullToEinoTool wraps a FullTool as Eino's tool.InvokableTool so that
// AgentRunner can pass it to react.AgentConfig.ToolsConfig.Tools.
//
// hooks may be nil — then Pre/PostToolCall is skipped (#3 compatible).
// When non-nil, PreToolCall fires before Execute and PostToolCall fires
// after, in line with #2 RunHooks contract (#4 wiring path).
// When hooks.NarrationProvider is non-nil, the adapter additionally emits
// narration events (use/result/error/rejected) at the relevant call sites.
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
//
// #8 narration emit sites:
//   - StateRejected: PreToolCall returned HookAction != Continue (short-circuit)
//   - StateUse:      PreToolCall returned HookActionContinue, BEFORE Execute (S0 D2)
//   - StateResult:   PostToolCall returned, effectiveErr == nil
//   - StateError:    PostToolCall returned, effectiveErr != nil (execErr OR postErr-when-execErr-nil per S1-D15)
func (a *fullToolEinoAdapter) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
	input := ToolInput(args)

	if a.hooks != nil && a.hooks.PreToolCall != nil {
		action, err := a.hooks.PreToolCall(ctx, a, args)
		if err != nil {
			return "", fmt.Errorf("PreToolCall: %w", err)
		}
		if a.hooks.Registry != nil {
			a.hooks.Registry.Record(action)
		}
		if action != HookActionContinue {
			// EMIT REJECTED: short-circuit before Execute; no use/result/error follow.
			a.emitNarration(ctx, narration.StateRejected, input, nil, nil, "")
			return "", fmt.Errorf("tool execution stopped by hook: action=%d", action)
		}
	}

	// EMIT USE: after PreToolCall Continue, before Execute (S0 D2 timing contract).
	a.emitNarration(ctx, narration.StateUse, input, nil, nil, "")

	// EMIT SSE tool_call_start. The active streaming path (runner_runstream.go's
	// streamScanToolCallChecker) scans only MODEL OUTPUT and never emits
	// tool-call lifecycle events, so without this the student UI shows no sign a
	// tool is running — during a 30–60s run_python it looks frozen (2026-05-29
	// bug). Emitting here (the tool lifecycle boundary) fires exactly once per
	// call, carries the real tool name + input, and works for every model
	// regardless of how it streams tool_calls. toolCallID is synthetic but stable
	// across this call's start/result/error so the frontend can correlate them.
	toolCallID := uuid.NewString()
	startedAt := time.Now()
	a.emitStreamToolStart(ctx, toolCallID, args)

	// Execute the underlying tool
	result, execErr := a.ft.Execute(ctx, input)
	var output string
	if result != nil {
		output = string(result)
	}

	// effectiveErr captures what the CALLER will observe. If PostToolCall errors
	// while execErr is nil, the wrapped postErr becomes the returned error AND
	// drives narration to StateError (not StateResult) per S1-D15 / S1 P0-1 fix.
	effectiveErr := execErr

	// PostToolCall always fires (cleanup semantic). Errors from PostToolCall
	// are logged + only surface to the caller when no execErr already exists.
	if a.hooks != nil && a.hooks.PostToolCall != nil {
		postAction, postErr := a.hooks.PostToolCall(ctx, a, output, execErr)
		// Record every action so a later Continue clears a prior Stop.
		if a.hooks.Registry != nil {
			a.hooks.Registry.Record(postAction)
		}
		if postErr != nil {
			log.Warnw("PostToolCall failed",
				"tool", a.ft.Name(),
				"post_err", postErr,
				"exec_err", execErr)
			if execErr == nil {
				effectiveErr = fmt.Errorf("PostToolCall: %w", postErr)
			}
		}
	}

	// EMIT RESULT or ERROR (based on effectiveErr — what caller sees).
	durationMs := time.Since(startedAt).Milliseconds()
	if effectiveErr != nil {
		a.emitNarration(ctx, narration.StateError, input, nil, effectiveErr, "")
		a.emitStreamToolError(ctx, toolCallID, effectiveErr, durationMs)
	} else {
		a.emitNarration(ctx, narration.StateResult, input, result, nil, "")
		a.emitStreamToolResult(ctx, toolCallID, output, durationMs)
	}

	if effectiveErr != nil {
		return output, effectiveErr
	}
	return output, nil
}

// emitNarration is fire-and-forget; no-op when NarrationProvider is nil.
// BackfillObservableInput is called BEFORE building the EmitPayload so that
// secrets/PII redaction (the tool's responsibility per #3 FullTool contract)
// happens before narration templates see the input (S1-D14).
//
// The `reason` parameter is reserved for #6 permission-pipeline to populate
// the rejection reason; v1 always passes "" (S1-D21). Do not remove or rename.
func (a *fullToolEinoAdapter) emitNarration(ctx context.Context, st narration.State, input ToolInput, result ToolResult, execErr error, reason string) {
	if a.hooks == nil || a.hooks.NarrationProvider == nil {
		return
	}
	obsInput := a.ft.BackfillObservableInput(input)
	a.hooks.NarrationProvider.Emit(ctx, a.hooks.NarrationRunID, a.ft.Name(), st, narration.EmitPayload{
		Input:  json.RawMessage(obsInput),
		Result: json.RawMessage(result),
		Err:    execErr,
		Reason: reason,
	})
}

// emitStream sends one SSE event onto the shared streaming channel pulled from
// ctx (injected by RunStream via WithStreamState). No-op when there is no
// stream state (e.g. the non-streaming Run path or unit tests) — so the
// adapter stays usable in both modes. Seq is owned by the single in-flight
// step; tool execution is sequential with the model-output scanner, so the
// unsynchronised increment matches the existing emit closure's contract.
func (a *fullToolEinoAdapter) emitStream(ctx context.Context, t stream.EventType, payload any) {
	state, ok := StreamStateFromContext(ctx)
	if !ok || state == nil || state.Ch == nil {
		return
	}
	state.Seq++
	ev, err := stream.Encode(t, payload, state.Seq, state.RunID, state.StepIdx)
	if err != nil {
		return
	}
	select {
	case state.Ch <- ev:
	case <-ctx.Done():
	}
}

// emitStreamToolStart emits tool_call_start with the tool name + a truncated
// input preview (so the frontend can show e.g. the skill's concrete format).
func (a *fullToolEinoAdapter) emitStreamToolStart(ctx context.Context, toolCallID, args string) {
	var inputPreview map[string]any
	if args != "" {
		_ = json.Unmarshal([]byte(args), &inputPreview)
		inputPreview = truncateMapValues(inputPreview, 500)
	}
	a.emitStream(ctx, stream.EventToolCallStart, stream.ToolCallStartPayload{
		ToolCallID:   toolCallID,
		ToolName:     a.ft.Name(),
		InputDigest:  inputSHA(args),
		InputPreview: inputPreview,
	})
}

// emitStreamToolResult emits tool_call_result with a truncated output preview.
func (a *fullToolEinoAdapter) emitStreamToolResult(ctx context.Context, toolCallID, output string, durationMs int64) {
	a.emitStream(ctx, stream.EventToolCallResult, stream.ToolCallResultPayload{
		ToolCallID: toolCallID,
		Preview:    truncateRunes(output, 500),
		DurationMs: durationMs,
	})
}

// emitStreamToolError emits tool_call_error when the tool (or its post-hook) failed.
func (a *fullToolEinoAdapter) emitStreamToolError(ctx context.Context, toolCallID string, err error, durationMs int64) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	a.emitStream(ctx, stream.EventToolCallError, stream.ToolCallErrorPayload{
		ToolCallID: toolCallID,
		Error:      truncateRunes(msg, 500),
		DurationMs: durationMs,
	})
}
