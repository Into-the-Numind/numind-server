# Feishu Agent-Led Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Codex-style Feishu runtime where Agents choose allowlisted business actions while the platform guarantees current-user isolation, pre-write scope checks, authorization recovery, actionable results, and exactly-once writes.

**Architecture:** Keep the existing catalog, encrypted HOME, operation state machine, authorization card, and durable Agent resume. Add a strict fixed-version `auth check` adapter used before every write/high-risk business invocation, enrich operation results with safe public failure semantics, and expose a read-only `lark_inspect` tool backed by the same catalog and scope checker. Reads remain execute-first; writes never infer safety from a started business call.

**Tech Stack:** Go 1.24, lark-cli 1.0.68, GORM/MySQL operation state, Eino Agent tools, testify, NDF v3.

---

## File map

- Create `internal/numind/biz/feishu/scope_preflight.go`: strict fixed-version `auth check` process adapter and parser.
- Create `internal/numind/biz/feishu/scope_preflight_test.go`: real-contract and adversarial parser/process tests.
- Create `internal/numind/biz/feishu/inspection.go`: connection/command inspection request and safe response model implemented by `FeishuOperationService`.
- Create `internal/numind/biz/feishu/inspection_test.go`: user, receipt, catalog, scope and leak-boundary tests.
- Create `internal/numind/biz/feishu/tool_result.go`: one safe formatter shared by live and resumed `lark_execute` results.
- Create `internal/numind/biz/feishu/tool_result_test.go`: stable category/schema/redaction tests.
- Create `internal/numind/biz/agent/tool_lark_inspect.go`: read-only Agent tool.
- Modify `internal/numind/biz/feishu/operation_service.go`: preflight ordering, read recovery, persisted business-started evidence and public failure result.
- Modify `internal/numind/biz/feishu/error_classifier.go`: generic read-only missing-scope recovery and stable validation/not-found codes; no started-write replay inference.
- Modify `internal/numind/biz/feishu/controlled_runner.go`: reuse bounded process execution without changing business envelope acceptance.
- Modify `internal/numind/biz/feishu_adapter.go`: compose one scope preflight and inject it into the operation service.
- Modify `internal/numind/biz/feishu_resume_dispatcher.go`: resume Agent with the same safe terminal tool schema for success and failure.
- Modify `internal/numind/biz/agent/tool_lark_execute.go`: use shared formatter and enforce unknown-result stop.
- Modify `internal/numind/biz/agent/tool_lark_retry_budget.go`: category-aware run guard.
- Modify `internal/numind/biz/agent/tool_lark_skill_read.go`: Agent-led hosted policy.
- Modify `internal/numind/biz/agent/factory_platform.go` and `internal/numind/biz/biz.go`: all-or-nothing registration of skill/read/inspect/execute.
- Modify related `_test.go` files for operation, integration, composition, factory, external resume and domain coverage.

### Task 1: Remove the unsafe started-write inference and lock the customer RED

**Files:**
- Modify: `internal/numind/biz/feishu/error_classifier.go`
- Modify: `internal/numind/biz/feishu/error_classifier_test.go`
- Modify: `internal/numind/biz/feishu/operation_service_test.go`

- [ ] **Step 1: Revert only commit `a5e24260`**

Run:

```bash
git revert --no-edit a5e24260
```

Expected: production inference and its GREEN-only operation test are removed; customer commit `1fd0d003` remains in history.

- [ ] **Step 2: Run the original customer test**

```bash
go test ./internal/numind/biz/feishu -run '^TestErrorClassifier_RealDocsUpdateMissingScopeWithoutScopeListStartsRecovery$' -count=1
```

Expected: FAIL because a started write remains unknown. This confirms the branch is back on the safe baseline.

- [ ] **Step 3: Reframe the RED around preflight rather than replay inference**

Replace the test expectation with a new operation-level test named `TestOperationService_DocsUpdateMissingScopeIsFoundBeforeBusinessInvocation`. Its fake preflight returns:

```go
&ScopeCheckResult{
    Granted: []string{"docx:document:readonly"},
    Missing: []string{"docx:document:write_only"},
}
```

Assert `waiting_user_auth`, exact sorted recovery scopes, preflight calls `1`, business runner calls `0`, and persisted operation contains no result ciphertext.

- [ ] **Step 4: Run the new test and verify RED**

```bash
go test ./internal/numind/biz/feishu -run '^TestOperationService_DocsUpdateMissingScopeIsFoundBeforeBusinessInvocation$' -count=1
```

Expected: FAIL to compile because `ScopeCheckResult` and preflight dependency do not exist.

- [ ] **Step 5: Commit the new RED**

```bash
git add internal/numind/biz/feishu/error_classifier_test.go internal/numind/biz/feishu/operation_service_test.go
git commit -m "test(qa): reproduce Feishu write scope preflight gap"
```

### Task 2: Implement the strict fixed-version scope preflight

**Files:**
- Create: `internal/numind/biz/feishu/scope_preflight.go`
- Create: `internal/numind/biz/feishu/scope_preflight_test.go`
- Modify: `internal/numind/biz/feishu/controlled_runner.go`

- [ ] **Step 1: Add real-contract failing tests**

Use executable fixtures for the two observed contracts:

```json
{"ok":true,"granted":["docx:document:readonly"],"missing":[]}
```

with exit `0`, and:

```json
{"ok":false,"granted":["docx:document:readonly"],"missing":["docx:document:write_only"],"suggestion":"ignored"}
```

with exit `1`. Assert exact argv bytes:

```text
auth\0check\0--scope\0docx:document:readonly docx:document:write_only\0--json\0
```

- [ ] **Step 2: Add adversarial failing tests**

Table-test: exit/ok mismatch, stderr present, duplicate JSON fields, unknown fields, trailing object, duplicate scopes, granted/missing overlap, incomplete partition, scope not requested, IM scope, empty request, timeout, stdout limit and invalid HOME. Every case must return an error and no accepted result.

- [ ] **Step 3: Implement the narrow interface and parser**

Create:

```go
type ScopeCheckResult struct {
    Granted []string
    Missing []string
}

type ScopePreflight interface {
    Check(context.Context, string, []string) (*ScopeCheckResult, error)
}

type ControlledScopePreflight struct {
    runner *ControlledLarkCLIRunner
}
```

`Check` canonicalizes a fresh copy of scopes, invokes the pinned runner process with the fixed argv, and strictly decodes only `ok/granted/missing/suggestion`. It never returns suggestion or raw process text.

- [ ] **Step 4: Run contract tests**

```bash
go test ./internal/numind/biz/feishu -run '^TestControlledScopePreflight_' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/numind/biz/feishu/scope_preflight.go internal/numind/biz/feishu/scope_preflight_test.go internal/numind/biz/feishu/controlled_runner.go
git commit -m "feat(feishu): add controlled scope preflight"
```

### Task 3: Put preflight before every write and preserve unknown-write fencing

**Files:**
- Modify: `internal/numind/biz/feishu/operation_service.go`
- Modify: `internal/numind/biz/feishu/operation_service_test.go`
- Modify: `internal/numind/biz/feishu/personal_workspace_integration_test.go`
- Modify: `internal/numind/biz/feishu_adapter.go`
- Modify: `internal/numind/biz/feishu_adapter_test.go`

- [ ] **Step 1: Extend the operation harness with a granted-by-default preflight fake**

The fake must record scopes and calls, return all requested scopes as granted by default, and allow ordered scripted results/errors. Existing tests must continue to reach the business runner without individual edits.

- [ ] **Step 2: Add state-machine REDs**

Add tests for:

```text
missing before write          preflight=1 business=0 waiting_user_auth
auth resume then granted      preflight=2 business=1 succeeded
same missing after auth       preflight=2 business=0 failed, one recovery
preflight protocol failure    preflight=1 business=0 failed, not unknown
high risk                     preflight before confirmation; business after confirm once
preflight granted + timeout   business=1 unknown; no retry
concurrent idempotency key    one logical preflight/business execution
```

- [ ] **Step 3: Inject `ScopePreflight` as a required dependency**

Add `Preflight ScopePreflight` to `OperationServiceDeps` and `preflight ScopePreflight` to the service. Constructor rejects nil. The composition root creates exactly one `ControlledScopePreflight` from the already version-verified controlled runner.

- [ ] **Step 4: Add `checkScopesBeforeWrite`**

Before high-risk confirmation and before `invokeOnce`, for `RiskWrite`/`RiskHigh`:

```go
check, err := s.preflightScopes(operation, persisted)
switch {
case err != nil:
    return s.commitTerminal(..., PublicCodeTemporaryError, nil, false)
case len(check.Missing) > 0:
    return s.startRecoveryAndWait(..., RecoveryUserScope, check.Missing, ..., PublicCodeScopeRequired, "")
default:
    // continue to confirmation/business runner
}
```

The vault callback returns `changed=false`; the business runner is never called from the preflight callback.

- [ ] **Step 5: Preserve the second fence**

Do not change `classifyInvocation` or `failClosedClassification` for started writes. A business timeout, malformed envelope or code-less missing scope after preflight remains `unknown` and is never replayed.

- [ ] **Step 6: Run operation and integration tests**

```bash
go test ./internal/numind/biz/feishu -run 'TestOperationService_|TestPersonalWorkspace_' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/numind/biz/feishu/operation_service.go internal/numind/biz/feishu/operation_service_test.go internal/numind/biz/feishu/personal_workspace_integration_test.go internal/numind/biz/feishu_adapter.go internal/numind/biz/feishu_adapter_test.go
git commit -m "feat(feishu): preflight scopes before workspace writes"
```

### Task 4: Make read recovery generic and return actionable terminal results

**Files:**
- Create: `internal/numind/biz/feishu/tool_result.go`
- Create: `internal/numind/biz/feishu/tool_result_test.go`
- Modify: `internal/numind/biz/feishu/error_classifier.go`
- Modify: `internal/numind/biz/feishu/error_classifier_test.go`
- Modify: `internal/numind/biz/feishu/operation_service.go`
- Modify: `internal/numind/biz/feishu/operation_service_test.go`
- Modify: `internal/numind/biz/agent/tool_lark_execute.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`
- Modify: `internal/numind/biz/feishu_resume_dispatcher.go`
- Modify: `internal/numind/biz/feishu_resume_dispatcher_test.go`

- [ ] **Step 1: Add classifier REDs**

For any registered `RiskRead` command, `authorization/missing_scope` with empty `missing_scopes` must recover using sorted catalog expected scopes. The same empty evidence for `RiskWrite` and `RiskHigh` must remain unknown after invocation start. Add distinct public codes for structured CLI validation and not-found tuples.

- [ ] **Step 2: Define the safe failure model**

Add:

```go
type OperationFailure struct {
    Code            string   `json:"code"`
    Category        string   `json:"category"`
    Retryable       bool     `json:"retryable"`
    BusinessStarted bool     `json:"business_started"`
    RequiredScopes  []string `json:"required_scopes,omitempty"`
}
```

Add `Failure *OperationFailure` to `OperationResult`, and `BusinessStarted bool` to persisted summary JSON. Build failures only from stable public codes and catalog scopes.

- [ ] **Step 3: Implement one shared formatter**

`MarshalLarkToolResult(*OperationResult)` returns exactly:

```json
{"ok":false,"state":"failed","operation_id":"...","failure":{"code":"feishu_not_found","category":"not_found","retryable":false,"business_started":true}}
```

or the success shape with `data`. It rejects waiting/invalid results and never serializes `Action`, Agent IDs, raw errors or URLs.

- [ ] **Step 4: Use the formatter on both paths**

The live `larkExecuteTool` and `WorkspaceResumeDispatcher` must call the same formatter. Dispatcher resumes the Agent for every valid terminal state, not only success. Unknown/cancelled results are safe explanations with `retryable=false`, not another business invocation.

- [ ] **Step 5: Run focused tests**

```bash
go test ./internal/numind/biz/feishu ./internal/numind/biz/agent -run 'Test(ErrorClassifier|MarshalLarkToolResult|LarkExecute|WorkspaceResumeDispatcher)' -count=1
```

Expected: PASS and redaction assertions find no raw message/hint/URL/token/receipt.

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/feishu/tool_result.go internal/numind/biz/feishu/tool_result_test.go internal/numind/biz/feishu/error_classifier.go internal/numind/biz/feishu/error_classifier_test.go internal/numind/biz/feishu/operation_service.go internal/numind/biz/feishu/operation_service_test.go internal/numind/biz/agent/tool_lark_execute.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/feishu_resume_dispatcher.go internal/numind/biz/feishu_resume_dispatcher_test.go
git commit -m "feat(feishu): return actionable workspace outcomes"
```

### Task 5: Add the current-user read-only `lark_inspect` tool

**Files:**
- Create: `internal/numind/biz/feishu/inspection.go`
- Create: `internal/numind/biz/feishu/inspection_test.go`
- Create: `internal/numind/biz/agent/tool_lark_inspect.go`
- Modify: `internal/numind/biz/agent/factory_platform.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`
- Modify: `internal/numind/biz/biz.go`

- [ ] **Step 1: Write Feishu inspection REDs**

`connection` returns only connection state plus docs/base/wiki/drive capability state. `command` accepts argv and current-run receipts, normalizes through Catalog, verifies receipts, checks scopes and returns:

```json
{
  "mode":"command",
  "command_path":"docs +update",
  "domain":"docs",
  "risk":"write",
  "ready":false,
  "granted_scopes":["docx:document:readonly"],
  "missing_scopes":["docx:document:write_only"]
}
```

Assert no operation row and no business runner call.

- [ ] **Step 2: Add boundary REDs**

Reject absent context identity, cross-run receipts, invalid argv, shell prefix other than one literal `lark-cli`, auth/config/whoami, IM, Drive write, unknown scopes and disconnected command inspection. Assert output excludes user ID, generation, app ID, URL, token and receipt.

- [ ] **Step 3: Implement inspection on `FeishuOperationService`**

Use existing accounts/catalog/receipts/vault/preflight dependencies. Inspection never calls recovery, confirmation, operation store create/transition or business runner.

- [ ] **Step 4: Implement Agent tool**

Create `LarkInspector` and `larkInspectTool` with strict schema:

```json
{
  "mode":"connection|command",
  "argv":["docs","+update", "..."],
  "skill_receipts":["opaque"]
}
```

Only command mode accepts argv/receipts. User/run IDs come from context.

- [ ] **Step 5: Register all three tools atomically**

Change factory injection to require reader + inspector + executor. Register in order `lark_skill_read`, `lark_inspect`, `lark_execute`, update metadata/tool-count tests, and update the single composition call in `biz.go`.

- [ ] **Step 6: Run tests and commit**

```bash
go test ./internal/numind/biz/feishu ./internal/numind/biz/agent ./internal/numind/biz -run 'Test.*(Inspect|PlatformToolFactory|WorkspacePublication)' -count=1
git add internal/numind/biz/feishu/inspection.go internal/numind/biz/feishu/inspection_test.go internal/numind/biz/agent/tool_lark_inspect.go internal/numind/biz/agent/factory_platform.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/biz.go
git commit -m "feat(agent): add safe Feishu workspace inspection"
```

### Task 6: Stop blind retries and update the Agent contract

**Files:**
- Modify: `internal/numind/biz/agent/tool_lark_retry_budget.go`
- Modify: `internal/numind/biz/agent/tool_lark_execute.go`
- Modify: `internal/numind/biz/agent/tool_lark_skill_read.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`
- Modify: `internal/numind/biz/agent/external_tool_resume_test.go`

- [ ] **Step 1: Add retry REDs**

Prove:

- `unknown_result` exhausts Feishu business execution for the rest of the Agent run.
- policy/validation correction gets at most one additional call.
- not-found/resource-denied are returned to the model but do not trigger automatic tool-level retry.
- waiting authorization and server-owned resume do not consume the model correction budget.
- a successful operation resets the correctable failure phase but never clears an unknown-result stop.

- [ ] **Step 2: Extend the run guard**

Store a terminal-stop bit and last correctable category in `larkExecuteRetryState`. `larkExecuteRetryBegin` rejects when terminal-stop is set. Mark unknown/cancelled started writes terminal; mark policy/validation correctable once; leave explicit new user turns as new Agent runs.

- [ ] **Step 3: Update hosted policy**

State in plain language:

```text
先执行业务命令；不要每次先检查权限。
平台会在写入前做只读 scope check 并在需要时显示授权卡。
只有用户明确询问连接状态或收到结构化失败时才调用 lark_inspect。
validation/policy 最多修正一次；not_found/resource_denied 询问用户；unknown_result 立即停止且不得换参数重试。
```

Remove the old blanket instruction that all failures should be fixed by changing command/receipts.

- [ ] **Step 4: Run Agent tests and commit**

```bash
go test ./internal/numind/biz/agent -run 'Test.*Lark' -count=1
git add internal/numind/biz/agent/tool_lark_retry_budget.go internal/numind/biz/agent/tool_lark_execute.go internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/agent/external_tool_resume_test.go
git commit -m "fix(agent): stop blind Feishu execution retries"
```

### Task 7: Prove shared behavior across Docs, Base, Wiki, Agents and users

**Files:**
- Modify: `internal/numind/biz/feishu/personal_workspace_integration_test.go`
- Modify: `internal/numind/biz/feishu/operation_service_test.go`
- Modify: `internal/numind/biz/agent/lark_external_resume_integration_test.go`
- Modify: `internal/numind/biz/feishu_adapter_test.go`

- [ ] **Step 1: Add a table-driven domain matrix**

Cover one write command per domain with the catalog-derived scope set:

```text
docs +create / +update
base +base-create / +record-create / +record-update
wiki +space-create / +node-create / +node-move
```

For each: missing preflight means business `0`; granted means business `1`. Drive search bypasses write preflight; Drive writes and IM are rejected.

- [ ] **Step 2: Add same-user multi-Agent and cross-user isolation**

Two Agent run IDs for one user must use the same active generation and separate operations. The same Agent definition under two user contexts must call two distinct vault homes/accounts; receipts cannot cross runs and no scope/capability state crosses users.

- [ ] **Step 3: Add durable resume coverage**

Restart service between waiting auth and completion. After approval, exact encrypted argv is recovered, preflight passes and business runs once. A duplicate browser acknowledgement and a second dispatcher callback do not rerun preflight/business after success.

- [ ] **Step 4: Run integration and race tests**

```bash
go test ./internal/numind/biz/feishu ./internal/numind/biz/agent ./internal/numind/biz -count=1
go test -race ./internal/numind/biz/feishu ./internal/numind/biz/agent -count=1
```

Expected: PASS with no race report.

- [ ] **Step 5: Commit**

```bash
git add internal/numind/biz/feishu/personal_workspace_integration_test.go internal/numind/biz/feishu/operation_service_test.go internal/numind/biz/agent/lark_external_resume_integration_test.go internal/numind/biz/feishu_adapter_test.go
git commit -m "test(feishu): cover Agent-led workspace runtime"
```

### Task 8: S4/S5 gates, atomic merge and Dev acceptance

**Files:**
- Modify: `docs/superpowers/qa/2026-07-18-feishu-resource-discovery-s5-acceptance.md`
- Modify: `.ndf/manifest.yaml`
- Modify: `.ndf-active` stage metadata

- [ ] **Step 1: Format and run focused checks**

```bash
gofmt -w $(git diff --name-only b499dd29...HEAD -- '*.go')
go test ./internal/numind/biz/feishu ./internal/numind/biz/agent ./internal/numind/biz -count=1
```

- [ ] **Step 2: Run repository gates**

```bash
PATH="$(go env GOPATH)/bin:$PATH" task lint
go test ./... -count=1
go test -race ./internal/numind/biz/feishu ./internal/numind/biz/agent ./internal/numind/store -count=1
git diff --check
```

Expected: all exit 0. Existing macOS sqlite deprecation warnings are non-failing.

- [ ] **Step 3: Run dual read-only reviews**

Spec reviewer checks every design invariant and the customer RED-first chain. Quality reviewer checks scope parser strictness, no started-write replay, auth loop limits, result redaction, external resume and multi-user isolation. P0/P1/P2 must be zero before merge.

- [ ] **Step 4: Record S5 acceptance**

Append exact commands/results and reviewer verdicts to the QA report. Set manifest/.ndf-active to S5 and commit:

```bash
git add docs/superpowers/qa/2026-07-18-feishu-resource-discovery-s5-acceptance.md .ndf/manifest.yaml
git commit -m "docs(ndf): accept Agent-led Feishu runtime"
```

- [ ] **Step 5: Merge and push**

From the feature worktree:

```bash
bash scripts/ndf/ndf-done.sh
```

Expected: develop fast-forward/merge, origin/develop pushed, worktree and local feature branch removed.

- [ ] **Step 6: Deploy Dev server**

From clean `numind-server/develop`:

```bash
bash scripts/cicd/release.sh dev server
```

Verify exact image commit, container healthy, public health code 0/status ok, lark-cli 1.0.68 and zero critical startup log matches.

- [ ] **Step 7: Real Dev acceptance**

Use the configured E2E account and a fresh Agent run to update the existing test document. Expected first run: one preflight operation enters `waiting_user_auth`, requested missing scope is exactly `docx:document:write_only`, and no docs business invocation starts. User approval is the only external consent step. After approval, verify one str_replace plus one append succeeds, fetch confirms exact content once, and a second write does not ask for authorization.

- [ ] **Step 8: Record S6**

Update manifest with deployed commit/image, health evidence and the remaining human Feishu-consent gate if present. Commit and push the documentation-only S6 record to develop.
