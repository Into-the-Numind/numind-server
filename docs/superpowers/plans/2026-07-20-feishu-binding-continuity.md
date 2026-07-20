# Feishu Binding Continuity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Durably advance every Feishu authorization card and require browsers to acknowledge the exact visible session, so stale, repeated, concurrent and restarted clients converge without cross-phase side effects.

**Architecture:** The operation summary keeps a bounded superseded-session lineage. An Agent Run CAS moves `pending_external_action_json` only from that lineage to the current server-produced action. The existing resume API accepts `session_id`; stale or missing sessions are read-only and return the current action. Vue keys requests by operation+session and applies responses only to the initiating current card.

**Tech Stack:** Go 1.24, Gin, GORM, MySQL/SQLite tests, Vue 3, Pinia, TypeScript, Vitest, Playwright.

---

## File map

- `internal/numind/biz/feishu/operation_service.go`: action lineage and replay evidence.
- `internal/numind/store/agent_run.go`: tenant/run/operation/tool/session CAS.
- `internal/numind/biz/agent/external_tool_resume.go`: narrow pending-action handoff.
- `internal/numind/biz/feishu_resume_dispatcher.go`: persist nonterminal actions.
- `internal/numind/biz/feishu/{auth_session_service,service}.go`: refresh handoff and exact browser session fence.
- `internal/numind/controller/v1/feishu/feishu.go`: strict request DTO only.
- `src/api/feishu.ts`, `src/stores/agentChat.ts`: exact request and reconciliation.
- `src/components/agent/{AgentMessageItem,FeishuActionCard}.vue`: captured session and immediate feedback.
- Corresponding Go/Vitest/Playwright files: permanent regression matrix.

## Task 1: Commit the backend customer RED

**Files:**

- Modify: `internal/numind/biz/feishu_resume_dispatcher_test.go`

- [ ] **Step 1: Give the existing fake a recording handoff method**

```go
// Add to dispatcherAgentResumerFake; the existing mutex protects the slice.
handoffSessions []string

func (f *dispatcherAgentResumerFake) HandoffExternalToolWait(
    _ context.Context, userID uint, runID uint64, operationID, toolCallID string,
    _ []string, action agent.ExternalActionPayload,
) (bool, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.handoffSessions = append(f.handoffSessions, action.SessionID)
    return true, nil
}
```

- [ ] **Step 2: Add the user-438-equivalent regression**

```go
func TestWorkspaceResumeDispatcherHandsCreateAppForwardToUserAuth(t *testing.T) {
    operations := &dispatcherOperationFake{result: &feishu.OperationResult{
        OperationID: "operation-user-438", State: model.FeishuOperationWaitingUserAuth,
        AgentRunID: 261, ToolCallID: "lark-call-438",
        Action: &feishu.OperationAction{
            Provider: feishu.ProviderLark, OperationID: "operation-user-438",
            SessionID: "user-auth-new", Phase: model.FeishuAuthPhaseUserAuth,
            ExpiresAt: time.Now().Add(time.Hour),
        },
    }}
    resumer := &dispatcherAgentResumerFake{}
    require.NoError(t, NewWorkspaceResumeDispatcher(operations, resumer).
        DispatchResume(context.Background(), 438, "operation-user-438"))
    require.Equal(t, []string{"user-auth-new"}, resumer.handoffSessions)
}
```

- [ ] **Step 3: Prove RED and commit it alone**

Run: `go test ./internal/numind/biz -run TestWorkspaceResumeDispatcherHandsCreateAppForwardToUserAuth -count=1`

Expected: FAIL because the dispatcher returns nil without a handoff.

```bash
git add internal/numind/biz/feishu_resume_dispatcher_test.go
git commit -m "test(qa): reproduce stale Feishu binding card"
```

## Task 2: Implement monotonic operation-to-Agent handoff

**Files:**

- Modify: `internal/numind/biz/feishu/operation_service.go`
- Modify: `internal/numind/biz/feishu/operation_service_test.go`
- Modify: `internal/numind/biz/feishu/auth_session_service.go`
- Modify: `internal/numind/biz/feishu/auth_session_service_test.go`
- Modify: `internal/numind/store/agent_run.go`
- Modify: `internal/numind/store/agent_run_external_action_test.go`
- Modify: `internal/numind/biz/agent/external_tool_resume.go`
- Modify: `internal/numind/biz/agent/external_tool_resume_test.go`
- Modify: `internal/numind/biz/feishu_resume_dispatcher.go`
- Modify: `internal/numind/biz/feishu_resume_dispatcher_test.go`

- [ ] **Step 1: Add bounded lineage**

```go
const operationSessionLineageLimit = 32

type persistedOperationSummary struct {
    // existing fields remain unchanged
    SupersededSessionIDs []string `json:"superseded_session_ids,omitempty"`
}

type OperationResult struct {
    // existing fields remain unchanged
    SupersededSessionIDs []string `json:"-"`
}
```

Implement `advanceOperationSession(summary, next)` to append the prior non-empty, different session once and retain the newest 32 IDs. Use it from `startRecoveryAndWait`, `resultFromOperation`, and every operation-bound refresh/recovery summary builder. Tests cover de-duplication, cap, create_app→user_auth and restart replay.

- [ ] **Step 2: Add an exact store capability**

```go
type IExternalActionTransitioner interface {
    TransitionPendingExternalAction(
        context.Context, uint, uint64, string, string, []string, []byte,
    ) (bool, error)
}
```

Canonicalize next JSON, lock one Agent Run, and require user, operation, tool, `terminated/waiting_for_user_choice`, not cancelled/deleted. Same next session is idempotent; otherwise current session must be in lineage. Tests cover tenant/identity/state mismatch and delayed old transition.

- [ ] **Step 3: Add the Agent resumer bridge and dispatcher call**

```go
func (r *AgentRunResumer) HandoffExternalToolWait(
    ctx context.Context, userID uint, runID uint64, operationID, toolCallID string,
    superseded []string, action ExternalActionPayload,
) (bool, error)
```

Marshal only `action.Persistent()`. For `waiting_connection`, `waiting_app_scope` and `waiting_user_auth`, require a complete action and successful handoff; missing action or failed CAS returns a retryable error. Emit `action_handoff_retry` through the existing dispatcher observer and test its safe-field boundary. `waiting_confirmation` retains its existing rolling-upgrade compatibility path, and terminal continuation logic stays unchanged.

- [ ] **Step 4: Run GREEN and commit**

Run: `go test ./internal/numind/biz ./internal/numind/biz/agent ./internal/numind/biz/feishu ./internal/numind/store -run 'Handoff|PendingExternalAction|OperationSession|ResumeDispatcher|RefreshOperation' -count=1`

Expected: PASS, including Task 1.

```bash
git add internal/numind/biz/feishu internal/numind/biz/agent internal/numind/biz/feishu_resume_dispatcher.go internal/numind/biz/feishu_resume_dispatcher_test.go internal/numind/store/agent_run.go internal/numind/store/agent_run_external_action_test.go
git commit -m "fix(feishu): persist authorization action handoffs"
```

## Task 3: Fence lifecycle resume and refresh by session

**Files:**

- Modify: `internal/numind/biz/feishu/service.go`
- Modify: `internal/numind/biz/feishu/service_test.go`
- Modify: `internal/numind/controller/v1/feishu/feishu.go`
- Modify: `internal/numind/controller/v1/feishu/feishu_test.go`
- Modify: `internal/numind/biz/feishu_adapter_test.go`
- Modify: `internal/numind/biz/feishu/personal_workspace_integration_test.go`

- [ ] **Step 1: Lock the API signature**

```go
type resumeRequest struct {
    Action string `json:"action"`
    SessionID string `json:"session_id,omitempty"`
}

Resume(context.Context, uint, string, string, string) (*OperationResult, error)
// ctx, userID, operationID, sessionID, action
```

Update all fakes through compiler errors. Preserve strict unknown-field rejection. Run `rg -n '\.Resume\(' internal/numind/biz/feishu internal/numind/controller/v1/feishu internal/numind/biz/feishu_adapter_test.go` and update every lifecycle caller, including `personal_workspace_integration_test.go`, to pass its exact current action session. The package must compile at the end of this step.

- [ ] **Step 2: Return the current action without side effects for stale input**

Add `lifecycleCurrentActionResult(operation, session)`. It returns operation/state, `AuthorizationUpdated`, session/phase/expiry, and no URL/scopes. Before any nonterminal mutation, compare request session with `recoverySession`. Missing/mismatched requests use this helper and must call zero mutable dependencies. Emit the existing structured observation outcome `stale_action_reconciled` with only safe correlation/classification fields; tests assert URL query, scopes, device code, secret, HOME and argv never appear.

- [ ] **Step 3: Handoff refreshed operation actions**

Extend `WorkspaceLifecycleAgentWaits` with `HandoffExternalToolWait`. After an operation-bound refresh succeeds, transition Agent pending action using the summary lineage. Manual sessions without an operation skip it. A committed refresh plus failed handoff returns a retryable dependency error; replay repairs it. The refresh failure path emits its own safe `action_handoff_retry` observation; Task 2 already owns and tests the dispatcher observation.

- [ ] **Step 4: Test, run and commit**

Tests assert exact current advancement; stale/missing/cross-user/generation mismatch zero calls; terminal idempotence; strict JSON; no secret/scopes/provider internals; refresh handoff and replay.

Run: `go test ./internal/numind/biz/feishu ./internal/numind/controller/v1/feishu ./internal/numind/biz -run 'WorkspaceLifecycle|ResumeOperation|Refresh.*Handoff' -count=1`

```bash
git add internal/numind/biz/feishu/service.go internal/numind/biz/feishu/service_test.go internal/numind/biz/feishu/personal_workspace_integration_test.go internal/numind/controller/v1/feishu/feishu.go internal/numind/controller/v1/feishu/feishu_test.go internal/numind/biz/feishu_adapter_test.go
git commit -m "fix(feishu): fence lifecycle resume by card session"
```

## Task 4: Commit the frontend customer RED

**Files:**

- Modify: `src/components/agent/__tests__/AgentMessageItem.spec.ts`

- [ ] **Step 1: Assert exact session submission**

Mount operation `op-438`, session `create-app-old`, click continue, then:

```ts
expect(resume).toHaveBeenCalledWith('op-438', 'create-app-old')
```

- [ ] **Step 2: Prove RED and commit it alone**

Run: `npx vitest run src/components/agent/__tests__/AgentMessageItem.spec.ts -t 'submits the exact Feishu card session'`

Expected: FAIL because the component currently submits only operation ID.

```bash
git add src/components/agent/__tests__/AgentMessageItem.spec.ts
git commit -m "test(qa): reproduce stale Feishu card acknowledgement"
```

## Task 5: Implement exact-session frontend reconciliation

**Files:**

- Modify: `src/api/feishu.ts`
- Modify: `src/api/feishu.test.ts`
- Modify: `src/stores/agentChat.ts`
- Modify: `src/stores/__tests__/agentChat-resume.spec.ts`
- Modify: `src/stores/__tests__/agentChat-session-epoch.spec.ts`
- Modify: `src/components/agent/AgentMessageItem.vue`
- Modify: `src/components/agent/FeishuActionCard.vue`
- Modify: `src/components/agent/__tests__/AgentMessageItem.spec.ts`
- Modify: `src/components/agent/__tests__/FeishuActionCard.spec.ts`
- Modify: `e2e/feishu-personal-workspace.spec.ts`

- [ ] **Step 1: Send the locked request**

```ts
export async function resumeFeishuOperation(
  operationId: string,
  sessionId: string,
  action: FeishuResumeAction = 'user_completed'
): Promise<FeishuOperationResult> {
  const { data } = await request.post(
    `/v1/feishu/operations/${encodeURIComponent(operationId)}/resume`,
    { action, session_id: sessionId },
    { timeout: 60_000 }
  )
  // retain the existing strict response validator
}
```

Reject an empty session before network access.

- [ ] **Step 2: Reconcile by operation+session**

Store signature becomes `(operationID, sessionID, action?)`; exact-match the pending message and use `${operationID}:${sessionID}` as in-flight key. Capture request-time `runID` and `session epoch`; response, error and finally may mutate state only while `(operationID, sessionID, runID, session epoch)` is unchanged. Add a same operation/session but different run/route race test. Replacement action clears old URL/notice; URL-less action starts existing auto-refresh.

- [ ] **Step 3: Submit the captured card session and improve feedback**

`AgentMessageItem` calls `store.resumeFeishuOperation(operationID, actionSessionID, action)`. While busy, `FeishuActionCard` shows `正在检测飞书授权，请稍候…`. `authorization_updated` reads `授权已进入下一步，正在为你切换到最新步骤。`

- [ ] **Step 4: Add browser regression**

Playwright sequence: render session A; snapshot advances to B while A request is in flight; stale response returns B without URL; refresh B; assert only B is actionable, A never paints `authorization_pending`, exact-B resume happens once, and the original Agent operation/continuation reaches its final state exactly once.

- [ ] **Step 5: Run and commit**

Run: `npx vitest run src/api/feishu.test.ts src/stores/__tests__/agentChat-resume.spec.ts src/stores/__tests__/agentChat-session-epoch.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts src/components/agent/__tests__/FeishuActionCard.spec.ts`

Run: `npx playwright test e2e/feishu-personal-workspace.spec.ts --project=mocked --workers=1`

Expected: PASS, including Task 4.

```bash
git add src/api/feishu.ts src/api/feishu.test.ts src/stores/agentChat.ts src/stores/__tests__ src/components/agent/AgentMessageItem.vue src/components/agent/FeishuActionCard.vue src/components/agent/__tests__ e2e/feishu-personal-workspace.spec.ts
git commit -m "fix(feishu): reconcile authorization cards by session"
```

## Task 6: Complete S4 gates and adversarial reviews

**Files:**

- Modify: `.ndf/manifest.yaml`
- Modify: both `.ndf-active` stage values

- [ ] **Step 1: Backend gates**

Run `go test` for store/agent/feishu/biz, the same packages under `-race`, `go test ./... -count=1`, `task lint`, and `git diff --check`. Assert the feature diff excludes `config_prod.yaml` and `migrations/`.

- [ ] **Step 2: Frontend gates**

Run `npm run lint`, `npm run type-check`, `npm run test:unit -- --run`, focused Playwright mocked contracts, and `git diff --check`.

- [ ] **Step 3: Verify RED history and dual review**

In each repository run `git log --reverse --format='%s' develop..HEAD | head -1` and require an exact `test(qa): reproduce ...` first feature commit. Dispatch independent Spec and Quality/Security reviewers using the NDF templates. Repair all P0/P1, rerun affected tests, and require P0=0/P1=0.

- [ ] **Step 4: Advance to S5 only after S4 is green**

Update manifest progress so completed equals total implementation tasks, record both reviews, and set stage S5 in manifest and both `.ndf-active` files. Do not merge or deploy in this task.

## Task 7: Run S5 local acceptance and write the QA report

**Files:**

- Create: `docs/superpowers/qa/2026-07-20-feishu-binding-continuity.md`
- Modify: `.ndf/manifest.yaml`
- Modify: both `.ndf-active` stage values

- [ ] **Step 1: Start the local backend and frontend**

Use the feature worktrees and local test configuration. Confirm backend `/healthz` and the Vite page are reachable before browser tests. Record process IDs and stop both processes after QA.

- [ ] **Step 2: Run local Playwright and browser QA**

Run the complete Feishu personal-workspace Playwright project against localhost, including the stale-session contract. Capture conclusions for create_app, processing, user_auth replacement, stale reconciliation and completed continuation. Require no P0 visual/functional regression.

- [ ] **Step 3: Re-run S4 computational gates**

Run the full backend tests/lint and frontend lint/type-check/unit tests again from clean worktrees. No Langfuse trace is needed because no LLM call is added.

- [ ] **Step 4: Write and commit the QA report**

Record commands, exit codes, test counts, local URLs, browser conclusions, secret-safety checks and exact SHAs. Set current task to atomic merge/Dev deployment. Do not call `ndf-done` until this report is complete.

## Task 8: Merge, deploy and verify Dev

**Files:**

- Modify: `.ndf/manifest.yaml` after merge
- Create: NDF S6 acceptance record in the configured Obsidian `features/` location

- [ ] **Step 1: Atomic merge**

Run `ndf-done` from both feature worktrees via the NDF multi-repo flow. Never deploy production.

- [ ] **Step 2: Backend-first Dev deployment**

Deploy `numind-server` first and verify its image SHA and `/healthz`. Then deploy `numind-web-v3` and verify its image SHA and `/api/healthz`. A missing-session request during the rolling window must remain read-only.

- [ ] **Step 3: Dev verification and acceptance record**

Verify server `/healthz`, web `/api/healthz`, image SHAs and startup logs. Run the browser stale-session contract against Dev. If a fresh Feishu account can be authorized automatically, complete create_app→user_auth→original task once; otherwise identify the remaining external human authorization without claiming it passed. Set manifest stage S6, record deployed SHAs, write the acceptance record, and keep production explicitly untouched. Human product confirmation remains the S6 gate; it does not block the requested Dev deployment itself.
