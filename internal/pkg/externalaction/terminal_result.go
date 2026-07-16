package externalaction

import (
	"encoding/json"
	"fmt"
)

// TerminalOutcome is the fixed, non-sensitive result of ending a Feishu
// external action without completing its original command. It is deliberately
// closed: callers cannot turn an arbitrary provider error into an Agent tool
// result that will be persisted in a transcript.
type TerminalOutcome string

const (
	// TerminalOutcomeCancelled means the user cancelled the operation before a
	// confirmation-gated Feishu write started.
	TerminalOutcomeCancelled TerminalOutcome = "feishu_operation_cancelled"
	// TerminalOutcomeFailed means the operation is known not to have completed.
	// It is safe to ask the user to start a fresh task, unlike an unknown write.
	TerminalOutcomeFailed TerminalOutcome = "feishu_operation_failed"
	// TerminalOutcomeUnknown means a started Feishu write was fenced while its
	// remote result could not be proved. It must never be rendered as success or
	// a user-requested cancellation.
	TerminalOutcomeUnknown TerminalOutcome = "feishu_operation_result_unknown"
)

// TerminalToolResult builds the sole transcript-safe error payload for one
// terminal external action. It contains no URL, device code, scopes, argv,
// account identifier, or raw provider output.
func TerminalToolResult(outcome TerminalOutcome) (json.RawMessage, error) {
	var state, message string
	switch outcome {
	case TerminalOutcomeCancelled:
		state = "cancelled"
		message = "飞书操作已取消，未执行。"
	case TerminalOutcomeFailed:
		state = "failed"
		message = "飞书操作未完成，请重新发起原任务。"
	case TerminalOutcomeUnknown:
		state = "unknown"
		message = "飞书操作结果未知，请先在飞书中核对后再试。"
	default:
		return nil, fmt.Errorf("external action terminal outcome is not allowlisted")
	}
	encoded, err := json.Marshal(struct {
		OK             bool   `json:"ok"`
		OperationState string `json:"operation_state"`
		Error          struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		OK:             false,
		OperationState: state,
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: string(outcome), Message: message},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal external action terminal result: %w", err)
	}
	return json.RawMessage(encoded), nil
}
