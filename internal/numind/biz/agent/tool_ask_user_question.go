package agent

import (
	"context"
	"encoding/json"

	"numind-server/internal/pkg/errno"
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
	return "Ask the user for information you cannot obtain yourself — their private/internal facts, specific needs, preferences, or a decision only they can make. Provide 2–4 options where EACH option is a concrete, complete candidate answer to the question (e.g. for '陪跑周期多长?' use '90天'/'180天'/'半年以上', NOT meta-categories like '陪跑模式细节'). The user can pick an option OR type their own answer in the free-text box that always appears below the options, so options are suggestions, not an exhaustive list. Yields the run until the user answers via POST /v1/agent-runs/:id/answer. Use this (not a plain text reply) whenever the task needs info only the user has and proceeding without it would produce a wrong or low-quality result; do NOT use it for info you can research or reasonably infer yourself."
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
				"minItems": 2,
				"maxItems": 4,
				"description": "2-4 concrete candidate answers to the question. Each MUST be a complete, selectable answer (e.g. '90天', '1v1私教'), NOT a meta-category like '陪跑模式细节' (which leaves the user nothing real to convey). The user always also has a free-text box below to type their own answer, so these are suggestions, not exhaustive.",
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
	if len(in.Options) < 2 || len(in.Options) > 4 {
		return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: options length must be 2–4 (got %d)", len(in.Options))
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
