package agent

import (
	"context"
	"encoding/json"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

type askUserQuestionInput struct {
	Question    string        `json:"question"`
	Options     []YieldOption `json:"options"`
	Header      string        `json:"header,omitempty"`
	MultiSelect bool          `json:"multi_select,omitempty"`
}

type askUserQuestionTool struct{ BaseTool }

// NewAskUserQuestionTool constructs the ask_user_question FullTool.
func NewAskUserQuestionTool() FullTool { return &askUserQuestionTool{} }

// Compile-time assertion.
var _ FullTool = (*askUserQuestionTool)(nil)

func (t *askUserQuestionTool) Name() string { return "ask_user_question" }
func (t *askUserQuestionTool) Description() string {
	return "Ask the user for information you cannot obtain yourself — their private/internal facts, specific needs, preferences, or a decision only they can make. Options: give 2–4 when there are a few likely answers (EACH a concrete, complete candidate answer, e.g. for '陪跑周期多长?' use '90天'/'180天'/'半年以上', NOT meta-categories like '陪跑模式细节'); give an EMPTY options array for a pure open-ended question (e.g. '请提供你们的陪跑周期和价格'). The user can pick an option OR type their own answer in the free-text box that always appears below, so options are suggestions, not exhaustive — never list more than 4. Yields the run until the user answers via POST /v1/agent-runs/:id/answer. Use this (not a plain text reply) whenever the task needs info only the user has and proceeding without it would produce a wrong or low-quality result; do NOT use it for info you can research or reasonably infer yourself."
}
func (t *askUserQuestionTool) UserFacingName() string { return "反问" }
func (t *askUserQuestionTool) NarrationVerb() string  { return "反问" }
func (t *askUserQuestionTool) IsReadOnly() bool       { return true }
func (t *askUserQuestionTool) AlwaysLoad() bool       { return true }

// InputSchema returns the JSON Schema describing this tool's parameters,
// so the LLM receives a structured function-calling contract (not just prose).
func (t *askUserQuestionTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"question": {"type": "string", "description": "The clarifying question to ask the user."},
			"options": {
				"type": "array",
				"minItems": 0,
				"maxItems": 4,
				"description": "0, or 2-4, concrete candidate answers. Provide 2-4 when there ARE a few likely answers — each MUST be a complete, selectable answer (e.g. '90天', '1v1私教'), NOT a meta-category like '陪跑模式细节' (which leaves the user nothing real to convey). Provide an EMPTY array for a pure open-ended question (e.g. '请提供你们的陪跑周期和价格') — the user answers via the always-present free-text box. Never give exactly 1. Do not pad past 4; the free-text box covers the rest.",
				"items": {
					"type": "object",
					"properties": {
						"key":         {"type": "string", "description": "Stable machine identifier for this option."},
						"label":       {"type": "string", "description": "Human-readable option text shown to the user."},
						"description": {"type": "string", "description": "Optional longer explanation of the option."}
					},
					"required": ["key", "label"]
				}
			},
			"header":       {"type": "string", "description": "Optional short chip label (max 12 characters)."},
			"multi_select": {"type": "boolean", "description": "Allow selecting more than one option (default false)."}
		},
		"required": ["question", "options"]
	}`)
}

// Execute validates the input and returns a *yieldError to pause the run.
// The runner.go yield handler detects the sentinel via errors.As and drives the
// state machine to TerminalWaitingForUserChoice.
func (t *askUserQuestionTool) Execute(_ context.Context, input ToolInput) (ToolResult, error) {
	var in askUserQuestionInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, errno.ErrBind.SetMessage("ask_user_question: invalid JSON: %s", err.Error())
	}
	if in.Question == "" {
		return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: question is empty")
	}
	// Tolerate the LLM over-supplying options: clamp to the first 4 instead of
	// failing the whole tool call. ask-question-freetext taught the agent that
	// "options are suggestions, not exhaustive", and a model duly returned 10 —
	// the old hard 2-4 check then died model_error and the user saw "服务不可用"
	// (dev run #127). The always-present free-text box covers anything beyond 4.
	if len(in.Options) > 4 {
		log.Warnw("ask_user_question: clamping options to 4", "got", len(in.Options))
		in.Options = in.Options[:4]
	}
	// Allowed shapes: 0 options (a pure open-ended question answered entirely via
	// the free-text box) or 2-4 concrete choices. Exactly 1 option is not a
	// meaningful choice, so it stays rejected.
	if len(in.Options) == 1 {
		return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: provide 0 options (open-ended) or 2-4 options (got 1)")
	}
	for i, opt := range in.Options {
		if opt.Key == "" || opt.Label == "" {
			return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: option[%d] missing key or label", i)
		}
	}
	if len([]rune(in.Header)) > 12 {
		return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: header exceeds 12 chars (got %d)", len([]rune(in.Header)))
	}
	return nil, &yieldError{Payload: YieldPayload(in)}
}
