# Stable Lark Execution Implementation Plan

> **For agentic workers:** Execute inline in the two NDF worktrees. Keep backend tasks before frontend tasks, preserve the customer-RED-first commit in each repository, and update `.ndf/manifest.yaml` after every task.

**Goal:** Make every Agent execute safe Feishu business commands without model-carried receipts and present recoverable corrections as progress instead of false terminal errors.

**Architecture:** `lark_execute` carries only business argv; the backend derives identity, command policy, scopes, risk, recovery and idempotency from trusted context and the existing command catalog. Legacy receipt fields are decoder-only compatibility data and never authorize execution. Explicitly recoverable soft results stay visible to the model but become progress events in the UI; hard and unknown-write failures remain errors.

**Tech Stack:** Go 1.24, Eino tool adapter, existing Feishu operation service/catalog, Vue 3, Pinia, TypeScript, Vitest, Playwright.

---

### Task 1: Commit customer-shaped backend RED

**Files:**
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`

- [ ] Add `TestLarkExecuteDoesNotRequireModelCarriedReceipts` with a capturing `LarkExecutor`. Invoke `lark_execute` once with `{"argv":["drive","+search","--query","有数飞书二次连接测试","--only-title","--doc-types","docx,wiki,bitable"]}` and once with a malformed legacy `skill_receipts` array. Assert both requests reach the executor and contain the same argv.
- [ ] Assert `InputSchema()` does not contain `skill_receipts`, while an input containing `user_id` still returns the fixed invalid-input code without calling the executor.
- [ ] Run `go test ./internal/numind/biz/agent -run 'TestLarkExecuteDoesNotRequireModelCarriedReceipts' -count=1`; expected RED: missing receipt is rejected before executor.
- [ ] Commit only the failing regression: `test(qa): reproduce Lark receipt instability`.

### Task 2: Move execution authorization fully behind the platform boundary

**Files:**
- Modify: `internal/numind/biz/agent/tool_lark_execute.go`
- Modify: `internal/numind/biz/agent/tool_lark_inspect.go`
- Modify: `internal/numind/biz/agent/tool_lark_skill_read.go`
- Modify: `internal/numind/biz/feishu/operation_service.go`
- Modify: `internal/numind/biz/feishu/inspection.go`
- Modify: affected tests in `internal/numind/biz/agent/` and `internal/numind/biz/feishu/`

- [ ] Change `lark_execute.InputSchema()` to require only `argv`; keep `stdin_json`. In `decodeLarkExecuteInput`, allow but ignore a legacy `skill_receipts` key. Continue rejecting every other key and continue consuming only one exact leading `lark-cli` token.
- [ ] Stop populating `ExecuteRequest.SkillReceipts`. In `FeishuOperationService.Execute`, remove `validateOperationReceipts` and `VerifyRequired`; normalize catalog argv before loading the current user's account exactly as today.
- [ ] Apply the same decoder-only compatibility rule to `lark_inspect command`; `connection` remains mode-only. Remove receipt validation from `inspectCommand`.
- [ ] Remove `Receipt` from `larkSkillReadOutput` and delete receipt-copy instructions from `larkHostedExecutionPolicy`. Keep discovery, pagination, routing, retry and unknown-write rules.
- [ ] Update tests that asserted verifier calls. Replace them with catalog-domain, current-user, scope preflight, forbidden-command and idempotency assertions.
- [ ] Run focused Agent and Feishu suites. Expected GREEN for Task 1 and all pre-existing safety tests.
- [ ] Commit: `fix(feishu): own execution authorization on the platform`.

### Task 3: Add accurate recoverable soft-error semantics

**Files:**
- Modify: `internal/numind/biz/agent/tool_lark_skill_read.go`
- Modify: `internal/numind/biz/agent/tool_soft_error.go`
- Modify: `internal/numind/biz/agent/adapter_full_to_eino.go`
- Modify: `internal/numind/biz/agent/stream/events.go`
- Modify: `internal/numind/biz/agent/adapter_full_to_eino_test.go`
- Modify: `internal/numind/biz/agent/stream/events_test.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`

- [ ] Replace message-only Lark soft results with fixed `{error,code,recoverable,retryable}` fields. Never interpolate model/provider input.
- [ ] Extend soft-error decoding to return metadata. Legacy `{error:"ERROR: ..."}` remains a non-recoverable error.
- [ ] For explicit recoverable soft results, emit polling narration `progress` with `正在调整执行方式` and SSE `tool_call_error` with `recoverable:true`. Keep the model-facing ToolResult unchanged so the ReAct loop can correct itself.
- [ ] For real Go errors, non-recoverable soft results, correction exhaustion and execution stopped, retain StateError and `recoverable:false/omitted`.
- [ ] Add tests for first rejection -> recoverable progress, second rejection -> terminal error, legacy soft error -> terminal error, and real Go error -> terminal error.
- [ ] Run `go test ./internal/numind/biz/agent/... ./internal/numind/biz/feishu/... -count=1`.
- [ ] Commit: `fix(agent): distinguish recoverable tool corrections`.

### Task 4: Commit Playwright RED, then render recovery without false red errors

**Files:**
- Create: `e2e/agent-tool-recovery.spec.ts`
- Modify: `src/types/agent-stream.ts`
- Modify: `src/stores/agentChat.ts`
- Modify: `src/stores/__tests__/agentChat-streaming.spec.ts`
- Modify: `src/components/agent/__tests__/AgentToolCallItem.spec.ts`

- [ ] Build a hermetic mocked SSE run: tool start -> `tool_call_error {recoverable:true}` -> second tool success -> terminal completed. Use `createDiagnostics(page)` before navigation and assert the current UI contains red `执行出错`. Run with Playwright to record DOM, console, network and screenshot. This is the required UI diagnosis and must fail before production edits.
- [ ] Commit only this failing test: `test(qa): reproduce false Lark execution errors`.
- [ ] Add optional `recoverable?: boolean` to `ToolCallErrorPayload`.
- [ ] In `agentChat` stream handling, recoverable error sets `current_state='progress'`, message `正在调整执行方式`, and does not retain the raw error as a user headline. Hard error keeps `current_state='error'` and `执行出错`.
- [ ] Add Vitest coverage for both branches and for the existing terminal finalizer.
- [ ] Re-run the Playwright diagnostic. Expected: no red `.tl-line.error` for the recovered call, a visible processing/progress line during recovery, final answer visible; hard-error fixture remains red.
- [ ] Commit: `fix(agent): present recoverable tool attempts as progress`.

### Task 5: Security, compatibility and full quality gates

**Files:**
- Modify only tests or implementation required by failing gates.

- [ ] Backend: run focused tests twice, `go test ./...`, `go test -race ./internal/numind/biz/agent/... ./internal/numind/biz/feishu/...`, and `task lint`.
- [ ] Frontend: run focused Vitest, `npm run test:unit -- --run`, `npm run lint`, `npm run type-check`, and Playwright `agent-tool-recovery` plus existing `agent-streaming`.
- [ ] Verify forbidden `auth/config/whoami/im`, injected identity/scope/risk/HOME, cross-user account access, unknown-write replay, legacy receipt compatibility, and no receipt in skill output.
- [ ] Search changed files for secrets and receipt leakage; inspect `git diff --check` and repository status in both worktrees.
- [ ] Write `docs/superpowers/qa/2026-07-19-lark-stable-execution-s5-acceptance.md` with exact commands, exit codes, known unrelated failures and Dev acceptance prompts.
- [ ] Commit QA evidence in backend: `docs(ndf): accept stable Lark execution`.

### Task 6: Review, atomic merge and Dev deployment

**Files:**
- Modify: `.ndf/manifest.yaml`
- Modify: `.ndf-active` metadata in both worktrees

- [ ] Perform spec review and quality review of the complete diffs. Block on any P0/P1 or security regression; fix and repeat tests.
- [ ] Mark S4/S5 decisions and progress in manifest; commit final docs.
- [ ] Run `ndf-done` in the backend worktree, then frontend worktree. Confirm both develop branches are pushed and worktrees/feature branches removed.
- [ ] Deploy backend first with `scripts/cicd/release.sh dev server` (or repository documented equivalent), verify public health, Docker healthy state, startup logs and pinned lark-cli.
- [ ] Deploy frontend with its Dev release script, verify HTTP 200 and asset load.
- [ ] Run Dev smoke: a new Agent conversation reads a document by title without receipts; a recoverable correction does not show red; a forbidden command is still rejected; an actual terminal error still shows red.
- [ ] Record S6 deployment image/commit/health evidence in manifest on develop and push the deployment record.
