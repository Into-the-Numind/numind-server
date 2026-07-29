# S4 Implementation Review — Feishu write command guidance

## Review scope

- Repository: `numind-server`
- Branch: `feature/feishu-write-command-guidance`
- Commits: `78e9bb99..f4840586`
- Worktree: `/private/tmp/wt-feishu-write-command-guidance-numind-server`
- Review mode: local spec-compliance and code-quality review. The session-level instruction prohibited subagent dispatch without explicit user authorization, so both NDF review templates were applied locally.

## Bug-from-Customer reproduction check

- PASS — first feature commit is `78e9bb99 test(qa): reproduce Feishu write command guidance conflict`.
- PASS — the committed regression failed before the implementation because the schema exposed `stdin_json`, the correction cap was five, and non-null `stdin_json` reached the executor.
- PASS — the same regression remains in `tool_lark_write_command_guidance_test.go` and passes after the fix.

## Spec-compliance matrix

| Requirement | Result | Evidence |
|---|---|---|
| Raise correctable-command attempts from 5 to 10 | PASS | `tool_lark_retry_budget.go`; attempts 1–10 and blocked attempt 11 covered |
| Remove the model-visible `stdin_json` contradiction | PASS | `lark_execute` schema exposes only `argv`; non-null rolling input is rejected before execution |
| Keep complete payload authorship in the Agent | PASS | Base batch payload remains one complete inline `--json` argv; no server-side business conversion |
| Show exact batch-create and CellValue references before the first write | PASS | Agent 1 prompt plus workflow order assertions |
| Return safe, useful pre-execution correction hints | PASS | command catalog validation and denial tests |
| Add scalar-only diagnostic evidence | PASS | safe skill-read and execute spans with forbidden-value tests |
| Leave item 7 final-report copy unchanged | PASS | before/after `## 7. 最终报告与安全统计标记` SHA-256 is identical |

## Findings

### Resolved during review

- P1: `internal/numind/biz/feishu/command_catalog.go:1231` — `SEC-observability-data-boundary` — syntactically valid but unregistered command tokens could have appeared as `command_class` — fix: return a class only when the path is in the immutable hosted catalog; otherwise record `invalid`.
- P1: `internal/numind/biz/agent/tool_lark_skill_read.go:104` — `SEC-observability-data-boundary` — an unvalidated skill/reference could have reached span input — fix: emit exact values only after the trusted reader resolves them; rejected inputs are recorded as fixed `invalid`.
- P1: `internal/numind/biz/agent/tool_lark_execute.go:224` — `OBS-control-flow-classification` — a valid durable authorization yield was initially classified as `invalid_wait` — fix: treat the expected `yieldError` path as `error_class=none`, `feishu_called=false`, with a dedicated regression test.

### Remaining

- P0: none.
- P1: none.
- P2: none.

## Code-quality and AI-service rule check

- Existing Agent trace context is reused; no new root trace is created.
- Both additions are spans, not generations; no LLM/provider call was added.
- `safePipelineToolSpan` makes missing Langfuse a no-op, verified by result equivalence without a trace.
- No Langfuse configuration is hard-coded.
- Span values are bounded scalars; argv, JSON payloads, tokens, table IDs, content, cursors, receipts, URLs, and raw provider errors are excluded and covered by tests.

## Conclusion

PASS
