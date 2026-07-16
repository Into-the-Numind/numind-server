# Feishu Stage Handoff — Requirement

## Problem

After personal-app creation completed successfully, the authorization worker synchronously dispatched the original operation with a five-second finalization context. The operation advanced to `executing`, created the next `user_auth` session, but lark-cli did not emit its URL before that five-second context ended. The new session and operation were marked failed. A click on the stale create-app card during `executing` returned HTTP 400.

## Required behavior

- Completing one authorization phase must have a dedicated bounded dispatch budget long enough to start the next authorization phase or finish one controlled operation. It must not reuse the five-second database-finalization budget.
- `user_completed` acknowledgements received while the operation is already `executing` must return the current operation state with HTTP 200; they must not start another operation or return a validation error.
- Repeated `user_completed` acknowledgements for terminal `failed`, `unknown`, or `cancelled` operations must return that stored terminal state with HTTP 200 and must never retry a Feishu operation.
- Existing `succeeded` compensation remains unchanged and idempotent.
- The already failed dev operation is not automatically replayed. After deployment, its stale card can close safely and the user may submit the original instruction again. The preserved app and encrypted HOME are reused, so a new app is not created.

## Acceptance

1. Customer regression tests fail before the production change and pass after it.
2. The automatic dispatch context exceeds the authorization URL-start window while remaining bounded.
3. Executing and terminal stale acknowledgements are side-effect-free and return stored state.
4. Focused tests, race test, full Go tests, and lint pass.
5. The fix is merged to `develop`, deployed to dev, and health-checked before the user retries.
