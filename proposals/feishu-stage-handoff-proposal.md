# Feishu Stage Handoff — Proposal

## Evidence

Dev logs for operation `c6efaebf-3465-4676-bee9-0236cc78aac8` show:

- create-app session completed at 14:05:57;
- the operation immediately moved from `waiting_connection` to `executing`;
- a `user_auth` session was created and claimed;
- exactly five seconds later the user-auth session and operation were marked failed;
- a stale `user_completed` acknowledgement during `executing` returned 400.

No business lark-cli invocation began, so no document write occurred.

Later dev logs for stale session `bca7670b-3475-4949-96f8-58c1056d60f5` show two refresh requests returning 500. The session still belonged to the user, but its linked operation was already terminal `failed`; `RefreshAction` nevertheless entered recovery, recovery rejected the terminal operation, and the service mapped that expected state to `ErrWorkspaceLifecycleUnavailable`.

Playwright runtime diagnosis confirms the expired card deliberately hides its URL, disables continue, and exposes only the refresh button. Therefore a backend-only acknowledgement change cannot close this card.

## Options

### A — Separate dispatch budget plus explicit terminal handoff (chosen)

Use a dedicated bounded context for durable post-auth dispatch, sized to the existing lark-cli hard ceiling. Treat `executing` and stored terminal acknowledgements as read-only state observations. Refresh returns one of two explicit outcomes: a fresh live action, or a terminal operation result. The frontend turns the latter into a closed card and a safe reissue instruction.

This fixes both the real stage boundary and the stale-card recovery path without weakening replay safety. It requires a coordinated backend/frontend response-shape change, but no schema or permission change.

### B — Increase the five-second constant

Rejected because it couples database cleanup and long orchestration, and leaves the stale-card 400 race intact.

### C — Introduce a new durable async queue

Deferred. It is a larger infrastructure project and is unnecessary while the current durable operation/session stores and managed worker already provide restart and idempotency boundaries.

### D — Return 404/409 for every terminal refresh

Rejected because the browser would still have to infer whether a generic error means “historical task safely ended” or a recoverable/network failure. A typed 200 terminal outcome is deterministic and lets the card settle without replay.

## Scope

- Backend auth-session dispatch context.
- Backend lifecycle acknowledgement semantics.
- Backend refresh result union (`action` or `terminal`).
- Frontend API/store/message-card handling for the terminal outcome.
- Reproduction and regression tests.
- No DB schema, permission, secret, or production-configuration changes; no automatic replay of terminal writes.
