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
	default: // StateProgress and any future
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
	verb, detail, message := t.renderer.Render(renderRequest{
		ToolName:       toolName,
		State:          state,
		Input:          data["input"].(map[string]any),
		Result:         data["result"].(map[string]any),
		ReasonFriendly: reasonFriendly,
	})

	// 4. If renderer produced empty for the chosen state, fall back to LLM stub.
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
