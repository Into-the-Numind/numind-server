package agent

import (
	"context"
	"encoding/json"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

type askUserQuestionInput struct {
	Questions []askUserQuestionItem `json:"questions"`
}

// askUserQuestionItem is one question in the ask_user_question array input.
// This struct is unmarshal-only (the LLM's tool call), so json tags carry no
// omitempty — it is never serialized.
type askUserQuestionItem struct {
	Question    string        `json:"question"`
	Options     []YieldOption `json:"options"`
	Header      string        `json:"header"`
	MultiSelect bool          `json:"multi_select"`
}

type askUserQuestionTool struct{ BaseTool }

// NewAskUserQuestionTool constructs the ask_user_question FullTool.
func NewAskUserQuestionTool() FullTool { return &askUserQuestionTool{} }

// Compile-time assertion.
var _ FullTool = (*askUserQuestionTool)(nil)

func (t *askUserQuestionTool) Name() string { return "ask_user_question" }
func (t *askUserQuestionTool) Description() string {
	return "Ask the user for information you cannot obtain yourself — their private/internal facts, specific needs, preferences, or a decision only they can make. Pass a `questions` array of 1–4 INDEPENDENT questions: when you need several distinct pieces of info (e.g. 陪跑周期 AND 客群 AND 业绩), make each its OWN question — never cram multiple topics into one question's options (a checkbox list of topic names like '陪跑/客群/业绩' is a meta-choice the user cannot actually answer). Per question: give 2–4 options when there are a few likely answers (EACH a concrete, complete candidate answer, e.g. for '陪跑周期多长?' use '90天'/'180天'/'半年以上', NOT a meta-category like '陪跑模式细节'); give an EMPTY options array for a pure open-ended question (e.g. '请提供你们的价格'). The user can pick an option OR type their own answer in the free-text box that always appears below each question, so options are suggestions, not exhaustive — never list more than 4. Question texts must be unique, and option labels must be unique within a question. Yields the run until the user answers via POST /v1/agent-runs/:id/answer. Use this (not a plain text reply) whenever the task needs info only the user has and proceeding without it would produce a wrong or low-quality result; do NOT use it for info you can research or reasonably infer yourself."
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
			"questions": {
				"type": "array",
				"minItems": 1,
				"maxItems": 4,
				"description": "1-4 INDEPENDENT questions to ask at once. When you need several distinct pieces of info, make each its own question here — do NOT cram multiple topics into one question's options. Question texts must be unique.",
				"items": {
					"type": "object",
					"properties": {
						"question": {"type": "string", "description": "The clarifying question to ask the user."},
						"options": {
							"type": "array",
							"minItems": 0,
							"maxItems": 4,
							"description": "0, or 2-4, concrete candidate answers for THIS question. Provide 2-4 when there ARE a few likely answers — each MUST be a complete, selectable answer (e.g. '90天', '1v1私教'), NOT a meta-category like '陪跑模式细节' (which leaves the user nothing real to convey). Provide an EMPTY array for a pure open-ended question — the user answers via the always-present free-text box. Never give exactly 1. Do not pad past 4; the free-text box covers the rest. Option labels must be unique within this question.",
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
						"header":       {"type": "string", "description": "Optional short chip label for this question (max 12 characters)."},
						"multi_select": {"type": "boolean", "description": "Allow selecting more than one option for this question (default false)."}
					},
					"required": ["question", "options"]
				}
			}
		},
		"required": ["questions"]
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
	if len(in.Questions) == 0 {
		return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: provide 1-4 questions (got 0)")
	}
	if len(in.Questions) > 4 {
		return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: provide at most 4 questions (got %d)", len(in.Questions))
	}
	seenQuestion := make(map[string]struct{}, len(in.Questions))
	out := make([]YieldQuestion, 0, len(in.Questions))
	for qi := range in.Questions {
		q := in.Questions[qi]
		if q.Question == "" {
			return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: question[%d] is empty", qi)
		}
		if _, dup := seenQuestion[q.Question]; dup {
			return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: duplicate question text %q", q.Question)
		}
		seenQuestion[q.Question] = struct{}{}
		// Tolerate the LLM over-supplying options for a question: clamp to the
		// first 4 instead of failing the whole tool call. ask-question-freetext
		// taught the agent that "options are suggestions, not exhaustive", and a
		// model duly returned 10 — the old hard 2-4 check then died model_error
		// and the user saw "服务不可用" (dev run #127). The always-present
		// free-text box covers anything beyond 4.
		if len(q.Options) > 4 {
			log.Warnw("ask_user_question: clamping options to 4", "question_index", qi, "got", len(q.Options))
			q.Options = q.Options[:4]
		}
		// Allowed shapes per question: 0 options (a pure open-ended question
		// answered entirely via the free-text box) or 2-4 concrete choices.
		// Exactly 1 option is not a meaningful choice, so it stays rejected.
		if len(q.Options) == 1 {
			return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: question[%d] provide 0 options (open-ended) or 2-4 options (got 1)", qi)
		}
		seenLabel := make(map[string]struct{}, len(q.Options))
		for oi, opt := range q.Options {
			if opt.Key == "" || opt.Label == "" {
				return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: question[%d] option[%d] missing key or label", qi, oi)
			}
			if _, dup := seenLabel[opt.Label]; dup {
				return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: question[%d] duplicate option label %q", qi, opt.Label)
			}
			seenLabel[opt.Label] = struct{}{}
		}
		if len([]rune(q.Header)) > 12 {
			return nil, errno.ErrInvalidInput.SetMessage("ask_user_question: question[%d] header exceeds 12 chars (got %d)", qi, len([]rune(q.Header)))
		}
		out = append(out, YieldQuestion{
			Question:    q.Question,
			Header:      q.Header,
			Options:     q.Options,
			MultiSelect: q.MultiSelect,
		})
	}
	return nil, &yieldError{Payload: YieldPayload{Questions: out}}
}
