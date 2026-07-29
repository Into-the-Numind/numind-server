package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/middleware"
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
		require.Contains(t, larkHostedExecutionPolicy, "`@file`")
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

func TestLangfuseLarkSkillReadContainsOnlySafeReferenceEvidence(t *testing.T) {
	const (
		runID           = uint64(359)
		requested       = "lark-base-record-batch-create.md"
		resolved        = "references/lark-base-record-batch-create.md"
		secretContent   = "https://open.feishu.cn/base/customer-secret-note-body"
		secretCursor    = "cursor-customer-secret"
		secretReceipt   = "receipt-customer-secret"
		secretReference = "references/customer-secret-never-log.md"
	)
	events := capturePipelineLangfuseEvents(t)
	ctx := WithRunID(langfuse.WithTrace(context.Background(), "run-359-skill-reference"), runID)
	tool := &larkSkillReadTool{executor: &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill:      "lark-base",
		Path:       resolved,
		Content:    secretContent,
		References: []string{secretReference},
		Receipt:    secretReceipt,
	}}}

	result, err := tool.Execute(ctx, ToolInput(fmt.Sprintf(
		`{"skill":"lark-base","reference":%q,"cursor":%q}`,
		requested,
		secretCursor,
	)))

	require.NoError(t, err)
	require.Contains(t, string(result), secretContent, "tool behavior must remain unchanged")
	created := findPipelineSpanEvent(t, *events, "span-create", "tool.lark_skill_read.execute")
	updated := findPipelineSpanUpdate(t, *events, created.ID)
	assert.Equal(t, map[string]any{
		"run_id":              runID,
		"skill":               "lark-base",
		"requested_reference": requested,
	}, created.Input)
	output, ok := updated.Output.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, output["ok"])
	assert.Equal(t, resolved, output["resolved_path"])
	assert.Equal(t, 1, output["page_count"])
	assert.Equal(t, pipelineToolTraceNoError, output["error_class"])
	assert.Contains(t, output, "duration_ms")

	encoded := pipelineEventsJSON(t, *events)
	for _, secret := range []string{secretContent, secretCursor, secretReceipt, secretReference} {
		assert.NotContains(t, encoded, secret)
	}
	for _, forbiddenKey := range []string{"content", "cursor", "receipt", "references"} {
		assert.NotContains(t, encoded, `"`+forbiddenKey+`"`)
	}
}

func TestLangfuseLarkSkillReadRejectsUntrustedReferenceWithoutLoggingIt(t *testing.T) {
	const runID = uint64(365)
	secretSkill := "customer-secret-skill"
	secretReference := "customer-secret-reference.md"
	events := capturePipelineLangfuseEvents(t)
	ctx := WithRunID(langfuse.WithTrace(context.Background(), "run-359-invalid-skill-reference"), runID)

	result, err := (&larkSkillReadTool{executor: &fakeSkillReadExecutor{err: feishu.ErrSkillReadInvalid}}).Execute(
		ctx,
		ToolInput(fmt.Sprintf(`{"skill":%q,"reference":%q}`, secretSkill, secretReference)),
	)

	require.NoError(t, err)
	require.Contains(t, string(result), `"code":"invalid_skill_input"`)
	created := findPipelineSpanEvent(t, *events, "span-create", "tool.lark_skill_read.execute")
	assert.Equal(t, map[string]any{
		"run_id":              runID,
		"skill":               "invalid",
		"requested_reference": "invalid",
	}, created.Input)
	encoded := pipelineEventsJSON(t, *events)
	assert.NotContains(t, encoded, secretSkill)
	assert.NotContains(t, encoded, secretReference)
}

func TestLarkExecuteLangfuseRecordsSafePreExecutionEvidence(t *testing.T) {
	const (
		traceRunID   = uint64(361)
		noTraceRunID = uint64(362)
		baseToken    = "bascnCustomerSecretABC123"
		tableID      = "tblCustomerSecretABC123"
		noteBody     = "customer-secret-note-body"
	)
	for _, runID := range []uint64{traceRunID, noTraceRunID} {
		larkExecuteRetryClearRun(runID)
		runID := runID
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
	}
	payload, err := json.Marshal(map[string]any{
		"fields": []string{"小红书笔记ID", "笔记正文", "完整分析"},
		"rows":   [][]any{{"xhs-1", noteBody}},
	})
	require.NoError(t, err)
	input, err := json.Marshal(map[string]any{"argv": []string{
		"base", "+record-batch-create",
		"--base-token", baseToken,
		"--table-id", tableID,
		"--json", string(payload),
	}})
	require.NoError(t, err)
	executor := &fakeLarkExecutor{}
	events := capturePipelineLangfuseEvents(t)
	traceCtx := middleware.NewContextWithUserID(
		langfuse.WithTrace(context.Background(), "run-359-execute-validation"),
		437,
	)
	traceCtx = WithToolCallID(WithRunID(traceCtx, traceRunID), "run-359-safe-validation")

	tracedResult, err := (&larkExecuteTool{executor: executor}).Execute(traceCtx, ToolInput(input))

	require.NoError(t, err)
	require.Contains(t, string(tracedResult), `"code":"command_validation"`)
	require.Empty(t, executor.snapshot())
	created := findPipelineSpanEvent(t, *events, "span-create", "tool.lark_execute.execute")
	updated := findPipelineSpanUpdate(t, *events, created.ID)
	assert.Equal(t, map[string]any{
		"run_id":        traceRunID,
		"command_class": "base +record-batch-create",
	}, created.Input)
	output, ok := updated.Output.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 1, output["attempt"])
	assert.Equal(t, 10, output["max_attempts"])
	assert.Equal(t, false, output["feishu_called"])
	assert.Equal(t, "command_validation", output["error_class"])

	encoded := pipelineEventsJSON(t, *events)
	for _, secret := range []string{baseToken, tableID, noteBody, string(payload)} {
		assert.NotContains(t, encoded, secret)
	}
	for _, forbiddenKey := range []string{"argv", "stdin_json", "base_token", "table_id", "json"} {
		assert.NotContains(t, encoded, `"`+forbiddenKey+`"`)
	}

	noTraceResult, err := (&larkExecuteTool{executor: &fakeLarkExecutor{}}).Execute(
		larkPersonalWorkspaceContext(437, noTraceRunID, "run-359-no-trace-validation"),
		ToolInput(input),
	)
	require.NoError(t, err)
	assert.JSONEq(t, string(tracedResult), string(noTraceResult), "Langfuse absence must not change business behavior")
}

func TestLarkExecuteLangfuseNeverRecordsStdinOrProviderErrors(t *testing.T) {
	t.Run("legacy stdin secret", func(t *testing.T) {
		const runID = uint64(363)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		secret := "stdin-customer-secret"
		events := capturePipelineLangfuseEvents(t)
		ctx := middleware.NewContextWithUserID(
			langfuse.WithTrace(context.Background(), "run-359-stdin-rejected"),
			437,
		)
		ctx = WithToolCallID(WithRunID(ctx, runID), "run-359-stdin-rejected")
		input, err := json.Marshal(map[string]any{
			"argv": []string{
				"base", "+record-list",
				"--base-token", "bascnCustomerSecretABC123",
				"--table-id", "tblCustomerSecretABC123",
			},
			"stdin_json": map[string]string{"secret": secret},
		})
		require.NoError(t, err)

		result, err := (&larkExecuteTool{executor: &fakeLarkExecutor{}}).Execute(ctx, ToolInput(input))

		require.NoError(t, err)
		require.Contains(t, string(result), `"code":"unsupported_stdin_json"`)
		assert.NotContains(t, pipelineEventsJSON(t, *events), secret)
	})

	t.Run("raw provider error", func(t *testing.T) {
		const runID = uint64(364)
		larkExecuteRetryClearRun(runID)
		t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
		secretError := "provider leaked customer-secret-error-body"
		events := capturePipelineLangfuseEvents(t)
		ctx := middleware.NewContextWithUserID(
			langfuse.WithTrace(context.Background(), "run-359-provider-error"),
			437,
		)
		ctx = WithToolCallID(WithRunID(ctx, runID), "run-359-provider-error")

		result, err := (&larkExecuteTool{executor: &fakeLarkExecutor{err: errors.New(secretError)}}).Execute(
			ctx,
			ToolInput(`{"argv":["base","+record-list","--base-token","bascnCustomerSecretABC123","--table-id","tblCustomerSecretABC123"]}`),
		)

		require.NoError(t, err)
		require.NotContains(t, string(result), secretError)
		created := findPipelineSpanEvent(t, *events, "span-create", "tool.lark_execute.execute")
		updated := findPipelineSpanUpdate(t, *events, created.ID)
		output, ok := updated.Output.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "unknown", output["feishu_called"])
		assert.Equal(t, "operation_error", output["error_class"])
		assert.NotContains(t, pipelineEventsJSON(t, *events), secretError)
	})
}

func TestLarkExecuteLangfuseTreatsDurableWaitAsExpectedControlFlow(t *testing.T) {
	const runID = uint64(366)
	larkExecuteRetryClearRun(runID)
	t.Cleanup(func() { larkExecuteRetryClearRun(runID) })
	events := capturePipelineLangfuseEvents(t)
	ctx := middleware.NewContextWithUserID(
		langfuse.WithTrace(context.Background(), "run-359-durable-wait"),
		437,
	)
	ctx = WithToolCallID(WithRunID(ctx, runID), "run-359-durable-wait")
	executor := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: "op-run-359-wait",
		State:       model.FeishuOperationWaitingUserAuth,
		Action: &feishu.OperationAction{
			Provider:    "lark",
			OperationID: "op-run-359-wait",
			SessionID:   "session-run-359-wait",
			Phase:       model.FeishuAuthPhaseUserAuth,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}

	result, err := (&larkExecuteTool{executor: executor}).Execute(
		ctx,
		ToolInput(`{"argv":["base","+record-list","--base-token","bascnABCDEFG123","--table-id","Tasks"]}`),
	)

	require.Nil(t, result)
	var yielded *yieldError
	require.ErrorAs(t, err, &yielded)
	created := findPipelineSpanEvent(t, *events, "span-create", "tool.lark_execute.execute")
	updated := findPipelineSpanUpdate(t, *events, created.ID)
	output, ok := updated.Output.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, output["feishu_called"])
	assert.Equal(t, pipelineToolTraceNoError, output["error_class"])
	assert.Empty(t, updated.StatusMessage)
}
