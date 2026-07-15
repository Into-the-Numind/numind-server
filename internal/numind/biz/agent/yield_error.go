package agent

import (
	"bytes"
	"encoding/json"
	"errors"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/externalaction"
)

// ErrYieldForUserQuestion is the sentinel error returned by ask_user_question
// tool.Execute() to signal the runner that the run should yield (pause) until
// the user answers via POST /v1/agent-runs/:id/answer.
var ErrYieldForUserQuestion = errors.New("agent: yield for user question")

// ExternalActionPayload is the external-action transport shared by runner
// yields, SSE events, and session snapshots.
type ExternalActionPayload = stream.ExternalActionPayload

func hasPendingExternalAction(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// ParsePendingExternalAction accepts only the restart-safe external-action
// identity persisted on agent_run. Unknown fields fail closed so a transient
// URL, credential, device code, or future unreviewed field is never replayed.
func ParsePendingExternalAction(raw []byte) (ExternalActionPayload, error) {
	persisted, err := externalaction.Parse(raw)
	if err != nil {
		return ExternalActionPayload{}, err
	}
	return ExternalActionPayload{
		Provider:    persisted.Provider,
		OperationID: persisted.OperationID,
		SessionID:   persisted.SessionID,
		ToolCallID:  persisted.ToolCallID,
		Phase:       persisted.Phase,
		ExpiresAt:   persisted.ExpiresAt,
	}, nil
}

// PauseType classifies why a run yielded, so the frontend can pick the right
// pause UI. It is carried on YieldPayload and surfaced over SSE
// (QuestionPromptPayload). The empty string is treated as PauseTypeQuestion for
// backward compatibility with rows/payloads persisted before feishu-integration.
//
// IMPORTANT: this is NOT a new TerminalReason/LoopEvent enum — those are fixed
// compile-time arrays ([14]TerminalReason / [21]LoopEvent). An auth pause still
// terminates the loop as TerminalWaitingForUserChoice via LoopEventAskUserPaused;
// PauseType only refines the rendering of that single waiting state.
const (
	// PauseTypeQuestion is an ordinary ask_user_question pause (default).
	PauseTypeQuestion = "question"
	// PauseTypeAuth is a third-party authorization pause (e.g. Feishu OAuth):
	// the frontend renders an authorization card from AuthURL instead of an
	// options/free-text question card.
	PauseTypeAuth = "auth"
)

// YieldPayload is the structured question payload carried by yieldError.
// The runner serializes this to JSON and stores it in agent_run.pending_question_json.
//
// agent-multi-question: a single yield may pose 1-4 independent questions at once
// (Claude Code's AskUserQuestion model), each with its own header/options/mode.
//
// feishu-integration: PauseType/AuthURL extend the payload to support
// authorization pauses (PauseTypeAuth) without adding a TerminalReason/LoopEvent.
// Both are omitempty so an ordinary question yield serializes identically to the
// pre-feishu shape (backward compatible). For an auth pause the runner-persisted
// pending_question_json carries them, and the SSE question_prompt event mirrors
// them so the streaming frontend (T13) can render an authorization card.
type YieldPayload struct {
	Questions []YieldQuestion `json:"questions"`

	// ExternalAction is the independent external-wait branch of a yield. It is
	// excluded from question JSON; runners persist its sanitized projection via
	// store.IExternalActionWriter and emit it as external_action instead.
	ExternalAction *ExternalActionPayload `json:"-"`

	// PauseType classifies the pause: "question" (default/empty) or "auth".
	PauseType string `json:"pause_type,omitempty"`

	// AuthURL is the third-party authorization URL, set only when PauseType=auth.
	AuthURL string `json:"auth_url,omitempty"`
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
		// feishu-integration: pause classification + auth URL (round-tripped so
		// the reload/non-stream path sees the same shape the runner persisted).
		PauseType string `json:"pause_type"`
		AuthURL   string `json:"auth_url"`
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
	return YieldPayload{
		Questions: probe.Questions,
		PauseType: probe.PauseType,
		AuthURL:   probe.AuthURL,
	}, nil
}

// yieldError wraps ErrYieldForUserQuestion with the payload.
// Use errors.As(err, &yieldErr) in runner.go to extract.
type yieldError struct {
	Payload YieldPayload
}

func (e *yieldError) Error() string        { return ErrYieldForUserQuestion.Error() }
func (e *yieldError) Is(target error) bool { return target == ErrYieldForUserQuestion }
func (e *yieldError) Unwrap() error        { return ErrYieldForUserQuestion }
