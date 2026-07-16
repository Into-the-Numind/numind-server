# Feishu Stage Handoff — Proposal

## Evidence

Dev logs for operation `c6efaebf-3465-4676-bee9-0236cc78aac8` show:

- create-app session completed at 14:05:57;
- the operation immediately moved from `waiting_connection` to `executing`;
- a `user_auth` session was created and claimed;
- exactly five seconds later the user-auth session and operation were marked failed;
- a stale `user_completed` acknowledgement during `executing` returned 400.

No business lark-cli invocation began, so no document write occurred.

## Options

### A — Separate dispatch budget plus idempotent stale acknowledgement (chosen)

Use a dedicated bounded context for durable post-auth dispatch, sized to the existing lark-cli hard ceiling. Treat `executing` and stored terminal acknowledgements as read-only state observations.

This fixes the real stage boundary without changing API shape, database schema, frontend, or replay safety.

### B — Increase the five-second constant

Rejected because it couples database cleanup and long orchestration, and leaves the stale-card 400 race intact.

### C — Introduce a new durable async queue

Deferred. It is a larger infrastructure project and is unnecessary while the current durable operation/session stores and managed worker already provide restart and idempotency boundaries.

## Scope

- Backend auth-session dispatch context.
- Backend lifecycle acknowledgement semantics.
- Reproduction and regression tests.
- No DB/API/frontend/configuration changes and no automatic replay of terminal writes.
