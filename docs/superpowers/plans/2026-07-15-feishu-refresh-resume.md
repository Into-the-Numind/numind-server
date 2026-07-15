# Feishu Refresh Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refreshing a Feishu authorization link keeps the original operation recoverable and activates the replacement authorization worker.

**Architecture:** `WorkspaceLifecycleService` validates that the old session is the operation's exact durable recovery session. `AuthSessionService` then calls one store transaction that creates the replacement, supersedes the old session, updates the operation summary and waiting account state; only after that commit does it start and activate the replacement worker. If startup fails, a second fenced transaction restores the original binding. If that transaction is temporarily unavailable, the original card can retry the exact replacement only when it has no live lease. Manual sessions without an operation remain unchanged.

**Status:** Completed in feature branch on 2026-07-15; the completion record below supersedes the planning checkboxes.

**Tech Stack:** Go 1.24, GORM, MySQL 8.0, existing Feishu authorization/session services, Testify.

---

## File structure

- `internal/numind/biz/feishu/service.go` — lifecycle contract and safe refresh orchestration.
- `internal/numind/store/feishu_workspace.go` — tenant-fenced atomic session replacement and operation-summary rebind.
- `internal/numind/biz/feishu/service_test.go` — regression tests using lifecycle fakes.
- `internal/numind/store/feishu_workspace_test.go` — persistence fence tests for a summary rebind.

### Task 1: Reproduce the broken refresh/resume path

**Files:**
- Modify: `internal/numind/biz/feishu/service_test.go`

- [ ] **Step 1: Extend the lifecycle fakes only as needed for a replacement operation session.**

  The fake workspace must retain the old operation and expose the replacement pending session by ID. The fake auth service must record `Activate` and `Abort` calls.

- [ ] **Step 2: Write the failing regression test.**

  Add `TestWorkspaceLifecycleRefreshRebindsOperationSessionBeforeResume`. Build a waiting `create_app` operation whose summary names `session-old`; make refresh return `session-new`; make the fake store return a pending `session-new` linked to the same operation. Assert:

  ```go
  action, err := svc.RefreshAction(context.Background(), 7, "session-old")
  require.NoError(t, err)
  require.Equal(t, "session-new", action.SessionID)
  require.Equal(t, "session-new", lifecycleSummarySessionID(t, workspace.operation))
  require.Equal(t, []string{"session-new"}, auth.activated)

  result, err := svc.Resume(context.Background(), 7, operationID, ResumeActionUserCompleted)
  require.NoError(t, err)
  require.Equal(t, model.FeishuOperationWaitingConnection, result.State)
  require.Zero(t, dispatcher.calls)
  ```

- [ ] **Step 3: Run only the regression test and confirm RED.**

  Run: `go test ./internal/numind/biz/feishu -run '^TestWorkspaceLifecycleRefreshRebindsOperationSessionBeforeResume$' -count=1`

  Expected: FAIL because the current refresh implementation leaves `session-old` in the operation summary and never activates `session-new`.

- [ ] **Step 4: Commit the failing reproduction.**

  Run:

  ```bash
  git add internal/numind/biz/feishu/service_test.go
  git commit -m 'test(qa): reproduce feishu refresh resume failure'
  ```

### Task 2: Add an atomic tenant-fenced session replacement

**Files:**
- Modify: `internal/numind/store/feishu_workspace.go`
- Modify: `internal/numind/store/feishu_workspace_test.go`

- [ ] **Step 1: Add `RefreshOperationSession` to `IFeishuWorkspaceStore`.**

  Its arguments must include user ID, generation, old session ID, operation ID, waiting state, replacement account state, replacement session, and replacement summary JSON. It returns `gorm.ErrRecordNotFound` when any ownership/state/session fence is no longer true.

- [ ] **Step 2: Implement the replacement inside one transaction.**

  Lock the active account, old pending session, and target operation. Decode the existing `session_id` from `result_summary_json`; require it to equal the old session ID and require the operation state, phase and scopes to match. Create the replacement, supersede the old session, update account waiting state and `result_summary_json` in the same transaction. Do not query an arbitrary active session and do not persist a URL or device code.

- [ ] **Step 3: Add store tests.**

  Verify a matching user/generation/waiting operation supersedes old session, creates the replacement and updates its summary. Verify wrong old session ID, wrong state, and retired/disconnecting account cause no partial write.

- [ ] **Step 4: Run store tests.**

  Run: `go test ./internal/numind/store -run 'Test.*RefreshOperationSession' -count=1`

  Expected: PASS.

### Task 3: Atomically refresh then activate in the authorization service

**Files:**
- Modify: `internal/numind/biz/feishu/service.go`
- Modify: `internal/numind/biz/feishu/service_test.go`

- [ ] **Step 1: Extend `WorkspaceLifecycleAuth`.**

  Add `RefreshOperationAction` so the authorization service, which owns session creation and worker activation, also owns the transaction boundary.

- [ ] **Step 2: Validate exact old operation binding before refresh.**

  For a session with `OperationID`, load only that operation under the current user and generation and validate it through the existing `recoverySession` rules. Reject a pending session that is not the summary's exact session.

- [ ] **Step 3: Persist atomically, then start and activate the replacement.**

  Build the replacement summary within the authorization service, call `RefreshOperationSession`, then stop the old worker and start the replacement. Activate it only after the transaction commit. The activation call uses a detached context because no request cancellation may strand a just-persisted replacement.

- [ ] **Step 4: Preserve manual refresh behavior.**

  When the old session has no `OperationID`, return the refreshed action exactly as before; no rebind or activation is required because manual workers do not wait behind the operation activation barrier.

- [ ] **Step 5: Run the regression test and related lifecycle suite.**

  Run:

  ```bash
  go test ./internal/numind/biz/feishu -run 'TestWorkspaceLifecycle(Refresh|Resume)' -count=1
  go test ./internal/numind/biz/feishu -count=1
  ```

  Expected: PASS, including the previously failing refresh/resume path.

### Task 4: Verify and land

**Files:**
- Verify only.

- [ ] **Step 1: Format and run all required checks.**

  Run:

  ```bash
  gofmt -w internal/numind/biz/feishu/service.go internal/numind/biz/feishu/service_test.go internal/numind/store/feishu_workspace.go internal/numind/store/feishu_workspace_test.go
  go test ./internal/numind/biz/feishu -count=1
  go test ./internal/numind/store -count=1
  task lint
  ```

- [ ] **Step 2: Inspect the final diff.**

  Run: `git diff --check && git diff -- internal/numind/biz/feishu/service.go internal/numind/store/feishu_workspace.go`

  Expected: only session rebind/activation behavior and its tests; no URL, device code, or credential persistence.

- [ ] **Step 3: Commit the implementation.**

  Run:

  ```bash
  git add internal/numind/biz/feishu/service.go internal/numind/biz/feishu/service_test.go internal/numind/store/feishu_workspace.go internal/numind/store/feishu_workspace_test.go
  git commit -m 'fix(feishu): resume operations after auth refresh'
  ```

## Plan self-review

- Spec coverage: task 1 covers the reported 500; task 2 preserves durable tenant/session fencing; task 3 activates the replacement only after persistence; task 4 checks absence of secret-bearing persistence and required lint.
- Placeholder scan: no deferred implementation or unspecified test behavior.
- Type consistency: the rebind method is the only new store contract; `Activate` and `Abort` already exist on `AuthSessionService` and are exposed through the lifecycle subset.

## Completion record

- [x] Added a failing regression test before the implementation commit.
- [x] Atomically create the replacement, supersede the old session, and rebind the waiting operation summary.
- [x] Start and activate the replacement only after the binding transaction commits.
- [x] Compensate a replacement that cannot start, so the original card remains retryable.
- [x] Reconcile an original card when the first compensation write was temporarily unavailable.
- [x] Fence stale-card recovery behind the replacement's live lease, and only stop a local worker after the fence commits.
- [x] Added lifecycle, service, persistence, compensation, and concurrency regression coverage.
- [x] Ran full tests, lint, and independent specification/quality reviews.
