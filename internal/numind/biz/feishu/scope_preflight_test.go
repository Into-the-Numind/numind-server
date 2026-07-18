package feishu

import (
	"context"
	"fmt"
	"testing"

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
