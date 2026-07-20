# Proposal: durable Feishu continuation as one visible task

## Decision

Repair the existing architecture instead of adding another confirmation or orchestration layer.

- Make the session snapshot contract self-identifying and keep a frontend fallback to `run.session_id` for rolling compatibility.
- Reconcile a newly discovered durable external action immediately; obtain its one-time URL from the existing refresh endpoint exactly once per action/session.
- Treat narration changes inside an existing tool group as timeline changes for follow-scroll.
- Preserve explicit error truth. Completion of the overall run is not evidence that each prior tool call succeeded.
- Persist bounded, provider-safe successful `lark_execute` results as protocol history across multiple yields. Tool arguments remain `{}` on reconstruction.
- Keep the unknown-write safety fence closed for writes while admitting only bounded catalog-normalized reads for outcome verification.

## Rejected alternatives

- Reloading the whole page or periodically replacing the complete chat: hides races and causes visible jumps.
- Persisting authorization URLs: creates stale secret-bearing links and weakens the current security boundary.
- Marking every prior error green when the run completes: visually pleasant but factually wrong.
- Letting an unknown write retry: can duplicate Bases, fields, records, documents, or wiki nodes.
- A deterministic platform workflow for all commands: removes useful LLM judgment and expands scope beyond this reliability repair.
