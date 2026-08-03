# AI Observability Verification

Feature: `agent-run-survives-exit`
Stage: Standard S4 / Task 6
Date: 2026-08-03

## Decision

- No new LLM or AI service call site is introduced by Task 6.
- Agent generations remain under the existing runner-owned Langfuse traces, including `agent-runtime-run` in `internal/numind/biz/agent/runner.go` and the existing streaming runner trace in `runner_runstream.go`.
- Controller SSE observation spans are observer-only. Browser disconnect is recorded as `observer_disconnect`; it is not an execution abort and does not write legacy disconnect or aborted-stream terminal state.
- `tool.ask_user_question.resume` remains in `internal/numind/biz/agent/answer.go`; no controller generation is added.

## Audit

Referenced rule file check:

```text
test -f .claude/rules/ai-service.md
result: not present in this checkout
```

Direct provider/raw AI HTTP audit:

```bash
rg -n '"numind-server/internal/numind/biz/(ali|volc|baidu)|internal/service/bailian_http|http\.Post|resty\.New' internal/numind/biz/agent internal/numind/controller/v1/agent
```

Result: no matches.

Langfuse generation/trace/resume audit:

```bash
rg -n 'CreateGeneration|CreateTrace|agent-runtime-run|tool.ask_user_question.resume' internal/numind/biz/agent internal/numind/controller/v1/agent
```

Result:

```text
internal/numind/biz/agent/answer.go:357: langfuse.CreateSpan(... "tool.ask_user_question.resume" ...)
internal/numind/biz/agent/runner.go:657: langfuse.CreateTrace(... "agent-runtime-run" ...)
internal/numind/biz/agent/attachment/fallback_service.go:374: langfuse.CreateTrace(... "attachment.fallback" ...)
internal/numind/biz/agent/runner_runstream.go:161: langfuse.CreateTrace(... "agent-runtime-runstream" ...)
```

No `CreateGeneration` matches were found in `internal/numind/biz/agent` or `internal/numind/controller/v1/agent`.
