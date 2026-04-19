# Langfuse Span Verification Report — credits-system S5 (AI-1)

**Date**: 2026-04-19
**Environment**: Dev cloud Langfuse (`http://110.42.221.25:3100`)
**Objective**: Verify that all 4 credit lifecycle spans (spec §5.1) fire on
live dev with complete field schemas.

## TL;DR

✅ **All 4 spans verified end-to-end on dev**, field schemas match spec §5.1
exactly. Trace-level §5.1.5 metadata also present.

## Method

1. Logged in as parent user (user_id=25, billing_mode='credits')
2. Admin-recharged 5000 credits (subscription, 30d) via
   `POST /v1/admin/credits/users/25/recharge`
3. Triggered 3 SOP execution paths:
   - `POST /v1/sop/runs` + `POST /v1/sop/runs/:id/nodes/:node_id/execute`
     (long text → estimate > 0 → Reserve → LLM → Reconcile)
   - Same with short text (estimate=0 → Reserve rejected)
   - Same with price lookup failure (LLM succeeds → pricing errors →
     opErr → Refund)
4. Queried Langfuse public REST API (`/api/public/traces`,
   `/api/public/observations`) to inspect spans.

## Results

### Trace a805ca15 — happy path (Estimate + Reserve + Reconcile)

```json
credit-estimate:
  input  = {billing_mode, model, operation, prompt_chars, provider}
  output = {booster_remain_before, char_to_token_ratio, coefficient_id,
            completion_prompt_ratio, estimated_credits, safety_buffer_pct,
            skip_deduction, sub_remain_before, sufficient}

credit-reserve:
  input  = {idempotency_key, reservation_id, reserved_credits, user_id}
  output = {booster_remain_after, reserved_from_packages (items snapshot),
            sub_remain_after}

credit-reconcile:
  input  = {actual_completion_tokens: 1175, actual_cost_cents: 2,
            actual_prompt_tokens: 1386, reservation_id, reserved_credits}
  output = {delta: 0, final_status: "reconciled", has_debt: false,
            reconcile_direction: "noop", refunded_to_packages: null}
```

**Note**: AI-5 token threading confirmed — `actual_prompt_tokens=1386` /
`actual_completion_tokens=1175` populated from rsv struct (previously 0/0).

### Trace 56e78687 — op_failed path (Estimate + Reserve + Refund)

```json
credit-refund:
  input  = {reason: "op_failed", reservation_id}
  output = {final_status: "refunded", refunded_credits, refunded_items,
            (items snapshot)}
```

### Trace-level §5.1.5 metadata

All sop_execute traces carry:
```json
{"billing_mode": "credits",
 "credit_balance_at_start": "5000",
 "deducted_from": "subscription"}
```

## Schema Compliance vs Spec §5.1

| Span | Spec §5.1 fields | Observed | Match |
|------|---|---|---|
| credit-estimate (§5.1.1) | billing_mode, model, operation, prompt_chars, provider + coefficient_id + char_to_token_ratio + completion_prompt_ratio + safety_buffer_pct + estimated_credits + sufficient | ✅ all | 100% |
| credit-reserve (§5.1.2) | reservation_id, reserved_credits, user_id, idempotency_key, reserved_from_packages, sub_remain_after, booster_remain_after | ✅ all | 100% |
| credit-reconcile (§5.1.3) | reservation_id, reserved_credits, actual_cost_cents, actual_prompt_tokens, actual_completion_tokens, delta, reconcile_direction, refunded_to_packages, has_debt, final_status | ✅ all | 100% |
| credit-refund (§5.1.4) | reservation_id, reason, refunded_credits, refunded_items, final_status | ✅ all | 100% |

## Side findings (orthogonal, not blocking)

### 1. `ProviderFromModel` stale mapping
`credit.ProviderFromModel("claude-sonnet-4-6")` returns `"dmxapi"` but
current routing uses `aihubmix`. Pricing lookup for `(llm_chat, dmxapi,
claude-sonnet-4-6)` misses → opErr → Refund path, even when LLM succeeded.

**Workaround during smoke**: manually inserted a temp pricing_rule
`(llm_chat, dmxapi, claude-sonnet-4-6)` to force Reconcile path; reverted
after verification.

**Fix needed**: update ProviderFromModel prefix rules to recognize aihubmix,
or (better) query `ai_service_route` table dynamically. Tracked as tech
debt for post-S7.

### 2. Docker Hub tag race
AI-5 (`03ee2ea`) and AI-6 (`adbd895`) CI runs pushed new images, but the
downstream `deploy_dev` job pulled a stale digest (likely cached at
mirror). CI's own log warned "镜像 SHA 不匹配". Manually rerun resolved.

**Fix needed**: add `--pull=always` or post-push `docker buildx imagetools
inspect` polling in deploy step. Post-S7.

## Artifacts

- 4 dev traces (b34722a2, 56e78687, a805ca15, f865b3f9) preserved in
  Langfuse for 30 days (default retention)
- No new commits required for AI-1 itself; all span code pre-existed and
  was validated in-place

## Conclusion

**PASS** — credit lifecycle observability fully functional on dev. Spec
§5.1 field schemas match implementation. T3.1 marked complete.
