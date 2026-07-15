# Feishu Refresh Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refreshing a Feishu authorization link keeps the original operation recoverable and activates the replacement authorization worker.

**Architecture:** `WorkspaceLifecycleService` will validate that the old session is the operation's exact durable recovery session. After `AuthSessionService` creates the replacement session, lifecycle code will validate the replacement session, atomically replace the operation summary's session ID and expiry under tenant/state/session fencing, then activate the waiting worker. Manual sessions without an operation remain unchanged.

**Tech Stack:** Go 1.24, GORM, MySQL 8.0, existing Feishu authorization/session services, Testify.

---

## File structure

- `internal/numind/biz/feishu/service.go` — lifecycle contract and safe refresh orchestration.
- `internal/numind/store/feishu_workspace.go` — tenant-fenced, transactionally checked operation-summary rebind.
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

### Task 2: Add a tenant-fenced operation summary rebind

**Files:**
- Modify: `internal/numind/store/feishu_workspace.go`
- Modify: `internal/numind/store/feishu_workspace_test.go`

- [ ] **Step 1: Add `RebindOperationRecoverySession` to `IFeishuWorkspaceStore`.**

  Its arguments must include user ID, generation, operation ID, waiting state, expected old session ID, and replacement summary JSON. It returns `gorm.ErrRecordNotFound` when any ownership/state/session fence is no longer true.

- [ ] **Step 2: Implement the rebind inside a transaction.**

  Lock the active account and target operation. Decode only the existing `session_id` from `result_summary_json`; require it to equal the expected old session ID and require the operation state to match. Update only `result_summary_json` with the already validated replacement summary. Do not query an arbitrary active session and do not persist a URL or device code.

- [ ] **Step 3: Add store tests.**

  Verify a matching user/generation/waiting operation updates its summary. Verify wrong old session ID, wrong state, and retired/disconnecting account return no update.

- [ ] **Step 4: Run store tests.**

  Run: `go test ./internal/numind/store -run 'Test.*RebindOperationRecoverySession' -count=1`

  Expected: PASS.

### Task 3: Rebind then activate in the lifecycle service

**Files:**
- Modify: `internal/numind/biz/feishu/service.go`
- Modify: `internal/numind/biz/feishu/service_test.go`

- [ ] **Step 1: Extend `WorkspaceLifecycleStore` and `WorkspaceLifecycleAuth`.**

  Add the new rebind method to the lifecycle store subset. Add existing auth lifecycle primitives `Activate(context.Context, string) error` and `Abort(string)` to the lifecycle auth subset so the lifecycle owner controls the same persistence-before-worker-release barrier used by `FeishuOperationService`.

- [ ] **Step 2: Validate exact old operation binding before refresh.**

  For a session with `OperationID`, load only that operation under the current user and generation and validate it through the existing `recoverySession` rules. Reject a pending session that is not the summary's exact session.

- [ ] **Step 3: Validate, persist, then activate the replacement.**

  After refresh returns, load the replacement session by its returned ID. Require pending state, same phase, same operation ID, user, and generation. Copy the validated existing operation summary, replace only `SessionID` and `ExpiresAt`, marshal it, call `RebindOperationRecoverySession`, then call `Activate`. On any validation/rebind/activation failure after a replacement is created, call `Abort` and return `ErrWorkspaceLifecycleUnavailable` without returning the live action.

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
