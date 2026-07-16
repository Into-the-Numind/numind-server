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

This makes a stale click safe without turning it into an implicit retry. The frontend already understands `executing` as nonterminal and terminal states as completed/closed outcomes, so no public contract or UI code changes are required.

## Current failed operation

The current dev operation is already terminal `failed`; automatically resurrecting it would weaken write-safety because a generic historical failure is not sufficient proof that no remote side effect occurred. After deployment, another click returns the stored failure with HTTP 200, closing the stale card. Reissuing the original instruction creates a fresh idempotent operation while reusing the app-ready account and encrypted lark-cli HOME.

## Tests

- A customer regression test proves the post-auth dispatcher receives a deadline longer than the authorization start window; the old five-second context fails this test immediately.
- Lifecycle service tests prove `user_completed` during `executing` and after terminal failure returns stored state and invokes neither operation resume nor Agent continuation.
- Existing success compensation, confirmation, cancellation, auth-session, and integration suites remain green.
- Run focused race tests, `go test ./...`, and `task lint` before merge.

## Non-goals

- No new async queue.
- No automatic replay of failed or unknown operations.
- No new frontend state, endpoint, database column, or permission scope.
