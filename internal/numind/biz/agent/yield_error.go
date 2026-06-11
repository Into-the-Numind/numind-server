package agent

import (
	"encoding/json"
	"errors"
)

// ErrYieldForUserQuestion is the sentinel error returned by ask_user_question
// tool.Execute() to signal the runner that the run should yield (pause) until
// the user answers via POST /v1/agent-runs/:id/answer.
var ErrYieldForUserQuestion = errors.New("agent: yield for user question")

// YieldPayload is the structured question payload carried by yieldError.
// The runner serializes this to JSON and stores it in agent_run.pending_question_json.
//
// agent-multi-question: a single yield may pose 1-4 independent questions at once
// (Claude Code's AskUserQuestion model), each with its own header/options/mode.
type YieldPayload struct {
	Questions []YieldQuestion `json:"questions"`
}

// YieldQuestion is one independent question with its own options/header/mode.
type YieldQuestion struct {
	Question    string        `json:"question"`
	Header      string        `json:"header,omitempty"`
	Options     []YieldOption `json:"options"`
	MultiSelect bool          `json:"multi_select"`
}

// YieldOption is a single multiple-choice option.
type YieldOption struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// ParsePendingQuestion unmarshals agent_run.pending_question_json into a
// YieldPayload, tolerating the pre-agent-multi-question single-question shape
// ({"question":...,"options":...}) by wrapping it as a one-element Questions
// slice. New rows persist {"questions":[...]}. This keeps in-flight waiting
// runs (paused before the rollout) answerable and reloadable after deploy.
// Returns an error only on malformed JSON.
func ParsePendingQuestion(raw []byte) (YieldPayload, error) {
	var probe struct {
		Questions []YieldQuestion `json:"questions"`
		// Legacy single-question top-level fields (pre-agent-multi-question).
		Question    string        `json:"question"`
		Header      string        `json:"header"`
		Options     []YieldOption `json:"options"`
		MultiSelect bool          `json:"multi_select"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return YieldPayload{}, err
	}
	if len(probe.Questions) == 0 && probe.Question != "" {
		probe.Questions = []YieldQuestion{{
			Question:    probe.Question,
			Header:      probe.Header,
			Options:     probe.Options,
			MultiSelect: probe.MultiSelect,
		}}
	}
	return YieldPayload{Questions: probe.Questions}, nil
}

// yieldError wraps ErrYieldForUserQuestion with the payload.
// Use errors.As(err, &yieldErr) in runner.go to extract.
type yieldError struct {
	Payload YieldPayload
}

func (e *yieldError) Error() string        { return ErrYieldForUserQuestion.Error() }
func (e *yieldError) Is(target error) bool { return target == ErrYieldForUserQuestion }
func (e *yieldError) Unwrap() error        { return ErrYieldForUserQuestion }
