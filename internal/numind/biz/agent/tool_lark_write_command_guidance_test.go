package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/model"
)

// Customer regression (Dev run 359): eight completed analyses never reached
// Feishu because five consecutive pre-execution command corrections exhausted
// the run while the model-visible stdin/file guidance contradicted the hosted
// command catalog.
func TestLarkPersonalWorkspace_Run359WriteCommandContract(t *testing.T) {
	t.Run("model sees one hosted inline JSON contract", func(t *testing.T) {
		schema := string((&larkExecuteTool{}).InputSchema())
		require.NotContains(t, schema, "stdin_json")
		require.Contains(t, schema, "complete inline")
		require.Contains(t, larkHostedExecutionPolicy, "完整内联")
		require.Contains(t, larkHostedExecutionPolicy, "不支持 `stdin_json`")
		require.Contains(t, larkHostedExecutionPolicy, "不支持 `@file`")
	})

	t.Run("ten failures exhaust and the eleventh call never reaches executor", func(t *testing.T) {
		const runID = uint64(359)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		require.Equal(t, 10, larkExecuteMaxCorrectableAttempts)

		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "must-not-run",
			State:       model.FeishuOperationSucceeded,
			Data:        json.RawMessage(`{"ok":true}`),
		}}
		tool := &larkExecuteTool{executor: executor}
		for attempt := 1; attempt < 10; attempt++ {
			result, err := tool.Execute(
				larkPersonalWorkspaceContext(437, runID, fmt.Sprintf("run-359-invalid-%d", attempt)),
				ToolInput(`{"argv":[]}`),
			)
			require.NoError(t, err)
			require.Contains(t, string(result), fmt.Sprintf(`"attempt":%d`, attempt))
			require.Contains(t, string(result), `"max_attempts":10`)
			require.Contains(t, string(result), `"recoverable":true`)
		}

		tenth, err := tool.Execute(
			larkPersonalWorkspaceContext(437, runID, "run-359-invalid-10"),
			ToolInput(`{"argv":[]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(tenth), `"code":"correction_exhausted"`)
		require.Contains(t, string(tenth), `"attempt":10`)
		require.Contains(t, string(tenth), `"recoverable":false`)

		eleventh, err := tool.Execute(
			larkPersonalWorkspaceContext(437, runID, "run-359-invalid-11"),
			ToolInput(`{"argv":["base","+record-list","--base-token","bascnABCDEFG123","--table-id","Tasks"]}`),
		)
		require.NoError(t, err)
		require.Contains(t, string(eleventh), `"code":"correction_exhausted"`)
		require.Empty(t, executor.snapshot())
	})

	t.Run("eight long rows stay Agent authored while non-null stdin is rejected", func(t *testing.T) {
		const runID = uint64(360)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })

		fields := []string{"小红书笔记ID", "笔记标题", "完整分析"}
		rows := make([][]any, 0, 8)
		for index := 1; index <= 8; index++ {
			rows = append(rows, []any{
				fmt.Sprintf("xhs-%d", index),
				fmt.Sprintf("标题 %d", index),
				fmt.Sprintf("第 %d 条独立分析：%s", index, strings.Repeat("长内容", 512)),
			})
		}
		payload, err := json.Marshal(map[string]any{"fields": fields, "rows": rows})
		require.NoError(t, err)
		argv := []string{
			"base", "+record-batch-create",
			"--base-token", "bascnABCDEFG123",
			"--table-id", "Tasks",
			"--json", string(payload),
		}
		command, err := feishu.NewCommandCatalog().Normalize(argv, nil)
		require.NoError(t, err)
		require.Equal(t, "base +record-batch-create", command.Path)
		require.Contains(t, command.Argv, string(payload), "the Agent-authored payload must remain one complete argv value")

		executor := &fakeLarkExecutor{result: &feishu.OperationResult{
			OperationID: "must-not-run-with-stdin",
			State:       model.FeishuOperationSucceeded,
			Data:        json.RawMessage(`{"ok":true}`),
		}}
		input, err := json.Marshal(map[string]any{
			"argv":       argv,
			"stdin_json": map[string]any{"must": "be rejected before executor"},
		})
		require.NoError(t, err)
		result, err := (&larkExecuteTool{executor: executor}).Execute(
			larkPersonalWorkspaceContext(437, runID, "run-359-non-null-stdin"),
			ToolInput(input),
		)
		require.NoError(t, err)
		require.Contains(t, string(result), `"code":"unsupported_stdin_json"`)
		require.Contains(t, string(result), `"feishu_called":false`)
		require.Empty(t, executor.snapshot())
	})
}
