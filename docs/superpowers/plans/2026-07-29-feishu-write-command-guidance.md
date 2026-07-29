# Feishu Write Command Guidance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Agents one correct Feishu Base batch-write contract, ten bounded correction attempts, exact-reference evidence, and safe command diagnostics while keeping the Agent responsible for the complete inline JSON payload.

**Architecture:** Keep the existing `lark_skill_read → lark_execute → CommandCatalog → OperationService` path. Remove `stdin_json` from the model protocol, retain null-only rolling compatibility at decode time, add a hosted inline-JSON override, make catalog validation directly correctable, update Agent 1 to read two exact references, and attach scalar-only Langfuse spans.

**Tech Stack:** Go 1.24, Eino tool schemas, existing Feishu Command Catalog, Langfuse spans, testify, checked-in Agent definition contracts.

---

## File responsibility map

| File | Responsibility in this change |
|---|---|
| `internal/numind/biz/agent/tool_lark_write_command_guidance_test.go` | Dedicated Dev run #359 customer regression and trace safety tests |
| `internal/numind/biz/agent/tool_lark_retry_budget.go` | Single correction-limit constant and per-run correction state |
| `internal/numind/biz/agent/tool_lark_execute.go` | Model input boundary, catalog correction path, safe execution span |
| `internal/numind/biz/agent/tool_lark_skill_read.go` | Hosted policy, fixed model errors, safe exact-reference span |
| `internal/numind/biz/feishu/command_catalog.go` | Inline transport defense, safe command class and validation hints |
| `internal/numind/biz/feishu/command_catalog_test.go` | Batch shape, indirection, risk and safe-hint tests |
| `docs/agent-definitions/three-agent-feishu-pipeline/agent-1-runtime-contract.md` | Exact pre-write reference sequence |
| `docs/agent-definitions/three-agent-feishu-pipeline/agent-1-system-prompt.md` | Deterministic composed Agent 1 prompt |
| `docs/agent-definitions/three-agent-feishu-pipeline/manifest.json` | Updated Agent 1 prompt digest |
| `internal/numind/biz/skill/three_agent_definition_contract_test.go` | Prompt composition and exact-reference contract |
| `internal/numind/biz/agent/three_agent_pipeline_workflow_contract_test.go` | Scripted call-order regression |

Dependency order is acyclic: Task 1 is tests only; Tasks 2–3 establish the tool/catalog contract; Task 4 consumes that contract in Agent instructions; Task 5 observes the settled behavior; Task 6 verifies all tasks together.

### Task 1: Commit the failing Dev run #359 regression first

**Files:**
- Create: `internal/numind/biz/agent/tool_lark_write_command_guidance_test.go`

- [ ] **Step 1: Add a customer regression that encodes the broken contract**

Create a same-package test using existing `fakeLarkExecutor`, `larkPersonalWorkspaceContext`, and `capturePipelineLangfuseEvents` helpers:

```go
package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/model"
)

func TestLarkPersonalWorkspace_Run359WriteCommandContract(t *testing.T) {
	const runID = uint64(359)
	larkExecuteRetryClearRun(runID)
	t.Cleanup(func() { larkExecuteRetryClearRun(runID) })

	schema := string((&larkExecuteTool{}).InputSchema())
	require.NotContains(t, schema, "stdin_json")
	require.Equal(t, 10, larkExecuteMaxCorrectableAttempts)
	require.Contains(t, larkHostedExecutionPolicy, "完整内联")
	require.Contains(t, larkHostedExecutionPolicy, "不支持 stdin_json")
	require.Contains(t, larkHostedExecutionPolicy, "不支持 @file")

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
		require.Contains(t, string(result), `"recoverable":true`)
	}
	tenth, err := tool.Execute(
		larkPersonalWorkspaceContext(437, runID, "run-359-invalid-10"),
		ToolInput(`{"argv":[]}`),
	)
	require.NoError(t, err)
	require.Contains(t, string(tenth), `"code":"correction_exhausted"`)
	require.Contains(t, string(tenth), `"attempt":10`)

	eleventh, err := tool.Execute(
		larkPersonalWorkspaceContext(437, runID, "run-359-invalid-11"),
		ToolInput(`{"argv":["base","+record-list","--base-token","bascnABCDEFG123","--table-id","Tasks"]}`),
	)
	require.NoError(t, err)
	require.Contains(t, string(eleventh), `"code":"correction_exhausted"`)
	require.Empty(t, executor.snapshot())
}
```

- [ ] **Step 2: Add a long eight-row inline payload and stdin contradiction subtest**

In the same file, construct eight distinct rows with `json.Marshal`, place the encoded JSON in one `argv` element, and assert `CommandCatalog.Normalize` succeeds. Then call `lark_execute` with the same argv plus non-null `stdin_json`, assert `unsupported_stdin_json`, `feishu_called=false`, and zero executor requests.

- [ ] **Step 3: Run the isolated test and prove RED**

Run:

```bash
go test ./internal/numind/biz/agent -run 'TestLarkPersonalWorkspace_Run359WriteCommandContract' -count=1
```

Expected: FAIL because the current Schema exposes `stdin_json`, the hosted policy says five attempts, and the fifth rejection exhausts the budget.

- [ ] **Step 4: Commit only the failing test**

```bash
git add internal/numind/biz/agent/tool_lark_write_command_guidance_test.go
git commit -m "test(qa): reproduce Feishu write command guidance conflict"
```

This must be the first feature-branch commit. Do not include implementation or documentation changes in it.

### Task 2: Make ten attempts and one model input contract

**Files:**
- Modify: `internal/numind/biz/agent/tool_lark_retry_budget.go`
- Modify: `internal/numind/biz/agent/tool_lark_execute.go`
- Modify: `internal/numind/biz/agent/tool_lark_skill_read.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`
- Test: `internal/numind/biz/agent/tool_lark_write_command_guidance_test.go`

- [ ] **Step 1: Change the single correction constant**

```go
const larkExecuteMaxCorrectableAttempts = 10
```

Replace hard-coded model-visible “5 次” text with formatting based on this constant. Do not change success reset, unknown fence, in-flight, or terminal-failure semantics.

- [ ] **Step 2: Remove `stdin_json` from the new model Schema**

Make `InputSchema()` expose only:

```go
"properties":{
    "argv":{"type":"array","minItems":1,"items":{"type":"string"},"description":"Controlled lark-cli argv; JSON bodies must be one complete inline value after --json; no shell, stdin, or file indirection."}
}
```

Keep `required:["argv"]` and `additionalProperties:false`.

- [ ] **Step 3: Add null-only rolling compatibility**

Change decoding to distinguish absent/null/non-null stdin:

```go
var errLarkExecuteStdinUnsupported = errors.New("stdin_json unsupported")

fields, err := decodeStrictLarkToolObject(input, "argv", "stdin_json", "skill_receipts")
// ...
if raw, ok := fields["stdin_json"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
    return larkExecuteInput{}, errLarkExecuteStdinUnsupported
}
```

Do not copy non-null stdin into `larkExecuteInput`. Handle this sentinel before catalog normalization and return `unsupported_stdin_json` with a fixed hint telling the Agent to put the complete JSON in the `--json` argv value.

- [ ] **Step 4: Update fixed model-facing errors**

All new protocol messages must say `lark_execute 只接受 argv` and must not instruct the model to use `stdin_json`. `larkWorkspaceCorrectionExhausted` must format the limit from `larkExecuteMaxCorrectableAttempts`.

- [ ] **Step 5: Update existing retry tests from five to ten**

Use `larkExecuteMaxCorrectableAttempts` in loops and expected counts. Assert:

- attempts `1..9` are recoverable;
- a success on attempt 10 reaches the executor and resets;
- a failure on attempt 10 exhausts;
- call 11 is blocked;
- another run receives a fresh budget.

- [ ] **Step 6: Run focused tests and commit GREEN**

Run:

```bash
go test ./internal/numind/biz/agent -run 'LarkPersonalWorkspace.*(Run359|Correct|Rejected|Malformed)' -count=1
```

Expected: PASS.

Commit:

```bash
git add internal/numind/biz/agent/tool_lark_retry_budget.go internal/numind/biz/agent/tool_lark_execute.go internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/agent/tool_lark_write_command_guidance_test.go
git commit -m "fix(feishu): unify execute input and correction budget"
```

### Task 3: Make hosted inline JSON and catalog feedback authoritative

**Files:**
- Modify: `internal/numind/biz/agent/tool_lark_execute.go`
- Modify: `internal/numind/biz/agent/tool_lark_skill_read.go`
- Modify: `internal/numind/biz/feishu/command_catalog.go`
- Modify: `internal/numind/biz/feishu/command_catalog_test.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`

- [ ] **Step 1: Add the hosted transport override**

Add this semantic content to `larkHostedExecutionPolicy`:

```text
有数托管环境的 JSON 必须作为对应 --json 参数后的一个完整内联 argv；不支持 stdin_json、@file、- 或本地文件/stdin 间接引用。官方技能同时展示内联与 @file 时，只采用内联示例。
```

Update `lark_execute.Description()` with the same shorter rule.

- [ ] **Step 2: Export a bounded safe command class**

Add:

```go
func SafeCommandClass(argv []string) string {
    if path, ok := safeCatalogCommandPath(argv); ok {
        return path
    }
    return "invalid"
}
```

The function must never include tokens after argv index 1.

- [ ] **Step 3: Return every safe catalog hint before operation execution**

Replace the inspect-only local branch with:

```go
if normalizeErr != nil {
    hint, ok := feishu.SafeCommandValidationHint(decoded.Argv, normalizeErr)
    if !ok {
        hint = "命令未通过托管目录校验"
    }
    // consume one correction attempt and return command_validation
}
```

Map `ErrCommandDenied` to `catalog_denied` for trace purposes and `ErrCommandInvalidArgument` to `catalog_validation`. The model-visible result stays a bounded validation correction with `feishu_called=false`.

- [ ] **Step 4: Add catalog table tests**

Cover these exact cases:

```go
tests := []struct {
    name string
    argv []string
    stdin []byte
    wantHint string
}{
    {"file indirection", batchArgv("@batch-create.json"), nil, "file or stdin indirection"},
    {"stdin", validBatchArgv(8), []byte(`{"secret":"never-log"}`), "stdin is not supported"},
    {"bad top level", batchArgv(`[]`), nil, "invalid record batch create json"},
    {"duplicate fields", batchArgv(`{"fields":["标题","标题"],"rows":[["a","b"]]}`), nil, "duplicate"},
    {"row width", batchArgv(`{"fields":["标题","状态"],"rows":[["a"]]}`), nil, "row width"},
}
```

Also assert 8 rows is `RiskWrite`, 21 and 200 rows are `RiskHigh`, and 201 rows is rejected.

- [ ] **Step 5: Run catalog and Agent tests**

Run:

```bash
go test ./internal/numind/biz/feishu -run 'CommandCatalog' -count=1
go test ./internal/numind/biz/agent -run 'LarkPersonalWorkspace.*(Run359|Catalog|Skill)' -count=1
```

Expected: PASS and no executor request for any pre-execution rejection.

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/agent/tool_lark_execute.go internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/feishu/command_catalog.go internal/numind/biz/feishu/command_catalog_test.go
git commit -m "fix(feishu): make inline JSON guidance authoritative"
```

### Task 4: Put both exact references before Agent 1’s first write

**Files:**
- Modify: `docs/agent-definitions/three-agent-feishu-pipeline/agent-1-runtime-contract.md`
- Modify: `docs/agent-definitions/three-agent-feishu-pipeline/agent-1-system-prompt.md`
- Modify: `docs/agent-definitions/three-agent-feishu-pipeline/manifest.json`
- Modify: `internal/numind/biz/skill/three_agent_definition_contract_test.go`
- Modify: `internal/numind/biz/agent/three_agent_pipeline_workflow_contract_test.go`

- [ ] **Step 1: Add failing exact-reference assertions**

Require Agent 1 runtime/final prompts to contain:

```text
lark-base-record-batch-create.md
lark-base-cell-value.md
完整内联 `--json`
```

Add an `assertOrder` contract proving both reference reads precede the first `+record-batch-create`.

- [ ] **Step 2: Add a reference workflow step helper**

```go
func skillReferenceStep(skill, reference string) pipelineModelStep {
    return step(
        "lark_skill_read",
        jsonString(map[string]string{"skill": skill, "reference": reference}),
        `{"ok":true}`,
    )
}
```

Update every scripted Agent 1 scenario that creates records so its steps read both references before the first batch write. Do not add reference receipts to `lark_execute`.

- [ ] **Step 3: Update the runtime contract**

Insert before the first batch-create rule:

```text
每次运行第一次批量创建前，必须分别调用 lark_skill_read 读取 lark-base-record-batch-create.md 和 lark-base-cell-value.md。随后由你自己构造完整内联 --json；不使用 stdin_json、@file 或后台格式转换。
```

- [ ] **Step 4: Deterministically rebuild Agent 1 prompt and digest**

Compose `agent-1-system-prompt.md` as:

```text
trimRight(agent-1-runtime-contract.md) + threeAgentSeparator + patchedPrompt1
```

Recalculate SHA-256 and update only Agent 1 `prompt_sha256` in `manifest.json`. Do not modify the business SSOT or Agent 2/3 prompt hashes.

- [ ] **Step 5: Run definition and workflow contracts**

Run:

```bash
go test ./internal/numind/biz/skill -run 'TestThreeAgentDefinitionContract' -count=1
go test ./internal/numind/biz/agent -run 'TestThreeAgentPipelineWorkflow_Agent1' -count=1
```

Expected: PASS; call logs show both exact reference reads before every first batch-create.

- [ ] **Step 6: Commit**

```bash
git add docs/agent-definitions/three-agent-feishu-pipeline/agent-1-runtime-contract.md docs/agent-definitions/three-agent-feishu-pipeline/agent-1-system-prompt.md docs/agent-definitions/three-agent-feishu-pipeline/manifest.json internal/numind/biz/skill/three_agent_definition_contract_test.go internal/numind/biz/agent/three_agent_pipeline_workflow_contract_test.go
git commit -m "fix(agent): require Base write references before batching"
```

### Task 5: Add scalar-only Langfuse evidence

**Files:**
- Modify: `internal/numind/biz/agent/tool_lark_skill_read.go`
- Modify: `internal/numind/biz/agent/tool_lark_execute.go`
- Test: `internal/numind/biz/agent/tool_lark_write_command_guidance_test.go`
- Reference: `.claude/rules/ai-service.md`

- [ ] **Step 1: Add a safe skill-read span**

Start `tool.lark_skill_read.execute` only with allowlisted scalars:

```go
span := startSafePipelineToolSpan(ctx, "tool.lark_skill_read.execute", map[string]any{
    "run_id":              RunIDFromContext(ctx),
    "skill":               request.Skill,
    "requested_reference": request.Reference,
})
```

End it with `ok`, canonical `resolved_path`, `page_count`, and a fixed `error_class`. Never pass content, cursor, receipt, raw input, or raw error.

- [ ] **Step 2: Add a safe execute span**

Start `tool.lark_execute.execute` with:

```go
map[string]any{
    "run_id":        runID,
    "command_class": feishu.SafeCommandClass(decoded.Argv),
}
```

End every return path with `attempt`, `max_attempts`, `feishu_called`, and fixed `error_class`. Use `false` for pre-execution/waiting/local-only, `true` for explicit success or `BusinessStarted`, and `"unknown"` when the operation layer cannot prove either.

- [ ] **Step 3: Add Langfuse safety tests**

Use `capturePipelineLangfuseEvents`. Assert:

- skill span contains canonical reference;
- execute span contains `base +record-batch-create`, attempt and max=10;
- pre-execution failure has `feishu_called=false`;
- encoded events do not contain sample Base token, table ID, note body, full JSON, URL, stdin secret, cursor, receipt, or raw provider error;
- business behavior is identical when Langfuse is absent.

- [ ] **Step 4: Check the AI service rule**

Verify against `.claude/rules/ai-service.md`:

- existing run trace is reused;
- these are spans, not generations;
- missing Langfuse is a no-op;
- no config is hard-coded;
- no new LLM/provider call is introduced.

- [ ] **Step 5: Run tests and commit**

Run:

```bash
go test ./internal/numind/biz/agent -run 'Run359|Langfuse.*Lark|Lark.*Langfuse' -count=1
```

Expected: PASS with both span create/end events and no forbidden values.

Commit:

```bash
git add internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_execute.go internal/numind/biz/agent/tool_lark_write_command_guidance_test.go
git commit -m "feat(feishu): trace safe write command evidence"
```

### Task 6: Full regression, lint, scope and copy audit

**Files:**
- Modify only if verification finds a defect in files already owned by Tasks 2–5.
- Create: `.ndf/decisions/feishu-write-command-guidance/0001-s5-verification.md`

- [ ] **Step 1: Format and check the diff**

Run:

```bash
gofmt -w internal/numind/biz/agent/tool_lark_retry_budget.go internal/numind/biz/agent/tool_lark_execute.go internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_write_command_guidance_test.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/agent/three_agent_pipeline_workflow_contract_test.go internal/numind/biz/feishu/command_catalog.go internal/numind/biz/feishu/command_catalog_test.go internal/numind/biz/skill/three_agent_definition_contract_test.go
git diff --check
```

Expected: no output from `git diff --check`.

- [ ] **Step 2: Run package tests**

```bash
go test ./internal/numind/biz/feishu -count=1
go test ./internal/numind/biz/agent -count=1
go test ./internal/numind/biz/skill -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the full backend suite and lint**

```bash
go test ./... -count=1
task lint
```

Expected: both exit 0.

- [ ] **Step 4: Audit prohibited scope**

Run:

```bash
git diff develop...HEAD -- config_prod.yaml migrations internal/numind/controller internal/numind/router.go
rg -n "后台.*转换|服务端.*构造.*payload" internal docs/agent-definitions/three-agent-feishu-pipeline
```

Expected: no config, migration, controller, router, or server-side business payload conversion changes.

Compare the final-report section of Agent 1 before/after and assert it is byte-identical; only the pre-write instruction may differ.

- [ ] **Step 5: Perform local spec-compliance and code-quality review**

Because this session may not dispatch subagents without explicit user authorization, perform both reviews locally using:

- `templates/ndf/review-spec-compliance.md`
- `templates/ndf/review-code-quality.md`

Record findings as `path:line: severity — rule-id — problem — fix`. Resolve every P0/P1 and all correctness-relevant P2 before S5.

- [ ] **Step 6: Write S5 verification evidence and commit**

Record commands, exit codes, run #359 acceptance matrix, Langfuse forbidden-value audit, and copy-scope audit in:

`.ndf/decisions/feishu-write-command-guidance/0001-s5-verification.md`

Commit:

```bash
git add .ndf/decisions/feishu-write-command-guidance/0001-s5-verification.md
git commit -m "docs: verify Feishu write command guidance fix"
```

## Plan self-review

- Spec §6.1 correction budget: Tasks 1–2.
- Spec §6.2 stdin contract: Tasks 1–2.
- Spec §6.3 hosted inline transport: Task 3.
- Spec §6.4 exact references and deterministic prompt: Task 4.
- Spec §6.5 catalog correction hints: Task 3.
- Spec §6.6 trace topology and `.claude/rules/ai-service.md`: Task 5.
- Spec §7 normal/error/exhaustion flows: Tasks 1–3.
- Spec §8 customer regression and full test matrix: Tasks 1–6.
- No API, DB, frontend or server-side payload conversion: Task 6 audit.
- No final user-summary copy change: Tasks 4 and 6 audit.
- Function/type consistency: `SafeCommandClass`, `errLarkExecuteStdinUnsupported`, existing `safePipelineToolSpan`, and existing retry helpers have one name throughout.
- Task dependency graph: `1 → 2 → 3 → 4 → 5 → 6`; no cycle.
- Atomicity: each task leaves the repository compiling after its GREEN step; Task 1 intentionally leaves only a committed failing customer test as required by the customer-bug rule.
