# H1/H2 verification: preserve downstream records

## Root cause and scope

- `ExecuteNodeStream` reset the current node and then called `CleanupDownstreamForRegeneration`, which soft-deleted every later node run plus run notes and chat messages.
- `DeleteBookmark` already deletes only the selected bookmark and does not call downstream cleanup.
- The fix removes only the regeneration cleanup call. Current-node reset, permission checks, credit handling, LLM execution, and the existing bookmark-application behavior are unchanged.

## RED/GREEN evidence

- First branch commit `03bae892` contains only the customer-bug regression tests.
- Before the fix, `TestExecuteNodeStream_RegenerationPreservesDownstreamRecords` failed because downstream cleanup was called once.
- After commit `afb3a5a2`, both regeneration preservation and selected-bookmark deletion tests pass.

## Verification

- `go test ./internal/numind/biz/sop -count=1`: PASS.
- `PATH="/Users/zhiyuchen/go/bin:$PATH" task lint`: PASS (including `go vet` and `golangci-lint`).
- `go test ./...`: all SOP, store, controller, billing, and other packages passed except the pre-existing Feishu test `timeout_wins_over_truncated_output`.
- The same Feishu failure reproduces on untouched `develop` with expected `polling_pending_timeout` versus actual `ambiguous`; it is outside this change and no Feishu file was modified.

## H2 review

- Commit order satisfies the customer-bug rule: failing reproduction test first, implementation second.
- Production diff changes one SOP business-logic block only; there is no schema, API, permission, payment, AI gateway, or Langfuse change.
- The store cleanup method remains available for its separate bookmark-application caller; this hotfix does not broaden into unrelated cleanup.
- Review result: PASS, no blocking findings.
