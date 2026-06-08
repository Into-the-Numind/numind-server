package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
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
// ParamsOneOf is built from ft.InputSchema() so the LLM receives the tool's real
// function-calling parameter schema instead of an empty object. Tools that do not
// declare a schema (BaseTool default returns nil) fall back to empty params — the
// historical behavior — so this change is zero-regression.
func (a *fullToolEinoAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        a.ft.Name(),
		Desc:        a.ft.Description(),
		ParamsOneOf: paramsOneOfFromInputSchema(a.ft.Name(), a.ft.InputSchema()),
	}, nil
}

// emptyParamsOneOf is the historical empty-params value (a parameterless object
// schema). Used as the defensive fallback whenever a tool has no usable schema.
func emptyParamsOneOf() *schema.ParamsOneOf {
	return schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})
}

// paramsOneOfFromInputSchema converts a tool's JSON Schema (json.RawMessage, as
// returned by FullTool.InputSchema()) into an Eino *schema.ParamsOneOf.
//
// Defensive fallback (ZERO regression): on nil / empty / unparseable input it
// returns emptyParamsOneOf(), so a tool with no schema (or a malformed one)
// behaves exactly as before this change. Info() therefore never errors and never
// panics on account of a tool's schema. toolName is used only for log context.
func paramsOneOfFromInputSchema(toolName string, raw json.RawMessage) *schema.ParamsOneOf {
	if len(bytes.TrimSpace(raw)) == 0 {
		return emptyParamsOneOf()
	}
	var js jsonschema.Schema
	if err := json.Unmarshal(raw, &js); err != nil {
		// A tool authored an invalid InputSchema(). Log and fall back rather than
		// breaking the agent run — the LLM still gets the tool, just without params.
		log.Warnw("paramsOneOfFromInputSchema: invalid InputSchema JSON, falling back to empty params",
			"tool", toolName, "err", err)
		return emptyParamsOneOf()
	}
	// Note: a raw value of "null" unmarshals into a zero-value jsonschema.Schema
	// (no type, no properties); NewParamsOneOfByJSONSchema accepts that and yields
	// an empty-but-non-nil ParamsOneOf, so the never-nil invariant still holds.
	return schema.NewParamsOneOfByJSONSchema(&js)
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
		// Soft interception (agent-security-hardening): a permission Deny blocks ONLY
		// this single tool call and feeds a message back to the LLM so the ReAct loop
		// continues — unless the per-run anti-loop guard trips. This branch MUST come
		// before the unconditional Record(action) below so the soft path can record
		// Continue as the FINAL registry write (R6-A): otherwise the run-end
		// applyHookOverride reads LastAction==PermissionDeny and mis-terminates a
		// handled soft deny as TerminalPermissionDenied.
		if action == HookActionPermissionDeny {
			if sd := SoftDenyFromCtx(ctx); sd != nil && sd.Enabled() {
				tripped, msg := sd.Resolve(a.ft.Name(), args)
				if !tripped {
					if a.hooks.Registry != nil {
						a.hooks.Registry.Record(HookActionContinue)
					}
					// User-facing narration stays the standard "操作被拦截" (reason="").
					// The full advisory (msg) goes to the LLM via the return value ONLY —
					// not into the narration Reason field (which is for a short identifier).
					// Soft deny intentionally omits tool_call_* SSE (no Execute ran), exactly
					// like a hard reject.
					a.emitNarration(ctx, narration.StateRejected, input, nil, nil, "")
					return msg, nil // tool-result fed back to the model → loop continues
				}
				// tripped: fall through to the hard-terminate path below.
			}
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
	// ask_user_question yields (never returns a tool result/error), so emitting a
	// tool_call_start here would create a streaming tool card that never resolves
	// ("正在准备提问..." stuck in 'use'). The yield surfaces instead as a
	// question_prompt event (consumeEinoStream), which IS the user-facing UI for
	// it. Skip the start to avoid the orphan card. The StateUse narration above
	// still fires for the polling path.
	if a.ft.Name() != "ask_user_question" {
		a.emitStreamToolStart(ctx, toolCallID, args)
	}

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
	// ask_user_question pauses the run via a yield sentinel error. On the
	// streaming path, capture the payload into the shared stream state so
	// consumeEinoStream surfaces a question_prompt + waiting_for_user_choice
	// terminal — and suppress the error narration / tool_call_error SSE, because
	// a yield is a pause, not a tool failure. The sentinel is still returned so
	// the eino graph stops. The non-stream Run path has no stream state, so it
	// falls through to the normal error branch (unchanged; yield handled in
	// runner.go after Generate).
	var yErr *yieldError
	if streamState, hasStream := StreamStateFromContext(ctx); errors.As(effectiveErr, &yErr) && hasStream && streamState != nil {
		p := yErr.Payload
		streamState.PendingYield = &p
	} else if effectiveErr != nil {
		a.emitNarration(ctx, narration.StateError, input, nil, effectiveErr, "")
		a.emitStreamToolError(ctx, toolCallID, effectiveErr, durationMs)
	} else {
		// Successful tool execution = the agent made progress: reset the soft-deny
		// anti-loop streak (consecutive + same-fp) so a healthy run that bounces off a
		// single block never trips. The per-fingerprint LIFETIME counter is preserved.
		if sd := SoftDenyFromCtx(ctx); sd != nil {
			sd.OnSuccess()
		}
		a.emitNarration(ctx, narration.StateResult, input, result, nil, "")
		a.emitStreamToolResult(ctx, toolCallID, output, durationMs)
		// Collect tool-generated images so the run finalizer can embed them as
		// markdown in the PERSISTED final answer. The transient SSE artifact event
		// is lost on reload (loadSessionSnapshot rebuilds from agent_run.messages,
		// which never stored the artifact) — User-reported, dev 2026-06-08. Uses a
		// ctx collector so both streaming and non-streaming runs are covered.
		if url, fname, mime := artifactFromToolResult(output); url != "" && strings.HasPrefix(mime, "image/") {
			imageCollectorFrom(ctx).add(url, fname)
		}
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
	// T3 (#5): route narration by the per-run id carried in ctx (injected via
	// WithRunID at run start) — the same source every other per-run hook reads.
	// Previously this read a.hooks.NarrationRunID, a field on the process-global
	// *RunHooks that the runner mutated per run; with 2+ concurrent runs the last
	// writer won, leaking one run's narration into another's stream.
	// RunIDFromContext returns 0 only if a caller forgot WithRunID (legacy/test);
	// Emit then routes to an unsubscribed runID=0 bucket and the event is harmlessly
	// dropped — both run paths always inject WithRunID before any tool executes.
	a.hooks.NarrationProvider.Emit(ctx, RunIDFromContext(ctx), a.ft.Name(), st, narration.EmitPayload{
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
// For file-producing tools it also surfaces the generated artifact (URL + MIME)
// so the frontend can render it — the preview is truncated to 500 runes and
// would otherwise swallow the (long, signed) artifact URL.
func (a *fullToolEinoAdapter) emitStreamToolResult(ctx context.Context, toolCallID, output string, durationMs int64) {
	artURL, artName, artMime := artifactFromToolResult(output)
	a.emitStream(ctx, stream.EventToolCallResult, stream.ToolCallResultPayload{
		ToolCallID:       toolCallID,
		Preview:          truncateRunes(output, 500),
		ArtifactURL:      artURL,
		ArtifactFilename: artName,
		ArtifactMime:     artMime,
		DurationMs:       durationMs,
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
