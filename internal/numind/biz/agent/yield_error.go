package agent

import "errors"

// ErrYieldForUserQuestion is the sentinel error returned by ask_user_question
// tool.Execute() to signal the runner that the run should yield (pause) until
// the user answers via POST /v1/agent-runs/:id/answer.
var ErrYieldForUserQuestion = errors.New("agent: yield for user question")

// YieldPayload is the structured question payload carried by yieldError.
// The runner serializes this to JSON and stores it in agent_run.pending_question_json.
type YieldPayload struct {
	Question    string        `json:"question"`
	Options     []YieldOption `json:"options"`
	Header      string        `json:"header,omitempty"`
	MultiSelect bool          `json:"multi_select"`
}

// YieldOption is a single multiple-choice option.
type YieldOption struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// yieldError wraps ErrYieldForUserQuestion with the payload.
// Use errors.As(err, &yieldErr) in runner.go to extract.
type yieldError struct {
	Payload YieldPayload
}

func (e *yieldError) Error() string        { return ErrYieldForUserQuestion.Error() }
func (e *yieldError) Is(target error) bool { return target == ErrYieldForUserQuestion }
func (e *yieldError) Unwrap() error        { return ErrYieldForUserQuestion }
