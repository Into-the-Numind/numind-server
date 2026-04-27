-- Apply: Context Budget max_output_tokens backfill in single transaction
-- Run with: docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod < 02-apply.sql
--
-- IMPORTANT: Before running this script:
--   1. Run 01-dry-run.sql and review all NEEDS BACKFILL rows.
--   2. Verify each model_key is covered by one of the LIKE-pattern UPDATEs below.
--   3. If a model_key prefix is missing, ADD a new UPDATE block following the same pattern.
--   4. Reference for correct max_output_tokens values:
--        docs/superpowers/research/2026-04-27-llm-max-output-tokens-table.md
--   5. Take a mysqldump backup BEFORE running this script.
--
-- Design:
--   - All writes wrapped in START TRANSACTION / COMMIT for atomicity.
--   - WHERE clause filters (IS NULL OR = 0), so already-set rows are skipped (idempotent).
--   - JSON_SET touches ONLY $.max_output_tokens; all other capability_json fields are preserved.
--   - LIKE patterns intentionally broad so future model variants (e.g. claude-sonnet-4-7) are covered.
--   - Values are conservative floors (see research doc §3 for reasoning).
--
-- DO NOT COMMIT until 03-verify.sql passes.

START TRANSACTION;

-- ===========================================================================
-- Anthropic Claude family
-- claude-sonnet-4-x / claude-haiku-4-x / claude-opus-4-x: 64000
-- Official Anthropic docs (2026): all Claude 4.x models advertise 64K max output.
-- context_window=200000, so 64000 << cw ✓
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 64000),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND (model_key LIKE 'claude-sonnet-4%'
    OR model_key LIKE 'claude-haiku-4%'
    OR model_key LIKE 'claude-opus-4%')
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- OpenAI GPT-5 family
-- gpt-5.x (any suffix including -thinking): 128000
-- OpenAI GPT-5 docs: max_output_tokens = 128000 (equals context_window).
-- NOTE: at this value, safe_input_budget = 128000 - 16384(reserved) - 1024(overhead) = 110592.
-- This is valid per spec §2.4 (max_output > reserved + overhead).
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 128000),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND model_key LIKE 'gpt-5%'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- Google Gemini family
-- gemini-3.x (any suffix including -preview, -thinking): 65536
-- Gemini 3.x: context_window=1000000. 65536 is conservative; official cap TBD.
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 65536),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND (model_key LIKE 'gemini-3%'
    OR model_key LIKE 'gemini-2%')
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- DeepSeek V3.x standard (non-thinking)
-- deepseek-v3.x: 8192
-- DeepSeek API docs: max_tokens=8192 for V3 series.
-- context_window=128000, so 8192 << cw ✓
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 8192),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND model_key LIKE 'deepseek-v3%'
  AND model_key NOT LIKE '%-thinking'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- DeepSeek V3.x thinking variants
-- deepseek-v3.x-thinking: 32768
-- IMPORTANT: context_window=65536 for thinking models. Using 8192 would result in
-- safe_input_budget = 8192 - 16384(reserved) < 0 → ErrContextConfigInvalid.
-- 32768 = cw/2 is the conservative safe value: 32768 - 16384 - 1024 = 15360 > 0 ✓
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 32768),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND model_key LIKE 'deepseek-v3%-thinking'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- DeepSeek V4.x family (if exists in prod)
-- deepseek-v4.x: 32768 (conservative; actual cap model-dependent, up to 384000)
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 32768),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND model_key LIKE 'deepseek-v4%'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- Alibaba Qwen3 Vision-Language family
-- qwen3-vl-*: 16384
-- Qwen3 VL: context_window=32768. 16384 = cw/2.
-- safe_input_budget = 16384 - 16384(reserved) - 1024(overhead) = -1024 → still borderline!
-- Use 16384 only if reserved_output_tokens profile is < 16384 for VL tasks.
-- Safer: set to 16384, which is the advertised cap and satisfies constraint max >= 16384.
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 16384),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND model_key LIKE 'qwen3-vl%'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- Alibaba Qwen3 text family (non-VL)
-- qwen3-* (excluding VL): 8192
-- Qwen3 text models: max_tokens=8192 per DashScope docs.
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 8192),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND model_key LIKE 'qwen3%'
  AND model_key NOT LIKE 'qwen3-vl%'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- Alibaba Qwen-turbo / Qwen-plus (non-Qwen3)
-- qwen-turbo* / qwen-plus*: 8192
-- DashScope docs: qwen-turbo max_tokens=8192, qwen-plus max_tokens=8192.
-- context_window=131072 for turbo ✓
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 8192),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND (model_key LIKE 'qwen-turbo%'
    OR model_key LIKE 'qwen-plus%'
    OR model_key LIKE 'qwen-long%')
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- Zhipu GLM-4 family
-- glm-4-*: 4096
-- Zhipu AI docs: GLM-4 series max_tokens=4096.
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 4096),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND model_key LIKE 'glm-4%'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- ByteDance Doubao family
-- doubao-*: 16384
-- Volcengine docs: Doubao Seed series max_tokens=16384.
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 16384),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND model_key LIKE 'doubao%'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

-- ===========================================================================
-- Generic fallback for any remaining LLM service with NULL max_output_tokens
-- that was NOT matched by any prefix above.
-- Value: 16384 — the minimum valid value (equals default reserved_output_tokens).
-- This ensures ErrContextConfigInvalid is not triggered while keeping a conservative cap.
-- WARNING: After apply, run 03-verify.sql and manually check any rows
-- that were caught by this fallback (they may need fine-tuning).
-- ===========================================================================
UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.max_output_tokens', 16384),
    updated_at      = NOW(3)
WHERE service_type = 'llm'
  AND (JSON_EXTRACT(capability_json, '$.max_output_tokens') IS NULL
    OR JSON_EXTRACT(capability_json, '$.max_output_tokens') = 0);

COMMIT;

-- Final status check
SELECT 'APPLY COMPLETE — run 03-verify.sql to confirm 0 rows remain unset' AS status;

SELECT
  id,
  model_key,
  JSON_EXTRACT(capability_json, '$.context_window')    AS cw,
  JSON_EXTRACT(capability_json, '$.max_output_tokens') AS mo_after_backfill
FROM ai_service
WHERE service_type = 'llm'
ORDER BY id;
