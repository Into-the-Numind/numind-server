-- =============================================================================
-- Migration: pricing_rule vocabulary fix (T-arch-prereq)
-- Date: 2026-04-18
-- Author: T-arch-prereq implementer
--
-- Fixes 3 dimensions of vocabulary drift that caused ALL 13 active routes to
-- fail pricing_rule lookup (cost_cents=0 for every LLM call):
--
-- Fix 1 — model truncation:
--   pricing_rule row "claude-sonnet-4-6-think" renamed to
--   "claude-sonnet-4-6-thinking" to match ai_service_route.provider_model_id.
--
-- Fix 2 — provider name drift:
--   Adds pricing_rule rows for provider "dmxapi-ssvip" (5 models) by copying
--   the aihubmix pricing. "Same price, different row" — update if real rates differ.
--
-- Fix 3 — thinking model variants (aihubmix):
--   Adds pricing_rule rows for *-thinking model_key variants (gemini-3.1-pro-preview-thinking,
--   deepseek-v3.2-thinking, gpt-5.4-thinking) which are routed as distinct entries
--   but share provider_model_id with their base model. Pricing copied from base.
--
-- NOTE: service_type vocabulary drift (route "llm" vs pricing_rule "llm_chat")
-- is fixed in the middleware layer (classifyServiceType in billing.go), not in SQL.
--
-- Idempotency: INSERT IGNORE skips duplicate rows; UPDATE targets the old model
-- name and is a no-op on second run (old name no longer exists).
-- =============================================================================

START TRANSACTION;

-- -----------------------------------------------------------------------
-- Fix 1: Rename claude-sonnet-4-6-think → claude-sonnet-4-6-thinking
-- (provider_model_id in ai_service_route is "claude-sonnet-4-6-thinking"
--  but pricing_rule had the 24-char truncated value "claude-sonnet-4-6-think")
-- -----------------------------------------------------------------------
UPDATE pricing_rule
SET model = 'claude-sonnet-4-6-thinking'
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'claude-sonnet-4-6-think'
  AND is_active = TRUE;

-- -----------------------------------------------------------------------
-- Fix 2: Add dmxapi-ssvip rows
-- Copies aihubmix pricing (same cost/sell), flat billing for text models,
-- tiered_token for gemini and gpt (tiers inserted separately below).
-- -----------------------------------------------------------------------

-- dmxapi-ssvip: claude-sonnet-4-6 (flat)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'claude-sonnet-4-6'
  AND is_active = TRUE;

-- dmxapi-ssvip: claude-sonnet-4-6-thinking (flat)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'claude-sonnet-4-6-thinking'
  AND is_active = TRUE;

-- dmxapi-ssvip: deepseek-v3.2 (flat)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'deepseek-v3.2'
  AND is_active = TRUE;

-- dmxapi-ssvip: gemini-3.1-pro-preview (tiered_token — row only; tiers below)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'gemini-3.1-pro-preview'
  AND is_active = TRUE;

-- dmxapi-ssvip: gpt-5.4 (tiered_token — row only; tiers below)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'gpt-5.4'
  AND is_active = TRUE;

-- -----------------------------------------------------------------------
-- Fix 2 (cont.): Add tiered rows for dmxapi-ssvip tiered_token models
-- Copies tier rows from the corresponding aihubmix rule.
-- -----------------------------------------------------------------------

-- dmxapi-ssvip gemini-3.1-pro-preview tiers (copy from aihubmix gemini rule)
INSERT IGNORE INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT
  new_rule.id,
  src_tier.token_type,
  src_tier.min_tokens,
  src_tier.max_tokens,
  src_tier.cost_per_mtok,
  src_tier.sell_per_mtok
FROM pricing_rule_tier src_tier
JOIN pricing_rule src_rule
  ON src_tier.rule_id = src_rule.id
  AND src_rule.service_type = 'llm_chat'
  AND src_rule.provider = 'aihubmix'
  AND src_rule.model = 'gemini-3.1-pro-preview'
  AND src_rule.is_active = TRUE
JOIN pricing_rule new_rule
  ON new_rule.service_type = 'llm_chat'
  AND new_rule.provider = 'dmxapi-ssvip'
  AND new_rule.model = 'gemini-3.1-pro-preview'
  AND new_rule.is_active = TRUE;

-- dmxapi-ssvip gpt-5.4 tiers (copy from aihubmix gpt-5.4 rule)
INSERT IGNORE INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT
  new_rule.id,
  src_tier.token_type,
  src_tier.min_tokens,
  src_tier.max_tokens,
  src_tier.cost_per_mtok,
  src_tier.sell_per_mtok
FROM pricing_rule_tier src_tier
JOIN pricing_rule src_rule
  ON src_tier.rule_id = src_rule.id
  AND src_rule.service_type = 'llm_chat'
  AND src_rule.provider = 'aihubmix'
  AND src_rule.model = 'gpt-5.4'
  AND src_rule.is_active = TRUE
JOIN pricing_rule new_rule
  ON new_rule.service_type = 'llm_chat'
  AND new_rule.provider = 'dmxapi-ssvip'
  AND new_rule.model = 'gpt-5.4'
  AND new_rule.is_active = TRUE;

-- -----------------------------------------------------------------------
-- Fix 3: Add aihubmix thinking model variants
-- These are separate ai_service rows (model_key = *-thinking) that share
-- the same provider_model_id with their base model. Pricing is identical.
-- -----------------------------------------------------------------------

-- aihubmix: gemini-3.1-pro-preview-thinking (tiered_token, same tiers as base)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, provider, 'gemini-3.1-pro-preview-thinking', billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'gemini-3.1-pro-preview'
  AND is_active = TRUE;

-- aihubmix: gemini-3.1-pro-preview-thinking tiers (copy from base)
INSERT IGNORE INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT
  new_rule.id,
  src_tier.token_type,
  src_tier.min_tokens,
  src_tier.max_tokens,
  src_tier.cost_per_mtok,
  src_tier.sell_per_mtok
FROM pricing_rule_tier src_tier
JOIN pricing_rule src_rule
  ON src_tier.rule_id = src_rule.id
  AND src_rule.service_type = 'llm_chat'
  AND src_rule.provider = 'aihubmix'
  AND src_rule.model = 'gemini-3.1-pro-preview'
  AND src_rule.is_active = TRUE
JOIN pricing_rule new_rule
  ON new_rule.service_type = 'llm_chat'
  AND new_rule.provider = 'aihubmix'
  AND new_rule.model = 'gemini-3.1-pro-preview-thinking'
  AND new_rule.is_active = TRUE;

-- aihubmix: deepseek-v3.2-thinking (flat, same price as deepseek-v3.2)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, provider, 'deepseek-v3.2-thinking', billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'deepseek-v3.2'
  AND is_active = TRUE;

-- aihubmix: gpt-5.4-thinking (tiered_token, same tiers as gpt-5.4)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, provider, 'gpt-5.4-thinking', billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'gpt-5.4'
  AND is_active = TRUE;

-- aihubmix: gpt-5.4-thinking tiers (copy from gpt-5.4)
INSERT IGNORE INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT
  new_rule.id,
  src_tier.token_type,
  src_tier.min_tokens,
  src_tier.max_tokens,
  src_tier.cost_per_mtok,
  src_tier.sell_per_mtok
FROM pricing_rule_tier src_tier
JOIN pricing_rule src_rule
  ON src_tier.rule_id = src_rule.id
  AND src_rule.service_type = 'llm_chat'
  AND src_rule.provider = 'aihubmix'
  AND src_rule.model = 'gpt-5.4'
  AND src_rule.is_active = TRUE
JOIN pricing_rule new_rule
  ON new_rule.service_type = 'llm_chat'
  AND new_rule.provider = 'aihubmix'
  AND new_rule.model = 'gpt-5.4-thinking'
  AND new_rule.is_active = TRUE;

COMMIT;

-- Verification query (run manually to confirm after migration):
-- SELECT COUNT(*) AS still_unmatched FROM (
--   SELECT r.id FROM ai_service_route r
--   JOIN ai_service s ON r.model_id = s.id
--   JOIN llm_provider p ON r.provider_id = p.id
--   WHERE r.is_active = TRUE AND s.deprecated_at IS NULL AND p.is_active = TRUE
--     AND NOT EXISTS (
--       SELECT 1 FROM pricing_rule pr
--       WHERE pr.is_active = TRUE
--         AND pr.provider = p.name
--         AND pr.model IN (s.model_key, r.provider_model_id)
--         AND pr.service_type IN ('llm_chat', 'llm_vision')
--     )
-- ) AS unmatched;
-- Expected result: 0
