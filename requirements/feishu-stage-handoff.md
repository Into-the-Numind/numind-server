# Feishu Stage Handoff — Requirement

## Problem

After personal-app creation completed successfully, the authorization worker synchronously dispatched the original operation with a five-second finalization context. The operation advanced to `executing`, created the next `user_auth` session, but lark-cli did not emit its URL before that five-second context ended. The new session and operation were marked failed. A click on the stale create-app card during `executing` returned HTTP 400.

The same stale card later expired. Its refresh button called the refresh endpoint with the original session. Because the linked operation was already terminal `failed`, recovery was impossible and the endpoint converted that normal terminal condition into HTTP 500. The browser therefore could not close the historical card or explain the safe next step.

## Required behavior

- Completing one authorization phase must have a dedicated bounded dispatch budget long enough to start the next authorization phase or finish one controlled operation. It must not reuse the five-second database-finalization budget.
- `user_completed` acknowledgements received while the operation is already `executing` must return the current operation state with HTTP 200; they must not start another operation or return a validation error.
- Every lifecycle entry (`user_completed`, `confirmed`, `cancelled`, and terminal refresh) must repair the linked Agent handoff without replaying a Feishu write:
  - `succeeded` compensates only the durable Agent continuation through the idempotent dispatcher;
  - `failed`, `unknown`, and `cancelled` append the allowlisted terminal tool result to the exact user/run/tool call and clear its pending external action.
- The same rule applies when an operation becomes terminal during the current acknowledgement, including app-scope completion, completed-auth dispatch, confirmation, and a concurrent confirm/cancel race.
- `executing` remains an honest HTTP 200 observation; it does not start a second operation.
- Refreshing a session whose linked operation is terminal must return HTTP 200 with an explicit terminal result. It must not attempt session recovery, expose a new URL, or return a generic internal error.
- The frontend must consume that terminal result, remove the stale URL and controls, and update the exact run: failed/unknown/cancelled unlock the input; succeeded enters `external_resume_ready` and keeps observing the original Agent continuation.
- Terminal copy must be state-specific. Only known `failed` tells the user to resend. `unknown` tells the user to verify Feishu before retrying, `cancelled` reports cancellation, and `succeeded` says the original task is continuing.
- The already failed dev operation is never replayed. Its Agent wait is terminalized; a fresh user instruction reuses the preserved app and encrypted HOME rather than creating another app.

## Acceptance

1. Customer regression tests fail before the production change and pass after it.
2. The automatic dispatch context exceeds the authorization URL-start window while remaining bounded.
3. Executing stale acknowledgements are side-effect-free; terminal acknowledgements perform only the exact durable Agent settlement described above.
4. Refreshing a terminal linked operation settles/compensates the Agent handoff, returns a typed terminal result, and never calls authorization recovery.
5. The expired card removes its controls, shows state-specific guidance, unlocks a failed/unknown/cancelled run or keeps observing a succeeded run, and does not create an ordinary Agent request or second Feishu write.
6. Focused tests, race test, full Go tests, frontend unit/E2E tests, lint, and type-check pass.
7. The backend and frontend are merged to `develop`, deployed to dev together, and health-checked before the user retries.
