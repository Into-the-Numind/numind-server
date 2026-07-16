# Feishu Stage Handoff Design

## Architecture

Keep durable state ownership unchanged:

- `AuthSessionService` finalizes the completed authorization session and account state.
- `WorkspaceResumeDispatcher` advances the existing encrypted operation and, only after success, resumes the original Agent tool call.
- `WorkspaceLifecycleService` translates browser acknowledgements into idempotent state observations or explicit confirmation/cancellation actions.

The defect is a context-boundary mismatch. `dispatchResumeDetached` currently uses `authSessionFinalizeTimeout` (five seconds), which is intended only for short database finalization. Replace it with a separate dispatch context bounded by `authSessionCLIHardCeiling` (12 minutes). This covers the existing 30-second URL-start window, execution-gate wait, controlled command retry budget, and Agent continuation while remaining finite. The dispatch continues independently of the browser request, matching the already committed durable session completion.

## Browser acknowledgement semantics

For `action=user_completed`:

- `succeeded`: retain existing compensation behavior.
- `executing`: return `{state:"executing"}` without dispatching, claiming, or mutating.
- `failed`, `unknown`, `cancelled`: return the stored terminal state without replaying or dispatching.
- recovery waiting states: retain existing exact-session behavior.
- all other states remain invalid.

This makes a stale click safe without turning it into an implicit retry.

## Expired-card refresh contract

The refresh endpoint returns a tagged result instead of a bare action:

```json
{"action":{"session_id":"...","kind":"user_auth","url":"..."}}
```

or, when the session is linked to a terminal operation:

```json
{"terminal":{"operation_id":"...","state":"failed"}}
```

Only `succeeded`, `failed`, `unknown`, and `cancelled` are allowed terminal states. A terminal result contains no action URL, device code, scopes, or authorization payload. `WorkspaceLifecycleService.RefreshAction` checks the stored operation immediately after loading it and returns the terminal variant before any call to authorization-session recovery.

The controller uses an allowlisted response DTO for both variants. This is a coordinated backend/frontend contract change: the two applications must be deployed together.

## Frontend settlement

The Feishu API and store return the tagged result. They update the live-action cache only for the `action` variant. For `terminal`, `AgentMessageItem` verifies that the returned operation ID matches the card that initiated the request, then asks the Agent chat store to settle that external action as terminal using the existing operation/run identity fences.

The settled card removes its URL and buttons and displays `原飞书任务已结束，请重新发送原指令。` It does not submit the original Agent prompt, call resume, or retry refresh. Automatic replay remains forbidden.

## Current failed operation

The current dev operation is already terminal `failed`; automatically resurrecting it would weaken write-safety because a generic historical failure is not sufficient proof that no remote side effect occurred. After deployment, refreshing its expired card returns the typed terminal result with HTTP 200 and closes the card. Reissuing the original instruction creates a fresh idempotent operation while reusing the app-ready account and encrypted lark-cli HOME.

## Tests

- A customer regression test proves the post-auth dispatcher receives a deadline longer than the authorization start window; the old five-second context fails this test immediately.
- Lifecycle service tests prove `user_completed` during `executing` and after terminal failure returns stored state and invokes neither operation resume nor Agent continuation.
- Lifecycle/controller tests prove a terminal refresh returns only the terminal variant, does not invoke auth recovery, and does not leak action fields.
- Frontend unit and Playwright tests prove the expired card consumes the terminal result, becomes noninteractive, shows the reissue instruction, and causes no Agent or repeat refresh request.
- Existing success compensation, confirmation, cancellation, auth-session, and integration suites remain green.
- Run focused race tests, `go test ./...`, `task lint`, frontend unit/E2E tests, `npm run lint`, and `npm run type-check` before merge.

## Non-goals

- No new async queue.
- No automatic replay of failed or unknown operations.
- No new endpoint, database column, permission scope, or visual layout.
