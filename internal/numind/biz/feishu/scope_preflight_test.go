package feishu

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Customer regression (Dev runs 220/221): the real lark-cli 1.0.68 scope
// check partitions the already-granted read scope and missing write scope
// before docs +update is invoked. The old runtime skipped this contract and
// learned about the permission only after starting a write process.
func TestControlledScopePreflight_RealDocsUpdateMissingWriteScope(t *testing.T) {
	const output = `{"ok":false,"granted":["docx:document:readonly"],"missing":["docx:document:write_only"],"suggestion":"not returned to callers"}`
	body := fmt.Sprintf(`
if [ "$#" -ne 5 ] || [ "$1" != "auth" ] || [ "$2" != "check" ] || [ "$3" != "--scope" ] || [ "$4" != "docx:document:readonly docx:document:write_only" ] || [ "$5" != "--json" ]; then
  exit 97
fi
printf '%%s' %s
exit 1
`, shellQuoteForControlledTest(output))
	preflight := NewControlledScopePreflight(controlledRunner(writeControlledFakeBinary(t, body)))

	got, err := preflight.Check(context.Background(), controlledTestHome(t), []string{
		"docx:document:write_only",
		"docx:document:readonly",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"docx:document:readonly"}, got.Granted)
	require.Equal(t, []string{"docx:document:write_only"}, got.Missing)
}

func TestControlledScopePreflight_RealGrantedReadScope(t *testing.T) {
	const output = `{"ok":true,"granted":["docx:document:readonly"],"missing":[]}`
	body := fmt.Sprintf(`
if [ "$#" -ne 5 ] || [ "$1" != "auth" ] || [ "$2" != "check" ] || [ "$3" != "--scope" ] || [ "$4" != "docx:document:readonly" ] || [ "$5" != "--json" ]; then
  exit 97
fi
printf '%%s' %s
`, shellQuoteForControlledTest(output))
	preflight := NewControlledScopePreflight(controlledRunner(writeControlledFakeBinary(t, body)))

	got, err := preflight.Check(
		context.Background(), controlledTestHome(t), []string{"docx:document:readonly"},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"docx:document:readonly"}, got.Granted)
	require.Empty(t, got.Missing)
}

func TestControlledScopePreflight_RejectsAmbiguousCLIContracts(t *testing.T) {
	tests := []struct {
		name   string
		output string
		stderr string
		exit   int
	}{
		{name: "exit_zero_with_missing", output: `{"ok":false,"granted":[],"missing":["docx:document:readonly"]}`},
		{name: "exit_one_with_ok", output: `{"ok":true,"granted":["docx:document:readonly"],"missing":[]}`, exit: 1},
		{name: "stderr", output: `{"ok":true,"granted":["docx:document:readonly"],"missing":[]}`, stderr: "warning"},
		{name: "duplicate_field", output: `{"ok":true,"ok":true,"granted":["docx:document:readonly"],"missing":[]}`},
		{name: "unknown_field", output: `{"ok":true,"granted":["docx:document:readonly"],"missing":[],"debug":"secret"}`},
		{name: "trailing_value", output: `{"ok":true,"granted":["docx:document:readonly"],"missing":[]} {}`},
		{name: "missing_array", output: `{"ok":true,"granted":["docx:document:readonly"]}`},
		{name: "null_array", output: `{"ok":true,"granted":["docx:document:readonly"],"missing":null}`},
		{name: "overlap", output: `{"ok":false,"granted":["docx:document:readonly"],"missing":["docx:document:readonly"]}`, exit: 1},
		{name: "incomplete_partition", output: `{"ok":false,"granted":[],"missing":[]}`, exit: 1},
		{name: "unrequested_scope", output: `{"ok":false,"granted":[],"missing":["docx:document:write_only"]}`, exit: 1},
		{name: "unknown_scope", output: `{"ok":false,"granted":[],"missing":["im:message"]}`, exit: 1},
		{name: "unexpected_exit", output: `{"ok":false,"granted":[],"missing":["docx:document:readonly"]}`, exit: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf("printf '%%s' %s\nprintf '%%s' %s >&2\nexit %d",
				shellQuoteForControlledTest(test.output), shellQuoteForControlledTest(test.stderr), test.exit)
			preflight := NewControlledScopePreflight(controlledRunner(writeControlledFakeBinary(t, body)))
			got, err := preflight.Check(
				context.Background(), controlledTestHome(t), []string{"docx:document:readonly"},
			)
			require.Error(t, err)
			require.Nil(t, got)
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestControlledScopePreflight_RejectsUnregisteredScopeInputsBeforeProcess(t *testing.T) {
	marker := controlledTestHome(t) + "/invoked"
	body := fmt.Sprintf("touch %s", shellQuoteForControlledTest(marker))
	preflight := NewControlledScopePreflight(controlledRunner(writeControlledFakeBinary(t, body)))
	home := controlledTestHome(t)
	tests := [][]string{
		nil,
		{},
		{"docx:document:readonly", "docx:document:readonly"},
		{"im:message"},
		// Both scopes are registered individually, but this exact set belongs to
		// no catalog command and therefore cannot be supplied by the model.
		{"docx:document:create", "docx:document:readonly"},
	}
	for _, scopes := range tests {
		got, err := preflight.Check(context.Background(), home, scopes)
		require.Error(t, err)
		require.Nil(t, got)
	}
	require.NoFileExists(t, marker)
}

func TestControlledScopePreflight_BoundsExecutionAndOutput(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		runner := controlledRunner(writeControlledFakeBinary(t, "sleep 10"))
		runner.timeout = 25 * time.Millisecond
		preflight := NewControlledScopePreflight(runner)
		started := time.Now()
		got, err := preflight.Check(
			context.Background(), controlledTestHome(t), []string{"docx:document:readonly"},
		)
		require.Error(t, err)
		require.Nil(t, got)
		require.Less(t, time.Since(started), 3*time.Second)
	})

	t.Run("stdout_limit", func(t *testing.T) {
		body := fmt.Sprintf("head -c %d /dev/zero | tr '\\000' x", ControlledLarkCLIMaxStdoutBytes+1)
		preflight := NewControlledScopePreflight(controlledRunner(writeControlledFakeBinary(t, body)))
		got, err := preflight.Check(
			context.Background(), controlledTestHome(t), []string{"docx:document:readonly"},
		)
		require.Error(t, err)
		require.Nil(t, got)
		require.True(t, strings.Contains(err.Error(), "output") || strings.Contains(err.Error(), "process"))
	})
}
