package agent

import (
	"context"
	"encoding/json"
	"fmt"

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

// softError returns a recoverable tool result (a ToolResult carrying an error
// message, nil Go error) instead of a hard error. A hard error from a tool
// propagates as a NodeRunError and kills the ENTIRE agent run; dev run #133 died
// exactly this way when a truncated multi-question tool call failed to unmarshal.
// A soft error feeds the message back into the ReAct loop so the model retries
// (e.g. with fewer / shorter questions) and the run survives. The shape mirrors
// web_search's returnSoftError (an {"error": "..."} object the model can read).
func (t *askUserQuestionTool) softError(format string, args ...any) (ToolResult, error) {
	out, _ := json.Marshal(map[string]string{"error": "ERROR: " + fmt.Sprintf(format, args...)})
	return ToolResult(out), nil
}

// Execute validates the input. On SUCCESS it returns a *yieldError sentinel to
// pause the run (the runner.go yield handler detects it via errors.As and drives
// the state machine to TerminalWaitingForUserChoice). On any validation FAILURE it
// returns a SOFT error (see softError) — a ToolResult carrying the message with a
// nil Go error — never a hard error: a malformed/truncated/over-long tool call from
// the model must never kill the run; it is fed back so the model corrects and retries.
func (t *askUserQuestionTool) Execute(_ context.Context, input ToolInput) (ToolResult, error) {
	var in askUserQuestionInput
	if err := json.Unmarshal(input, &in); err != nil {
		// dev run #133: a large "ask everything at once" multi-question call arrived
		// truncated → "unexpected end of JSON input". Soft-error AND steer the retry
		// smaller so it fits within the model's output budget next time.
		return t.softError("ask_user_question 参数解析失败（很可能一次问的问题太多/太长导致 JSON 被截断）：%s。请重新调用，一次只问 1-2 个最关键的问题，每个问题给 0 个或 2-4 个简短选项。", err.Error())
	}
	if len(in.Questions) == 0 {
		return t.softError("ask_user_question: 请提供 1-4 个问题（当前 0 个）。")
	}
	if len(in.Questions) > 4 {
		// Reject (not clamp) >4 questions: each question is a user-facing yield slot,
		// so silently dropping the 5th would lose a question the user never sees. (By
		// contrast, >4 OPTIONS within a question are clamped below — the always-present
		// free-text box still covers anything past the 4th suggestion.)
		return t.softError("ask_user_question: 一次最多 4 个问题（当前 %d）。请只保留最关键的，分批问。", len(in.Questions))
	}
	seenQuestion := make(map[string]struct{}, len(in.Questions))
	out := make([]YieldQuestion, 0, len(in.Questions))
	for qi := range in.Questions {
		q := in.Questions[qi]
		if q.Question == "" {
			return t.softError("ask_user_question: question[%d] 文本为空。", qi)
		}
		if _, dup := seenQuestion[q.Question]; dup {
			return t.softError("ask_user_question: 重复的问题文本 %q。", q.Question)
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
			return t.softError("ask_user_question: question[%d] 请给 0 个（开放式）或 2-4 个选项（当前 1 个）。", qi)
		}
		seenLabel := make(map[string]struct{}, len(q.Options))
		for oi, opt := range q.Options {
			if opt.Key == "" || opt.Label == "" {
				return t.softError("ask_user_question: question[%d] option[%d] 缺少 key 或 label。", qi, oi)
			}
			if _, dup := seenLabel[opt.Label]; dup {
				return t.softError("ask_user_question: question[%d] 重复的选项 label %q。", qi, opt.Label)
			}
			seenLabel[opt.Label] = struct{}{}
		}
		if len([]rune(q.Header)) > 12 {
			return t.softError("ask_user_question: question[%d] header 超过 12 字（当前 %d）。", qi, len([]rune(q.Header)))
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
