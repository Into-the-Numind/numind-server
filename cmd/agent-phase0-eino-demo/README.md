# Phase 0 V2 — Eino + aiservice Integration Demo

**Assumption verified**: A2 — `cloudwego/eino` can wrap `aiservice.Chat()` as a `ChatModel` without losing Langfuse tracing, billing (Reserve/Reconcile), or route fallback.

## Prerequisites

- numind-server repo root with `config_local.yaml` present (or `--config` flag)
- DB accessible at the host configured in `config_local.yaml` (dev DB is fine)
- Langfuse configured in `config_local.yaml` (or disabled — demo gracefully skips tracing)

## How to Run

```bash
# From numind-server repo root
cd /path/to/numind-server

# Happy path — asks the ReAct agent "今天是星期几？"
go run ./cmd/agent-phase0-eino-demo/

# Error path — triggers non-existent model → aiservice error → Langfuse error generation
go run ./cmd/agent-phase0-eino-demo/ --error-path

# Custom config path
go run ./cmd/agent-phase0-eino-demo/ --config /path/to/config.yaml
```

### Expected happy-path output

```
[phase0-eino-demo] Running happy path: 今天是星期几？
[phase0-eino-demo] Final answer: 今天是 <day>，YYYY-MM-DD。
[phase0-eino-demo] Trace ID: <uuid> (check Langfuse backend for generation + span)
```

### Expected error-path output

```
[phase0-eino-demo] Error path: got expected error: ...
[phase0-eino-demo] Error path: Trace ID <uuid> — check Langfuse for error generation
exit status 1
```

## S5 Acceptance SQL

Run these after the happy-path demo to confirm billing (Reserve / Reconcile) worked.

Replace `<demo_start_time>` with the timestamp just before running the demo.

**SQL ① — Reserve** (at least 1 row expected):

```sql
SELECT *
FROM credit_reservation
WHERE created_at > '<demo_start_time>'
ORDER BY id DESC
LIMIT 1;
```

**SQL ② — Reconcile** (at least 1 row expected, `source_type` ∈ {trial, subscription, cycle, booster}):

```sql
SELECT *
FROM credit_transaction
WHERE source_type IS NOT NULL
  AND created_at > '<demo_start_time>'
ORDER BY id DESC
LIMIT 1;
```

## Langfuse Acceptance

After happy-path run, check the Langfuse backend (tag = `phase0-verification`):

- `≥ 1 generation` with `usage.total_tokens > 0`
- `≥ 1 span` named `span-tool-exec-get_current_date`
- No error generations

## Architecture Notes

- `adapter.go` — `AiserviceAdapter` implements Eino's `model.ChatModel` interface by delegating to `aiservice.Chat(ctx, "phase0-eino-demo", req)` (3-arg form). Langfuse trace propagates through `ctx`.
- `tools.go` — `get_current_date` implements `tool.InvokableTool`. Returns today's UTC date in ISO 8601 format.
- `observability.go` — `instrumentedToolCall` wraps tool calls with a Langfuse span using `CreateSpan(traceID, spanID, name, opts...)` / `EndSpan(traceID, spanID, opts...)`.
- `main.go` — bootstraps DB + aiservice gateway + Langfuse from `config_local.yaml`, then runs a single ReAct loop via `react.NewAgent`.

## Key API Differences from Spec (Eino v0.8.13)

| Spec §3.x expected | Actual Eino v0.8.13 |
|---|---|
| `react.Config{}` | `react.AgentConfig{}` — struct renamed |
| `Config.Tools []tool.Tool` | `AgentConfig.ToolsConfig compose.ToolsNodeConfig` with `Tools []tool.BaseTool` |
| `agent.Run(ctx, string)` | `agent.Generate(ctx, []*schema.Message)` |
| `ChatModel` as primary interface | `ToolCallingChatModel` preferred; `ChatModel` deprecated but functional |

The adapter implements the deprecated `model.ChatModel` (which is still accepted by `AgentConfig.Model`) to keep the implementation minimal. A future production implementation should target `model.ToolCallingChatModel`.
