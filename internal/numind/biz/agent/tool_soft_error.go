package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// softToolError returns an LLM-readable error payload with a nil Go error.
//
// Eino v0.8.13 has no tool-error→tool-message hook: a non-nil Go error from a
// tool's Execute becomes a NodeRunError that TERMINATES the whole agent run
// (observed as dev runs 136/137, state_reason=model_error). Model-input-derived
// errors (unmarshal failures, missing/invalid fields) and recoverable runtime
// failures (retrieval/upload/store outages) must therefore be returned as a
// successful ToolResult whose JSON body carries the error, so the LLM sees it
// and self-corrects — the same contract as web_search.returnSoftError and
// Claude Code's is_error tool_result. Reserve non-nil Go errors for the yield
// pause mechanism; ctx cancellation is handled at the Eino framework layer,
// never in the tool.
func softToolError(tool, format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(map[string]string{"error": "ERROR: " + tool + ": " + msg})
	return ToolResult(out), nil
}

// softToolErrorMessage reports whether output is a SOFT tool error — a ToolResult
// returned with a nil Go error whose JSON body carries the "ERROR: " contract.
// It reads ONLY the dedicated "error" field that softToolError and every tool's
// returnSoftError helper set (e.g. image_gen / web_search / web_fetch / file_read /
// ask_user_question). That field is populated EXCLUSIVELY on the soft-error path,
// so a successful result whose content merely contains the text "ERROR:" never
// false-positives. Returns the message and true when a soft error is detected.
//
// Used by the Eino adapter to narrate StateError (not StateResult) for soft
// failures: a nil Go error keeps the ReAct loop alive, but the UI must show
// failure, not a false "✓ done" badge (customer-reported, dev run 169).
//
// We deliberately read ONLY the "error" field, never every string field: scanning
// content/body fields would risk false-positiving a tool that legitimately returns
// text starting with "ERROR:" (a fetched web page, a read file, bash output).
func softToolErrorMessage(output string) (string, bool) {
	s := strings.TrimSpace(output)
	if !strings.HasPrefix(s, "{") {
		return "", false
	}
	var obj struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return "", false
	}
	if strings.HasPrefix(obj.Error, "ERROR: ") {
		return obj.Error, true
	}
	return "", false
}
