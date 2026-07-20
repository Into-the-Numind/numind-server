# Feishu multi-authorization continuity design

## Contracts

### Snapshot

`GET /sessions/:id/snapshot` returns top-level `session_id`, `run`, and `messages`. The frontend accepts top-level `session_id` first and `run.session_id` only as a rolling-upgrade fallback, then validates it against both the requested and active sessions.

Snapshot external actions remain URL-free. On first rendering a pending URL-free action, the component performs one operation-bound refresh. A guard keyed by operation/session prevents loops; navigation, replacement, terminal state, and run mismatches discard late responses.

### Timeline truth

The message list watches a small monotonic presentation signature (message count plus the last tool aggregate state/event count), allowing updated narration inside an existing message to follow the viewport. No raw model reasoning is added.

`finalizeToolGroups` converts only queued/use/progress calls to a terminal visual state. Existing error/rejected states remain unchanged on live completion and reload.

### Multi-yield provider history

Narration events carry server-internal input/result bytes excluded from JSON. Successful `lark_execute` results are converted at persistence time into a provider-safe assistant tool-call envelope and matching tool result. Arguments are always stripped. Results are valid JSON and bounded; oversize results use a valid JSON preview envelope. UI transformation ignores protocol-only turns.

### Unknown write verification

The run guard records `unknown_result`. It continues to deny every write. Up to three serialized commands whose argv normalizes through the existing catalog as `RiskRead` may execute for verification. These reads do not clear the terminal write fence. All existing identity, scope, catalog, rate, operation, and idempotency checks remain in force.

## Failure behavior

- Snapshot identity mismatch: discard response and retry on the next poll.
- URL refresh failure: show the existing actionable refresh error; never retry in a loop.
- Provider result persistence failure: retain UI narration and fail closed to existing history behavior.
- Unknown verification exhaustion: return the existing stopped contract; do not write.
