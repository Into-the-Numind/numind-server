# Feishu Stage Handoff — Requirement

## Problem

After personal-app creation completed successfully, the authorization worker synchronously dispatched the original operation with a five-second finalization context. The operation advanced to `executing`, created the next `user_auth` session, but lark-cli did not emit its URL before that five-second context ended. The new session and operation were marked failed. A click on the stale create-app card during `executing` returned HTTP 400.

The same stale card later expired. Its refresh button called the refresh endpoint with the original session. Because the linked operation was already terminal `failed`, recovery was impossible and the endpoint converted that normal terminal condition into HTTP 500. The browser therefore could not close the historical card or explain the safe next step.

## Required behavior

- Completing one authorization phase must have a dedicated bounded dispatch budget long enough to start the next authorization phase or finish one controlled operation. It must not reuse the five-second database-finalization budget.
- `user_completed` acknowledgements received while the operation is already `executing` must return the current operation state with HTTP 200; they must not start another operation or return a validation error.
- Repeated `user_completed` acknowledgements for terminal `failed`, `unknown`, or `cancelled` operations must return that stored terminal state with HTTP 200 and must never retry a Feishu operation.
- Existing `succeeded` compensation remains unchanged and idempotent.
- Refreshing a session whose linked operation is terminal must return HTTP 200 with an explicit terminal result. It must not attempt session recovery, expose a new URL, or return a generic internal error.
- The frontend must consume that terminal result, close the stale external-action card, remove its controls, and tell the user: `原飞书任务已结束，请重新发送原指令。`
- The already failed dev operation is not automatically replayed. The user submits the original instruction again. The preserved app and encrypted HOME are reused, so a new app is not created.

## Acceptance

1. Customer regression tests fail before the production change and pass after it.
2. The automatic dispatch context exceeds the authorization URL-start window while remaining bounded.
3. Executing and terminal stale acknowledgements are side-effect-free and return stored state.
4. Refreshing a terminal linked operation returns a typed terminal result and never calls authorization recovery.
5. The expired card becomes terminal after that result, has no refresh/continue control, and does not trigger an Agent request or a second refresh.
6. Focused tests, race test, full Go tests, frontend unit/E2E tests, lint, and type-check pass.
7. The backend and frontend are merged to `develop`, deployed to dev together, and health-checked before the user retries.
