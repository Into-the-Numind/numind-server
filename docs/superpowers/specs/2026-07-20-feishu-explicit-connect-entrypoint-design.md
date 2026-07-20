# Feishu Explicit Connect Entrypoint Design

## Architecture

`lark_connect` accepts `{}` and derives user/run/tool-call identity from context. `FeishuOperationService.Connect` creates a durable `workspace connect` operation with an encrypted server-only `ConnectionOnly` request. Existing operation execution starts create-app or reauth recovery when disconnected. Once the account is connected, the operation commits `{"connected":true}` without invoking lark-cli. The standard external-action yield and continuation path therefore remains authoritative.

The settings component calls the existing manual lifecycle endpoint. It renders only the transient official URL returned in the live response, never persists it, and acknowledges the exact server-owned session. Manual actions use the connect endpoint; operation-bound explicit-connect actions use the existing operation resume endpoint. A restored URL-free action uses the exact-session refresh endpoint. Status remains read-only.

## Invariants

- `lark_inspect` stays read-only.
- Only the service can construct a connection-only operation.
- Connection-only requests carry only fixed `offline_access`, have no business scopes, and never reach CLI business execution.
- Every Agent action has exact operation/session/tool-call identity and uses the existing durable pending external action.
- Repeated calls with the same run/tool call are idempotent. Settings, explicit connect, and business-triggered recovery share one account-generation bootstrap owner, so no two app/auth workers can run in parallel.
- A crash orphan is protected during the normal operation-to-session gap and can be atomically retired after two minutes; stale state cannot block reconnection forever.
- Settings never accepts user-supplied identity, scope, app credentials, or authorization URL.

## UX states

- none/error/reauth: primary button starts immediately and becomes loading.
- live action: show one official-link button plus “我已完成，继续”; suppress duplicate starts while pending.
- restored action without a URL: show one “恢复授权步骤” action before completion.
- Agent-owned business authorization: show a read-only handoff message; Settings never takes over its scopes.
- connected: show success; reauthorize remains explicit.
- failure: preserve current state, show bounded retry feedback.

## Compatibility

No schema or new public endpoint. `POST /v1/feishu/connect` keeps `{intent:"manual"}` and adds only the strict, backward-compatible `{intent:"manual",action:"user_completed",session_id:"..."}` variant. Existing operations and old Agent transcripts remain readable because `connection_only` is optional and absent on historical encrypted requests. Restart reconstruction accepts only `lark_connect` or `lark_execute` as the target Feishu tool name.
