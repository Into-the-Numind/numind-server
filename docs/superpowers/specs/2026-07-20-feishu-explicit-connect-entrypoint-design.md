# Feishu Explicit Connect Entrypoint Design

## Architecture

`lark_connect` accepts `{}` and derives user/run/tool-call identity from context. `FeishuOperationService.Connect` creates a durable `workspace connect` operation with an encrypted server-only `ConnectionOnly` request. Existing operation execution starts create-app or reauth recovery when disconnected. Once the account is connected, the operation commits `{"connected":true}` without invoking lark-cli. The standard external-action yield and continuation path therefore remains authoritative.

The settings component calls the existing manual lifecycle endpoint. It renders only the transient official URL returned in the live response, never persists it, and uses a new click to let the server advance/reconcile the flow. Status remains read-only.

## Invariants

- `lark_inspect` stays read-only.
- Only the service can construct a connection-only operation.
- Connection-only requests have no business scopes and never reach CLI execution.
- Every Agent action has exact operation/session/tool-call identity and uses the existing durable pending external action.
- Repeated calls with the same run/tool call are idempotent; different explicit requests converge through the account/session state machine.
- Settings never accepts user-supplied identity, scope, app credentials, or authorization URL.

## UX states

- none/error/reauth: primary button starts immediately and becomes loading.
- live action: show one official-link button plus “我已完成，继续”; suppress duplicate starts while pending.
- connected: show success; reauthorize remains explicit.
- failure: preserve current state, show bounded retry feedback.

## Compatibility

No schema or public endpoint change. Existing operations and old Agent transcripts remain readable because `connection_only` is optional and absent on historical encrypted requests.
