package agent

import (
	"encoding/json"
	"fmt"
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
