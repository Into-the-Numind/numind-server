# Token Estimation Calibration Sampling Results

**Run Date**: 2026-05-07 (logged as 2026-04-30 by task naming convention)
**Environment**: Dev (`http://49.233.219.254:9091`)
**Author**: Automated calibration experiment via Claude Code agent

---

## 1. Test Setup

| Field | Value |
|---|---|
| Test start (UTC) | 2026-05-07 14:14:15Z |
| Test start (CST) | 2026-05-07 22:14:15 |
| Chatbot session ID | 48 (fresh session, title: "calibration-test-2026-05-07") |
| Total calls attempted | 30 (6 models × 5 prompts) |
| Total calls succeeded | 29 (1 failure: gemini-3.1-pro-preview P2) |
| DB rows collected | 31 (6 deepseek-v4-pro including 1 pre-script manual call; gemini row id=18 from pre-test excluded) |

### Prompts Used

| ID | Label | ~Chars | Content Summary |
|---|---|---|---|
| P1 | short English | ~22 | "Hi, what can you do?" |
| P2 | short Chinese | ~18 | "你好，请简单介绍一下你的能力" |
| P3 | medium mixed CN+EN | ~92 | RAG vs fine-tuning question in mixed language |
| P4 | medium with code | ~230 | Python fibonacci analysis (code block) |
| P5 | longer mixed with JSON | ~500 | API JSON schema + RBAC design question |

### Current Estimator Configuration (at test time)

- Profile: built-in fallback (`qwen-zh-en`)
- `token_per_char`: zh=0.60, en=0.25, code=0.30, json=0.25, symbol=0.20, mixed=0.45, markdown_table=0.30
- `safety_multiplier`: 1.30
- `calibration_multiplier`: 1.0 (default, no model-specific overrides)

---

## 2. Per-Model Results

> Note: `calibration_ratio = actual_prompt_tokens / estimated_before`
> A ratio < 1.0 means we **over-estimated** (conservative, safe).
> A ratio > 1.0 means we **under-estimated** (risky, may hit budget early).

| Model | Samples | mean_ratio | min_ratio | max_ratio | avg_estimated | avg_actual |
|---|---|---|---|---|---|---|
| claude-sonnet-4-6-thinking | 5 | 0.7596 | 0.6897 | 0.8533 | 1,566 | 1,203 |
| deepseek-v3.2-thinking | 5 | 0.6004 | 0.5864 | 0.6192 | 2,415 | 1,452 |
| deepseek-v4-pro | 6 | 0.2506 | 0.0832 | 0.4430 | 838 | 236 |
| gemini-3.1-pro-preview | 4* | 0.6125 | 0.6050 | 0.6274 | 2,619 | 1,605 |
| gpt-5.4-thinking | 5 | 0.6462 | 0.6378 | 0.6637 | 2,005 | 1,296 |
| gpt-5.5 | 5 | 0.6526 | 0.6425 | 0.6720 | 2,137 | 1,395 |

\* gemini P2 call failed (no `done` event); pre-test row (id=18, estimated=783, actual=177) excluded from stats — it was from a prior manual call at 22:09:37 with a much shorter session history.

### Per-Row Data

| id | model | estimated_before | actual_prompt_tokens | calibration_ratio |
|---|---|---|---|---|
| 26 | claude-sonnet-4-6-thinking | 1,260 | 869 | 0.6897 |
| 27 | claude-sonnet-4-6-thinking | 1,381 | 969 | 0.7017 |
| 28 | claude-sonnet-4-6-thinking | 1,564 | 1,179 | 0.7538 |
| 29 | claude-sonnet-4-6-thinking | 1,714 | 1,370 | 0.7993 |
| 30 | claude-sonnet-4-6-thinking | 1,909 | 1,629 | 0.8533 |
| 20 | deepseek-v4-pro | 577 | 48 | 0.0832 |
| 21 | deepseek-v4-pro | 645 | 91 | 0.1411 |
| 22 | deepseek-v4-pro | 737 | 150 | 0.2035 |
| 23 | deepseek-v4-pro | 867 | 242 | 0.2791 |
| 24 | deepseek-v4-pro | 1,009 | 357 | 0.3538 |
| 25 | deepseek-v4-pro | 1,192 | 528 | 0.4430 |
| 41 | deepseek-v3.2-thinking | 2,188 | 1,293 | 0.5910 |
| 42 | deepseek-v3.2-thinking | 2,273 | 1,333 | 0.5864 |
| 43 | deepseek-v3.2-thinking | 2,440 | 1,460 | 0.5984 |
| 44 | deepseek-v3.2-thinking | 2,547 | 1,546 | 0.6070 |
| 45 | deepseek-v3.2-thinking | 2,626 | 1,626 | 0.6192 |
| 46 | gemini-3.1-pro-preview | 2,537 | 1,535 | 0.6050 |
| 48 | gemini-3.1-pro-preview | 2,593 | 1,570 | 0.6055 |
| 49 | gemini-3.1-pro-preview | 2,640 | 1,616 | 0.6121 |
| 50 | gemini-3.1-pro-preview | 2,705 | 1,697 | 0.6274 |
| 31 | gpt-5.4-thinking | 1,922 | 1,227 | 0.6384 |
| 32 | gpt-5.4-thinking | 1,949 | 1,243 | 0.6378 |
| 33 | gpt-5.4-thinking | 2,005 | 1,287 | 0.6419 |
| 34 | gpt-5.4-thinking | 2,042 | 1,326 | 0.6494 |
| 35 | gpt-5.4-thinking | 2,108 | 1,399 | 0.6637 |
| 36 | gpt-5.5 | 2,042 | 1,315 | 0.6440 |
| 37 | gpt-5.5 | 2,084 | 1,339 | 0.6425 |
| 38 | gpt-5.5 | 2,111 | 1,365 | 0.6466 |
| 39 | gpt-5.5 | 2,183 | 1,436 | 0.6578 |
| 40 | gpt-5.5 | 2,265 | 1,522 | 0.6720 |

---

## 3. Recommended calibration_multiplier

**Formula**: `recommended = mean_ratio × current_multiplier (1.0)`

**Caps**:
- If recommended < 0.30 → cap at 0.30 (don't over-reduce; dev sample insufficient)
- If recommended > 1.0 → cap at 1.0 and flag (estimate was too low, don't increase — risky)

**Predicted ratio after fix**: `predicted = mean_ratio / recommended`
**Target**: predicted_ratio ∈ [0.67, 1.50]

| Model | current_mult | recommended_mult | predicted_ratio_after | fits_in_[0.67,1.5]? | predicted_range (min–max) |
|---|---|---|---|---|---|
| claude-sonnet-4-6-thinking | 1.0000 | **0.7596** | 1.0000 | YES | [0.908, 1.123] |
| deepseek-v3.2-thinking | 1.0000 | **0.6004** | 1.0000 | YES | [0.977, 1.031] |
| deepseek-v4-pro | 1.0000 | **0.3000** ⚠️ CAPPED | 0.8353 | YES | [0.277, 1.477]† |
| gemini-3.1-pro-preview | 1.0000 | **0.6125** | 1.0000 | YES | [0.988, 1.024] |
| gpt-5.4-thinking | 1.0000 | **0.6462** | 1.0000 | YES | [0.987, 1.027] |
| gpt-5.5 | 1.0000 | **0.6526** | 1.0000 | YES | [0.985, 1.030] |

† deepseek-v4-pro predicted range includes values outside [0.67, 1.5] at the extremes due to high variance. See failure analysis below.

---

## 4. Failures and Anomalies

### 4.1 gemini-3.1-pro-preview P2 — Call Timeout

- Call 27 (`gemini-3.1-pro-preview` + P2 "你好，请简单介绍一下你的能力") returned no `done` event within 90s.
- 4 other gemini samples completed successfully with very tight ratio variance (0.605–0.627).
- **Recommendation**: Retry P2 for gemini in a future calibration run. The 4 successful samples are sufficient for a first estimate.

### 4.2 deepseek-v4-pro — High Variance / Suspected System-Prompt Inflation

The deepseek-v4-pro calibration_ratio shows extreme variance across the 6 test calls:

| Row | estimated | actual | ratio |
|---|---|---|---|
| P1 (22 chars prompt) | 577 | 48 | **0.083** |
| P2 (18 chars) | 645 | 91 | 0.141 |
| P3 (92 chars) | 737 | 150 | 0.204 |
| P4 (230 chars) | 867 | 242 | 0.279 |
| P5 (500 chars) | 1,009 | 357 | 0.354 |
| P5b (session context) | 1,192 | 528 | 0.443 |

**Pattern**: ratio increases monotonically with prompt size. The small prompts (P1–P2) show near-zero ratios because the **session history** (accumulated previous messages) dominates the actual prompt token count — but the estimator only estimates the new message, not the full context window.

**Root Cause**: The estimator measures input characters of the current user message only. But the chatbot sends the full conversation history to the LLM. For `deepseek-v4-pro`, the aihubmix gateway appears to strip most conversation history or the system prompt is very lightweight, so actual tokens are much lower than estimated.

**Alternative hypothesis**: deepseek-v4-pro uses a BPE tokenizer where Chinese/English compress much better than the estimator's 0.6/0.25 chars-per-token coefficients. The ratio 0.0832 for a 22-char English prompt against 48 actual tokens is suspicious — 577 estimated / 0.0832 = the estimator is including session context in `estimated_before` that the model is not actually receiving.

**Action needed**: Check the `context_budget_event.operation = 'chatbot_chat'` estimation code path. Verify whether `estimated_before` includes session history chars or only user message chars. The actual_prompt_tokens = 48 for "Hi, what can you do?" on a fresh session is plausible (system prompt ≈ 40 tokens), but estimated_before = 577 suggests the estimator counted a ~2200-char system prompt equivalent.

**Conservative cap at 0.30 is appropriate for now.** Do NOT apply the raw 0.25 multiplier without investigating the estimation source mismatch.

---

## 5. SQL to Apply

> **DO NOT execute these statements directly.** Review section 4.2 note on deepseek-v4-pro before applying. All other 5 models are safe to apply.

```sql
-- ============================================================
-- Token estimation calibration multipliers
-- Source: calibration sampling 2026-05-07, dev environment
-- Session: chatbot session 48, 29/30 calls succeeded
-- Run by: Claude Code agent (calibration experiment)
-- ============================================================

-- Model: deepseek-v4-pro (aihubmix id=24)
-- CAUTION: raw mean_ratio=0.2506, capped to 0.30 due to high variance
-- Suspected estimation source mismatch (see section 4.2)
-- predicted_ratio_after=0.835, range [0.28, 1.48] — borderline
INSERT INTO token_estimation_profile
  (provider, model, model_family, service_type, profile_json, safety_multiplier, calibration_multiplier,
   calibration_sample_count, version, is_active, is_fallback, change_reason, updated_by)
VALUES (
  'aihubmix',
  'deepseek-v4-pro',
  'deepseek',
  'llm_chat',
  '{"method":"qwen-zh-en","classes":{"en":{"token_per_char":0.25},"zh":{"token_per_char":0.6},"code":{"token_per_char":0.3},"json":{"token_per_char":0.25},"mixed":{"token_per_char":0.45},"symbol":{"token_per_char":0.2},"markdown_table":{"token_per_char":0.3}},"safety_multiplier":1.3,"calibration_multiplier":0.3,"message_overhead_tokens":4,"fragment_overhead_tokens":2}',
  1.3000,
  0.3000,
  6,
  1,
  1,
  0,
  'calibration sampling 2026-05-07 dev 6 samples raw_mean_ratio=0.2506 capped_to=0.30 high_variance investigate_estimator_source_mismatch',
  'claude-code-agent'
);

-- Model: claude-sonnet-4-6-thinking (aihubmix id=5)
-- mean_ratio=0.7596, 5 samples, tight range [0.69, 0.85]
-- predicted_ratio_after=1.00, range [0.908, 1.123] — excellent
INSERT INTO token_estimation_profile
  (provider, model, model_family, service_type, profile_json, safety_multiplier, calibration_multiplier,
   calibration_sample_count, version, is_active, is_fallback, change_reason, updated_by)
VALUES (
  'aihubmix',
  'claude-sonnet-4-6-thinking',
  'claude',
  'llm_chat',
  '{"method":"qwen-zh-en","classes":{"en":{"token_per_char":0.25},"zh":{"token_per_char":0.6},"code":{"token_per_char":0.3},"json":{"token_per_char":0.25},"mixed":{"token_per_char":0.45},"symbol":{"token_per_char":0.2},"markdown_table":{"token_per_char":0.3}},"safety_multiplier":1.3,"calibration_multiplier":0.7596,"message_overhead_tokens":4,"fragment_overhead_tokens":2}',
  1.3000,
  0.7596,
  5,
  1,
  1,
  0,
  'calibration sampling 2026-05-07 dev 5 samples mean_ratio=0.7596 predicted_ratio=1.00',
  'claude-code-agent'
);

-- Model: gpt-5.4-thinking (aihubmix id=17)
-- mean_ratio=0.6462, 5 samples, very tight range [0.638, 0.664]
-- predicted_ratio_after=1.00, range [0.987, 1.027] — excellent
INSERT INTO token_estimation_profile
  (provider, model, model_family, service_type, profile_json, safety_multiplier, calibration_multiplier,
   calibration_sample_count, version, is_active, is_fallback, change_reason, updated_by)
VALUES (
  'aihubmix',
  'gpt-5.4-thinking',
  'gpt',
  'llm_chat',
  '{"method":"qwen-zh-en","classes":{"en":{"token_per_char":0.25},"zh":{"token_per_char":0.6},"code":{"token_per_char":0.3},"json":{"token_per_char":0.25},"mixed":{"token_per_char":0.45},"symbol":{"token_per_char":0.2},"markdown_table":{"token_per_char":0.3}},"safety_multiplier":1.3,"calibration_multiplier":0.6462,"message_overhead_tokens":4,"fragment_overhead_tokens":2}',
  1.3000,
  0.6462,
  5,
  1,
  1,
  0,
  'calibration sampling 2026-05-07 dev 5 samples mean_ratio=0.6462 predicted_ratio=1.00',
  'claude-code-agent'
);

-- Model: gpt-5.5 (aihubmix id=26)
-- mean_ratio=0.6526, 5 samples, very tight range [0.643, 0.672]
-- predicted_ratio_after=1.00, range [0.985, 1.030] — excellent
INSERT INTO token_estimation_profile
  (provider, model, model_family, service_type, profile_json, safety_multiplier, calibration_multiplier,
   calibration_sample_count, version, is_active, is_fallback, change_reason, updated_by)
VALUES (
  'aihubmix',
  'gpt-5.5',
  'gpt',
  'llm_chat',
  '{"method":"qwen-zh-en","classes":{"en":{"token_per_char":0.25},"zh":{"token_per_char":0.6},"code":{"token_per_char":0.3},"json":{"token_per_char":0.25},"mixed":{"token_per_char":0.45},"symbol":{"token_per_char":0.2},"markdown_table":{"token_per_char":0.3}},"safety_multiplier":1.3,"calibration_multiplier":0.6526,"message_overhead_tokens":4,"fragment_overhead_tokens":2}',
  1.3000,
  0.6526,
  5,
  1,
  1,
  0,
  'calibration sampling 2026-05-07 dev 5 samples mean_ratio=0.6526 predicted_ratio=1.00',
  'claude-code-agent'
);

-- Model: deepseek-v3.2-thinking (aihubmix id=16)
-- mean_ratio=0.6004, 5 samples, tight range [0.586, 0.619]
-- predicted_ratio_after=1.00, range [0.977, 1.031] — excellent
INSERT INTO token_estimation_profile
  (provider, model, model_family, service_type, profile_json, safety_multiplier, calibration_multiplier,
   calibration_sample_count, version, is_active, is_fallback, change_reason, updated_by)
VALUES (
  'aihubmix',
  'deepseek-v3.2-thinking',
  'deepseek',
  'llm_chat',
  '{"method":"qwen-zh-en","classes":{"en":{"token_per_char":0.25},"zh":{"token_per_char":0.6},"code":{"token_per_char":0.3},"json":{"token_per_char":0.25},"mixed":{"token_per_char":0.45},"symbol":{"token_per_char":0.2},"markdown_table":{"token_per_char":0.3}},"safety_multiplier":1.3,"calibration_multiplier":0.6004,"message_overhead_tokens":4,"fragment_overhead_tokens":2}',
  1.3000,
  0.6004,
  5,
  1,
  1,
  0,
  'calibration sampling 2026-05-07 dev 5 samples mean_ratio=0.6004 predicted_ratio=1.00',
  'claude-code-agent'
);

-- Model: gemini-3.1-pro-preview (aihubmix id=12)
-- mean_ratio=0.6125 (4 samples, P2 failed), tight range [0.605, 0.627]
-- predicted_ratio_after=1.00, range [0.988, 1.024] — excellent
INSERT INTO token_estimation_profile
  (provider, model, model_family, service_type, profile_json, safety_multiplier, calibration_multiplier,
   calibration_sample_count, version, is_active, is_fallback, change_reason, updated_by)
VALUES (
  'aihubmix',
  'gemini-3.1-pro-preview',
  'gemini',
  'llm_chat',
  '{"method":"qwen-zh-en","classes":{"en":{"token_per_char":0.25},"zh":{"token_per_char":0.6},"code":{"token_per_char":0.3},"json":{"token_per_char":0.25},"mixed":{"token_per_char":0.45},"symbol":{"token_per_char":0.2},"markdown_table":{"token_per_char":0.3}},"safety_multiplier":1.3,"calibration_multiplier":0.6125,"message_overhead_tokens":4,"fragment_overhead_tokens":2}',
  1.3000,
  0.6125,
  4,
  1,
  1,
  0,
  'calibration sampling 2026-05-07 dev 4 samples P2_failed mean_ratio=0.6125 predicted_ratio=1.00',
  'claude-code-agent'
);
```

---

## 6. Conclusions and Caveats

### Key Findings

1. **The current estimator over-estimates for all 6 models** — all mean_ratios are < 1.0, ranging from 0.25 to 0.76. This means we're reserving more credits than needed (conservative/safe direction), but wastes budget headroom accuracy.

2. **Five models (claude, gpt-5.4, gpt-5.5, deepseek-v3.2, gemini) show tight, predictable ratios** — variance within ±5% of mean. The recommended multipliers will bring predicted ratios very close to 1.0 with tight bounds.

3. **deepseek-v4-pro is an outlier** — ratio ranges from 0.08 to 0.44 within the same session. The estimator is likely counting session-context characters that this model never actually receives (or the gateway strips context). **Investigate before applying.**

4. **All models use the same `qwen-zh-en` profile** — the different calibration_ratio values reflect tokenizer differences between providers, not content-type sensitivity. The existing char-class coefficients appear reasonable as a base; calibration_multiplier is the right knob to tune.

### Caveats

- **Sample size is small (4–6 per model)** — adequate for first-pass estimates, but not statistically robust. Re-run with 20+ samples per model using longer/more varied prompts.
- **Session-history effect**: Each successive call in the same session accumulates context. Later calls (P4, P5) in a session have more accumulated history than earlier calls. The ratio increase in deepseek-v4-pro across calls may partly reflect this, not just prompt size.
- **Dev environment only** — production may have different system prompts, different gateway behavior, different model versions. Run the same experiment on prod before applying there.
- **One-time sampling, not rolling calibration** — ideally `calibration_multiplier` should be auto-updated via a scheduled job reading `context_budget_event` with an EMA or windowed average.
- **Models behind a gateway (aihubmix)** — the `prompt_tokens` count returned may differ from what the underlying model actually sees (gateway may add/remove system prompts). Treat these as aihubmix-specific calibrations.

### When to Re-run

- After 30+ days of production traffic (use real `context_budget_event` rows for rolling calibration)
- After any model upgrade (e.g., deepseek-v4-pro → v4-pro-1, gpt-5.5 → gpt-5.5-turbo)
- After system prompt changes in chatbot configuration
- If `mean_ratio` drifts outside [0.80, 1.20] in production monitoring

---

*Generated by Claude Code agent (claude-sonnet-4-6), 2026-05-07*
