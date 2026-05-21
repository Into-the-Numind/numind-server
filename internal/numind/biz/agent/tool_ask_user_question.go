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
	return "Ask the user a clarifying question with structured options (2–4 choices). Yields the run until the user answers via POST /v1/agent-runs/:id/answer. Use when the user's intent is ambiguous and proceeding incorrectly would waste significant resources or produce the wrong output."
}
func (t *askUserQuestionTool) UserFacingName() string { return "反问" }
func (t *askUserQuestionTool) NarrationVerb() string  { return "反问" }
func (t *askUserQuestionTool) IsReadOnly() bool       { return true }
func (t *askUserQuestionTool) AlwaysLoad() bool       { return true }

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
	return nil, &yieldError{Payload: YieldPayload{
		Question:    in.Question,
		Options:     in.Options,
		Header:      in.Header,
		MultiSelect: in.MultiSelect,
	}}
}
