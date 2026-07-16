package externalaction

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalToolResultIsFixedStructuredAndNonSensitive(t *testing.T) {
	for _, tc := range []struct {
		outcome TerminalOutcome
		state   string
	}{
		{outcome: TerminalOutcomeCancelled, state: "cancelled"},
		{outcome: TerminalOutcomeFailed, state: "failed"},
		{outcome: TerminalOutcomeUnknown, state: "unknown"},
	} {
		t.Run(string(tc.outcome), func(t *testing.T) {
			result, err := TerminalToolResult(tc.outcome)
			require.NoError(t, err)
			assert.JSONEq(t, `{"ok":false,"operation_state":"`+tc.state+`","error":{"code":"`+string(tc.outcome)+`"`+
				`,"message":"`+terminalMessageForTest(tc.outcome)+`"}}`, string(result))
			assert.NotContains(t, string(result), "https://")
			assert.NotContains(t, string(result), "device_code")
			assert.NotContains(t, string(result), "scope")
		})
	}

	_, err := TerminalToolResult(TerminalOutcome("future_unreviewed_outcome"))
	require.Error(t, err)
}

func terminalMessageForTest(outcome TerminalOutcome) string {
	switch outcome {
	case TerminalOutcomeCancelled:
		return "飞书操作已取消，未执行。"
	case TerminalOutcomeFailed:
		return "飞书操作未完成，请重新发起原任务。"
	default:
		return "飞书操作结果未知，请先在飞书中核对后再试。"
	}
}
