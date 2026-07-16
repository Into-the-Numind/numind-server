# Feishu Split Device Authorization Design

- Feature: `feishu-device-code-auth`
- NDF track/stage: Standard / S2
- Date: 2026-07-16
- Repositories: `numind-server`, `numind-web-v3`
- Status: approved in conversation; written-spec review pending
- Requirement: `requirements/feishu-device-code-auth.md`
- Proposal: `proposals/feishu-device-code-auth-proposal.md`
- ADR: `.ndf/decisions/feishu-device-code-auth/0001-split-device-flow.md`

## 1. Decision summary

Replace only the `user_auth` part of the Feishu authorization lifecycle with the
pinned `lark-cli 1.0.68` split device protocol:

1. start a short process with `auth login --scope ... --no-wait --json`;
2. return the official verification URL while encrypting the exact device-code
   resume credential on the server;
3. when the user clicks `我已完成，继续`, complete the same authorization with
   `auth login --device-code ... --json`;
4. fenced-publish the resulting per-user CLI HOME and atomically mark the
   account/session complete;
5. use the existing durable dispatcher to resume the exact persisted Feishu
   operation and Agent tool call at most once.

There is no independent Feishu Agent and no direct Go OAuth implementation.
The existing Agent continues to choose Docs/Base/Wiki operations through
`lark_execute`; the platform owns authentication, credentials, concurrency,
and operation recovery.

The implementation extracts a focused `DeviceAuthFlow` instead of adding the
new protocol to the already large `AuthSessionService`. `AuthSessionService`
continues to orchestrate `create_app`, `app_scope`, and `user_auth`, but delegates
the two user-auth protocol phases to the focused component.

## 2. Problem and evidence

The current `AuthSessionCLI.RunBlocking` contract assumes one process will emit
a verification URL and then stay alive until the user finishes authorization.
`AuthSessionService.claimAndStart` waits up to 30 seconds for that URL and keeps
the URL only in an in-memory registry.

The pinned CLI and its bundled official guidance define a different protocol:

- `lark-cli auth login --scope "..." --no-wait --json` returns a
  `verification_url` and `device_code`, then exits;
- a later server action runs `lark-cli auth login --device-code <device_code>`.

Because the current data model deliberately excludes device codes, the server
cannot complete the second phase after the user leaves the request to authorize
in Feishu. The observable result is a pending user-auth session, eventual
operation failure, generic `Internal server error`, and an Agent run that cannot
continue the original document operation.

The generic Agent stream-start ordering defect was repaired separately. This
feature validates that the Feishu external-action continuation appears without a
page reload, but it must not reopen or redesign the generic SSE protocol unless
Playwright proves a Feishu-specific defect.

## 3. Goals and non-goals

### 3.1 Goals

- Return the official user-authorization card immediately without a background
  process that must survive the user's Feishu interaction.
- Preserve sufficient encrypted server state to complete authorization after a
  backend restart or on another service instance.
- Persist one isolated lark-cli HOME per Numind user and connection generation.
- Resume the exact original Docs/Base/Wiki operation without a new prompt or
  regenerated argv.
- Make repeat clicks, concurrent confirmations, stale links, lease loss, and
  crash recovery deterministic and safe.
- Replace expected authorization outcomes with explicit user-visible states,
  not generic 400/500 errors.
- Keep recovery credentials out of the browser, LLM context, Agent sandbox,
  logs, metrics, and public errors.
- Preserve execute-first permission behavior: only a controlled missing-scope or
  revoked result may create an incremental authorization action.

### 3.2 Non-goals

- No independent Feishu Agent.
- No IM capability.
- No new HTTP endpoint or settings-page workflow.
- No direct Go implementation of Feishu OAuth.
- No redesign of personal app creation or app-scope approval.
- No broad shell-permission rewrite.
- No claim of remote exactly-once semantics. Feishu writes remain at-most-once;
  an ambiguous remote write result becomes `unknown` and is not auto-replayed.
- No new LLM call, prompt round, Langfuse trace root, or generation point.

## 4. Architecture and component boundaries

### 4.1 `AuthSessionService`

Keep the existing service as the lifecycle orchestrator:

- create/get/rebind durable auth sessions;
- keep the existing `create_app` blocking worker and `app_scope` approval path;
- route `user_auth` start and completion to `DeviceAuthFlow`;
- retain URL-registry behavior for live-only URLs;
- call the existing durable `WorkspaceResumeDispatcher` only after committed
  authorization success.

`AuthSessionService` must no longer call `RunBlocking` for `user_auth`.

### 4.2 `DeviceAuthFlow`

Add a focused backend component under `internal/numind/biz/feishu` with two
operations:

```go
type DeviceAuthFlow interface {
    Start(ctx context.Context, request DeviceAuthStartRequest) (*DeviceAuthStartResult, error)
    Complete(ctx context.Context, request DeviceAuthCompleteRequest) (*DeviceAuthCompleteResult, error)
}
```

The exact Go names may change in S3, but the responsibility boundary is fixed:

- claim/renew/release a user-auth session lease;
- call the split CLI adapter;
- encrypt/decrypt the resume credential;
- validate session/account/operation/scope ownership;
- prepare, but not prematurely publish, a candidate HOME;
- classify completion outcomes;
- ask the store to perform fenced terminal or success transactions.

It does not parse HTTP requests, render UI text, select business scopes, execute
the resumed Docs/Base/Wiki command, or call the LLM.

### 4.3 Split CLI adapter

Replace the user-auth use of `AuthSessionCLI.RunBlocking` with a typed adapter:

```go
type DeviceAuthCLI interface {
    StartUserAuth(ctx context.Context, home string, scopes []string) (DeviceAuthStart, error)
    CompleteUserAuth(ctx context.Context, home, deviceCode string) (DeviceAuthOutcome, error)
    AuthStatus(ctx context.Context, home string) (bool, error)
}
```

`ControlledLarkCLIRunner` remains the trusted process boundary and continues to
use `exec.CommandContext` with a fixed absolute binary, never a shell.

### 4.4 Resume-credential cipher

Add a purpose-separated cipher interface rather than treating the device code
as ordinary session metadata. It reuses the existing configured keyring and key
version model but has its own cryptographic purpose string, such as
`feishu-auth-resume/v1`.

### 4.5 Vault candidate publication

The current `WithHome` helper can publish a changed HOME before the auth-session
lease is fenced. User-auth completion therefore needs a candidate API:

1. restore the current vault revision to a private temporary HOME;
2. let the CLI modify that HOME;
3. validate user auth and seal an encrypted candidate in memory;
4. publish that candidate only inside the store's session/account/vault fenced
   transaction.

The existing `WithHome` path remains valid for ordinary operations and the
existing create-app flow.

### 4.6 Frontend

The frontend continues to use the existing API, Pinia store, Agent timeline,
`FeishuActionCard`, QR generation, busy state, session-epoch fences, and refresh
endpoint. The only contract expansion is that an operation-resume response may
carry a newly minted live action plus a safe notice code.

## 5. Pinned CLI protocol

### 5.1 Start

The only accepted user-auth start command shape is:

```text
lark-cli auth login --scope <canonical-space-separated-scopes> --no-wait --json
```

Requirements:

- binary version must already have passed the existing `1.0.68` probe;
- scopes come only from the server-owned catalog and are canonicalized, sorted,
  deduplicated, length-bounded, and IM-rejected;
- stdout and stderr remain separately bounded; the JSON line cap stays at or
  below 1 MiB;
- success requires process exit success and JSON `ok == true`;
- the fixture must contain non-empty `verification_url`, `device_code`, and a
  bounded expiry value from the pinned CLI contract;
- the verification URL is treated as an opaque string after validation and must
  use HTTPS on `open.feishu.cn` or `open.larksuite.com` with the expected device
  authorization path;
- `device_code` is opaque, length-bounded, never logged, and never normalized;
- effective session expiry is the earliest of the CLI credential expiry and the
  server's bounded auth-session duration;
- missing fields, duplicate JSON envelopes, unknown schema, oversized output,
  invalid URL, invalid expiry, or `ok != true` fail closed.

The start process must exit before the action is returned. No user-auth worker
or child process remains alive.

### 5.2 Complete

The only accepted completion command shape is:

```text
lark-cli auth login --device-code <opaque-device-code> --json
```

The adapter passes the secret as a direct argv element because the pinned CLI
has no stdin form. Structured process logging must replace that value with
`[REDACTED]` before any log/error/audit call.

Normalized outcomes are:

| Outcome | Meaning | Durable action |
|---|---|---|
| `completed` | CLI success and user identity evidence is valid | prepare candidate HOME and finalize success |
| `pending` | user has not yet completed the official step | release lease, retain unexpired credential |
| `rejected` | Feishu explicitly reports denial/cancellation | clear credential, mark rejected, prepare replacement |
| `expired` | Feishu explicitly reports expired device flow | clear credential, mark expired, prepare replacement |
| `retryable_dependency` | network/5xx/controlled transient failure | reconcile `AuthStatus`; otherwise release lease and retain credential while valid |
| `protocol_failure` | malformed/unknown CLI contract | clear credential, mark failed, surface safe component error |
| `ambiguous` | deadline/process interruption after CLI may have written HOME | inspect candidate HOME with `AuthStatus`; never guess from text |

The complete operation uses a bounded server-owned context after the session is
claimed. Browser cancellation cannot abandon a committed authorization, while
the CLI and lease still have finite deadlines and heartbeat renewal. One HTTP
completion attempt may wait for at most 30 seconds, further bounded by the
credential's remaining lifetime. On deadline, the flow runs the controlled
`AuthStatus` reconciliation: proven user identity proceeds as success;
otherwise the lease is released, the still-valid credential is retained, and
the response is `authorization_pending`. The request never waits for the full
12-minute session lifetime.

## 6. Persistence and migration

### 6.1 `feishu_auth_session` additions

Add one forward-compatible migration with the following columns:

| Column | MySQL type | Null/default | Meaning |
|---|---|---|---|
| `protocol_version` | `TINYINT UNSIGNED` | `NOT NULL DEFAULT 1` | legacy/blocking = 1; split user auth = 2 |
| `resume_credential_ciphertext` | `LONGBLOB` | nullable | authenticated encryption of the opaque device code |
| `resume_key_version` | `VARCHAR(32)` | nullable | keyring version needed for decryption |
| `resume_expires_at` | `DATETIME(3)` | nullable | exact CLI resume-credential expiry |
| `scope_hash` | `CHAR(64)` | nullable | SHA-256 of canonical requested scopes |

No new index is required. Existing lookup and lease indexes continue to serve
session ownership and reclaim queries.

For protocol-v2 `user_auth` sessions:

- starting: credential columns are all null;
- waiting for user: ciphertext, key version, resume expiry, and scope hash are
  all present and valid;
- partial credential state is invalid and must fail closed;
- terminal state clears ciphertext, key version, and resume expiry in the same
  state transition; non-secret scope hash may remain for audit correlation;
- `create_app` and `app_scope` never populate credential columns.

### 6.2 Encryption binding

Authenticated encryption AAD binds at least:

```text
purpose=feishu-auth-resume/v1
user_id
generation
app_id
operation_id-or-manual
auth_session_id
scope_hash
resume_expires_at
key_version
```

Changing any bound value makes decryption fail. The plaintext contains only the
opaque device code and a small schema version; it does not contain the URL,
tokens, HOME, scopes, or user content.

### 6.3 Legacy pending sessions

During migration, existing protocol-v1 `pending` sessions whose phase is
`user_auth` are marked `superseded`, their lease is cleared, and `completed_at`
is set. They have no resume credential and cannot be completed safely.

Linked waiting operations remain intact. The first exact resume or refresh of
such an operation creates and rebinds a protocol-v2 replacement instead of
returning `Internal server error`. Completed sessions and connected accounts are
unchanged.

### 6.4 Cleanup

Every terminal transition clears the secret fields. A bounded cleanup method
also clears credential fields from expired, abandoned protocol-v2 sessions in
small indexed batches. Cleanup is idempotent and never changes an operation from
waiting to executed.

## 7. State machine and concurrency

The existing session states remain the only durable state vocabulary:

| State/shape | Meaning | Allowed next action |
|---|---|---|
| `pending`, v2, no credential, no live lease | start not finished | claim and retry start |
| `pending`, v2, credential present, no live lease | waiting for user | claim completion |
| `pending`, live lease | another owner is starting/completing | observe as processing |
| `completed` | HOME/account/session transaction committed | idempotent dispatch/read only |
| `expired` | Feishu or server expiry proved | no completion; create replacement |
| `rejected` | Feishu explicitly rejected | no completion; create replacement |
| `failed` | deterministic protocol/crypto/system failure | no secret reuse; explicit recovery |
| `superseded` | link, generation, scope, or binding was replaced | old session never resumes |

The existing `lease_owner + lease_until` compare-and-swap is retained. Owners
renew before expiry. All mutating store methods require the exact owner and an
unexpired lease. A late owner may finish its local CLI process, but cannot attach
a credential, publish a vault candidate, terminalize the session, or update the
account after its lease fence is lost.

For one `user_id + generation + operation_id-or-manual + user_auth + canonical
scope set`, at most one session is actionable. Operation metadata remains the
source of truth for the current session; the browser cannot submit or select a
session/device code through the resume endpoint.

## 8. Start transaction

For a new or safely recoverable protocol-v2 user-auth session:

1. validate account ownership, generation, app ID, operation waiting state, and
   canonical scope hash;
2. claim the session lease;
3. restore the current user HOME without publishing changes;
4. run the short no-wait CLI start;
5. strictly validate URL, credential, and expiry;
6. encrypt the credential with the exact AAD;
7. conditionally attach ciphertext/key/expiry/hash and release the lease only if
   the session is still pending, owned, current-generation, and unexpired;
8. set account state to `waiting_user_auth` in the same store transaction;
9. place the live URL in the existing process-local URL registry and return the
   live action.

If the CLI succeeded but the database attach failed, the unreferenced device
flow is discarded and expires naturally. The session remains reclaimable with
no credential, so a later owner can generate a fresh flow. A URL is never
returned unless its matching encrypted credential was durably attached first.

The URL itself remains live-only. It is excluded from auth-session metadata,
operation summaries, durable Agent pending-action JSON, logs, and status
snapshots. If a page reload or cross-instance request no longer has the URL, the
existing refresh path supersedes the session and mints a fresh link.

## 9. Completion and fenced Vault publication

`WorkspaceLifecycleService.Resume(action=user_completed)` loads the current
account, operation, and exact recovery session. For `user_auth`, it calls
`CompleteUserAuthorization` instead of merely observing the old background
worker.

Completion order is fixed:

1. validate user, account, generation, app, operation, phase, scopes, protocol,
   credential presence, and expiry;
2. claim the exact session and capture the expected vault revision;
3. decrypt the credential only while the lease is owned;
4. restore the current HOME to a private temporary directory;
5. run the completion CLI with heartbeat renewal;
6. classify the result and, for possible success, validate `AuthStatus`, user
   identity, app ID, and fixed CLI version;
7. seal a candidate HOME in memory without publishing it;
8. execute one database transaction that fences all of:
   - session is still `pending` with the same lease owner and unexpired lease;
   - account still has the same user, generation, and app ID;
   - linked operation still waits on this session when `operation_id` exists;
   - vault still has the expected user, generation, and revision;
9. within that transaction publish the candidate vault revision, set the
   account `connected=true` / `connection_state=connected`, set verified CLI
   evidence, mark the session `completed`, clear credential and lease fields,
   and commit;
10. after commit, call the existing durable dispatcher for the exact operation.

The authorization completion attempt is bounded to 30 seconds as defined in
Section 5.2. After the success transaction commits, the existing detached
dispatcher retains its separate bounded runtime; it is not tied to the HTTP
request deadline.

The vault, account, and session share the same database, so publication and
finalization are atomic. A candidate encrypted in memory but not committed is
not an active credential snapshot.

Repeated confirmation semantics:

- active lease: return `authorization_processing` without a second CLI call;
- still pending after a definite not-yet-authorized result: return
  `authorization_pending` and retain the current card;
- completed: dispatch/read the existing operation idempotently;
- terminal old session: observe or create the exact safe replacement; never
  execute the old credential;
- terminal operation: use the existing success compensation or terminal
  settlement; never replay the Feishu write.

## 10. Rejection, expiry, and replacement

For explicit `rejected` or `expired` completion:

1. conditionally terminalize the owned old session and clear its credential;
2. create a protocol-v2 replacement session with no credential;
3. atomically rebind the still-waiting operation summary to the replacement;
4. run the normal start flow for the replacement;
5. return the new live action in the same resume response.

If replacement start is temporarily unavailable, return a typed dependency
error while leaving the exact replacement session recoverable. The card keeps
its retry/refresh path; no original operation is lost or executed.

Refreshing a link follows the same replacement invariant. A late response from
an older frontend session is additionally blocked by the existing frontend
route/session epoch, operation ID, session ID, and run ID checks.

## 11. Crash recovery

| Crash boundary | Durable evidence | Recovery |
|---|---|---|
| before credential attach | v2 pending, no credential | reclaim and rerun no-wait start |
| after credential attach | encrypted credential and expiry | another instance can complete |
| CLI completion interrupted | candidate HOME may contain auth evidence, DB unchanged | run controlled `AuthStatus`; publish only if exact evidence and fences pass, otherwise retain/retry while unexpired |
| candidate sealed, transaction not committed | no active vault revision change | discard candidate and retry/reconcile |
| transaction committed, HTTP response lost | session completed and vault/account current | repeated click observes completion and idempotently dispatches |
| session completed, dispatcher interrupted | operation and Agent continuation claims are durable | redispatch; terminal operation never calls CLI again |

No recovery branch uses requested scopes as proof that scopes were granted.
Ordinary commands remain the authoritative capability probe.

## 12. HTTP API contract

No route is added. Authentication derives `user_id` from the existing login
token. The browser cannot submit user ID, generation, app ID, session ID,
device code, scopes, URL, or CLI arguments to operation resume.

### 12.1 Resume request

```http
POST /v1/feishu/operations/:operation_id/resume
Content-Type: application/json

{"action":"user_completed"}
```

### 12.2 Resume response

The existing operation data is extended as follows:

```json
{
  "operation_id": "op-123",
  "state": "waiting_user_auth",
  "notice_code": "authorization_expired",
  "action": {
    "operation_id": "op-123",
    "session_id": "auth-new",
    "phase": "user_auth",
    "url": "https://open.feishu.cn/...",
    "expires_at": "2026-07-16T12:00:00Z"
  }
}
```

`action` remains allowlisted. It never includes scopes, provider internals,
device code, app ID, HOME data, CLI output, or tokens. `url` is present only
when this request safely minted a new live action. Existing non-live actions may
omit it.

Allowed `notice_code` values are:

| Code | Frontend copy/behavior |
|---|---|
| `authorization_pending` | 尚未检测到授权完成；retain current link and retry later |
| `authorization_processing` | 正在确认授权状态；retain card and prevent duplicate work |
| `authorization_rejected` | 本次授权未通过，已生成新的授权链接 |
| `authorization_expired` | 原链接已过期，已生成新的授权链接 |
| `authorization_updated` | 授权步骤已更新，已生成新的授权链接 |

No notice is required for normal operation success/terminal states.

### 12.3 Status behavior matrix

| Condition | HTTP | Operation state | Action | Notice |
|---|---:|---|---|---|
| not yet authorized | 200 | `waiting_user_auth` | omitted | `authorization_pending` |
| another owner processing | 200 | `waiting_user_auth` | omitted | `authorization_processing` |
| rejected with replacement | 200 | `waiting_user_auth` | new live action | `authorization_rejected` |
| expired with replacement | 200 | `waiting_user_auth` | new live action | `authorization_expired` |
| legacy/stale exact binding replaced | 200 | `waiting_user_auth` | new live action | `authorization_updated` |
| authorization committed | 200 | updated operation state | only if next external step exists | omitted |
| invalid fixed action/body | 400 | n/a | none | none |
| cross-user/not found | 404 | n/a | none | none |
| current lifecycle conflict without safe replacement | 409 | n/a | none | none |
| CLI/Feishu dependency temporarily unavailable | 503 | operation preserved | none | none |
| unexpected internal invariant violation | 500 | operation preserved | none | none |

Expected pending, processing, rejection, expiry, and legacy replacement are not
mapped to generic `ErrInternalServer`.

### 12.4 Refresh

`POST /v1/feishu/actions/:session_id/refresh` retains its exclusive tagged
union: a new live action or a linked terminal operation. It accepts no body and
never returns a device code. It also supports safe recovery from protocol-v1
superseded user-auth sessions and protocol-v2 actions whose live URL was lost.

## 13. Frontend behavior

The UI reuses the existing card and design tokens. No new page, modal, or visual
system is introduced.

Required changes:

- `FeishuOperationResult.action` may contain `url` when resume created a live
  replacement;
- add the optional safe `notice_code` union;
- update the exact existing `ExternalActionMessage` in place with the new
  operation/session/phase/URL/expiry;
- regenerate the QR code from the new opaque URL;
- clear a prior local transport error when session ID changes;
- preserve the existing card and URL when action is omitted for
  pending/processing or dependency failure;
- map notice codes to fixed Chinese copy; never show raw CLI strings;
- retain the existing busy state and duplicate-click guard;
- retain session-epoch and identity checks so stale async responses cannot
  mutate a newer route or card;
- keep `aria-live="polite"`, `role="alert"`, keyboard behavior, mobile wrapping,
  and the existing Numind green/serif/token system.

The card represents only the external step. It does not synthesize an assistant
answer. After committed authorization, the durable dispatcher resumes the
original tool result and the existing Agent stream supplies processing,
reasoning, and final-answer events. The acceptance test requires those events to
appear without a full page refresh.

An old frontend safely ignores the additional response fields. If it drops a
new URL, the card's existing missing-link refresh path remains available. This
allows backend-first deployment, although dev validation must cover the final
paired versions.

## 14. Agent and shell boundary

`lark_execute` remains the only Agent entry for Feishu business execution and
connection recovery. `lark_skill_read` continues to expose syntax guidance, but
local-agent instructions cannot override the hosted credential boundary.

Add one narrow semantic bash-validator rule that rejects command basename
`lark-cli`, including absolute paths and the already parsed wrappers such as
`sudo`, `command`, `exec`, `env`, `nohup`, and `time`. The friendly result tells
the Agent to use `lark_execute`. This rule does not prohibit unrelated shell
commands or redesign the sandbox policy.

The current `bash_exec` implementation executes in a dedicated Docker sandbox,
not in the backend auth runner's process namespace. Production verification
must preserve that isolation. Because the pinned CLI accepts the device code
only in argv, no untrusted process may share a readable process namespace with
the auth runner. If deployment evidence disproves that assumption, production
release is blocked until the runner uses a separate UID/process namespace.

## 15. Error classification

The CLI adapter and lifecycle map errors by controlled structured evidence, not
by speculative prose:

| Class | User meaning | Secret handling | Retry/write rule |
|---|---|---|---|
| authorization pending | official step not complete | retain while valid | completion may be retried; business write not started |
| processing | another owner has lease | unchanged | observe/retry later |
| rejected | user/Feishu denied | clear | mint new auth action; no business write |
| expired | credential expired | clear | mint new auth action; no business write |
| revoked/missing user scope | connected identity lacks exact operation scope | new exact incremental flow | resume only pre-execution waiting operation |
| app scope missing | tenant app approval required | no user credential change | existing app-scope card |
| resource ACL | connected account lacks resource access | no OAuth | tell user to share the resource; no replay |
| transient auth dependency | network/5xx/timeout with no success proof | retain if valid | reconcile/retry auth only |
| protocol/crypto failure | incompatible output or invalid ciphertext | clear/fail closed | explicit component error; no business write |
| unknown business write | remote side effect may have happened | n/a | operation `unknown`; never auto-replay |

The UI must not claim “Feishu docs and sandbox are temporarily unavailable”
unless separate controlled evidence proves both. Generic failures remain honest,
safe, and retryable.

## 16. Security and privacy

- All store reads/writes bind authenticated user ID and current generation.
- App ID is revalidated against account/HOME evidence before decryption and
  publication.
- Resume ciphertext uses authenticated encryption and purpose-separated AAD.
- Ciphertext is decrypted only by the current unexpired lease owner.
- Temporary HOME permissions remain directory `0700` and file `0600`.
- Logs, metrics, traces, test snapshots, error wrapping, and audit rows exclude:
  device code, URL query, HOME content, tokens, app secret, document/Base/Wiki
  content, and complete argv.
- Audit-safe identifiers are user ID/internal hash, generation, operation ID,
  session ID, phase, state transition, lease attempt, CLI version, duration,
  error class, and recovery path.
- URL is returned only to the authenticated owner through live connect/refresh/
  resume/SSE responses and is treated as opaque by the browser.
- A terminal session clears the credential in its state transaction; bounded
  cleanup is defense in depth.
- Unbind increments generation and permanently invalidates old sessions,
  operations, candidates, and callbacks.
- No administration API can view, export, or complete a user's authorization.

## 17. Observability

Emit redacted state events and metrics for:

- device start attempt/success/failure and duration;
- complete pending/processing/success/rejected/expired/protocol failure;
- lease claim, renewal failure, expiry, and reclaim;
- resume decryption failure by class, never plaintext;
- candidate HOME validation and fenced publish conflict;
- completed-session redispatch and Agent continuation retry;
- session expiry/cleanup count;
- operation final state, especially `unknown` writes.

Correlate with user, generation, operation, session, and attempt IDs. Never use
URL, scopes, document content, device code, or CLI output as metric labels.

No new LLM call is introduced, so trace topology is unchanged:

- trace root: existing Agent run;
- generations: existing Agent model calls only;
- new generation/span requirement: none;
- existing Feishu operation/session correlation remains sufficient.

## 18. Test strategy

### 18.1 Customer regression first

The first code commit in `numind-server` must be a failing reproduction test,
with message `test(qa): reproduce split Feishu device authorization failure` (or
equivalent compliant prefix). It proves the current blocking contract cannot
produce a restart-safe URL/credential transition and leaves the operation in
the reported failure path. The test fails before implementation and remains
permanently after it becomes green.

The frontend's first code commit must likewise add a failing regression for the
approved contract gap before implementation: a live replacement returned by
resume must update the same card without a page refresh. If the existing test
suite already contains an exact failing test, S3 may reference and extend it,
but the branch commit chain must still satisfy the customer-bug rule.

### 18.2 Backend unit and contract tests

- strict pinned start/complete fixtures: success, pending, rejection, expiry,
  non-JSON, missing/duplicate fields, invalid URL/expiry, oversize, `ok=false`,
  and version mismatch;
- canonical scopes and scope hash, including IM rejection;
- credential encrypt/decrypt, AAD mismatch for every owner field, key rotation,
  terminal clearing, and redacted argv/errors;
- v2 state-shape validation and protocol-v1 migration;
- lease claim/renew/reclaim, duplicate confirmation, concurrent owners, and late
  owner publication rejection;
- start attach ordering proves no URL is returned before durable ciphertext;
- candidate vault CAS, generation/app/session fence, transaction rollback, and
  committed-response-loss reconciliation;
- rejection/expiry/legacy replacement and operation-summary rebind;
- terminal operation matrix preserves success compensation and never replays
  failed/unknown/cancelled writes;
- bash-validator variants for direct, absolute-path, and wrapper invocation,
  plus benign shell non-regression.

### 18.3 Backend integration tests

- never connected through create-app/app-scope/user-auth into exact operation
  resume;
- backend restart or second instance between start and complete;
- CLI success followed by candidate failure, transaction failure, and dispatcher
  interruption;
- operation-domain-independent fixtures for Docs create, Base read, and Wiki
  update;
- no credential or URL query in API JSON, persistent Agent action JSON, logs,
  errors, or fixture snapshots;
- two-user isolation and generation invalidation;
- repeat resume produces one business CLI attempt;
- ambiguous write becomes `unknown` and is not retried.

### 18.4 Frontend runtime diagnosis and tests

Before frontend code changes, use Playwright diagnostics to capture DOM, network,
store state, and stream events for the existing resume flow. Static source
reasoning alone is not sufficient.

Add unit/component/E2E coverage for:

- immediate busy feedback after clicking continue;
- pending/processing notices retain the current link and re-enable safely;
- rejected/expired/updated response replaces the same card, session, URL,
  expiry, and QR;
- replacement clears previous local transport error;
- duplicate click sends one completion request;
- stale response after navigation/session change is ignored;
- dependency error retains link and refresh/retry action;
- success completes the external card and the Agent's processing/final response
  appears without page reload;
- reload snapshot contains no URL and safely offers refresh;
- ARIA, keyboard, desktop, and mobile behavior remain correct.

Required checks are frontend focused tests, `npm run test:e2e`,
`npm run lint`, and `npm run type-check`.

### 18.5 Full capability dev acceptance

With a real Feishu test tenant, validate:

1. first Docs create triggers only the required authorization and returns the
   created document link without page refresh;
2. Docs create/read/update all succeed;
3. Base create/read/update all succeed;
4. Wiki create/read/update all succeed;
5. a second same-scope command executes directly;
6. a newly required scope creates only an incremental action;
7. rejection, expiry, repeated click, backend restart, and two-user isolation;
8. one create prompt produces at most one remote document;
9. no IM permission or capability appears.

Only the official Feishu authorization/approval click requires the human user;
the system, database, deployment, logs, and automated checks remain AI-operated.

## 19. Development, rollout, and rollback

### 19.1 Implementation order

S3 must place backend contract tasks before frontend tasks:

1. failing backend and frontend customer regressions;
2. migration/model/store and secret cipher;
3. split CLI adapter and strict fixtures;
4. candidate vault and fenced success transaction;
5. `DeviceAuthFlow` state machine and lifecycle integration;
6. controller error mapping and locked response contract;
7. Agent shell boundary;
8. frontend API/types/store/card changes;
9. focused, race, full, Playwright, and security verification.

### 19.2 NDF gates

- S4: code only in the feature worktrees; first commits are failing customer
  regressions; backend and frontend use the locked API contract.
- S5: run local backend/frontend, full automated checks, Playwright E2E, and
  browser QA before merge. No deferral to dev.
- S6: `ndf-done` merges/pushes both develop branches, then deploy backend and
  frontend to dev for real Feishu acceptance.
- S7/prod: separate explicit production authorization is required.

Backend-first deployment is compatible with the old frontend. The final dev
acceptance still uses the paired versions.

### 19.3 Rollback

Before rolling back to code that does not understand protocol v2:

1. stop new authorization starts;
2. mark pending protocol-v2 user-auth sessions `superseded`, clear credential
   and lease fields, and preserve linked operations;
3. deploy the previous backend image;
4. keep the nullable columns and completed vault/account data; do not run a
   destructive down migration;
5. let a later supported version generate fresh actions for preserved waiting
   operations.

Completed connections and completed operations are not deleted. Ambiguous or
terminal writes are never reopened by rollback.

## 20. PRD coverage and S2 gate

| PRD requirement | Design coverage |
|---|---|
| immediate no-wait authorization card | Sections 5, 8 |
| encrypted exact-session resume credential | Sections 6, 16 |
| no secret in browser/Agent/logs | Sections 12–16 |
| resume after restart/other instance | Sections 7, 11 |
| complete HOME/account/session then resume exact task | Sections 9, 11 |
| repeat/concurrent clicks do not duplicate writes | Sections 7, 9, 18 |
| explicit pending/rejected/expired/system outcomes | Sections 10, 12, 15 |
| old pending sessions safely replaced | Sections 6.3, 10, 12.4 |
| Agent uses `lark_execute`, not raw `lark-cli` | Section 14 |
| Docs/Base/Wiki without IM | Sections 3, 18.5 |
| no page refresh for Agent continuation | Sections 13, 18.4–18.5 |
| observability without secrets | Section 17 |
| customer regression first | Section 18.1 |

The design introduces no unresolved product choice. The remaining S2 hard gate
is the customer's review of this written specification. After approval, invoke
`writing-plans` and produce the S3 implementation plan; do not begin S4 code
before that plan passes its gates.
