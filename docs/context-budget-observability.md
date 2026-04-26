# Context Budget Observability

This document describes the observability metadata emitted by the context-budget system (spec §11).

---

## Usage Record Metadata (spec §11.2)

Every AI service call that passes through the `ContextBudgetCredits` middleware appends the following fields to `usage_record.metadata` (JSON object stored in the `metadata` column):

| Field | Type | Description |
|-------|------|-------------|
| `context_budget_event_id` | uint64 | FK to `context_budget_event.id` |
| `token_profile_id` | uint64 | DB ID of the token profile used (0 = default) |
| `budget_policy_id` | uint64 | DB ID of the context budget policy row |
| `safe_input_budget` | int | Safe token budget threshold for this call |
| `estimated_prompt_tokens_before` | int | Estimated tokens before compression planning |
| `estimated_prompt_tokens_after` | int | Estimated tokens after accepted plan |
| `estimated_completion_tokens` | int | Reserved output token budget from policy |
| `reserved_output_tokens` | int | Same as estimated_completion_tokens |
| `compression_status` | string | `"ok"` (no compression) or `"compressed"` |
| `compression_actions` | []string | Action types applied: `summarize`, `reference`, `drop`, `reuse_summary` |
| `reservation_id` | uint64 | Credit reservation ID (0 for legacy-tier users) |

Example metadata JSON:
```json
{
  "context_budget_event_id": 4821,
  "token_profile_id": 3,
  "budget_policy_id": 7,
  "safe_input_budget": 835638,
  "estimated_prompt_tokens_before": 120000,
  "estimated_prompt_tokens_after": 42000,
  "estimated_completion_tokens": 16384,
  "reserved_output_tokens": 16384,
  "compression_status": "compressed",
  "compression_actions": ["summarize", "reference"]
}
```

---

## Langfuse Generation Metadata (spec §11.1)

When a Langfuse trace context is present, the `Tracing` middleware includes the following fields in the generation `output.metadata` map:

| Field | Type | Description |
|-------|------|-------------|
| `context_budget_event_id` | uint64 | FK to context_budget_event |
| `context_window` | int | Model context window size |
| `max_output_tokens` | int | Model maximum output tokens |
| `reserved_output_tokens` | int | Output token budget from policy |
| `safe_ratio` | float64 | Safe ratio used in budget formula |
| `fixed_overhead_tokens` | int | Fixed overhead per request |
| `safe_input_budget` | int | Computed safe input budget |
| `estimated_before` | int | Tokens estimated before compression |
| `estimated_after` | int | Tokens estimated after compression |
| `compression_actions` | []string | Non-keep action types applied |
| `dropped_fragment_count` | int | Number of fragments with ActionDrop |
| `summarized_fragment_count` | int | Number of fragments with ActionSummarize |
| `critical_fragment_count` | int | Number of fragments marked critical |
| `token_profile_id` | uint64 | Token profile DB ID |
| `token_profile_fallback` | bool | True when default/fallback profile used |
| `calibration_skipped` | bool | True when actual usage tokens unavailable |

---

## Privacy Rules (spec §11.3)

The following are NEVER written to logs, Langfuse, or usage_record.metadata:

- Fragment `.Content` text (actual message content, sales scripts, conversation history)
- Rendered prompt text (the full assembled messages array sent to the provider)
- User PII embedded in fragments
- Any field from `ContextFragment` other than ID and structural metadata

This is enforced structurally: `budgetMetadata` (the struct passed between middlewares) has no `Content` or `Text` fields. Only scalar IDs, token counts, and boolean flags propagate through the observability pipeline.

---

## Querying Budget Events

**Admin API**: 通过 `GET /v1/admin/context-budget/events?operation=...&status=...&page=...` 查询近期 budget events，
返回元数据（不含 prompt 内容）。详见 admin API 路由 `internal/numind/admin_router.go`。

To investigate a specific budget event from its usage record:

```sql
-- 1. Find the event ID from the usage record
SELECT metadata->>'$.context_budget_event_id' AS event_id,
       metadata->>'$.compression_status' AS compression_status,
       metadata->>'$.estimated_prompt_tokens_before' AS tokens_before,
       metadata->>'$.estimated_prompt_tokens_after' AS tokens_after
FROM usage_record
WHERE id = <usage_record_id>;

-- 2. Look up the budget event details
SELECT * FROM context_budget_event WHERE id = <event_id>;

-- 3. Aggregate compression events by operation
SELECT operation,
       COUNT(*) AS total_calls,
       SUM(CASE WHEN metadata->>'$.compression_status' = 'compressed' THEN 1 ELSE 0 END) AS compressed_calls,
       AVG(CAST(metadata->>'$.estimated_prompt_tokens_before' AS UNSIGNED)) AS avg_tokens_before
FROM usage_record
WHERE metadata->>'$.context_budget_event_id' IS NOT NULL
GROUP BY operation;
```

---

## Troubleshooting Budget Failures

**Symptom: `ErrContextTooLarge` returned to caller**
- The estimated token count exceeded `safe_input_budget` even after all compression phases.
- Query `context_budget_event` with `status='failed'` and `error_code='context_too_large'`.
- Check `estimated_after` and `safe_input_budget` to understand the gap.
- Consider increasing `reserved_output_tokens` (reduces safe budget) or switching to a larger context window model.

**Symptom: `compression_status=compressed` but high token usage**
- Compression is running but the estimated-after is still large.
- Check `compression_actions` — if only `reference` appears, summarize actions are not eligible.
- Verify fragment `Compressibility` values: fragments must have `CompressSummarize` or `CompressDrop` to be eligible.

**Symptom: `token_profile_fallback=true` in Langfuse**
- No token profile matched the operation; a default profile was applied.
- Token estimates may be less accurate for this operation's content mix.
- Register a dedicated token profile for the operation via the admin API.

**Symptom: `calibration_skipped=true`**
- The streaming response ended without a final `Usage` chunk.
- Credit reconciliation fell back to the estimated cost.
- Check provider adapter for final-chunk `Usage` population.
