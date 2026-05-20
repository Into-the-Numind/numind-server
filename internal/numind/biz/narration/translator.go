package narration

import (
	"context"
	"encoding/json"
	"time"
)

// LLMFallback is the v1 abstraction point for #14 to plug aiservice.Chat.
// Implementations MUST handle their own errors and return safe defaults
// (verb, detail) — never propagate error out (S1-D12).
type LLMFallback interface {
	Render(ctx context.Context, toolName string, state State, payload EmitPayload) (verb, detail string)
}

// stubLLMFallback is the deterministic v1 default (no LLM call).
type stubLLMFallback struct{}

func (stubLLMFallback) Render(_ context.Context, toolName string, state State, _ EmitPayload) (string, string) {
	switch state {
	case StateUse, StateQueued:
		return "正在执行", toolName
	case StateResult:
		return "完成", toolName
	case StateError:
		return "执行出错", toolName
	case StateRejected:
		return "操作被拦截", toolName
	default:
		// StateProgress hits this branch (no dedicated case to flag as TODO
		// for #13 sandbox push that will emit real progress narration).
		// Any future State value also lands here as the safe default.
		return "处理中", toolName
	}
}

// Translator combines the yaml Renderer + LLM fallback.
// Always returns a usable Event; never returns error.
type Translator struct {
	renderer *Renderer
	fallback LLMFallback
}

func NewTranslator(r *Renderer, f LLMFallback) *Translator {
	if f == nil {
		f = stubLLMFallback{}
	}
	return &Translator{renderer: r, fallback: f}
}

// Translate renders the Event for one (toolName, state, payload). Always
// produces a usable Event; on yaml miss, falls back to LLMFallback for
// verb+detail; on payload.OverrideMessage non-empty (reserved #14), the
// override wins regardless.
func (t *Translator) Translate(ctx context.Context, payload EmitPayload, toolName string, state State) Event {
	// 1. Classify error (only meaningful for StateError; benign otherwise).
	_, reasonFriendly := ClassifyError(payload.Err)

	// 2. Build template data (nil-safe; defensive JSON unmarshal).
	data := buildTemplateData(payload, reasonFriendly)

	// 3. Try yaml renderer (handles tool→defaults fallback internally).
	// Defensive comma-ok assertions: buildTemplateData always returns non-nil
	// map[string]any for "input" and "result", but future refactors that change
	// buildTemplateData's contract would silently corrupt narration. The
	// comma-ok form falls back to empty maps rather than panicking.
	inputMap, _ := data["input"].(map[string]any)
	if inputMap == nil {
		inputMap = map[string]any{}
	}
	resultMap, _ := data["result"].(map[string]any)
	if resultMap == nil {
		resultMap = map[string]any{}
	}
	verb, detail, message := t.renderer.Render(renderRequest{
		ToolName:       toolName,
		State:          state,
		Input:          inputMap,
		Result:         resultMap,
		ReasonFriendly: reasonFriendly,
	})

	// 4. If renderer produced empty for the chosen state, fall back to LLM stub.
	// S2 P1 amendment to spec §5: spec used `verb + " " + detail` unconditionally,
	// but that produces a trailing space when detail is empty. Guarded form below
	// drops the space and yields cleaner output (e.g., "正在执行" instead of
	// "正在执行 "). Stub always returns non-empty detail in v1, so behavior is
	// identical to spec in practice; difference only matters for future fallback
	// impls that may return empty detail.
	if message == "" {
		verb, detail = t.fallback.Render(ctx, toolName, state, payload)
		message = verb
		if detail != "" {
			message = verb + " " + detail
		}
	}

	// 5. v1 OverrideMessage always "" (reserved for #14 LLM-supplied narration).
	if payload.OverrideMessage != "" {
		message = payload.OverrideMessage
	}

	return Event{
		RunID:      payload.RunID,
		ToolCallID: payload.ToolCallID,
		ToolName:   toolName,
		State:      state,
		Verb:       verb,
		Detail:     detail,
		Icon:       iconForState(state),
		Message:    message,
		Reason:     payload.Reason,
		Timestamp:  nowFunc(),
	}
}

// buildTemplateData wraps payload fields in maps suitable for text/template access.
// Inputs that fail to JSON-unmarshal become empty maps (NEVER nil — templates
// blow up on nil map deref).
func buildTemplateData(p EmitPayload, reasonFriendly string) map[string]any {
	inputMap := map[string]any{}
	if len(p.Input) > 0 {
		_ = json.Unmarshal(p.Input, &inputMap) // best effort; ignore unmarshal err
	}
	resultMap := map[string]any{}
	if len(p.Result) > 0 {
		_ = json.Unmarshal(p.Result, &resultMap)
	}
	return map[string]any{
		"input":           inputMap,
		"result":          resultMap,
		"reason_friendly": reasonFriendly,
		"verb":            "",
		"detail":          "",
	}
}

// nowFunc allows tests to inject a deterministic timestamp.
var nowFunc = time.Now
