# Feishu Stage Handoff Design

## Architecture

Keep durable state ownership unchanged:

- `AuthSessionService` finalizes the completed authorization session and account state.
- `WorkspaceResumeDispatcher` advances the existing encrypted operation and, only after success, resumes the original Agent tool call.
- `WorkspaceLifecycleService` translates browser acknowledgements into idempotent state observations or explicit confirmation/cancellation actions.

The defect is a context-boundary mismatch. `dispatchResumeDetached` currently uses `authSessionFinalizeTimeout` (five seconds), which is intended only for short database finalization. Replace it with a separate dispatch context bounded by `authSessionCLIHardCeiling` (12 minutes). This covers the existing 30-second URL-start window, execution-gate wait, controlled command retry budget, and Agent continuation while remaining finite. The dispatch continues independently of the browser request, matching the already committed durable session completion.

## Browser acknowledgement semantics

For `action=user_completed`:

- `succeeded`: compensate only the idempotent Agent continuation; never re-run Confirm, Cancel, or lark-cli.
- `executing`: return `{state:"executing"}` without dispatching, claiming, or mutating.
- `failed`, `unknown`, `cancelled`: terminalize the exact linked Agent wait and return the stored state without replaying Feishu.
- recovery waiting states: retain existing exact-session behavior.
- all other states remain invalid.

The same terminal settlement is applied after app-scope completion and completed-auth dispatch, because the operation can become terminal inside the current request. Confirmation and cancellation use the same matrix. A concurrent cancel that observes a committed success compensates continuation; one that observes failed/unknown/cancelled terminalizes the wait. This makes every stale or racing click safe without turning it into a Feishu retry.

## Expired-card refresh contract

The refresh endpoint returns a tagged result instead of a bare action:

```json
{"action":{"session_id":"...","kind":"user_auth","url":"..."}}
```

or, when the session is linked to a terminal operation:

```json
{"terminal":{"operation_id":"...","state":"failed"}}
```

Only `succeeded`, `failed`, `unknown`, and `cancelled` are allowed terminal states. A terminal result contains no action URL, device code, scopes, or authorization payload. `WorkspaceLifecycleService.RefreshAction` checks the stored operation immediately after loading it, compensates/terminalizes the exact Agent handoff, and returns the terminal variant before any authorization-session recovery.

The controller uses an allowlisted response DTO for both variants. This is a coordinated backend/frontend contract change: the two applications must be deployed together.

## Frontend settlement

The Feishu API models an exclusive tagged union and validates exactly one well-formed branch at runtime before any store mutation. Refresh has no shared connection-store side effect; the route owner applies a live action only after its session epoch, operation, session, and run identity fences pass. For `terminal`, `AgentMessageItem` also verifies the returned operation ID before settling the exact action.

The settled card removes its URL and buttons. `failed` displays `原飞书任务已结束，请重新发送原指令。`; `unknown` requires the user to verify Feishu; `cancelled` reports cancellation; `succeeded` reports that the original task is continuing. Failed/unknown/cancelled set the exact current run to the terminal `aborted_tools` projection and unlock input. Succeeded sets the run to `external_resume_ready`, preserving status observation until the original continuation finishes. No branch submits a new Agent prompt or retries the Feishu write.

## Current failed operation

The current dev operation is already terminal `failed`; automatically replaying its Feishu command would weaken write-safety. After deployment, refreshing its expired card terminalizes the linked Agent wait, returns the typed terminal result with HTTP 200, closes the card, and unlocks input. Reissuing the original instruction creates a fresh idempotent operation while reusing the app-ready account and encrypted lark-cli HOME.

## Tests

- A customer regression test proves the post-auth dispatcher receives a deadline longer than the authorization start window; the old five-second context fails this test immediately.
- Lifecycle service tests prove `user_completed` during `executing` is read-only; all four lifecycle entries compensate success or terminalize failed/unknown/cancelled Agent waits, including terminal transitions created during the current request and concurrent confirm/cancel races.
- Lifecycle/controller tests prove a terminal refresh returns only the terminal variant, does not invoke auth recovery, and does not leak action fields.
- Frontend unit and Playwright tests prove the expired card consumes the terminal result, becomes noninteractive, uses state-specific copy, unlocks or continues observing the exact run, and causes no ordinary Agent request or repeat Feishu write.
- Existing success compensation, confirmation, cancellation, auth-session, and integration suites remain green.
- Run focused race tests, `go test ./...`, `task lint`, frontend unit/E2E tests, `npm run lint`, and `npm run type-check` before merge.

## Non-goals

- No new async queue.
- No automatic replay of failed or unknown operations.
- No new endpoint, database column, permission scope, or visual layout.
