# H1-D2: Convert all five early-validation paths to soft errors

**Status:** Accepted
**Date:** 2026-05-29
**Author:** ai

## Context

`internal/numind/biz/agent/tool_file_read.go` `Execute` has five early-validation paths:

1. `invalid input JSON` — `json.Unmarshal` failed
2. `file_url is required` — input is empty
3. `user not authenticated` — `middleware.UserIDFromCtx` returned `ok=false`
4. `URL does not match agent-attachments path format` — `extractUserIDFromURL` rejected
5. `file not owned by current user` — ownership mismatch

All five returned `(nil, errno.Err*)`. Per `tool_web_fetch.go:80-95`, Eino v0.8.13 has no tool-error → tool-message hook; any Go error returned from a tool propagates as a `NodeRunError` which **terminates the agent run** before the LLM ever sees the message.

Some of these (e.g. ownership mismatch, URL pattern mismatch) are obvious LLM-self-correctable conditions. Others (e.g. unauthenticated context) are arguably "harness invariant violations" — should they be `Fatal` instead?

## Decision

**All five paths return soft errors via `returnSoftError`.** No path is preserved as `Fatal`.

## Rationale

1. **Reference implementations agree.** Codex `codex-rs/core/src/tools/handlers/view_image.rs::ViewImageHandler::handle` has six independent validation failures and uses `RespondToModel` for every single one. Claude Code `FileReadTool.ts` returns `ValidationResult { result: false, message, errorCode }` for all validation failures, surfaced as a `tool_result` block. Neither project reserves a `Fatal`-class condition inside the tool's validation logic — `Fatal` is for *harness* invariants ("tool invoked with incompatible payload type"), which file_read does not have a path for.

2. **`unauthenticated` is not a harness invariant.** Context missing a `userID` typically means the tool was called outside an authenticated agent flow (test harness, admin tool borrowing the registry, an aspirational future SSE-without-auth path). Hard error here kills the run with no actionable signal; soft error lets the LLM (or the test) see the literal "user not authenticated" string and react. The harness *should* gate this earlier (via `userctx` middleware on the agent route), so file_read seeing an unauthenticated context is a "shouldn't happen" condition — but soft error is the safer landing.

3. **Symmetry of treatment is a feature.** Mixing hard and soft errors in the same function is a recipe for the kind of bug that produced this hotfix: the same Eino layer routes all of them, and "this one is fatal but that one is not" forces every future maintainer to re-derive the distinction. Single contract: every Execute path returns `(ToolResult, nil)`; if there is ever a true harness invariant violation (tool called with `ToolInput` that fails type-assertion before this function runs), it will surface upstream of Execute.

## Consequences

- **No regression risk for the unauthenticated case.** Production traffic always carries auth via the agent route middleware; the unauthenticated path is unreachable in normal flow.
- **Test simplification:** all five paths share one assertion shape (`err == nil` + `out.Content` contains `"ERROR: ..."`).
- **Future tools should follow the same contract.** This decision generalises: tool validation failures are soft, tool execution errors are soft, only harness contract violations (which Execute does not see) are Fatal.
