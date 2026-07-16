# Feishu Stage Handoff Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Prevent the post-authorization worker from timing out at five seconds and let an expired historical Feishu card settle safely when its linked operation is already terminal.

**Architecture:** Keep durable operation/session ownership unchanged. Give the detached dispatcher a dedicated 12-minute bounded context, make stale `user_completed` acknowledgements read-only, and change refresh to a tagged `action | terminal` result. The frontend preserves its existing identity fences and converts only a matching terminal result into the existing terminal external-action state.

**Tech Stack:** Go, Gin, GORM, Vue 3, Pinia, TypeScript, Vitest, Playwright.

---

### Task 1: Commit backend customer reproductions in red

**Files:**
- Modify: `internal/numind/biz/feishu/auth_session_service_test.go`
- Modify: `internal/numind/biz/feishu/service_test.go`

**Step 1: Add the post-auth deadline regression.**

Capture the context deadline received by the resume dispatcher after authorization completion and assert that it exceeds the authorization URL-start window while remaining bounded by the CLI hard ceiling.

**Step 2: Add stale acknowledgement regressions.**

For `executing`, `failed`, `unknown`, and `cancelled`, call `Resume(..., user_completed)` and assert HTTP-layer-independent service success, the stored state, zero resume-dispatch calls, and zero Agent continuation calls.

**Step 3: Run the focused tests and prove RED.**

Run:

```bash
go test ./internal/numind/biz/feishu -run 'Test.*(Dispatch.*Deadline|UserCompleted.*Executing|UserCompleted.*Terminal)' -count=1
```

Expected: FAIL on the five-second deadline and current validation errors.

**Step 4: Commit only the failing tests.**

```bash
git add internal/numind/biz/feishu/*_test.go
git commit -m "test(qa): reproduce feishu authorization stage handoff failure"
```

### Task 2: Implement and verify backend contract

**Files:**
- Modify: `internal/numind/biz/feishu/auth_session_service.go`
- Modify: `internal/numind/biz/feishu/service.go`
- Modify: `internal/numind/biz/feishu/service_test.go`
- Modify: `internal/numind/controller/v1/feishu/feishu.go`
- Modify: `internal/numind/controller/v1/feishu/feishu_test.go`

**Step 1: Add the dedicated dispatcher context.**

Add a helper that detaches cancellation while preserving values and applies `authSessionCLIHardCeiling`. Use it only in `dispatchResumeDetached`; retain the five-second helper for database finalization and restoration.

**Step 2: Make stale completion acknowledgements read-only.**

Before recovery-session validation, return the stored operation summary for `executing`, `failed`, `unknown`, and `cancelled`. Keep existing `succeeded` compensation and waiting-state behavior unchanged.

**Step 3: Add the tagged refresh result.**

Define `RefreshActionResult` with exactly one optional `Action` or `Terminal`. Define a terminal DTO containing only operation ID and allowlisted terminal state. Change `IFeishuService.RefreshAction` and implementations/fakes accordingly.

**Step 4: Short-circuit terminal refresh.**

After loading the linked operation, return `Terminal` before `recoverySession` or any auth refresh call. Active paths return `Action` and preserve current generation/session protections.

**Step 5: Allowlist the controller response.**

Return nested `action` or `terminal` JSON. Add tests for both variants, ownership/error behavior, absence of terminal URL/scopes/device code, and zero auth recovery on terminal operations.

**Step 6: Verify backend.**

Run:

```bash
go test ./internal/numind/biz/feishu ./internal/numind/controller/v1/feishu -count=1
go test -race ./internal/numind/biz/feishu -count=1
go test ./... -count=1
task lint
```

Expected: PASS.

**Step 7: Commit production backend changes.**

```bash
git add internal/numind/biz/feishu internal/numind/controller/v1/feishu
git commit -m "fix(feishu): settle authorization stage handoffs safely"
```

### Task 3: Commit frontend customer reproduction in red

**Files:**
- Modify: `e2e/feishu-personal-workspace.spec.ts`
- Modify: `src/components/agent/__tests__/AgentMessageItem.spec.ts`

**Step 1: Add a terminal-refresh browser scenario.**

Serve an expired external-action card, answer its one refresh request with `{terminal:{operation_id,state:'failed'}}`, click refresh, and assert the card becomes terminal, removes controls, and says `原飞书任务已结束，请重新发送原指令。` Assert no Agent answer request and no second refresh.

**Step 2: Add the focused component regression.**

Mock the store refresh result as terminal and assert matching operation/run settlement. Also prove a mismatched operation ID is ignored.

**Step 3: Run and prove RED.**

Run:

```bash
npx vitest run src/components/agent/__tests__/AgentMessageItem.spec.ts
npx playwright test e2e/feishu-personal-workspace.spec.ts --project=mocked --grep "terminal refresh" --reporter=line
```

Expected: FAIL because the current client assumes every 200 refresh response is a live action.

**Step 4: Commit only the failing tests.**

```bash
git add e2e/feishu-personal-workspace.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts
git commit -m "test(qa): reproduce stale feishu refresh failure"
```

### Task 4: Implement and verify frontend settlement

**Files:**
- Modify: `src/api/feishu.ts`
- Modify: `src/api/feishu.test.ts`
- Modify: `src/stores/feishu.ts`
- Modify: `src/stores/__tests__/feishu.spec.ts`
- Modify: `src/stores/agentChat.ts`
- Modify: `src/components/agent/AgentMessageItem.vue`
- Modify: `src/components/agent/FeishuActionCard.vue`
- Modify: `src/components/agent/__tests__/AgentMessageItem.spec.ts`
- Modify: `src/components/agent/__tests__/FeishuActionCard.spec.ts`

**Step 1: Model the tagged response.**

Add `FeishuRefreshResult` and an allowlisted terminal state type. Make the API/store return that result and cache only `result.action`.

**Step 2: Expose narrow terminal settlement.**

Expose an Agent chat store method that delegates to the existing private `settleExternalAction(operationID, 'terminal', runID)` path.

**Step 3: Handle the terminal variant with identity fences.**

In `handleExternalRefresh`, retain request epoch/session/operation/run checks. For a matching terminal operation, settle and return. For live action, retain current stream-reducer behavior. Ignore malformed, empty, or mismatched variants.

**Step 4: Update terminal copy.**

Use `原飞书任务已结束，请重新发送原指令。` without changing card layout or design tokens.

**Step 5: Verify frontend.**

Run:

```bash
npx vitest run src/api/feishu.test.ts src/stores/__tests__/feishu.spec.ts src/components/agent/__tests__/AgentMessageItem.spec.ts src/components/agent/__tests__/FeishuActionCard.spec.ts
npx playwright test e2e/feishu-personal-workspace.spec.ts --project=mocked --reporter=line
npm run lint
npm run type-check
```

Expected: PASS.

**Step 6: Commit production frontend changes.**

```bash
git add src e2e/feishu-personal-workspace.spec.ts
git commit -m "fix(feishu): close terminal authorization cards"
```

### Task 5: Review, integrate, deploy, and validate dev

**Files:**
- Modify: `.ndf/manifest.yaml`
- Modify: each worktree `.ndf-active` stage metadata

**Step 1: Run independent specification and quality reviews.**

Resolve all blocking findings, rerun affected tests, and record reviewer outcomes.

**Step 2: Run final verification from clean worktrees.**

Repeat backend full tests/lint and frontend focused tests/E2E/lint/type-check. Confirm only intended files changed and no secret or production configuration changed.

**Step 3: Complete both NDF worktrees.**

Run `ndf-done` inside the backend worktree and then the frontend worktree. Confirm both `develop` branches were pushed and the worktrees/feature branches were removed.

**Step 4: Deploy backend and frontend develop to dev.**

Deploy the server first and web immediately afterward so the tagged contract is not left half-deployed. Do not deploy production.

**Step 5: Validate the real dev flow.**

Health-check APIs and web, log in with the configured E2E account, reproduce an expired terminal card, confirm refresh returns 200 terminal and the card closes, then issue a fresh Feishu document instruction and confirm the existing app is reused and the next authorization/business stage proceeds without a five-second failure.

**Step 6: Update NDF status and report.**

Mark S7 complete only after deployment evidence and final `ndf-status` consistency check.
